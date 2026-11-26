package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Tests for the cause on the row, describe that finds things, and events by
// reason. They come from an agent benchmark: agents filter every result with
// grep, head or tail, and every extra call reads the whole context again.

// mkRestarted builds a pod that runs and is ready now but restarted many
// times. last is the last termination state of its container.
func mkRestarted(ns, name, owner string, restarts int64, last map[string]any, memLimit string) unstructured.Unstructured {
	p := mkPod(ns, name, "ReplicaSet", owner, map[string]any{"app": owner}, "Running")
	cs := p.Object["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
	cs["restartCount"] = restarts
	cs["lastState"] = map[string]any{"terminated": last}
	cs["lastTerminationState"] = map[string]any{"terminated": last}
	if memLimit != "" {
		ctr := p.Object["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		ctr["resources"] = map[string]any{"limits": map[string]any{"memory": memLimit}}
	}
	return p
}

func mkEvent(ns, pod, uid, typ, reason, msg string, count int64) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"name": pod + "." + reason, "namespace": ns, "creationTimestamp": "2026-11-13T11:00:00Z"},
		"involvedObject": map[string]any{"kind": "Pod", "name": pod, "uid": uid, "namespace": ns},
		"type":           typ, "reason": reason, "message": msg, "count": count,
		"lastTimestamp": "2026-11-13T11:58:00Z",
	}}
}

func restartFixture() *fakeReader {
	f := &fakeReader{logs: map[string]string{}}
	for i := 0; i < 10; i++ {
		f.objs = append(f.objs, mkPod("shop", fmt.Sprintf("web-5d7c9-%02d", i), "ReplicaSet", "web-5d7c9", map[string]any{"app": "web"}, "Running"))
	}
	f.objs = append(f.objs, mkPod("shop", "worker-0", "StatefulSet", "worker", map[string]any{"app": "worker"}, "Crash"))
	finished := "2026-11-13T09:30:00Z"
	exit0 := map[string]any{"exitCode": int64(0), "reason": "Completed", "startedAt": "2026-11-13T09:00:00Z", "finishedAt": finished}
	// Two pods of one workload restarted by the kubelet after liveness failures.
	for _, n := range []string{"cart-6f8d4-aaaaa", "cart-6f8d4-bbbbb"} {
		f.objs = append(f.objs, mkRestarted("shop", n, "cart-6f8d4", 2986, exit0, ""))
		f.objs = append(f.objs, mkEvent("shop", n, "uid-"+n, "Warning", "Unhealthy", "Liveness probe failed: HTTP probe failed with statuscode: 500", 40))
	}
	oom := map[string]any{"exitCode": int64(137), "reason": "OOMKilled", "startedAt": "2026-11-13T08:00:00Z", "finishedAt": "2026-11-13T08:00:05Z"}
	f.objs = append(f.objs, mkRestarted("shop", "importer-7c9-xyz12", "importer-7c9", 12, oom, "64Mi"))
	f.objs = append(f.objs, mkRestarted("shop", "proxy-5d-qqqqq", "proxy-5d", 5, map[string]any{"exitCode": int64(255), "reason": "Error"}, ""))
	// One restarted pod whose events are gone.
	f.objs = append(f.objs, mkRestarted("shop", "quiet-6b-zzzzz", "quiet-6b", 4, exit0, ""))
	return f
}

// A pod that only restarted has its own row, and the row says why.
func TestRestartedPodsHaveTheirOwnRowWithTheCause(t *testing.T) {
	f := restartFixture()
	r, _ := newRunner(t, f)
	out := call(t, r, "list", map[string]any{"kind": "pods"}).Text
	mustNotContain(t, out, "restarted, running and ready now")

	cart := lineWith(t, out, "cart-6f8d4-aaaaa")
	mustContain(t, cart, "(+1 like it)", "restarted by the kubelet because its liveness probe fails", "HTTP probe failed with statuscode: 500")
	mustContain(t, lineWith(t, out, "importer-7c9-xyz12"), "OOM killed at its memory limit of 64Mi", "12 restarts")
	mustContain(t, lineWith(t, out, "proxy-5d-qqqqq"), "exits with code 255", "5 restarts")
	mustContain(t, lineWith(t, out, "quiet-6b-zzzzz"), "restarted 4 times", "last exit code was 0")

	// Order: unhealthy, then the rows of pods that only restarted.
	if strings.Index(out, "worker-0") > strings.Index(out, "cart-6f8d4-aaaaa") {
		t.Errorf("an unhealthy pod comes after a restarted one:\n%s", out)
	}
	// The healthy pods are still one summary line.
	mustContain(t, out, "healthy, not listed (10): web x10")
}

