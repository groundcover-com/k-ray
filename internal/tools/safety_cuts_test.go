package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
)

// Item 3: every piece of cluster text is redacted before any cut. A cut that
// falls inside a quoted secret must not leave the first words of it.

// leak is a message in which a quoted secret value runs across byte n: the
// cut of a site that keeps n bytes falls inside the value, after its first
// word. The text before the secret is words, so a cut at a word is possible.
func leak(n int) string {
	if n < 60 {
		n = 60
	}
	pad := strings.Repeat("x ", (n-40)/2)
	return pad + `{"password":"correct horse battery staple zz1 zz2 zz3 zz4 zz5 zz6","k":"v"}`
}

var leakWords = []string{"correct", "horse", "battery", "staple", "zz1", "zz4", "zz6"}

func noLeak(t *testing.T, site, out string) {
	t.Helper()
	for _, w := range leakWords {
		if strings.Contains(out, w) {
			t.Errorf("%s: the word %q of a secret is in the output:\n%s", site, w, out)
			return
		}
	}
}

func event(ns, name, typ, reason, kind, obj, msg string) string {
	return fmt.Sprintf(`{"apiVersion":"v1","kind":"Event","metadata":{"name":%s,"namespace":%s},"type":%s,"reason":%s,"message":%s,"lastTimestamp":"2026-11-13T11:59:00Z","involvedObject":{"kind":%s,"name":%s,"namespace":%s}}`,
		jsonQuote(name), jsonQuote(ns), jsonQuote(typ), jsonQuote(reason), jsonQuote(msg), jsonQuote(kind), jsonQuote(obj), jsonQuote(ns))
}

func runCall(t *testing.T, objs string, tool string, args map[string]any) string {
	t.Helper()
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+objs+"]"), logs: map[string]string{}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	res := call(t, r, tool, args)
	return res.Text
}

func podWaiting(name, reason, msg string) string {
	return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"` + name + `","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Pending","containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{"waiting":{"reason":` + jsonQuote(reason) + `,"message":` + jsonQuote(msg) + `}}}]}}`
}

// events.go: the message column of the events table (300 bytes), and the same
// table inside describe.
func TestCutEventsTableRedactsBeforeTheCut(t *testing.T) {
	pod := podWaiting("web-1", "CrashLoopBackOff", "")
	ev := event("shop", "e1", "Warning", "Failed", "Pod", "web-1", leak(300))
	out := runCall(t, ev, "events", map[string]any{"namespace": "shop"})
	mustContain(t, out, "Failed")
	noLeak(t, "events table", out)
	out = runCall(t, pod+","+ev, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "recent events")
	noLeak(t, "describe events table", out)
}

// shape/health.go: a container waiting message in the problem of a pod (300).
func TestCutPodProblemRedactsBeforeTheCut(t *testing.T) {
	pod := podWaiting("web-1", "CreateContainerError", leak(300-len("container app: CreateContainerError: ")))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list", map[string]any{"kind": "pods", "namespace": "shop"}},
		{"get", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"}},
		{"describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"}},
	} {
		out := runCall(t, pod, c.tool, c.args)
		mustContain(t, out, "CreateContainerError")
		noLeak(t, "pod problem via "+c.tool, out)
	}
}

// shape/health.go: a node condition message (300).
func TestCutNodeConditionRedactsBeforeTheCut(t *testing.T) {
	node := `{"apiVersion":"v1","kind":"Node","metadata":{"name":"n1","creationTimestamp":"2026-11-13T10:00:00Z"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"KubeletNotReady","message":` + jsonQuote(leak(300-len("not ready: KubeletNotReady "))) + `}]}}`
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list", map[string]any{"kind": "nodes"}},
		{"get", map[string]any{"kind": "node", "name": "n1"}},
		{"describe", map[string]any{"kind": "node", "name": "n1"}},
	} {
		out := runCall(t, node, c.tool, c.args)
		mustContain(t, out, "KubeletNotReady")
		noLeak(t, "node condition via "+c.tool, out)
	}
}

