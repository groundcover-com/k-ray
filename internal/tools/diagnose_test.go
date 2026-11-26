package tools

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// Acceptance tests for diagnosis: each broken scenario in testdata must get
// its likely cause from one describe call.

func podMetric(ns, name string, containers map[string][2]string) unstructured.Unstructured {
	var cs []any
	for c, u := range containers {
		cs = append(cs, map[string]any{"name": c, "usage": map[string]any{"cpu": u[0], "memory": u[1]}})
	}
	return unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name, "namespace": ns}, "containers": cs}}
}

func diagFixture(t *testing.T) *fakeReader {
	f := fixture(t)
	f.metrics[kube.PodMetrics] = append(f.metrics[kube.PodMetrics],
		podMetric("pay", "hot-1", map[string][2]string{"render": {"980m", "100Mi"}}),
		podMetric("pay", "fat-1", map[string][2]string{"batch": {"20m", "200Mi"}}),
		podMetric("pay", "nolimit-1", map[string][2]string{"cache": {"50m", "300Mi"}}),
		podMetric("pay", "checkout-v2-a", map[string][2]string{"web": {"40m", "90Mi"}}),
	)
	f.logs["pay/crash-1/app#previous"] = "2026-11-13T11:57:00Z starting ledger\n" +
		"2026-11-13T11:57:01Z connecting with password=crashSecret9\n" +
		"2026-11-13T11:57:02Z FATAL config: DATABASE_URL is not set\n"
	return f
}

func describe(t *testing.T, r *Runner, kind, name, ns string) string {
	t.Helper()
	res := call(t, r, "describe", map[string]any{"kind": kind, "name": name, "namespace": ns})
	if res.IsError {
		t.Fatalf("describe %s %s: %s", kind, name, res.Text)
	}
	mustNotContain(t, res.Text, "crashSecret9")
	return res.Text
}

func TestDiagnoseOOMKill(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "api-7f9c8-x2k", "shop")
	mustContain(t, out,
		"likely cause:",
		"container api is OOM killed at its memory limit of 1024Mi (14 restarts)",
		"Memory use now: 900Mi (88% of the limit)",
		"resources (usage now, from the metrics API):",
		"container api: CPU 250m (25% of 1000m limit), memory 900Mi (88% of 1024Mi limit): near OOM",
		"container sidecar: CPU 10m, memory 50Mi: no requests or limits",
		"containers: api (registry.example/app:1.2), sidecar (registry.example/proxy:2)",
	)
	mustNotContain(t, out, "apiVersion:")
	if strings.Index(out, "likely cause:") > strings.Index(out, "owner chain:") {
		t.Errorf("the cause must come before the details:\n%s", out)
	}
}

func TestDiagnoseUnschedulable(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "worker-1", "shop")
	mustContain(t, out,
		"likely cause:",
		"the pod requests 20480Mi memory, more than any node can offer (largest: node-a with 16384Mi allocatable)",
		"It cannot fit on the current nodes",
	)
}

func TestDiagnoseNodesFull(t *testing.T) {
	f := diagFixture(t)
	for i := range f.objs {
		if f.objs[i].GetName() == "worker-1" {
			cs, _, _ := unstructured.NestedSlice(f.objs[i].Object, "spec", "containers")
			cs[0].(map[string]any)["resources"] = map[string]any{"requests": map[string]any{"cpu": "500m", "memory": "12Gi"}}
			_ = unstructured.SetNestedSlice(f.objs[i].Object, cs, "spec", "containers")
		}
	}
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "worker-1", "shop")
	// node-a: 16384Mi allocatable, 512+128+128+512+4096 = 5376Mi requested.
	mustContain(t, out, "the pod requests 12288Mi memory; the most free on any node is 11008Mi (node-a: 16384Mi allocatable, 5376Mi requested by other pods)")
}

