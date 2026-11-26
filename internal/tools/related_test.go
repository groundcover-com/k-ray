package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
)

// Fixtures for the objects around the described object. Each test builds the
// few objects it needs; the helpers only fill in the usual metadata.

// ob builds an object. extra may hold "spec", "status" and "metadata" (merged
// into the usual metadata).
func ob(apiVersion, kind, ns, name string, extra map[string]any) unstructured.Unstructured {
	meta := map[string]any{"name": name, "uid": "uid-" + strings.ToLower(kind) + "-" + name, "creationTimestamp": "2026-11-13T09:00:00Z"}
	if ns != "" {
		meta["namespace"] = ns
	}
	m := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	for k, v := range extra {
		if k == "metadata" {
			for mk, mv := range v.(map[string]any) {
				meta[mk] = mv
			}
			continue
		}
		m[k] = v
	}
	return unstructured.Unstructured{Object: m}
}

func conds(cs ...map[string]any) []any {
	out := make([]any, len(cs))
	for i, c := range cs {
		out[i] = c
	}
	return out
}

func relEvent(ns, kind, name, uid, typ, reason, msg string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"name": name + "." + reason, "namespace": ns, "creationTimestamp": "2026-11-13T11:00:00Z"},
		"involvedObject": map[string]any{"kind": kind, "name": name, "uid": uid, "namespace": ns},
		"type":           typ, "reason": reason, "message": msg, "count": int64(12),
		"lastTimestamp": "2026-11-13T11:58:00Z",
	}}
}

// appDeployment is a Deployment whose template has labels, a port, a claim, a
// ConfigMap key reference and a Secret reference.
func appDeployment(ns, name string, replicas, created int64, labels map[string]any) unstructured.Unstructured {
	return ob("apps/v1", "Deployment", ns, name, map[string]any{
		"spec": map[string]any{
			"replicas": replicas, "selector": map[string]any{"matchLabels": labels},
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "app", "image": "registry.example/" + name + ":1",
						"ports": []any{map[string]any{"name": "http", "containerPort": int64(8080)}},
						"env": []any{
							map[string]any{"name": "LOG", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": name + "-config", "key": "LOG"}}},
							map[string]any{"name": "DB_PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": name + "-db", "key": "password"}}},
						},
						"volumeMounts": []any{map[string]any{"name": "data", "mountPath": "/data"}},
					}},
					"volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"}}},
				},
			},
		},
		"status": map[string]any{"replicas": created, "readyReplicas": created, "availableReplicas": created, "updatedReplicas": created},
	})
}

func ownedRS(ns, name, dep string, replicas, created int64, labels map[string]any, extra map[string]any) unstructured.Unstructured {
	rsLabels := map[string]any{}
	for k, v := range labels {
		rsLabels[k] = v
	}
	spec := map[string]any{"replicas": replicas}
	status := map[string]any{"replicas": created, "readyReplicas": created}
	for k, v := range extra {
		status[k] = v
	}
	return ob("apps/v1", "ReplicaSet", ns, name, map[string]any{
		"metadata": map[string]any{
			"labels":          rsLabels,
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": dep, "uid": "uid-deployment-" + dep, "controller": true}},
		},
		"spec": spec, "status": status,
	})
}

func describeText(t *testing.T, r *Runner, args map[string]any) string {
	t.Helper()
	res := call(t, r, "describe", args)
	if res.IsError {
		t.Fatalf("describe %v: %s", args, res.Text)
	}
	return res.Text
}

func noSecretReads(t *testing.T, f *fakeReader) {
	t.Helper()
	for _, c := range f.calls {
		if strings.Contains(c, "secret") {
			t.Errorf("a read touched secrets: %s", c)
		}
	}
}

