package shape

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groundcover-com/kray/internal/redact"
)

// --- the table of defaults ---

// setPath puts a value at a path (a "[]" step makes a list of one map).
func setPath(root map[string]any, path string, v any) {
	segs := strings.Split(path, ".")
	cur := root
	for i, seg := range segs {
		last := i == len(segs)-1
		key := strings.TrimSuffix(seg, "[]")
		isList := strings.HasSuffix(seg, "[]")
		switch {
		case last:
			cur[key] = v
		case isList:
			next := map[string]any{}
			cur[key] = []any{next}
			cur = next
		default:
			next, _ := cur[key].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cur[key] = next
			}
			cur = next
		}
	}
}

// present reports whether the last key of a path is in the object.
func present(root map[string]any, path string) bool {
	found := false
	eachParent(root, strings.Split(path, "."), func(m map[string]any, key string) {
		if _, ok := m[key]; ok {
			found = true
		}
	})
	return found
}

func notDefault(want any) any {
	switch w := want.(type) {
	case int:
		return w + 7
	case bool:
		return !w
	case string:
		return w + "-other"
	}
	return "other"
}

// Every row of the table: the field goes at its default, and stays at any
// other value.
func TestDefaultRows(t *testing.T) {
	perKind := map[string]int{}
	for _, d := range DefaultRows() {
		perKind[d.Kind]++
		t.Run(d.Kind+"/"+d.Path, func(t *testing.T) {
			at := map[string]any{"kind": d.Kind}
			setPath(at, d.Path, d.Want)
			if !present(at, d.Path) {
				t.Fatalf("test setup: %s not set", d.Path)
			}
			n := dropDefaults(at)
			if present(at, d.Path) || n != 1 {
				t.Errorf("the default %v of %s was not dropped (dropped %d)", d.Want, d.Path, n)
			}

			other := map[string]any{"kind": d.Kind}
			setPath(other, d.Path, notDefault(d.Want))
			if n := dropDefaults(other); n != 0 || !present(other, d.Path) {
				t.Errorf("a value that is not the default of %s was dropped", d.Path)
			}

			// A value of another kind is never touched.
			wrong := map[string]any{"kind": "Secret-like"}
			setPath(wrong, d.Path, d.Want)
			if n := dropDefaults(wrong); n != 0 {
				t.Errorf("a row of %s dropped a field of another kind", d.Kind)
			}

			for _, u := range d.Unless {
				dep := map[string]any{"kind": d.Kind}
				setPath(dep, d.Path, d.Want)
				// The sibling goes next to the field: same parent path.
				i := strings.LastIndex(d.Path, ".")
				setPath(dep, d.Path[:i]+"."+u, true)
				if n := dropDefaults(dep); n != 0 {
					t.Errorf("%s was dropped although %s is set", d.Path, u)
				}
			}
		})
	}
	var kinds []string
	for k, n := range perKind {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
	}
	t.Logf("rows per kind: %s", strings.Join(kinds, " "))
	for _, k := range []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Service", "HorizontalPodAutoscaler", "PersistentVolumeClaim", "Node", "ConfigMap"} {
		if perKind[k] == 0 {
			t.Errorf("no default rows for %s", k)
		}
	}
}

func jsonObj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func trimmed(t *testing.T, s string) (string, Report) {
	t.Helper()
	o := jsonObj(t, s)
	rep := Trim(o, false)
	return YAML(o), rep
}