// Events are read once for the whole list, not once for each pod, and the
// light diagnosis reads no logs, no nodes and no ConfigMaps.
func TestListReadsEventsOnceAndNoLogsForRestartedPods(t *testing.T) {
	f := restartFixture()
	f.objs = f.objs[:0]
	for _, o := range restartFixture().objs {
		if o.GetName() != "worker-0" { // no crashed pod: only the light diagnosis runs
			f.objs = append(f.objs, o)
		}
	}
	r, _ := newRunner(t, f)
	call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true})
	counts := map[string]int{}
	for _, c := range f.calls {
		counts[strings.SplitN(c, " ", 2)[0]]++
	}
	if counts["events"] != 1 || counts["logs"] != 0 || counts["list"] != 1 || counts["get"] != 0 {
		t.Errorf("reads: %v\n%v", counts, f.calls)
	}
	// With a pod that crashes, the deeper diagnosis reads that pod's logs,
	// but still not its events again.
	f2 := restartFixture()
	r2, _ := newRunner(t, f2)
	call(t, r2, "list", map[string]any{"kind": "pods", "allNamespaces": true})
	n := 0
	for _, c := range f2.calls {
		if strings.HasPrefix(c, "events ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d event reads with a crashed pod: %v", n, f2.calls)
	}
}

// The grep check: whatever a text filter keeps of a row must keep its cause.
func TestEveryRowIsOneLineWithItsCause(t *testing.T) {
	r, _ := newRunner(t, restartFixture())
	out := call(t, r, "list", map[string]any{"kind": "pods"}).Text
	for _, name := range []string{"worker-0", "cart-6f8d4-aaaaa", "importer-7c9-xyz12", "proxy-5d-qqqqq", "quiet-6b-zzzzz"} {
		l := lineWith(t, out, name)
		if !strings.Contains(l, "container ") {
			t.Errorf("the line of %s has no cause: %q", name, l)
		}
		if len(l) > 520 {
			t.Errorf("the line of %s is %d bytes", name, len(l))
		}
	}
}

func TestCompactCause(t *testing.T) {
	long := strings.Repeat("word ", 80)
	cases := []struct {
		in, want string
		max      int
	}{
		{
			"container app exits with code 1 (application error), 7 restarts. Last error before the exit: FATAL config: DATABASE_URL is not set",
			"container app exits with code 1 (application error), 7 restarts; last error: FATAL config: DATABASE_URL is not set", 160,
		},
		{
			"container app is OOM killed at its memory limit of 64Mi (3 restarts). Memory use now: 60Mi (93% of the limit). The limit is too low for this workload, or the container leaks memory.",
			"container app is OOM killed at its memory limit of 64Mi (3 restarts); memory use now: 60Mi (93% of the limit)", 120,
		},
	}
	for _, c := range cases {
		if got := compactCause(c.in, c.max); got != c.want {
			t.Errorf("compactCause\n got: %q\nwant: %q", got, c.want)
		}
	}
	// A long quote keeps its start and its end, and the line stays short.
	q := "container app exits with code 1 (application error), 7 restarts. Last error before the exit: level=FATAL svc=ledger msg=\"database connection failed\" request_id=abc " + long + " error=\"password authentication failed for user ledger_app\""
	got := compactCause(q, 240)
	if len(got) > 240 || !strings.Contains(got, `msg="database connection failed"`) || !strings.HasSuffix(got, `password authentication failed for user ledger_app"`) || !strings.Contains(got, " ... ") {
		t.Errorf("long quote (%d bytes): %q", len(got), got)
	}
}

