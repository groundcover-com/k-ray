package tools

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Item 10: related lines that call a normal state a problem.

func quota(name string, hard, used map[string]any, spec map[string]any) unstructured.Unstructured {
	extra := map[string]any{"status": map[string]any{"hard": hard, "used": used}}
	if spec != nil {
		extra["spec"] = spec
	}
	return ob("v1", "ResourceQuota", "prod", name, extra)
}

func describeDeployment(t *testing.T, objs ...unstructured.Unstructured) string {
	t.Helper()
	f := &fakeReader{logs: map[string]string{}, objs: objs}
	r, _ := newRunner(t, f)
	return describeText(t, r, map[string]any{"kind": "deployment", "name": "api", "namespace": "prod"})
}

var apiLabels = map[string]any{"app": "api"}

// A workload that is short of pods, and quotas at their limit for things it
// does not use, or at 0 for a resource it does not request: not a problem.
func TestSilentQuotaOfAnUnrelatedResource(t *testing.T) {
	out := describeDeployment(t,
		appDeployment("prod", "api", 3, 1, apiLabels),
		quota("objects", map[string]any{"count/configmaps": "10", "services": "5"}, map[string]any{"count/configmaps": "10", "services": "5"}, nil),
		quota("gpu", map[string]any{"requests.nvidia.com/gpu": "0", "limits.memory": "0"}, map[string]any{"requests.nvidia.com/gpu": "0", "limits.memory": "0"}, nil),
	)
	mustNotContain(t, out, "resourcequota/")
}

// Scoped quotas count only the pods in their scope: the usage proves nothing.
func TestSilentQuotaWithAScope(t *testing.T) {
	out := describeDeployment(t,
		appDeployment("prod", "api", 3, 1, apiLabels),
		quota("besteffort", map[string]any{"pods": "2"}, map[string]any{"pods": "2"}, map[string]any{"scopes": []any{"BestEffort"}}),
	)
	mustNotContain(t, out, "resourcequota/")
}

// A FailedCreate event on the ReplicaSet that names the quota is the evidence,
// even when the usage is below the hard limit that the quota shows.
func TestQuotaNamedByAFailedCreateEvent(t *testing.T) {
	out := describeDeployment(t,
		appDeployment("prod", "api", 3, 1, apiLabels),
		ownedRS("prod", "api-6f", "api", 3, 1, apiLabels, nil),
		relEvent("prod", "ReplicaSet", "api-6f", "uid-replicaset-api-6f", "Warning", "FailedCreate", `Error creating: pods "api-6f-x" is forbidden: exceeded quota: scoped, requested: limits.memory=1Gi`),
		quota("scoped", map[string]any{"limits.memory": "8Gi"}, map[string]any{"limits.memory": "2Gi"}, nil),
		quota("other", map[string]any{"pods": "50"}, map[string]any{"pods": "3"}, nil),
	)
	mustContain(t, out, "resourcequota/scoped")
	mustContain(t, lineWith(t, out, "resourcequota/scoped"), "FailedCreate")
	mustNotContain(t, out, "resourcequota/other")
}

func TestQuotaAtItsPodLimitWhenTheWorkloadIsShort(t *testing.T) {
	out := describeDeployment(t,
		appDeployment("prod", "api", 3, 1, apiLabels),
		quota("compute", map[string]any{"pods": "10", "count/configmaps": "10"}, map[string]any{"pods": "10", "count/configmaps": "10"}, nil),
	)
	mustContain(t, out, "resourcequota/compute at its limit (the workload has 1 of 3 pods): pods used 10 of 10")
	mustNotContain(t, out, "configmaps")
}

// relNodePods: a pod that was created seconds ago is not stuck.
func TestSilentNodeWithYoungPendingPods(t *testing.T) {
	node := ob("v1", "Node", "", "node-a", map[string]any{"status": map[string]any{"conditions": conds(map[string]any{"type": "Ready", "status": "True"})}})
	pend := func(name, created string) unstructured.Unstructured {
		return ob("v1", "Pod", "prod", name, map[string]any{
			"metadata": map[string]any{"creationTimestamp": created},
			"spec":     map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{"name": "c", "image": "x"}}},
			"status":   map[string]any{"phase": "Pending"},
		})
	}
	run := func(objs ...unstructured.Unstructured) string {
		r, _ := newRunner(t, &fakeReader{objs: objs, logs: map[string]string{}})
		return describeText(t, r, map[string]any{"kind": "node", "name": "node-a"})
	}
	out := run(node, pend("young", "2026-11-13T11:59:30Z"))
	mustNotContain(t, out, "still pending")
	out = run(node, pend("young", "2026-11-13T11:59:30Z"), pend("old", "2026-11-13T11:50:00Z"))
	mustContain(t, out, "1 pod is assigned to it and still pending: prod/old")
	mustNotContain(t, out, "prod/young")
}