func TestDiagnoseServiceSelectorMismatch(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "service", "checkout", "pay")
	mustContain(t, out,
		"service has no ready endpoints",
		"likely cause:",
		"selector app=checkout matches 0 pods in namespace pay",
		"Pods with other values for the same label: app=checkout-v2 (2 pods).",
		"type ClusterIP, ports 80→8080, selector app=checkout",
	)
}

func TestDiagnoseServicePodsNotReady(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "service", "web", "pay")
	mustContain(t, out, "service has no ready endpoints", "likely cause:", "selector app=web matches 1 pod, but none is ready: pod/probe-1: container web: running but not ready")
}

func TestDiagnoseImagePull(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "img-1", "pay")
	mustContain(t, out,
		"likely cause:",
		"container pay cannot pull its image: the image or tag registry.example/pay:1.4.2 does not exist in the registry",
	)
}

func TestDiagnoseProbeFailure(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "probe-1", "pay")
	mustContain(t, out,
		"likely cause:",
		"container web is not ready because its readiness probe fails (×42, last 50s ago): HTTP probe failed with statuscode: 503",
		"Probe: HTTP GET /ready on port 8080",
	)
}

func TestDiagnoseCrashShowsLastCrashLogs(t *testing.T) {
	f := diagFixture(t)
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "crash-1", "pay")
	mustContain(t, out,
		"likely cause:",
		"container app exits with code 1 (application error), 7 restarts. Last error before the exit: FATAL config: DATABASE_URL is not set",
		"last crash of container app (previous instance):",
		"FATAL config: DATABASE_URL is not set",
		"password=[REDACTED:key-name]",
	)
	found := false
	for _, c := range f.calls {
		if c == "logs pay/crash-1 app previous=true" {
			found = true
		}
	}
	if !found {
		t.Errorf("previous logs not read: %v", f.calls)
	}
}

func TestDiagnoseWorkloadNamesThePodCause(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "deployment", "api", "shop")
	mustContain(t, out, "likely cause:", "pod/api-7f9c8-x2k: container api is OOM killed at its memory limit of 1024Mi")
}

func TestDescribeHealthyPodHasNoCause(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "checkout-v2-a", "pay")
	mustContain(t, out, "looks wrong: nothing found", "container web: CPU 40m (40% of 100m request, no limit), memory 90Mi (70% of 128Mi request, no limit): no memory limit")
	mustNotContain(t, out, "likely cause:")
}

func TestTopVerdicts(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "top", map[string]any{"allNamespaces": true}).Text
	mustContain(t, out, "VERDICT", "verdicts: 1 CPU at limit, 1 near OOM")
	for pod, verdict := range map[string]string{
		"hot-1":         "CPU at limit",
		"api-7f9c8-x2k": "near OOM",
		"fat-1":         "over-provisioned",
		"nolimit-1":     "no requests or limits",
	} {
		ok := false
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, pod) && strings.Contains(l, verdict) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("no %q verdict for %s:\n%s", verdict, pod, out)
		}
	}
	if strings.Index(out, "hot-1") > strings.Index(out, "fat-1") {
		t.Errorf("highest pressure not first:\n%s", out)
	}
}

func TestListWarnsBeforeTheCrash(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}).Text
	var hot string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "hot-1 ") {
			hot = l
		}
	}
	if !strings.Contains(hot, "container render: CPU at 98% of the 1000m limit, throttling likely") {
		t.Errorf("hot-1 row: %q\n%s", hot, out)
	}
	// Info verdicts (over-provisioned, no limits) are not problems in list.
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "fat-1 ") || strings.HasPrefix(l, "nolimit-1 ") {
			t.Errorf("info verdict made a row: %q", l)
		}
	}
}