// A Deployment whose ReplicaSet cannot create pods: the description says why
// (quota), and shows the claim, the ConfigMap key, the autoscaler and the
// policy that go with it. A Secret is named, never read.
func TestDescribeWorkloadShowsTheRelatedProblems(t *testing.T) {
	labels := map[string]any{"app": "api"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		appDeployment("prod", "api", 3, 0, labels),
		ownedRS("prod", "api-6f", "api", 3, 0, labels, map[string]any{"conditions": conds(map[string]any{
			"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate",
			"message": `pods "api-6f-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=10, limited: pods=10`,
		})}),
		ownedRS("prod", "api-5e", "api", 0, 0, labels, nil),
		relEvent("prod", "ReplicaSet", "api-6f", "uid-replicaset-api-6f", "Warning", "FailedCreate", `Error creating: pods "api-6f-x" is forbidden: exceeded quota: compute`),
		ob("v1", "ResourceQuota", "prod", "compute", map[string]any{"status": map[string]any{
			"hard": map[string]any{"pods": "10", "requests.cpu": "20"}, "used": map[string]any{"pods": "10", "requests.cpu": "4"},
		}}),
		ob("v1", "ResourceQuota", "prod", "other", map[string]any{"status": map[string]any{
			"hard": map[string]any{"pods": "50"}, "used": map[string]any{"pods": "3"},
		}}),
		ob("v1", "PersistentVolumeClaim", "prod", "data", map[string]any{
			"spec": map[string]any{"storageClassName": "fast"}, "status": map[string]any{"phase": "Pending"},
		}),
		ob("storage.k8s.io/v1", "StorageClass", "", "standard", map[string]any{"metadata": map[string]any{"annotations": map[string]any{"storageclass.kubernetes.io/is-default-class": "true"}}}),
		ob("storage.k8s.io/v1", "StorageClass", "", "gold", nil),
		ob("v1", "ConfigMap", "prod", "api-config", map[string]any{"data": map[string]any{"OTHER": "x"}}),
		ob("autoscaling/v2", "HorizontalPodAutoscaler", "prod", "api", map[string]any{
			"spec": map[string]any{"maxReplicas": int64(5), "scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}, "metrics": []any{map[string]any{"type": "Resource"}}},
			"status": map[string]any{"currentReplicas": int64(0), "conditions": conds(map[string]any{
				"type": "ScalingActive", "status": "False", "reason": "FailedGetResourceMetric",
				"message": "the HPA was unable to compute the replica count: failed to get cpu utilization: missing request for cpu in container app of Pod api-6f-x",
			})},
		}),
		ob("networking.k8s.io/v1", "NetworkPolicy", "prod", "db-only", map[string]any{"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}, "policyTypes": []any{"Ingress"},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "frontend"}}}},
				"ports": []any{map[string]any{"port": int64(8080), "protocol": "TCP"}},
			}},
		}}),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "api", "namespace": "prod"})
	mustContain(t, out,
		"related, with a problem:",
		`- replicaset/api-6f: replica failure: FailedCreate: pods "api-6f-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=10, limited: pods=10`,
		"- resourcequota/compute at its limit (the workload has 0 of 3 pods): pods used 10 of 10",
		`- persistentvolumeclaim/data: claim is Pending; storage class "fast" does not exist (storage classes that exist: gold, standard)`,
		"- configmap/api-config: exists, but has no key LOG",
		"- horizontalpodautoscaler/api: ScalingActive is False: FailedGetResourceMetric: the HPA was unable to compute the replica count: failed to get cpu utilization: missing request for cpu in container app of Pod api-6f-x",
		"secrets referenced, names only, never read: api-db",
		"network policies that select these pods:",
		"- db-only: ingress: from pods app=frontend, on 8080/TCP",
	)
	mustNotContain(t, out, "resourcequota/other", "api-5e")
	// The same fact is said once: the ReplicaSet condition already holds the quota text.
	if n := strings.Count(out, "exceeded quota: compute"); n != 1 {
		t.Errorf("the quota text appears %d times:\n%s", n, out)
	}
	noSecretReads(t, f)
}