// relServicesOf: a numeric targetPort that no declared container port matches
// is a fault even when the endpoints are ready (readiness does not test the
// Service port). When the pods declare no port at all it is not a problem.
func TestNumericTargetPortThatNoDeclaredPortMatchesIsReportedWithReadyEndpoints(t *testing.T) {
	svc := ob("v1", "Service", "prod", "api", map[string]any{"spec": map[string]any{
		"selector": apiLabels, "ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8081)}},
	}})
	ready := ob("discovery.k8s.io/v1", "EndpointSlice", "prod", "api-1", map[string]any{
		"metadata":  map[string]any{"labels": map[string]any{"kubernetes.io/service-name": "api"}},
		"endpoints": []any{map[string]any{"addresses": []any{"10.0.0.1"}, "conditions": map[string]any{"ready": true}}},
		"ports":     []any{map[string]any{"port": int64(8081)}},
	})
	out := describeDeployment(t, appDeployment("prod", "api", 3, 3, apiLabels), svc, ready)
	mustContain(t, out, "related, with a problem", "targetPort 8081", "declared: http 8080")

	// The pods declare no port: nothing to compare with.
	dep := appDeployment("prod", "api", 3, 3, apiLabels)
	tpl, _, _ := unstructured.NestedMap(dep.Object, "spec", "template", "spec")
	cs := tpl["containers"].([]any)
	delete(cs[0].(map[string]any), "ports")
	_ = unstructured.SetNestedField(dep.Object, tpl, "spec", "template", "spec")
	out = describeDeployment(t, dep, svc, ready)
	mustNotContain(t, out, "targetPort 8081")
}

// An informational condition (a claim that is Unused=False is in use) is no
// problem on the claim and none on the workload that mounts it.
func TestSilentClaimWithUnusedConditionFalse(t *testing.T) {
	pvc := ob("v1", "PersistentVolumeClaim", "prod", "data", map[string]any{
		"spec": map[string]any{"storageClassName": "fast"},
		"status": map[string]any{"phase": "Bound", "conditions": conds(map[string]any{
			"type": "Unused", "status": "False", "reason": "PodUsingPVC", "message": "A pod is currently referencing this PVC",
		})},
	})
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{pvc}, logs: map[string]string{}})
	out := describeText(t, r, map[string]any{"kind": "persistentvolumeclaim", "name": "data", "namespace": "prod"})
	mustContain(t, out, "state: healthy", "looks wrong: nothing found")
	mustNotContain(t, out, "Unused is", "condition Unused")
	out = describeDeployment(t, appDeployment("prod", "api", 3, 3, apiLabels), pvc)
	mustNotContain(t, out, "persistentvolumeclaim/data", "Unused")
}

// A budget that allows no disruption by its own setting is a plain fact.
func TestSilentPodDisruptionBudgetThatForbidsByItsOwnSetting(t *testing.T) {
	pdb := func(spec, status map[string]any) unstructured.Unstructured {
		spec["selector"] = map[string]any{"matchLabels": apiLabels}
		return ob("policy/v1", "PodDisruptionBudget", "prod", "api", map[string]any{"spec": spec, "status": status})
	}
	status := map[string]any{"expectedPods": int64(3), "currentHealthy": int64(3), "desiredHealthy": int64(3), "disruptionsAllowed": int64(0)}
	for name, spec := range map[string]map[string]any{
		"minAvailable all": {"minAvailable": int64(3)},
		"maxUnavailable 0": {"maxUnavailable": int64(0)},
	} {
		out := describeDeployment(t, appDeployment("prod", "api", 3, 3, apiLabels), pdb(spec, status))
		mustContain(t, out, "allows no disruptions by its own setting")
		mustNotContain(t, out, "poddisruptionbudget/api: ", "no disruptions allowed")
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}
	// A budget that would allow one, but a pod is not healthy: a warning.
	sick := map[string]any{"expectedPods": int64(3), "currentHealthy": int64(2), "desiredHealthy": int64(2), "disruptionsAllowed": int64(0)}
	out := describeDeployment(t, appDeployment("prod", "api", 3, 3, apiLabels), pdb(map[string]any{"minAvailable": int64(2)}, sick))
	mustContain(t, out, "no disruptions allowed (2 of 3 pods healthy)")
	mustNotContain(t, out, "own setting")
}

// A CronJob with no recorded success and no Job that can be found: the result
// of the last run is not known, and no failure is claimed.
func TestSilentCronJobWhoseLastJobCannotBeFound(t *testing.T) {
	spec := map[string]any{"schedule": "0 * * * *", "jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "c:1"}}}}}}}
	cj := ob("batch/v1", "CronJob", "prod", "nightly", map[string]any{"spec": spec, "status": map[string]any{"lastScheduleTime": "2026-11-13T11:00:00Z"}})
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{cj}, logs: map[string]string{}})
	out := describeText(t, r, map[string]any{"kind": "cronjob", "name": "nightly", "namespace": "prod"})
	mustContain(t, out, "result of the last run is not known")
	mustNotContain(t, out, "did not succeed")
}