// A Python traceback: the cause quotes the exception line, the last line of
// the traceback, and the last application frame.
func TestLastErrorOfATracebackIsItsExceptionLine(t *testing.T) {
	f := diagFixture(t)
	f.logs["pay/crash-1/app#previous"] = `2026-11-13T11:57:00Z INFO consumer starting
2026-11-13T11:57:01Z Traceback (most recent call last):
2026-11-13T11:57:01Z   File "/usr/local/lib/python3.11/site-packages/aiokafka/client.py", line 100, in bootstrap
2026-11-13T11:57:01Z     raise err
2026-11-13T11:57:01Z   File "/app/consumer/main.py", line 42, in run
2026-11-13T11:57:01Z     await client.bootstrap()
2026-11-13T11:57:01Z aiokafka.errors.LeaderNotAvailableError: [Error 5] LeaderNotAvailableError
`
	r, _ := newRunner(t, f)
	d := describe(t, r, "pod", "crash-1", "pay")
	mustContain(t, d, "Last error before the exit: aiokafka.errors.LeaderNotAvailableError: [Error 5] LeaderNotAvailableError at /app/consumer/main.py:42 (run)")
	mustNotContain(t, d, "Last error before the exit: Traceback")
	list := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}).Text
	mustContain(t, lineWith(t, list, "crash-1"), "last error: aiokafka.errors.LeaderNotAvailableError: [Error 5] LeaderNotAvailableError at /app/consumer/main.py:42 (run)")
}

// The last error of a Java trace names its last cause.
func TestLastErrorOfAJavaTraceNamesTheCause(t *testing.T) {
	f := diagFixture(t)
	f.logs["pay/crash-1/app#previous"] = fmt.Sprintf(javaTrace, 1)
	r, _ := newRunner(t, f)
	d := describe(t, r, "pod", "crash-1", "pay")
	mustContain(t, d, "Caused by: java.net.ConnectException: Connection to gateway.example:443 timed out")
	mustNotContain(t, d, "hunter2secretvalue99")
}

// --- describe with a label selector ---

func mkDeployment(ns, name string, labels map[string]any, sel map[string]any) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "dep-" + name, "labels": labels, "creationTimestamp": "2026-11-13T09:00:00Z"},
		"spec":     map[string]any{"replicas": int64(2), "selector": map[string]any{"matchLabels": sel}, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "registry.example/" + name + ":1"}}}}},
		"status":   map[string]any{"replicas": int64(2), "readyReplicas": int64(2), "availableReplicas": int64(2), "updatedReplicas": int64(2)},
	}}
}

func mkReplicaSet(ns, name, deployment string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": name, "namespace": ns, "uid": "o-" + name, "creationTimestamp": "2026-11-13T09:00:00Z",
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": deployment, "uid": "dep-" + deployment, "controller": true}},
		},
		"spec":   map[string]any{"replicas": int64(2)},
		"status": map[string]any{"replicas": int64(2), "readyReplicas": int64(2)},
	}}
}

// workload adds a Deployment, its ReplicaSet and n pods.
func workload(f *fakeReader, ns, name string, n int, labels map[string]any) {
	f.objs = append(f.objs, mkDeployment(ns, name, labels, labels), mkReplicaSet(ns, name+"-5d7c9", name))
	for i := 0; i < n; i++ {
		f.objs = append(f.objs, mkPod(ns, fmt.Sprintf("%s-5d7c9-%02d", name, i), "ReplicaSet", name+"-5d7c9", labels, "Running"))
	}
}