// A healthy workload with healthy relations stays small, and says so in one line.
func TestDescribeHealthyWorkloadStaysSmall(t *testing.T) {
	labels := map[string]any{"app": "api"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		appDeployment("prod", "api", 2, 2, labels),
		ownedRS("prod", "api-6f", "api", 2, 2, labels, nil),
		ob("v1", "PersistentVolumeClaim", "prod", "data", map[string]any{"spec": map[string]any{"storageClassName": "standard"}, "status": map[string]any{"phase": "Bound"}}),
		ob("v1", "ConfigMap", "prod", "api-config", map[string]any{"data": map[string]any{"LOG": "info"}}),
		ob("v1", "Service", "prod", "api", map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "api"}, "ports": []any{map[string]any{"port": int64(80), "targetPort": "http"}}}}),
		ob("autoscaling/v2", "HorizontalPodAutoscaler", "prod", "api", map[string]any{
			"spec":   map[string]any{"maxReplicas": int64(5), "scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}},
			"status": map[string]any{"currentReplicas": int64(2)},
		}),
		mkPod("prod", "api-6f-aaaaa", "ReplicaSet", "api-6f", labels, "Running"),
		mkPod("prod", "api-6f-bbbbb", "ReplicaSet", "api-6f", labels, "Running"),
	)
	r, _ := newRunner(t, f)
	res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "api", "namespace": "prod"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	mustContain(t, res.Text, "related, no problem: 1 replicaset, 1 claim, 1 configmap, 1 autoscaler, 1 service; secrets referenced, names only, never read: api-db")
	mustNotContain(t, res.Text, "related, with a problem")
	if len(res.Text) > 1500 {
		t.Errorf("a healthy workload took %d bytes:\n%s", len(res.Text), res.Text)
	}
	// Everything the object needs is read by the call; count the reads of the relations.
	if n := f.total(); n > 14 {
		t.Errorf("%d reads: %v", n, f.calls)
	}
	noSecretReads(t, f)
}

// A custom resource has no rule of its own: its conditions that are not
// healthy are shown, newest first, by describe and in the CAUSE of a list row.
func TestCustomResourceConditions(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		ob("example.com/v1", "Widget", "prod", "w1", map[string]any{"status": map[string]any{"conditions": conds(
			map[string]any{"type": "Ready", "status": "False", "reason": "DependencyMissing", "message": `database "db" is not ready; password=condSecret99xyz`, "lastTransitionTime": "2026-11-13T10:00:00Z"},
			map[string]any{"type": "Synced", "status": "True", "lastTransitionTime": "2026-11-13T11:30:00Z"},
			map[string]any{"type": "Stalled", "status": "True", "reason": "RetriesExhausted", "message": "gave up after 5 tries", "lastTransitionTime": "2026-11-13T11:50:00Z"},
		)}}),
		ob("example.com/v1", "Widget", "prod", "w2", map[string]any{"status": map[string]any{"conditions": conds(map[string]any{"type": "Ready", "status": "True"})}}),
	)
	r, _ := newRunner(t, f)
	withCR, err := r.Kinds.With([]kinds.CustomResource{{Group: "example.com", Version: "v1", Resource: "widgets", Kind: "Widget", Namespaced: true}})
	if err != nil {
		t.Fatal(err)
	}
	r.Kinds = withCR
	out := describeText(t, r, map[string]any{"kind": "widgets.example.com", "name": "w1", "namespace": "prod"})
	mustContain(t, out, "state: unhealthy", "- Stalled is True: RetriesExhausted: gave up after 5 tries", `- Ready is False: DependencyMissing: database "db" is not ready`)
	mustNotContain(t, out, "condSecret99xyz") // a message is redacted like every other text
	if strings.Index(out, "Stalled is True") > strings.Index(out, "Ready is False") {
		t.Errorf("the newest condition must come first:\n%s", out)
	}
	list := call(t, r, "list", map[string]any{"kind": "widgets.example.com", "namespace": "prod"}).Text
	mustContain(t, list, "1 unhealthy, 1 healthy")
	row := lineWith(t, list, "w1")
	mustContain(t, row, "Stalled is True: RetriesExhausted: gave up after 5 tries")
	mustNotContain(t, list, "condSecret99xyz")
	mustNotContain(t, lineWith(t, list, "w2"), "Stalled")
}