// shape/conditions.go: the message of a condition of any kind (200).
func TestCutConditionMessageRedactsBeforeTheCut(t *testing.T) {
	sts := `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"db","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},"spec":{"replicas":1},"status":{"replicas":1,"readyReplicas":1,"conditions":[{"type":"Foo","status":"False","reason":"R","message":` + jsonQuote(leak(200)) + `}]}}`
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list", map[string]any{"kind": "statefulsets", "namespace": "shop"}},
		{"get", map[string]any{"kind": "statefulset", "name": "db", "namespace": "shop"}},
		{"describe", map[string]any{"kind": "statefulset", "name": "db", "namespace": "shop"}},
	} {
		out := runCall(t, sts, c.tool, c.args)
		mustContain(t, out, "Foo is False")
		noLeak(t, "condition message via "+c.tool, out)
	}
}

func crashPod(name string, waitReason string, restarts int, exit int, extraStatus string) string {
	return fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q,"namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":%d,%s"state":{"waiting":{"reason":%q}},"lastState":{"terminated":{"exitCode":%d,"reason":"Error"}}}]}}`, name, restarts, extraStatus, waitReason, exit)
}

// diagnose.go: probe failure events (200 bytes each).
func TestCutProbeEventsRedactBeforeTheCut(t *testing.T) {
	cases := map[string]struct {
		pod string
		ev  string
	}{
		"liveness, crash": {
			crashPod("web-1", "CrashLoopBackOff", 5, 1, ""),
			event("shop", "e1", "Warning", "Unhealthy", "Pod", "web-1", "Liveness probe failed: "+leak(200-len("Liveness probe failed: "))),
		},
		"liveness, exit 0": {
			crashPod("web-1", "CrashLoopBackOff", 5, 0, ""),
			event("shop", "e1", "Warning", "Unhealthy", "Pod", "web-1", "Liveness probe failed: "+leak(200)),
		},
		"readiness": {
			`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T10:00:00Z"}}}]}}`,
			event("shop", "e1", "Warning", "Unhealthy", "Pod", "web-1", "Readiness probe failed: "+leak(200)),
		},
		"startup": {
			`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T10:00:00Z"}}}]}}`,
			event("shop", "e1", "Warning", "Unhealthy", "Pod", "web-1", leakStartup()),
		},
	}
	for name, c := range cases {
		out := runCall(t, c.pod+","+c.ev, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
		mustContain(t, out, "likely cause:")
		noLeak(t, name, out)
	}
}

func leakStartup() string { return "Startup probe failed: " + leak(200-len("Startup probe failed: ")) }

// diagnose.go: an image pull message that the diagnosis does not know (200).
func TestCutImagePullMessageRedactsBeforeTheCut(t *testing.T) {
	pod := podWaiting("web-1", "ErrImagePull", leak(200-len("x:1: ")))
	out := runCall(t, pod, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "cannot pull its image")
	noLeak(t, "image pull", out)
}

func pendingPod(msg, spec string) string {
	return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{` + spec + `"containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":` + jsonQuote(msg) + `}]}}`
}

// diagnose.go: the scheduler's reasons (900), the tolerations (300) and the
// node selection (400) of the pod.
func TestCutSchedulerTextRedactsBeforeTheCut(t *testing.T) {
	pre := "0/3 nodes are available: 3 node(s) had untolerated taint "
	cases := map[string]string{
		"reasons": pendingPod(pre+leak(900-len("3 node(s) had untolerated taint ")), ""),
		"tolerations": pendingPod(pre+"{a: b}",
			`"tolerations":[{"key":"dedicated","operator":"Equal","value":`+jsonQuote(leak(300-len("dedicated=")))+`,"effect":"NoSchedule"}],`),
		"node selection": pendingPod("0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
			`"nodeSelector":{"zone":`+jsonQuote(leak(400-len("zone=")))+`},`),
	}
	for name, pod := range cases {
		out := runCall(t, pod, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
		mustContain(t, out, "no node accepts the pod")
		noLeak(t, name, out)
	}
}

// diagnose.go and evidence.go: the command and arguments of a container (200)
// and the exec command of a probe.
func TestCutCommandRedactsBeforeTheCut(t *testing.T) {
	long := leak(200 - len("sh -c "))
	pod := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1","args":["sh","-c",` + jsonQuote(long) + `],
"livenessProbe":{"exec":{"command":["sh","-c",` + jsonQuote(long) + `]}}}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}`
	out := runCall(t, pod, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "args: sh -c")
	noLeak(t, "command", out)
}