func TestDescribeBySelector(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	workload(f, "shop", "web", 5, map[string]any{"app": "web", "tier": "front"})
	workload(f, "shop", "edge", 2, map[string]any{"app": "edge", "tier": "front"})
	f.objs = append(f.objs, mkPod("shop", "solo-0", "", "", map[string]any{"app": "solo"}, "Running"))
	for i := 0; i < 5; i++ {
		workload(f, "shop", fmt.Sprintf("job%d", i), 1, map[string]any{"app": fmt.Sprintf("job%d", i), "tier": "back"})
	}
	workload(f, "other", "web", 1, map[string]any{"app": "web", "tier": "front"})
	r, _ := newRunner(t, f)

	// One match: describe it.
	one := call(t, r, "describe", map[string]any{"kind": "pod", "labelSelector": "app=solo"})
	if one.IsError {
		t.Fatal(one.Text)
	}
	mustContain(t, one.Text, "describe pod solo-0", "state: healthy")

	// Pods of one workload: the workload, once.
	wl := call(t, r, "describe", map[string]any{"kind": "pod", "labelSelector": "app=web"}).Text
	mustContain(t, wl, "describe deployment web", "selector app=web matches 5 pods of deployment/web", "pods (selector app=web,tier=front): 5, 0 with problems")
	if strings.Count(wl, "describe deployment") != 1 {
		t.Errorf("the workload is described more than once:\n%s", wl)
	}

	// Two workloads: each is described.
	two := call(t, r, "describe", map[string]any{"kind": "pod", "labelSelector": "tier=front"}).Text
	mustContain(t, two, "matches 2 objects: deployment/edge, deployment/web", "== deployment/edge · namespace shop", "== deployment/web · namespace shop", "pods (selector app=edge,tier=front): 2", "pods (selector app=web,tier=front): 5")

	// All namespaces.
	all := call(t, r, "describe", map[string]any{"kind": "deployment", "labelSelector": "app=web", "allNamespaces": true}).Text
	mustContain(t, all, "== deployment/web · namespace other", "== deployment/web · namespace shop")

	// More than three: the list, with a note that says how to describe one.
	many := call(t, r, "describe", map[string]any{"kind": "deployment", "labelSelector": "tier=back"}).Text
	mustContain(t, many, "list deployments · namespace shop", "selector tier=back", "5 objects match, more than 3", "describe deployment job0")
	mustNotContain(t, many, "== deployment/")

	// Pods of five workloads are five targets: the list of pods.
	pods := call(t, r, "describe", map[string]any{"kind": "pod", "labelSelector": "tier=back"}).Text
	mustContain(t, pods, "list pods · namespace shop", "5 objects match")

	none := call(t, r, "describe", map[string]any{"kind": "pod", "labelSelector": "app=nothing"}).Text
	mustContain(t, none, "no objects found")

	for _, args := range []map[string]any{
		{"labelSelector": "app=web"},                              // no kind
		{"kind": "pod", "name": "x", "labelSelector": "app=web"},  // both
		{"kind": "pod", "labelSelector": "app in (web"},           // does not parse
		{"kind": "secrets", "labelSelector": "app=web"},           // never
		{"kind": "pod", "labelSelector": "app=web", "bogus": "x"}, // strict
		{"kind": "pod", "labelSelector": "app=web", "allNamespaces": "yes"},
	} {
		if res := call(t, r, "describe", args); !res.IsError {
			t.Errorf("%v must be refused:\n%s", args, res.Text)
		}
	}
	for _, c := range f.calls {
		if strings.Contains(c, "secret") {
			t.Errorf("a read touched secrets: %s", c)
		}
	}
}

// --- describe by name alone ---