func TestAutoscalerDescribeShowsTheMetricReasonInFull(t *testing.T) {
	msg := "the HPA was unable to compute the replica count: failed to get cpu utilization: missing request for cpu in container app of Pod api-6f-x"
	f := &fakeReader{logs: map[string]string{}}
	labels := map[string]any{"app": "api"}
	f.objs = append(f.objs,
		appDeployment("prod", "api", 2, 2, labels),
		ob("autoscaling/v2", "HorizontalPodAutoscaler", "prod", "api", map[string]any{
			"spec":   map[string]any{"minReplicas": int64(2), "maxReplicas": int64(5), "scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}, "metrics": []any{map[string]any{"type": "Resource"}}},
			"status": map[string]any{"currentReplicas": int64(2), "conditions": conds(map[string]any{"type": "ScalingActive", "status": "False", "reason": "FailedGetResourceMetric", "message": msg})},
		}),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "hpa", "name": "api", "namespace": "prod"})
	mustContain(t, out, "ScalingActive is False: FailedGetResourceMetric: "+msg, "target deployment/api: 2 replicas wanted, 2 existing, 2 ready; autoscaler min 2, max 5", "current metrics: none")
	list := call(t, r, "list", map[string]any{"kind": "hpa", "namespace": "prod"}).Text
	mustContain(t, lineWith(t, list, "api"), "missing request for cpu in container app")
	// A target that does not exist.
	f.objs = f.objs[1:]
	out = describeText(t, r, map[string]any{"kind": "hpa", "name": "api", "namespace": "prod"})
	mustContain(t, out, "- deployment/api: the target does not exist in namespace prod")
}

func TestDescribeNetworkPolicy(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		mkPod("prod", "db-0", "", "", map[string]any{"app": "db"}, "Running"),
		mkPod("prod", "db-1", "", "", map[string]any{"app": "db"}, "Running"),
		mkPod("prod", "web-0", "", "", map[string]any{"app": "web"}, "Running"),
		ob("networking.k8s.io/v1", "NetworkPolicy", "prod", "db-in", map[string]any{"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "db"}}, "policyTypes": []any{"Ingress", "Egress"},
			"ingress": []any{
				map[string]any{"from": []any{
					map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "backend"}}},
					map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"team": "data"}}},
					map[string]any{"ipBlock": map[string]any{"cidr": "10.0.0.0/8", "except": []any{"10.1.0.0/16"}}},
				}, "ports": []any{map[string]any{"port": int64(5432)}}},
			},
		}}),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "networkpolicy", "name": "db-in", "namespace": "prod"})
	mustContain(t, out, "selects 2 pods: db-0, db-1",
		"rules: ingress: from pods app=backend or namespaces team=data or block 10.0.0.0/8 except 10.1.0.0/16, on 5432/TCP; egress: nothing is allowed")
	// The same text is shown from the side of the target pod.
	pod := describeText(t, r, map[string]any{"kind": "pod", "name": "db-0", "namespace": "prod"})
	mustContain(t, pod, "network policies that select these pods:", "- db-in: ingress: from pods app=backend")
	web := describeText(t, r, map[string]any{"kind": "pod", "name": "web-0", "namespace": "prod"})
	mustNotContain(t, web, "network policies")
}

func TestDescribeJobShowsItsFailedPod(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		ob("batch/v1", "Job", "prod", "migrate", map[string]any{
			"spec":   map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"job-name": "migrate"}}, "backoffLimit": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "m", "image": "migrate:1"}}}}},
			"status": map[string]any{"failed": int64(3), "conditions": conds(map[string]any{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded", "message": "Job has reached the specified backoff limit"})},
		}),
		failedPod(),
	)
	f.logs["prod/migrate-abcde/app"] = "2026-11-13T11:50:00Z starting\n2026-11-13T11:50:01Z ERROR: relation \"users\" does not exist\n"
	f.logs["prod/migrate-abcde/app#previous"] = f.logs["prod/migrate-abcde/app"]
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "job", "name": "migrate", "namespace": "prod"})
	mustContain(t, out, "state: unhealthy", "failed: BackoffLimitExceeded", "pod/migrate-abcde",
		"last log lines of container app in the failed pod/migrate-abcde:", `ERROR: relation "users" does not exist`)
	mustNotContain(t, out, "Failed is True")
}