func TestNonDefaultValuesStay(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Pod","metadata":{"name":"p"},"spec":{
	  "dnsPolicy":"Default","restartPolicy":"OnFailure","schedulerName":"gpu-scheduler","terminationGracePeriodSeconds":120,
	  "enableServiceLinks":false,"preemptionPolicy":"Never","priority":1000,"serviceAccountName":"builder","serviceAccount":"builder",
	  "tolerations":[{"key":"node.kubernetes.io/not-ready","operator":"Exists","effect":"NoExecute","tolerationSeconds":600},
	                 {"key":"node.kubernetes.io/unreachable","operator":"Exists","effect":"NoExecute","tolerationSeconds":300},
	                 {"key":"dedicated","operator":"Equal","value":"gpu","effect":"NoSchedule"}],
	  "volumes":[{"name":"kube-api-access-x","projected":{}},{"name":"cfg","configMap":{"name":"c","defaultMode":384}},{"name":"tmp","emptyDir":{}}],
	  "containers":[{"name":"c","image":"app:1.0","imagePullPolicy":"Always",
	    "terminationMessagePath":"/dev/other","terminationMessagePolicy":"FallbackToLogsOnError",
	    "ports":[{"containerPort":53,"protocol":"UDP"}],
	    "livenessProbe":{"httpGet":{"path":"/h","port":80,"scheme":"HTTPS"},"timeoutSeconds":5,"periodSeconds":20,"successThreshold":1,"failureThreshold":6,"initialDelaySeconds":30}}]}}`)
	for _, kept := range []string{
		"dnsPolicy: Default", "restartPolicy: OnFailure", "schedulerName: gpu-scheduler", "terminationGracePeriodSeconds: 120",
		"enableServiceLinks: false", "preemptionPolicy: Never", "priority: 1000", "serviceAccountName: builder",
		"tolerationSeconds: 600", "key: dedicated", "defaultMode: 384", "emptyDir: {}", "imagePullPolicy: Always",
		"terminationMessagePath: /dev/other", "terminationMessagePolicy: FallbackToLogsOnError", "protocol: UDP",
		"scheme: HTTPS", "timeoutSeconds: 5", "periodSeconds: 20", "failureThreshold: 6", "initialDelaySeconds: 30", "name: tmp",
	} {
		if !strings.Contains(out, kept) {
			t.Errorf("a value that is not the default was dropped: %q\n%s", kept, out)
		}
	}
	if strings.Contains(out, "kube-api-access") {
		t.Errorf("the service account volume stayed:\n%s", out)
	}
	if strings.Count(out, "node.kubernetes.io/unreachable") != 0 {
		t.Errorf("the default unreachable toleration stayed:\n%s", out)
	}
}

func TestImagePullPolicyOnlyWhenDefaultForTheTag(t *testing.T) {
	for _, c := range []struct {
		image, policy string
		stays         bool
	}{
		{"app:1.0", "IfNotPresent", false},
		{"app:1.0", "Always", true},
		{"app", "Always", false},
		{"app:latest", "Always", false},
		{"app:latest", "IfNotPresent", true},
		{"reg.io:5000/app", "Always", false},
		{"reg.io:5000/app", "IfNotPresent", true},
		{"app@sha256:aaaa", "IfNotPresent", false},
		{"app:1.0", "Never", true},
	} {
		out, _ := trimmed(t, fmt.Sprintf(`{"kind":"Pod","spec":{"containers":[{"name":"c","image":%q,"imagePullPolicy":%q}]}}`, c.image, c.policy))
		if got := strings.Contains(out, "imagePullPolicy"); got != c.stays {
			t.Errorf("image %s policy %s: stays=%v, want %v\n%s", c.image, c.policy, got, c.stays, out)
		}
	}
}

func TestDeploymentStrategyPartialDefaults(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Deployment","spec":{"revisionHistoryLimit":5,"progressDeadlineSeconds":600,
	  "strategy":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":1,"maxUnavailable":"25%"}}}}`)
	for _, want := range []string{"revisionHistoryLimit: 5", "maxSurge: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"progressDeadlineSeconds", "maxUnavailable", "type: RollingUpdate"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	recreate, _ := trimmed(t, `{"kind":"Deployment","spec":{"strategy":{"type":"Recreate"}}}`)
	if !strings.Contains(recreate, "type: Recreate") {
		t.Errorf("Recreate was dropped:\n%s", recreate)
	}
}

func TestServiceRules(t *testing.T) {
	out, rep := trimmed(t, `{"kind":"Service","spec":{"clusterIP":"10.0.0.1","clusterIPs":["10.0.0.1"],"ipFamilies":["IPv4"],"ipFamilyPolicy":"SingleStack",
	  "sessionAffinity":"None","internalTrafficPolicy":"Cluster","type":"ClusterIP",
	  "ports":[{"port":80,"targetPort":80,"protocol":"TCP"},{"port":443,"targetPort":8443,"protocol":"TCP"},{"port":9090,"targetPort":"metrics"}]}}`)
	for _, gone := range []string{"clusterIPs", "ipFamilies", "ipFamilyPolicy", "sessionAffinity", "internalTrafficPolicy", "targetPort: 80\n", "protocol"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"clusterIP: 10.0.0.1", "type: ClusterIP", "targetPort: 8443", "targetPort: metrics", "port: 80"} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost %q:\n%s", kept, out)
		}
	}
	if rep.Dropped == 0 {
		t.Error("no count of dropped fields")
	}
	v6, _ := trimmed(t, `{"kind":"Service","spec":{"clusterIP":"fd00::1","clusterIPs":["fd00::1","10.0.0.2"],"ipFamilies":["IPv6"],"ipFamilyPolicy":"SingleStack"}}`)
	for _, kept := range []string{"ipFamilies", "IPv6", "clusterIPs", "10.0.0.2"} {
		if !strings.Contains(v6, kept) {
			t.Errorf("an IPv6 or dual address was dropped (%q):\n%s", kept, v6)
		}
	}
	dual, _ := trimmed(t, `{"kind":"Service","spec":{"ipFamilies":["IPv4","IPv6"],"ipFamilyPolicy":"PreferDualStack"}}`)
	if !strings.Contains(dual, "PreferDualStack") || !strings.Contains(dual, "IPv6") {
		t.Errorf("dual stack was dropped:\n%s", dual)
	}
}