func TestDescribeByNameAlone(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	workload(f, "shop", "storefront", 2, map[string]any{"app": "storefront"})
	f.objs = append(f.objs, unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "storefront", "namespace": "shop"},
		"spec": map[string]any{"selector": map[string]any{"app": "storefront"}, "ports": []any{map[string]any{"port": int64(80)}}},
	}})
	workload(f, "shop-a", "cart-api", 1, map[string]any{"app": "cart-api"})
	workload(f, "shop-a", "checkout", 1, map[string]any{"app": "checkout"})
	workload(f, "shop-b", "checkout", 1, map[string]any{"app": "checkout"})
	f.objs = append(f.objs, mkPod("shop", "solo-0", "", "", map[string]any{"app": "solo"}, "Running"))
	// Pods whose workload is not an allowed kind: found as pods, then as their owner.
	for i := 0; i < 2; i++ {
		f.objs = append(f.objs, mkPod("shop-b", fmt.Sprintf("batcher-5d7c9-%02d", i), "ReplicaSet", "batcher-5d7c9", map[string]any{"app": "batcher"}, "Running"))
	}
	f.objs = append(f.objs, mkReplicaSet("shop-b", "batcher-5d7c9", "batcher"), mkDeployment("shop-b", "batcher-deploy", map[string]any{"app": "batcher"}, map[string]any{"app": "batcher"}))
	// Pods whose workload has another name: the pods are found, and their workload is described once.
	for i := 0; i < 2; i++ {
		f.objs = append(f.objs, mkPod("shop-b", fmt.Sprintf("ghost-5d7c9-%02d", i), "ReplicaSet", "ghost-5d7c9", map[string]any{"app": "phantom"}, "Running"))
	}
	f.objs = append(f.objs, mkReplicaSet("shop-b", "ghost-5d7c9", "phantom"), mkDeployment("shop-b", "phantom", map[string]any{"app": "phantom"}, map[string]any{"app": "phantom"}))
	r, _ := newRunner(t, f)

	desc := func(args map[string]any) string {
		t.Helper()
		res := call(t, r, "describe", args)
		if res.IsError {
			t.Fatalf("%v: %s", args, res.Text)
		}
		return res.Text
	}

	// Exact name in another namespace than the default: the workload is
	// described, and the service of the same name is named.
	out := desc(map[string]any{"name": "storefront"})
	mustContain(t, out, "describe deployment storefront", "found by name: deployment/storefront · namespace shop", "also matched:", "service/storefront in namespace shop", "pods (selector app=storefront): 2")

	// The same name in two namespaces and only one has a problem: every match is
	// shown. The match in the context namespace (shop) comes first, then the
	// ones with problems. The question may be about the other.
	f.objs = append(f.objs, mkDeployment("shop-c", "storefront", map[string]any{"app": "storefront"}, map[string]any{"app": "storefront"}))
	for i := range f.objs {
		if f.objs[i].GetKind() == "Deployment" && f.objs[i].GetNamespace() == "shop-c" {
			_ = unstructured.SetNestedField(f.objs[i].Object, int64(0), "status", "availableReplicas")
			_ = unstructured.SetNestedField(f.objs[i].Object, int64(0), "status", "readyReplicas")
		}
	}
	bad := desc(map[string]any{"name": "storefront"})
	mustContain(t, bad, "found by name: 3 objects match storefront in 2 namespaces; all are described below (problems first)",
		"== deployment/storefront in namespace shop-c", "== deployment/storefront in namespace shop", "== service/storefront in namespace shop")
	if strings.Index(bad, "namespace shop-c") < strings.Index(bad, "== deployment/storefront in namespace shop\n") {
		t.Errorf("the match in the context namespace must come first:\n%s", bad)
	}
	f.objs = f.objs[:len(f.objs)-1]

	// A name that starts with the name and a dash.
	pre := desc(map[string]any{"name": "cart"})
	mustContain(t, pre, "describe deployment cart-api")
	mustNotContain(t, pre, "found by name") // one object, no workload of pods, no other match: the title says it all

	// The same name in two namespaces: both are described, as short blocks.
	amb := desc(map[string]any{"name": "checkout"})
	mustContain(t, amb, "found by name: 2 objects match checkout in 2 namespaces; all are described below", "== deployment/checkout in namespace shop-a", "== deployment/checkout in namespace shop-b")
	mustNotContain(t, amb, "owner chain", "containers:", "none is clearly the one")
	// A namespace picks one.
	mustContain(t, desc(map[string]any{"name": "checkout", "namespace": "shop-b"}), "describe deployment checkout · namespace shop-b")

	// A pod without a workload.
	mustContain(t, desc(map[string]any{"name": "solo-0"}), "describe pod solo-0")
	mustContain(t, desc(map[string]any{"name": "solo"}), "describe pod solo-0")

	// Pods found by their name: their workload is described once.
	batch := desc(map[string]any{"name": "batcher"})
	mustContain(t, batch, "describe deployment batcher-deploy")
	_ = batch

	ghost := desc(map[string]any{"name": "ghost"})
	mustContain(t, ghost, "describe deployment phantom", "found by name: deployment/phantom · namespace shop-b (the workload of 2 pods)")
	mustNotContain(t, ghost, "also matched")

	// With a kind and allNamespaces.
	mustContain(t, desc(map[string]any{"kind": "deployment", "name": "cart-api", "allNamespaces": true}), "describe deployment cart-api")

	// Nothing: say so and name the closest names.
	none := desc(map[string]any{"name": "chekout"})
	mustContain(t, none, "no object named chekout", "closest names:", "deployment shop-a/checkout", "deployment shop-b/checkout")
	nothing := desc(map[string]any{"name": "zzzzzz"})
	mustContain(t, nothing, "no object named zzzzzz", "looked at deployments, statefulsets, daemonsets, jobs, cronjobs, services and pods")
	mustNotContain(t, nothing, "closest names")

	// The reads are bounded: one metadata list for each fixed kind, and pods
	// only when nothing else matched. No Secret, no node, and no full list
	// of a kind in all namespaces.
	f.calls = nil
	desc(map[string]any{"name": "storefront"})
	lists := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "list ") && strings.HasSuffix(c, "  ") {
			t.Errorf("a full list of a kind in all namespaces: %s", c)
		}
		if strings.HasPrefix(c, "listmeta ") {
			lists++
			if strings.Contains(c, "pods") || strings.Contains(c, "secret") || strings.Contains(c, "nodes") {
				t.Errorf("unneeded read: %s", c)
			}
		}
	}
	if lists != 6 {
		t.Errorf("%d lists, want 6: %v", lists, f.calls)
	}

	// Invalid names are refused before any read.
	f.calls = nil
	if res := call(t, r, "describe", map[string]any{"name": "Not A Name"}); !res.IsError {
		t.Errorf("a bad name must be refused: %s", res.Text)
	}
	if len(f.calls) != 0 {
		t.Errorf("reads for a bad name: %v", f.calls)
	}
	if res := call(t, r, "describe", map[string]any{}); !res.IsError {
		t.Errorf("describe without arguments must be refused")
	}
	if res := call(t, r, "describe", map[string]any{"kind": "pod"}); !res.IsError || !strings.Contains(res.Text, "name is required") {
		t.Errorf("describe with a kind and no name: %s", res.Text)
	}
}