func TestCronJobSignalsInDescribeAndList(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	cj := func(name, schedule string, extra map[string]any) unstructured.Unstructured {
		spec := map[string]any{"schedule": schedule, "jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "c:1"}}}}}}}
		o := ob("batch/v1", "CronJob", "prod", name, map[string]any{"spec": spec, "status": map[string]any{}})
		for k, v := range extra {
			_ = unstructured.SetNestedField(o.Object, v, "status", k)
		}
		return o
	}
	f.objs = append(f.objs,
		cj("never", "*/10 * * * *", nil),
		cj("feb", "0 3 31 2 *", nil),
		cj("ok", "0 * * * *", map[string]any{"lastScheduleTime": "2026-11-13T11:00:00Z", "lastSuccessfulTime": "2026-11-13T11:01:00Z"}),
		cj("failing", "0 * * * *", map[string]any{"lastScheduleTime": "2026-11-13T11:00:00Z"}),
		ob("batch/v1", "Job", "prod", "failing-29", map[string]any{
			"metadata": map[string]any{"creationTimestamp": "2026-11-13T11:00:00Z", "ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "CronJob", "name": "failing", "uid": "x", "controller": true}}},
			"spec":     map[string]any{"completions": int64(1)},
			"status":   map[string]any{"failed": int64(1), "conditions": conds(map[string]any{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded", "message": "Job has reached the specified backoff limit"})},
		}),
	)
	r, _ := newRunner(t, f)
	mustContain(t, describeText(t, r, map[string]any{"kind": "cronjob", "name": "never", "namespace": "prod"}),
		"state: warning", "never scheduled: it is 3h old and the schedule \"*/10 * * * *\" should have fired")
	mustContain(t, describeText(t, r, map[string]any{"kind": "cronjob", "name": "feb", "namespace": "prod"}),
		"state: unhealthy", `schedule "0 3 31 2 *" can never fire`)
	mustNotContain(t, describeText(t, r, map[string]any{"kind": "cronjob", "name": "ok", "namespace": "prod"}), "never scheduled", "can never fire", "did not succeed")
	fail := describeText(t, r, map[string]any{"kind": "cronjob", "name": "failing", "namespace": "prod"})
	mustContain(t, fail, "last run did not succeed: scheduled 1h ago, last success never", "- job/failing-29: failed: BackoffLimitExceeded")
	list := call(t, r, "list", map[string]any{"kind": "cronjobs", "namespace": "prod"}).Text
	mustContain(t, lineWith(t, list, "feb"), "can never fire")
	mustContain(t, lineWith(t, list, "*/10"), "never scheduled")
	mustContain(t, list, "4 cronjobs: 1 unhealthy, 2 warning, 1 healthy")
}

func TestDescribePodShowsTheInitContainerThatWaits(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	p := mkPod("prod", "app-0", "", "", map[string]any{"app": "app"}, "Pending")
	spec := p.Object["spec"].(map[string]any)
	spec["initContainers"] = []any{map[string]any{"name": "wait-db", "image": "busybox"}}
	p.Object["status"] = map[string]any{
		"phase": "Pending",
		"initContainerStatuses": []any{map[string]any{
			"name": "wait-db", "ready": false, "restartCount": int64(0), "image": "busybox", "imageID": "",
			"state": map[string]any{"running": map[string]any{"startedAt": "2026-11-13T09:00:00Z"}},
		}},
		"containerStatuses": []any{map[string]any{"name": "app", "ready": false, "restartCount": int64(0), "image": "x", "imageID": "", "state": map[string]any{"waiting": map[string]any{"reason": "PodInitializing"}}}},
	}
	f.objs = append(f.objs, p)
	f.logs["prod/app-0/wait-db"] = "waiting for db:5432\nwaiting for db:5432\nwaiting for db:5432 password=hunter2hunter2\n"
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "app-0", "namespace": "prod"})
	mustContain(t, out, "init container wait-db has not completed: running for 3h", "its last log lines:", "waiting for db:5432")
	mustNotContain(t, out, "hunter2hunter2")
	// A pod whose init container completed has nothing to add.
	done := mkPod("prod", "app-1", "", "", map[string]any{"app": "app"}, "Running")
	done.Object["status"].(map[string]any)["initContainerStatuses"] = []any{map[string]any{
		"name": "wait-db", "ready": true, "image": "busybox", "imageID": "",
		"state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "reason": "Completed"}},
	}}
	f.objs = append(f.objs, done)
	mustNotContain(t, describeText(t, r, map[string]any{"kind": "pod", "name": "app-1", "namespace": "prod"}), "init container")
}

func TestServiceNumericTargetPortHint(t *testing.T) {
	labels := map[string]any{"app": "api"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		ob("v1", "Service", "prod", "api", map[string]any{"spec": map[string]any{"selector": labels, "ports": []any{map[string]any{"port": int64(80), "targetPort": int64(9090)}}}}),
		ob("v1", "Service", "prod", "ext", map[string]any{"spec": map[string]any{"type": "ExternalName", "externalName": "db.example.com"}}),
	)
	pod := mkPod("prod", "api-0", "", "", labels, "Running")
	pod.Object["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["ports"] = []any{map[string]any{"containerPort": int64(8080)}}
	f.objs = append(f.objs, pod)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "service", "name": "api", "namespace": "prod"})
	mustContain(t, out, "targetPort 9090, which no container port of the selected pods declares (declared: 8080)", "Strong hint of a wrong port")
	mustContain(t, describeText(t, r, map[string]any{"kind": "service", "name": "ext", "namespace": "prod"}), "external name: db.example.com")
	// The same check from the workload side.
	dep := appDeployment("prod", "api", 1, 1, labels)
	f.objs = append(f.objs, dep)
	wl := describeText(t, r, map[string]any{"kind": "deployment", "name": "api", "namespace": "prod"})
	mustContain(t, wl, "- service/api: service port 80 has targetPort 9090")
}

func TestAmbiguousNameShowsEveryMatch(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	for _, ns := range []string{"a", "b"} {
		f.objs = append(f.objs, appDeployment(ns, "web", 2, 2, map[string]any{"app": "web"}))
	}
	// The one in b has a problem; the one in a does not.
	for i := range f.objs {
		if f.objs[i].GetNamespace() == "b" {
			_ = unstructured.SetNestedField(f.objs[i].Object, int64(0), "status", "availableReplicas")
		}
	}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"name": "web"})
	mustContain(t, out, "found by name: 2 objects match web in 2 namespaces; all are described below (problems first)",
		"== deployment/web in namespace b", "== deployment/web in namespace a", "0 of 2 replicas available")
	if strings.Index(out, "namespace b") > strings.Index(out, "namespace a") {
		t.Errorf("the match with a problem must come first:\n%s", out)
	}
	// With a kind and -A: the same.
	all := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "allNamespaces": true})
	mustContain(t, all, "== deployment/web in namespace a", "== deployment/web in namespace b")
}