// A multi-word secret across two elements of a command is one string once the
// command is joined: the joined text is redacted before it is cut.
func TestCutCommandRedactsAcrossElementsBeforeTheCut(t *testing.T) {
	pod := `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1","args":["echo",` + jsonQuote(strings.Repeat("x ", 80)+`{"password":"correct`) + `,"horse","battery","staple","zz1\",\"k\":\"v\"}"]}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}`
	out := runCall(t, pod, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "args: echo")
	noLeak(t, "command across elements", out)
}

// related.go: the warning event of a ReplicaSet in the block of a Deployment
// (220 bytes).
func TestCutReplicaSetEventRedactsBeforeTheCut(t *testing.T) {
	objs := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"shop","uid":"d1","creationTimestamp":"2026-11-13T10:00:00Z"},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"api"}}},"status":{"replicas":0,"availableReplicas":0,"readyReplicas":0,"updatedReplicas":0}},
{"apiVersion":"apps/v1","kind":"ReplicaSet","metadata":{"name":"api-1","namespace":"shop","uid":"r1","labels":{"app":"api"},"creationTimestamp":"2026-11-13T10:00:00Z","ownerReferences":[{"apiVersion":"apps/v1","kind":"Deployment","name":"api","uid":"d1","controller":true}]},"spec":{"replicas":2},"status":{"replicas":0,"readyReplicas":0,"conditions":[{"type":"ReplicaFailure","status":"True","reason":"FailedCreate","message":"pods are forbidden"}]}},
` + strings.Replace(event("shop", "e1", "Warning", "FailedCreate", "ReplicaSet", "api-1", "Error creating: "+leak(220)), `"involvedObject":{`, `"involvedObject":{"uid":"r1",`, 1)
	out := runCall(t, objs, "describe", map[string]any{"kind": "deployment", "name": "api", "namespace": "shop"})
	mustContain(t, out, "event FailedCreate")
	noLeak(t, "replicaset event", out)
}

// runner.go: the arguments in the audit line (512 bytes) are redacted before
// they are cut.
func TestCutAuditArgumentsRedactBeforeTheCut(t *testing.T) {
	r, path := newRunner(t, &fakeReader{})
	call(t, r, "events", map[string]any{"namespace": leak(512)})
	r.Call(context.Background(), "events", []byte(leak(512)))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	noLeak(t, "audit line", string(b))
	if !strings.Contains(string(b), `"tool":"events"`) {
		t.Fatalf("no audit line: %s", b)
	}
}

// difference.go: flatDiff values (60) and a nodeSelector or securityContext
// change are redacted before they are cut.
func TestCutFlatDiffRedactsBeforeTheCut(t *testing.T) {
	a := map[string]string{"role": "x", "level": "s0"}
	b := map[string]string{"role": leak(60 - len("unset -> ")), "level": "s1"}
	lines := flatDiff("pod securityContext", a, b, redactor{redact.New(nil)})
	out := strings.Join(lines, "\n")
	mustContain(t, out, "pod securityContext.level: s0 -> s1")
	noLeak(t, "flatDiff", out)
}