func TestJobRules(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Job","spec":{"backoffLimit":6,"completionMode":"NonIndexed","suspend":false,"parallelism":1,"completions":1,
	  "selector":{"matchLabels":{"batch.kubernetes.io/controller-uid":"abc"}},
	  "template":{"metadata":{"labels":{"app":"x","batch.kubernetes.io/controller-uid":"abc","batch.kubernetes.io/job-name":"j","controller-uid":"abc","job-name":"j"}},
	  "spec":{"restartPolicy":"Never","containers":[{"name":"c","image":"i:1"}]}}}}`)
	for _, gone := range []string{"backoffLimit", "completionMode", "suspend", "parallelism", "selector", "controller-uid", "job-name"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"completions: 1", "restartPolicy: Never", "app: x"} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost %q:\n%s", kept, out)
		}
	}
	idx, _ := trimmed(t, `{"kind":"Job","spec":{"backoffLimit":6,"backoffLimitPerIndex":1,"podReplacementPolicy":"TerminatingOrFailed","podFailurePolicy":{"rules":[]}}}`)
	if !strings.Contains(idx, "backoffLimit: 6") || !strings.Contains(idx, "podReplacementPolicy: TerminatingOrFailed") {
		t.Errorf("a default that depends on a sibling field was dropped:\n%s", idx)
	}
	cron, _ := trimmed(t, `{"kind":"CronJob","spec":{"schedule":"* * * * *","concurrencyPolicy":"Forbid","successfulJobsHistoryLimit":3,"suspend":false,
	  "jobTemplate":{"spec":{"backoffLimit":2,"template":{"spec":{"dnsPolicy":"ClusterFirst","restartPolicy":"OnFailure","containers":[{"name":"c","image":"i:1","terminationMessagePath":"/dev/termination-log"}]}}}}}}`)
	for _, kept := range []string{"concurrencyPolicy: Forbid", "backoffLimit: 2", "restartPolicy: OnFailure", "schedule"} {
		if !strings.Contains(cron, kept) {
			t.Errorf("lost %q:\n%s", kept, cron)
		}
	}
	for _, gone := range []string{"successfulJobsHistoryLimit", "suspend", "dnsPolicy", "terminationMessagePath"} {
		if strings.Contains(cron, gone) {
			t.Errorf("still has %q:\n%s", gone, cron)
		}
	}
}

func TestOtherKindsDefaults(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"HorizontalPodAutoscaler","spec":{"minReplicas":1,"maxReplicas":5}}`)
	if strings.Contains(out, "minReplicas") || !strings.Contains(out, "maxReplicas: 5") {
		t.Errorf("hpa:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"HorizontalPodAutoscaler","spec":{"minReplicas":2,"maxReplicas":5}}`)
	if !strings.Contains(out, "minReplicas: 2") {
		t.Errorf("hpa min 2:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"PersistentVolumeClaim","spec":{"volumeMode":"Filesystem","accessModes":["ReadWriteOnce"]}}`)
	if strings.Contains(out, "volumeMode") || !strings.Contains(out, "ReadWriteOnce") {
		t.Errorf("pvc:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"PersistentVolumeClaim","spec":{"volumeMode":"Block"}}`)
	if !strings.Contains(out, "volumeMode: Block") {
		t.Errorf("pvc block:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"Node","spec":{"podCIDR":"10.0.0.0/24","podCIDRs":["10.0.0.0/24"],"unschedulable":true},"status":{"daemonEndpoints":{"kubeletEndpoint":{"Port":10250}}}}`)
	if strings.Contains(out, "podCIDRs") || strings.Contains(out, "10250") || !strings.Contains(out, "unschedulable: true") {
		t.Errorf("node:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"Node","status":{"daemonEndpoints":{"kubeletEndpoint":{"Port":10251}}}}`)
	if !strings.Contains(out, "10251") {
		t.Errorf("node port:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"ConfigMap","immutable":false,"data":{"a":"b"}}`)
	if strings.Contains(out, "immutable") {
		t.Errorf("configmap:\n%s", out)
	}
	out, _ = trimmed(t, `{"kind":"ConfigMap","immutable":true,"data":{"a":"b"}}`)
	if !strings.Contains(out, "immutable: true") {
		t.Errorf("configmap immutable:\n%s", out)
	}
}

func TestEmptyMapsThatMeanSomethingStay(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"NetworkPolicy","spec":{"podSelector":{},"ingress":[{"from":[{"namespaceSelector":{}}]}]},"status":{"x":{}}}`)
	for _, want := range []string{"podSelector: {}", "namespaceSelector: {}"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "status") {
		t.Errorf("an empty status stayed:\n%s", out)
	}
}

// --- status ---

func TestHealthyConditionsAreOneLine(t *testing.T) {
	out, rep := trimmed(t, `{"kind":"Pod","status":{"phase":"Running","conditions":[
	  {"type":"Initialized","status":"True","lastTransitionTime":"2026-11-15T09:00:00Z"},
	  {"type":"Ready","status":"True","lastTransitionTime":"2026-11-15T09:01:00Z"},
	  {"type":"ContainersReady","status":"True"},{"type":"PodScheduled","status":"True"}]}}`)
	if !strings.Contains(out, "conditions ok: Initialized, Ready, ContainersReady, PodScheduled") {
		t.Errorf("no one-line conditions:\n%s", out)
	}
	if strings.Contains(out, "lastTransitionTime") || strings.Contains(out, "\n  conditions:") {
		t.Errorf("healthy conditions stayed in full:\n%s", out)
	}
	if rep.Dropped < 4 {
		t.Errorf("dropped = %d", rep.Dropped)
	}
}

func TestUnhealthyConditionsStayInFull(t *testing.T) {
	cases := map[string]string{
		"pod not ready":          `{"kind":"Pod","status":{"conditions":[{"type":"Ready","status":"False","reason":"ContainersNotReady","message":"containers with unready status: [api]","lastTransitionTime":"2026-11-15T09:01:00Z"},{"type":"PodScheduled","status":"True"}]}}`,
		"pod unknown":            `{"kind":"Pod","status":{"conditions":[{"type":"Ready","status":"Unknown","reason":"NodeLost","message":"gone"}]}}`,
		"node pressure":          `{"kind":"Node","status":{"conditions":[{"type":"MemoryPressure","status":"True","reason":"KubeletHasInsufficientMemory","message":"kubelet has insufficient memory available","lastHeartbeatTime":"2026-11-15T12:00:00Z"},{"type":"Ready","status":"True"}]}}`,
		"pod being evicted":      `{"kind":"Pod","status":{"conditions":[{"type":"DisruptionTarget","status":"True","reason":"PreemptionByScheduler","message":"preempted"},{"type":"Ready","status":"True"}]}}`,
		"deployment deadline":    `{"kind":"Deployment","status":{"conditions":[{"type":"Progressing","status":"True","reason":"ProgressDeadlineExceeded","message":"did not progress"},{"type":"Available","status":"True"}]}}`,
		"deployment replica":     `{"kind":"Deployment","status":{"conditions":[{"type":"ReplicaFailure","status":"True","reason":"FailedCreate","message":"quota exceeded"},{"type":"Available","status":"False","reason":"MinimumReplicasUnavailable","message":"no replicas"}]}}`,
		"custom resource failed": `{"kind":"Widget","status":{"conditions":[{"type":"Synced","status":"False","reason":"Err","message":"cannot sync"},{"type":"Ready","status":"True"}]}}`,
	}
	for name, in := range cases {
		o := jsonObj(t, in)
		var wantMsgs []string
		for _, c := range o["status"].(map[string]any)["conditions"].([]any) {
			m := c.(map[string]any)
			if !ConditionHealthy(m["type"].(string), m["status"].(string), fmt.Sprint(m["reason"])) {
				wantMsgs = append(wantMsgs, fmt.Sprint(m["message"]))
			}
		}
		if len(wantMsgs) == 0 {
			t.Fatalf("%s: test setup has no unhealthy condition", name)
		}
		Trim(o, false)
		out := YAML(o)
		for _, msg := range wantMsgs {
			if !strings.Contains(out, msg) {
				t.Errorf("%s: unhealthy condition lost %q:\n%s", name, msg, out)
			}
		}
		if i := strings.Index(out, "conditions ok:"); i >= 0 {
			line := strings.SplitN(out[i:], "\n", 2)[0]
			for _, bad := range []string{"DisruptionTarget", "MemoryPressure", "Synced", "ReplicaFailure"} {
				if strings.Contains(line, bad) {
					t.Errorf("%s: %s is on the healthy line: %s", name, bad, line)
				}
			}
		}
	}
}

func TestContainerStatusKeepsStateDropsRepeats(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Pod","spec":{"containers":[{"name":"api","image":"i:1","resources":{"requests":{"cpu":"1"},"limits":{"cpu":"2"}},
	    "volumeMounts":[{"name":"data","mountPath":"/data"}]}]},
	  "status":{"podIP":"10.0.0.5","podIPs":[{"ip":"10.0.0.5"}],"hostIP":"10.1.0.1","hostIPs":[{"ip":"10.1.0.1"}],
	  "allocatedResources":{"cpu":"1"},
	  "containerStatuses":[{"name":"api","image":"i:1","imageID":"docker.io/library/i@sha256:0123456789abcdef0123456789abcdef","containerID":"containerd://deadbeef",
	    "ready":true,"started":true,"restartCount":3,"state":{"running":{"startedAt":"2026-11-15T09:00:00Z"}},
	    "lastState":{"terminated":{"exitCode":137,"reason":"OOMKilled","containerID":"containerd://cafe","startedAt":"2026-11-15T08:00:00Z"}},
	    "allocatedResources":{"cpu":"1"},"resources":{"requests":{"cpu":"1"},"limits":{"cpu":"2"}},
	    "volumeMounts":[{"name":"data","mountPath":"/data","recursiveReadOnly":"Disabled"},{"name":"kube-api-access-x","mountPath":"/var/run/secrets/kubernetes.io/serviceaccount"}]}]}}`)
	for _, kept := range []string{"restartCount: 3", "ready: true", "image: i:1", "imageDigest: sha256:0123456789ab", "OOMKilled", "exitCode: 137", "running:", "startedAt", "podIP: 10.0.0.5", "hostIP: 10.1.0.1"} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost %q:\n%s", kept, out)
		}
	}
	for _, gone := range []string{"containerID", "imageID", "started: true", "podIPs", "hostIPs", "allocatedResources", "recursiveReadOnly", "deadbeef", "cafe"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	if strings.Count(out, "limits:") != 1 {
		t.Errorf("the resources of the status repeat the spec:\n%s", out)
	}
}

func TestContainerStatusKeepsWhatDiffers(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Pod","spec":{"containers":[{"name":"api","image":"i:1","resources":{"requests":{"cpu":"1"}},"volumeMounts":[{"name":"data","mountPath":"/data"}]}]},
	  "status":{"podIP":"10.0.0.5","podIPs":[{"ip":"10.0.0.5"},{"ip":"fd00::5"}],
	  "allocatedResources":{"cpu":"2"},
	  "containerStatuses":[{"name":"api","image":"i:1","imageID":"unknown","started":false,"ready":false,"restartCount":0,"state":{"waiting":{"reason":"ContainerCreating"}},
	    "allocatedResources":{"cpu":"2"},"resources":{"requests":{"cpu":"2"}},
	    "volumeMounts":[{"name":"data","mountPath":"/other"}]}]}}`)
	for _, kept := range []string{"fd00::5", "started: false", "imageID: unknown", "cpu: \"2\"", "mountPath: /other", "ContainerCreating"} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost %q:\n%s", kept, out)
		}
	}
}

func TestStartedStaysWhenNotRunning(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Pod","status":{"containerStatuses":[{"name":"a","started":true,"state":{"terminated":{"exitCode":0}}}]}}`)
	if !strings.Contains(out, "started: true") {
		t.Errorf("started was dropped for a container that is not running:\n%s", out)
	}
}

// --- metadata ---

func TestMetadataRules(t *testing.T) {
	out, _ := trimmed(t, `{"kind":"Pod","metadata":{"name":"p-1","generateName":"p-","namespace":"ns","uid":"u","resourceVersion":"5","generation":2,"selfLink":"/x",
	  "creationTimestamp":"2026-11-15T09:00:00Z","deletionTimestamp":"2026-11-15T10:00:00Z","deletionGracePeriodSeconds":30,"finalizers":["example.com/protect"],
	  "labels":{"app":"a","pod-template-hash":"abc"},"annotations":{"team":"x"},
	  "ownerReferences":[{"apiVersion":"apps/v1","kind":"ReplicaSet","name":"rs-1","uid":"u2","controller":true,"blockOwnerDeletion":true},
	                     {"apiVersion":"v1","kind":"Node","name":"n1","uid":"u3"}]}}`)
	for _, gone := range []string{"uid", "resourceVersion", "generation", "selfLink", "generateName", "pod-template-hash", "blockOwnerDeletion", "apiVersion: apps"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	for _, kept := range []string{
		"name: p-1", "namespace: ns", "creationTimestamp", "deletionTimestamp", "deletionGracePeriodSeconds: 30", "example.com/protect", "app: a", "team: x",
		"- ReplicaSet/rs-1 (controller)", "- Node/n1",
	} {
		if !strings.Contains(out, kept) {
			t.Errorf("lost %q:\n%s", kept, out)
		}
	}
}

func TestGenerationKeptAndSaidWhenAhead(t *testing.T) {
	o := jsonObj(t, `{"kind":"Deployment","metadata":{"name":"d","generation":5},"status":{"observedGeneration":4}}`)
	rep := Trim(o, false)
	out := YAML(o)
	if !strings.Contains(out, "generation: 5") || !strings.Contains(out, "observedGeneration: 4") {
		t.Errorf("generation not kept:\n%s", out)
	}
	if len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "generation 5") || !strings.Contains(rep.Notes[0], "observedGeneration 4") {
		t.Errorf("notes = %q", rep.Notes)
	}
	same := jsonObj(t, `{"kind":"Deployment","metadata":{"name":"d","generation":5},"status":{"observedGeneration":5,"replicas":1}}`)
	rep = Trim(same, false)
	out = YAML(same)
	if strings.Contains(out, "eneration") || len(rep.Notes) != 0 {
		t.Errorf("equal generations stayed or were reported:\n%s %q", out, rep.Notes)
	}
}

func TestFullKeepsEverythingButManagedFields(t *testing.T) {
	o := jsonObj(t, `{"kind":"Pod","metadata":{"name":"p","uid":"u1","generation":1,"managedFields":[{"manager":"x"}],"labels":{"pod-template-hash":"abc"},
	  "ownerReferences":[{"kind":"ReplicaSet","name":"r","uid":"u2"}],"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},
	  "spec":{"dnsPolicy":"ClusterFirst","containers":[{"name":"c","terminationMessagePath":"/dev/termination-log"}]},
	  "status":{"podIPs":[{"ip":"1.1.1.1"}],"podIP":"1.1.1.1","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"c","containerID":"x://1","started":true,"state":{"running":{}}}]}}`)
	rep := Trim(o, true)
	fullRep := rep
	out := YAML(o)
	for _, kept := range []string{"uid: u1", "generation: 1", "pod-template-hash", "uid: u2", "ClusterFirst", "terminationMessagePath", "podIPs", "containerID", "started: true", "type: Ready"} {
		if !strings.Contains(out, kept) {
			t.Errorf("full lost %q:\n%s", kept, out)
		}
	}
	for _, gone := range []string{"managedFields", "last-applied", "conditions ok"} {
		if strings.Contains(out, gone) {
			t.Errorf("full still has %q:\n%s", gone, out)
		}
	}
	if fullRep.Dropped != 0 {
		t.Errorf("full counted %d dropped fields", fullRep.Dropped)
	}
}

// --- long values ---

func longLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %02d of the file with some text to fill it\n", i)
	}
	return strings.TrimRight(b.String(), "\n")
}

func cutOf(t *testing.T, s string) (string, Report) {
	t.Helper()
	obj := map[string]any{"kind": "Widget", "metadata": map[string]any{"annotations": map[string]any{"a": s}}}
	var rep Report
	CutLong(obj, &rep)
	return obj["metadata"].(map[string]any)["annotations"].(map[string]any)["a"].(string), rep
}

func TestCutLongText(t *testing.T) {
	short := strings.Repeat("x", 300)
	if got, rep := cutOf(t, short); got != short || rep.CutValues != 0 {
		t.Errorf("a value of 300 bytes was cut: %q", got)
	}
	text := longLines(40)
	got, rep := cutOf(t, text)
	if rep.CutValues != 1 {
		t.Errorf("CutValues = %d", rep.CutValues)
	}
	if !strings.HasPrefix(got, "line 00 of the file") || !strings.Contains(got, "line 02") {
		t.Errorf("the first lines are gone: %q", got)
	}
	if strings.Contains(got, "line 39") || len(got) > 300 {
		t.Errorf("not cut: %d bytes", len(got))
	}
	left := len(text) - (len(got) - len(got[strings.LastIndex(got, "\n"):]))
	want := fmt.Sprintf("… [%s bytes, ", Thousands(left))
	if !strings.Contains(got, want) || !strings.Contains(got, " lines left out; full shows all]") {
		t.Errorf("the marker does not say how much was left out (want %q): %q", want, got)
	}
	// 40 lines, the head shows some of them.
	shown := strings.Count(got[:strings.LastIndex(got, "\n")], "\n") + 1
	if !strings.Contains(got, fmt.Sprintf("%d lines left out", 40-shown)) {
		t.Errorf("wrong line count (shown %d): %q", shown, got)
	}
}

func TestCutLongSingleLine(t *testing.T) {
	text := strings.Repeat("alpha beta gamma ", 60)
	got, _ := cutOf(t, text)
	if !strings.HasPrefix(got, "alpha beta gamma") || len(got) > 300 || !strings.Contains(got, "bytes left out; full shows all]") {
		t.Errorf("single line cut: %q", got)
	}
	if strings.Contains(got, "lines") {
		t.Errorf("a single line has no line count: %q", got)
	}
	// Multi-byte characters are never split.
	uni := strings.Repeat("é", 400)
	got, _ = cutOf(t, uni)
	if !strings.Contains(got, "…") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("split a character: %q", got)
	}
}

func TestBase64AndBinaryBecomeMarkers(t *testing.T) {
	b64 := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo=", 20)
	got, rep := cutOf(t, b64)
	if got != fmt.Sprintf("[base64, %s chars]", Thousands(len(b64))) || rep.CutValues != 1 {
		t.Errorf("base64: %q", got)
	}
	bin := string([]byte{0xff, 0xfe, 0x00, 0x01, 0x80}) + "abc"
	got, _ = cutOf(t, bin)
	if got != "[binary data, 8 bytes]" {
		t.Errorf("binary: %q", got)
	}
	// A short text with a color code is text.
	if got, _ := cutOf(t, "\x1b[31mred\x1b[0m"); !strings.Contains(got, "red") {
		t.Errorf("short text with control characters: %q", got)
	}
	// A long hex digest or a long plain word is cut like text, not hidden... but 300 bytes is only a
	// base64-looking string when it has no spaces, so here the rule says base64.
	cm := map[string]any{"kind": "ConfigMap", "binaryData": map[string]any{"logo.png": strings.Repeat("A", 100), "tiny": "AAAA"}}
	var r Report
	CutLong(cm, &r)
	bd := cm["binaryData"].(map[string]any)
	if bd["logo.png"] != "[base64, 100 chars]" || bd["tiny"] != "AAAA" {
		t.Errorf("binaryData: %v", bd)
	}
}

func TestStatusIsNeverCut(t *testing.T) {
	msg := strings.Repeat("panic: runtime error ", 40)
	obj := map[string]any{"kind": "Pod", "status": map[string]any{"containerStatuses": []any{map[string]any{"state": map[string]any{"terminated": map[string]any{"message": msg}}}}}}
	var rep Report
	CutLong(obj, &rep)
	if got := obj["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["state"].(map[string]any)["terminated"].(map[string]any)["message"]; got != msg || rep.CutValues != 0 {
		t.Error("a message of the status was cut")
	}
}

func TestRedactionMarkIsNeverCut(t *testing.T) {
	long := redact.Mark(redact.RuleKeyName)
	got, rep := cutOf(t, long)
	if got != long || rep.CutValues != 0 {
		t.Errorf("a redaction mark was changed: %q", got)
	}
}

func TestConfigMapOutline(t *testing.T) {
	cm := map[string]any{
		"kind": "ConfigMap",
		"data": map[string]any{
			"app.conf":  longLines(60),
			"LOG_LEVEL": "info",
			"flags":     "a\nb\nc",
			"blob":      strings.Repeat("QUJD", 100),
		},
		"binaryData": map[string]any{"logo.png": strings.Repeat("iVBO", 50)},
	}
	var rep Report
	CutLong(cm, &rep)
	got := strings.Join(rep.Outline, "\n")
	for _, want := range []string{"LOG_LEVEL: 4 bytes, 1 line", "flags: 5 bytes, 3 lines", "app.conf: ", ", 60 lines, cut", "blob: base64 or binary, 400 bytes", "logo.png: base64 or binary, 200 bytes"} {
		if !strings.Contains(got, want) {
			t.Errorf("outline lacks %q:\n%s", want, got)
		}
	}
	d := cm["data"].(map[string]any)
	if d["LOG_LEVEL"] != "info" || d["flags"] != "a\nb\nc" {
		t.Errorf("small values were changed: %v", d)
	}
	if !strings.Contains(d["app.conf"].(string), "lines left out; full shows all]") || d["blob"] != "[base64, 400 chars]" {
		t.Errorf("large values: %v", d)
	}
	// All small: no outline.
	small := map[string]any{"kind": "ConfigMap", "data": map[string]any{"a": "1", "b": "2"}}
	var r2 Report
	CutLong(small, &r2)
	if r2.Outline != nil {
		t.Errorf("outline for small data: %v", r2.Outline)
	}
}

func TestOutlineIsCapped(t *testing.T) {
	data := map[string]any{"big": longLines(50)}
	for i := 0; i < 50; i++ {
		data[fmt.Sprintf("k%02d", i)] = "v"
	}
	var rep Report
	CutLong(map[string]any{"kind": "ConfigMap", "data": data}, &rep)
	if len(rep.Outline) != outlineMax+1 || !strings.HasPrefix(rep.Outline[outlineMax], "and 21 more keys") {
		t.Errorf("outline: %d lines, last %q", len(rep.Outline), rep.Outline[len(rep.Outline)-1])
	}
}

// --- redaction before the cut ---

func TestRedactionSeesTheWholeValueBeforeTheCut(t *testing.T) {
	const (
		before = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
		after  = "ghp_ZyXwVuTsRqPoNmLkJiHgFeDcBa9876543210"
	)
	filler := strings.Repeat("a line of configuration that is harmless\n", 12)
	cases := map[string]string{
		"secret in the head":          "token_url: https://x/y?t=" + before + "\n" + filler + "end",
		"secret after the cut point":  filler + "# deploy with " + after + "\nend",
		"both":                        "first " + before + "\n" + filler + "last " + after,
		"secret that crosses the cut": strings.Repeat("x", headBytes-10) + " " + before + " " + strings.Repeat("y ", 200),
		"key name past the cut":       filler + "db_password: hunter2hunter2hunter2\n" + filler,
		"password in a long command":  strings.Repeat("--flag=value ", 20) + "--password=hunter2hunter2 " + strings.Repeat("--other=1 ", 20),
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			obj := map[string]any{
				"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "c", "annotations": map[string]any{"note": val}},
				"data": map[string]any{"file": val},
			}
			red := redact.New(nil)
			Trim(obj, false)
			red.Object(obj)
			var rep Report
			CutLong(obj, &rep)
			out := red.Text(YAML(obj) + "\n" + strings.Join(rep.Outline, "\n"))
			if rep.CutValues == 0 {
				t.Fatal("test setup: nothing was cut")
			}
			for _, secret := range []string{before, after, "hunter2hunter2"} {
				for _, frag := range []string{secret, secret[:12]} {
					if strings.Contains(out, frag) {
						t.Errorf("secret or a part of it (%q) is in the output:\n%s", frag, out)
					}
				}
			}
		})
	}
	// The head still shows that something was redacted.
	obj := map[string]any{"kind": "ConfigMap", "data": map[string]any{"file": "token_url: https://x/y?t=" + before + "\n" + filler}}
	red := redact.New(nil)
	red.Object(obj)
	var rep Report
	CutLong(obj, &rep)
	if !strings.Contains(YAML(obj), "[REDACTED:") {
		t.Errorf("no marker in the head:\n%s", YAML(obj))
	}
}

// --- size ---

func readFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return jsonObj(t, string(b))
}