func TestAmbiguousNameWithSixMatchesNamesAllOfThem(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	for _, ns := range []string{"a", "b", "c", "d", "e", "f"} {
		f.objs = append(f.objs, appDeployment(ns, "web", 2, 2, map[string]any{"app": "web"}))
	}
	for i := range f.objs { // the one in f is the broken one
		if f.objs[i].GetNamespace() == "f" {
			_ = unstructured.SetNestedField(f.objs[i].Object, int64(0), "status", "availableReplicas")
		}
	}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"name": "web"})
	mustContain(t, out, "found by name: 6 objects match web in 6 namespaces; 4 are described below, and the other 2 get one line each (problems first)",
		"not described here (one line each):")
	if n := strings.Count(out, "== deployment/web"); n != 4 {
		t.Errorf("%d blocks, want 4:\n%s", n, out)
	}
	// The broken one is a block; the other five are accounted for.
	mustContain(t, out, "== deployment/web in namespace f")
	for _, ns := range []string{"a", "b", "c", "d", "e"} {
		if !strings.Contains(out, "deployment/web in namespace "+ns) {
			t.Errorf("match in namespace %s is missing:\n%s", ns, out)
		}
	}
	mustContain(t, out, "next: describe deployment web namespace=")
}

func TestReadsOfRelationsAreBounded(t *testing.T) {
	labels := map[string]any{"app": "api"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, appDeployment("prod", "api", 2, 2, labels), ownedRS("prod", "api-6f", "api", 2, 2, labels, nil))
	for i := 0; i < 20; i++ {
		f.objs = append(f.objs, ob("v1", "ConfigMap", "prod", fmt.Sprintf("cm%d", i), nil))
	}
	r, _ := newRunner(t, f)
	describeText(t, r, map[string]any{"kind": "deployment", "name": "api", "namespace": "prod"})
	counts := f.counts()
	// get + events + the list of pods + rs, hpa, pdb, services, networkpolicies + one claim list + one configmap get.
	if f.total() > 12 {
		t.Errorf("%d reads: %v", f.total(), f.calls)
	}
	if counts["logs"] != 0 {
		t.Errorf("no log is needed: %v", f.calls)
	}
	noSecretReads(t, f)
}