func TestDiagnosisWorksWithoutMetrics(t *testing.T) {
	f := diagFixture(t)
	f.metricsErr = kube.ErrNoMetrics
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "api-7f9c8-x2k", "shop")
	mustContain(t, out, "container api is OOM killed at its memory limit of 1024Mi (14 restarts)", "resources: usage not available (no metrics API)")
	mustNotContain(t, out, "Memory use now")
	if res := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}); res.IsError {
		t.Fatal(res.Text)
	}
}

// A pod that no node accepts because of taints and a node selector: the
// cause must give the scheduler's own reasons in full and what the pod asks
// for. It must not say that a node has room.
func TestDiagnoseUnschedulableByTaintsAndSelector(t *testing.T) {
	f := diagFixture(t)
	msg := "0/14 nodes are available: 1 node(s) had untolerated taint {ci: true}, 3 node(s) had untolerated taint {main: true}, 3 node(s) had untolerated taint {gpu: reserved}, 7 node(s) didn't match Pod's node affinity/selector. preemption: 0/14 nodes are available: 14 Preemption is not helpful for scheduling."
	f.objs = append(f.objs, unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "proxy-1", "namespace": "shop", "uid": "pod-proxy-1", "creationTimestamp": "2026-11-13T09:00:00Z"},
		"spec": map[string]any{
			"containers":   []any{map[string]any{"name": "proxy", "image": "proxy:1", "resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "256Mi"}}}},
			"nodeSelector": map[string]any{"pool": "edge"},
			"tolerations": []any{
				map[string]any{"key": "node.kubernetes.io/not-ready", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": int64(300)},
				map[string]any{"key": "edge", "operator": "Equal", "value": "true", "effect": "NoSchedule"},
			},
		},
		"status": map[string]any{"phase": "Pending", "conditions": []any{map[string]any{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": msg}}},
	}})
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "proxy-1", "shop")
	mustContain(t, out,
		"likely cause:",
		"no node accepts the pod (0 of 14)",
		"3 node(s) had untolerated taint {main: true}",
		"3 node(s) had untolerated taint {gpu: reserved}",
		"7 node(s) didn't match Pod's node affinity/selector",
		"The pod tolerates only: edge=true:NoSchedule",
		"The pod selects nodes with: pool=edge",
	)
	mustNotContain(t, out, "has room for the pod", "names another reason", "Preemption is not helpful")
}

// A container that the kubelet restarts after liveness probe failures stops
// cleanly, with exit code 0. The cause must still name the probe.
func TestDiagnoseRestartsByLivenessProbe(t *testing.T) {
	f := diagFixture(t)
	f.objs = append(f.objs,
		unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": "cart-1", "namespace": "shop", "uid": "pod-cart-1", "creationTimestamp": "2026-11-13T09:00:00Z"},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "cart", "image": "cart:1"}}},
			"status": map[string]any{
				"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
				"containerStatuses": []any{map[string]any{
					"name": "cart", "ready": true, "restartCount": int64(2986),
					"state":     map[string]any{"running": map[string]any{"startedAt": "2026-11-13T11:50:00Z"}},
					"lastState": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "reason": "Completed", "startedAt": "2026-11-13T08:00:00Z", "finishedAt": "2026-11-13T11:49:58Z"}},
				}},
			},
		}},
		unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Event", "type": "Warning", "reason": "Unhealthy", "count": int64(17),
			"message":        `Liveness probe failed: command timed out: "/bin/grpc_health_probe -addr=:7070" timed out after 1s`,
			"lastTimestamp":  "2026-11-13T11:59:00Z",
			"involvedObject": map[string]any{"kind": "Pod", "name": "cart-1", "namespace": "shop", "uid": "pod-cart-1"},
			"metadata":       map[string]any{"name": "cart-1.1", "namespace": "shop", "creationTimestamp": "2026-11-13T11:59:00Z"},
		}})
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "cart-1", "shop")
	mustContain(t, out,
		"likely cause:",
		"container cart is restarted by the kubelet because its liveness probe fails (×17",
		"timed out after 1s",
		"2,986 restarts",
		"exit code 0",
	)
}