// A name alone is audited like the other arguments.
func TestDescribeArgumentsAreAudited(t *testing.T) {
	r, auditPath := newRunner(t, bigFixture())
	call(t, r, "describe", map[string]any{"name": "web-5d7c9-00", "allNamespaces": true})
	call(t, r, "describe", map[string]any{"kind": "pod", "labelSelector": "app=web"})
	b, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"allNamespaces":true`, `"name":"web-5d7c9-00"`, `"labelSelector":"app=web"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit line lacks %s:\n%s", want, b)
		}
	}
}

// --- events by reason ---

func TestEventsByReason(t *testing.T) {
	r, auditPath := newRunner(t, diagFixture(t))
	all := call(t, r, "events", map[string]any{"namespace": "pay"}).Text
	mustContain(t, all, "FailedAttachVolume", "Unhealthy")
	one := call(t, r, "events", map[string]any{"namespace": "pay", "reason": "Unhealthy"}).Text
	mustContain(t, one, "reason Unhealthy", "Unhealthy", "Readiness probe failed")
	mustNotContain(t, one, "FailedAttachVolume")
	none := call(t, r, "events", map[string]any{"namespace": "pay", "reason": "Evicted"}).Text
	mustContain(t, none, "no events found for these filters")
	for _, bad := range []any{"bad reason", "x;y", 5, strings.Repeat("a", 80)} {
		if res := call(t, r, "events", map[string]any{"reason": bad}); !res.IsError {
			t.Errorf("reason %v must be refused: %s", bad, res.Text)
		}
	}
	b, _ := os.ReadFile(auditPath)
	if !strings.Contains(string(b), `"reason":"Unhealthy"`) {
		t.Errorf("audit line lacks the reason:\n%s", b)
	}
}

// A name in a cause keeps its capital letter after the semicolon.
func TestCompactCauseKeepsNames(t *testing.T) {
	in := `container app cannot start: it refers to the configmap "x", which does not exist in namespace pay. ConfigMaps in the namespace: notifier-config.`
	got := compactCause(in, 240)
	if !strings.Contains(got, "; ConfigMaps in the namespace: notifier-config") {
		t.Errorf("got %q", got)
	}
	if got := compactCause("container a is OOM killed (1 restart). The limit is too low.", 240); got != "container a is OOM killed (1 restart); the limit is too low" {
		t.Errorf("got %q", got)
	}
}