func failedPod() unstructured.Unstructured {
	p := mkPod("prod", "migrate-abcde", "Job", "migrate", map[string]any{"job-name": "migrate"}, "Running")
	p.Object["status"] = map[string]any{"phase": "Failed", "containerStatuses": []any{map[string]any{
		"name": "app", "ready": false, "restartCount": int64(0),
		"state": map[string]any{"terminated": map[string]any{"exitCode": int64(1), "reason": "Error"}},
	}}}
	return p
}

func TestDescribeClaimIngressAndNode(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		ob("v1", "PersistentVolumeClaim", "prod", "data", map[string]any{"spec": map[string]any{"storageClassName": "fast"}, "status": map[string]any{"phase": "Pending"}}),
		ob("v1", "PersistentVolumeClaim", "prod", "bound", map[string]any{"spec": map[string]any{"storageClassName": "standard", "volumeName": "pv-1"}, "status": map[string]any{"phase": "Bound"}}),
		ob("v1", "PersistentVolume", "", "pv-1", map[string]any{"status": map[string]any{"phase": "Bound"}}),
		ob("storage.k8s.io/v1", "StorageClass", "", "standard", nil),
		ob("v1", "Service", "prod", "web", map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "web"}, "ports": []any{map[string]any{"port": int64(80)}}}}),
		ob("networking.k8s.io/v1", "Ingress", "prod", "site", map[string]any{"spec": map[string]any{"rules": []any{map[string]any{"host": "a.example.com", "http": map[string]any{"paths": []any{
			map[string]any{"path": "/", "backend": map[string]any{"service": map[string]any{"name": "web", "port": map[string]any{"number": int64(8080)}}}},
			map[string]any{"path": "/x", "backend": map[string]any{"service": map[string]any{"name": "gone", "port": map[string]any{"number": int64(80)}}}},
		}}}}}}),
		ob("v1", "Node", "", "node-1", map[string]any{"status": map[string]any{"conditions": conds(map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "MemoryPressure", "status": "True", "message": "low memory"})}}),
	)
	pend := mkPod("prod", "late-0", "", "", map[string]any{"app": "late"}, "Running")
	pend.Object["spec"].(map[string]any)["nodeName"] = "node-1"
	pend.Object["status"] = map[string]any{"phase": "Pending"}
	f.objs = append(f.objs, pend)
	r, _ := newRunner(t, f)
	mustContain(t, describeText(t, r, map[string]any{"kind": "pvc", "name": "data", "namespace": "prod"}),
		`storage class: storage class "fast" does not exist (storage classes that exist: standard)`)
	mustContain(t, describeText(t, r, map[string]any{"kind": "pvc", "name": "bound", "namespace": "prod"}), "volume pv-1: bound")
	ing := describeText(t, r, map[string]any{"kind": "ingress", "name": "site", "namespace": "prod"})
	mustContain(t, ing, "- service/web: the service has no port 8080 (it has 80)", "- service/gone: the backend service does not exist in namespace prod")
	node := describeText(t, r, map[string]any{"kind": "node", "name": "node-1"})
	mustContain(t, node, "MemoryPressure: low memory", "- node/node-1: 1 pod is assigned to it and still pending: prod/late-0", "pods on this node: 1")
}

// The reads of the related objects have a lower priority: they use at most
// half of the slots, so the diagnosis of the object itself never waits behind
// them.
func TestRelatedReadsLeaveSlotsFree(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}, latency: 20 * time.Millisecond}
	cr := newCallReader(f)
	k := kinds.Default()
	pods, _ := k.Lookup("pods")
	ctx := lowPriority(context.Background())
	var fns []func()
	for i := 0; i < 12; i++ {
		fns = append(fns, func() { _, _ = cr.List(ctx, pods, fmt.Sprintf("ns%d", i), "", 10) })
	}
	parallel(fns...)
	if f.maxInflight > maxLowReads {
		t.Errorf("%d low priority reads at once, more than %d", f.maxInflight, maxLowReads)
	}
	if f.total() != 12 {
		t.Errorf("%d reads, want 12", f.total())
	}
}