func fixtures(t *testing.T) map[string]map[string]any {
	pod := readFixture(t, "pod.json")
	dep := readFixture(t, "deployment.json")
	dep["spec"].(map[string]any)["template"].(map[string]any)["spec"] = readFixture(t, "pod.json")["spec"]

	conf := longLines(70) + "\nupstream backend { server api.shop.svc:80; }\n" + strings.Repeat("# tuning\n", 20)
	cm := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": "api-config", "namespace": "shop", "uid": "c0ffee00-0000-4000-8000-000000000003", "resourceVersion": "4412",
			"creationTimestamp": "2026-10-02T08:00:00Z",
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"LOG_LEVEL":"info"}}`,
				"example.com/rendered-from":                        `{"chart":"api","version":"3.2.1","values":{"replicas":3,"image":{"repository":"registry.example.com/shop/api","tag":"2.14.1"},"resources":{"limits":{"cpu":"500m","memory":"512Mi"},"requests":{"cpu":"250m","memory":"256Mi"}},"ingress":{"enabled":true,"hosts":["api.example.com"]}}}`,
			},
		},
		"data": map[string]any{
			"app.conf":  conf,
			"LOG_LEVEL": "info",
			"DB_HOST":   "ledger-db.shop.svc.cluster.local",
			"features":  "checkout-v2\nfast-search",
		},
		"binaryData": map[string]any{"logo.png": strings.Repeat("iVBORw0KGgoAAAANSUhEUgAA", 60)},
	}
	return map[string]map[string]any{
		"pod": pod, "deployment": dep, "service": readFixture(t, "service.json"), "configmap": cm, "node": readFixture(t, "node.json"),
	}
}

func deepCopy(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(m)
	return jsonObj(t, string(b))
}

// sizes renders an object three ways: as kubectl prints it (the object as it
// is, without managedFields), as the output was before the compaction, and
// as it is now by default (with its outline and the line that says what was
// left out).
func sizes(t *testing.T, obj map[string]any) (raw, old, now int, text string) {
	t.Helper()
	r := deepCopy(t, obj)
	if md, ok := r["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
	}
	raw = len(YAML(r))

	o := deepCopy(t, obj)
	legacyTrim(o, false)
	redact.New(nil).Object(o)
	old = len(YAML(o))

	n := deepCopy(t, obj)
	rep := Trim(n, false)
	redact.New(nil).Object(n)
	CutLong(n, &rep)
	text = YAML(n)
	if len(rep.Outline) > 0 {
		text = "data outline:\n- " + strings.Join(rep.Outline, "\n- ") + "\n---\n" + text
	}
	text += fmt.Sprintf("\nleft out: %d default or repeated fields, %d long values; full=true shows them.", rep.Dropped, rep.CutValues)
	return raw, old, len(text), text
}

func TestCompactSizes(t *testing.T) {
	fx := fixtures(t)
	for _, name := range []string{"pod", "deployment", "service", "configmap", "node"} {
		raw, old, now, text := sizes(t, fx[name])
		t.Logf("%-10s kubectl yaml %6d   before %6d (%3d%%)   now %6d (%3d%%)", name, raw, old, 100*old/raw, now, 100*now/raw)
		if now > old+100 { // the note and the outline are part of the new size
			t.Errorf("%s: the new default (%d) is larger than the old output (%d)", name, now, old)
		}
		if (name == "pod" || name == "deployment") && now*2 > raw {
			t.Errorf("%s: the new default is %d bytes, more than half of the %d bytes of kubectl:\n%s", name, now, raw, text)
		}
	}
}

// A pod of the fixture, after: nothing a debugger needs is gone.
func TestCompactPodKeepsWhatADebuggerNeeds(t *testing.T) {
	_, _, _, text := sizes(t, fixtures(t)["pod"])
	for _, want := range []string{
		"name: api-7d9f8b6c5d-x2k4m", "namespace: shop", "creationTimestamp", "app: api", "prometheus.io/port", "ReplicaSet/api-7d9f8b6c5d (controller)",
		"image: registry.example.com/shop/api:2.14.1", "containerPort: 8080", "name: LOG_LEVEL", "value: info", "DB_HOST", "name: api-db", "key: password",
		"fieldPath: metadata.name", "cpu: 500m", "memory: 512Mi", "mountPath: /etc/api", "mountPath: /tmp", "path: /healthz", "path: /ready",
		"initialDelaySeconds: 15", "failureThreshold: 30", "periodSeconds: 5", "readOnlyRootFilesystem: true", "args:", "--sink",
		"name: config", "name: scratch", "emptyDir: {}", "nodeName: worker-2", "serviceAccountName: api",
		"conditions ok: PodReadyToStartContainers, Initialized, Ready, ContainersReady, PodScheduled",
		"restartCount: 0", "ready: true", "imageDigest: sha256:3b7e1c0f9a5d", "running:", "phase: Running", "podIP: 10.244.2.17", "hostIP: 10.0.4.12", "qosClass: Burstable",
		"left out: ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lost %q:\n%s", want, text)
		}
	}
	for _, gone := range []string{"uid:", "resourceVersion", "containerID", "kube-api-access", "terminationMessage", "dnsPolicy", "default-scheduler", "protocol: TCP"} {
		if strings.Contains(text, gone) {
			t.Errorf("still has %q", gone)
		}
	}
}

func TestDefaultRowsNameKnownKinds(t *testing.T) {
	// A row names a kind and a path that the trimming can reach: a typo in a
	// kind would make the row do nothing and the test above would still pass
	// for it, so check the kinds against the ones that the tool reads.
	known := map[string]bool{}
	for k := range podSpecRoots {
		known[k] = true
	}
	for _, k := range []string{"Service", "HorizontalPodAutoscaler", "PersistentVolumeClaim", "Node", "ConfigMap"} {
		known[k] = true
	}
	for _, d := range DefaultRows() {
		if !known[d.Kind] {
			t.Errorf("row %s %s names an unknown kind", d.Kind, d.Path)
		}
	}
}
