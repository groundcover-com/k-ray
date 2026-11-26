package tools

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The fallback for a named object with no namespace: the default namespace is
// read first, as always. Only when the object is not there, one list of the
// kind in all namespaces finds it.

func deploymentObj(t *testing.T, ns, name, app string) unstructured.Unstructured {
	t.Helper()
	return toObj(t, "apps/v1", "Deployment", map[string]any{
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": map[string]any{"app": app}},
		"spec": map[string]any{
			"replicas": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": app}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "registry.example/" + app + ":1"}}}},
		},
		"status": map[string]any{"replicas": int64(1), "readyReplicas": int64(1), "availableReplicas": int64(1), "updatedReplicas": int64(1)},
	})
}

// locateFixture is a cluster where the default namespace is shop.
//   - shop: deployment web (pod web-1), deployment api
//   - bank: deployment ledger (pod ledger-0), deployment api
//   - billing: deployment invoice-api (pod invoice-api-1)
//   - a and b: deployment dup, pod twin
//   - a: deployment multi; b: statefulset multi
func locateFixture(t *testing.T) *fakeReader {
	t.Helper()
	var objs []unstructured.Unstructured
	pod := func(ns, name, app string) {
		p := scenarioPod(ns, name, app, nil, corev1.PodRunning, "64Mi")
		objs = append(objs, toObj(t, "v1", "Pod", &p))
	}
	objs = append(objs, deploymentObj(t, "shop", "web", "web"), deploymentObj(t, "shop", "api", "api"), deploymentObj(t, "bank", "api", "api"),
		deploymentObj(t, "bank", "ledger", "ledger"), deploymentObj(t, "billing", "invoice-api", "invoice"),
		deploymentObj(t, "a", "dup", "dup"), deploymentObj(t, "b", "dup", "dup"), deploymentObj(t, "a", "multi", "multi"))
	objs = append(objs, toObj(t, "apps/v1", "StatefulSet", map[string]any{
		"metadata": map[string]any{"name": "multi", "namespace": "b"},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "multi"}}},
	}))
	pod("shop", "web-1", "web")
	pod("bank", "ledger-0", "ledger")
	pod("billing", "invoice-api-1", "invoice")
	pod("a", "twin", "dup")
	pod("b", "twin", "dup")
	objs = append(objs, toObj(t, "v1", "Node", map[string]any{"metadata": map[string]any{"name": "node-a"}}))
	return &fakeReader{objs: objs, logs: map[string]string{
		"shop/web-1/app":            "2026-11-13T11:00:00Z web line\n",
		"bank/ledger-0/app":         "2026-11-13T11:00:00Z ledger line\n",
		"billing/invoice-api-1/app": "2026-11-13T11:00:00Z invoice line\n",
	}}
}

func (f *fakeReader) has(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// sameAsExplicit checks that an object in the default namespace is answered
// with the same reads and the same text as when the namespace is given.
func sameAsExplicit(t *testing.T, tool string, args map[string]any) {
	t.Helper()
	f1 := locateFixture(t)
	r1, _ := newRunner(t, f1)
	a := call(t, r1, tool, args)
	f2 := locateFixture(t)
	r2, _ := newRunner(t, f2)
	explicit := map[string]any{"namespace": "shop"}
	for k, v := range args {
		explicit[k] = v
	}
	b := call(t, r2, tool, explicit)
	if a.IsError || b.IsError {
		t.Fatalf("the call failed:\n%s\n%s", a.Text, b.Text)
	}
	if fmtCounts(f1.counts()) != fmtCounts(f2.counts()) {
		t.Errorf("reads without namespace %s, with namespace %s", fmtCounts(f1.counts()), fmtCounts(f2.counts()))
	}
	if f1.has("list deployments  ")+f1.has("listmeta deployments ") > 0 && f2.has("list deployments  ")+f2.has("listmeta deployments ") == 0 {
		t.Errorf("the fallback list ran for an object in the default namespace: %v", f1.calls)
	}
	if a.Text != b.Text {
		t.Errorf("text differs:\n--- no namespace\n%s\n--- namespace\n%s", a.Text, b.Text)
	}
	mustNotContain(t, a.Text, "found in namespace")
}

func TestLocateDescribe(t *testing.T) {
	t.Run("default namespace: same reads and text", func(t *testing.T) {
		sameAsExplicit(t, "describe", map[string]any{"kind": "deployment", "name": "web"})
	})
	t.Run("one other namespace", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "ledger"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "found in namespace bank; none in the default namespace shop", "describe deployment ledger · namespace bank")
		// The search reads the metadata of the kind, then the one match in full.
		if f.has("get deployments shop/ledger") != 1 || f.has("listmeta deployments ") != 1 || f.has("get deployments bank/ledger") != 1 || f.has("list deployments  ") != 0 {
			t.Errorf("reads: %v", f.calls)
		}
	})
	t.Run("two namespaces", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "dup"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "2 objects match dup in 2 namespaces", "== deployment/dup in namespace a", "== deployment/dup in namespace b")
	})
	t.Run("nowhere", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "ledgr"})
		if !res.IsError {
			t.Fatalf("want an error:\n%s", res.Text)
		}
		mustContain(t, res.Text, `"ledgr" not found`, "also looked in all namespaces", "closest names:", "deployment bank/ledger")
	})
	t.Run("namespace given, object elsewhere", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "ledger", "namespace": "shop"})
		if !res.IsError {
			t.Fatalf("want an error:\n%s", res.Text)
		}
		if f.has("list ") != 0 || f.total() != 1 {
			t.Errorf("no fallback was expected: %v", f.calls)
		}
	})
	t.Run("cluster-scoped kind", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "describe", map[string]any{"kind": "node", "name": "node-a"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustNotContain(t, res.Text, "found in namespace")
		missing := call(t, r, "describe", map[string]any{"kind": "node", "name": "node-z"})
		if !missing.IsError || strings.Contains(missing.Text, "all namespaces") {
			t.Errorf("a cluster-scoped kind has no fallback:\n%s", missing.Text)
		}
	})
}

func TestLocateGet(t *testing.T) {
	t.Run("default namespace: same reads and text", func(t *testing.T) {
		sameAsExplicit(t, "get", map[string]any{"kind": "deployment", "name": "web"})
	})
	t.Run("one other namespace", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "get", map[string]any{"kind": "deployment", "name": "ledger"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "found in namespace bank; none in the default namespace shop", "get deployment ledger · namespace bank", "name: ledger")
		if f.has("get deployments shop/ledger") != 1 || f.has("listmeta deployments ") != 1 || f.has("get deployments bank/ledger") != 1 || f.total() != 3 {
			t.Errorf("reads: %v", f.calls)
		}
	})
	t.Run("two namespaces", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "get", map[string]any{"kind": "deployment", "name": "dup"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "2 objects are named dup, in 2 namespaces: give namespace", "a/dup", "b/dup", "next: get deployment dup namespace=a")
		mustNotContain(t, res.Text, "replicas")
	})
	t.Run("nowhere", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "get", map[string]any{"kind": "deployment", "name": "ledgr"})
		if !res.IsError {
			t.Fatalf("want an error:\n%s", res.Text)
		}
		mustContain(t, res.Text, `"ledgr" not found`, "also looked in all namespaces", "deployment bank/ledger")
	})
	t.Run("namespace given, object elsewhere", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "get", map[string]any{"kind": "deployment", "name": "ledger", "namespace": "shop"})
		if !res.IsError || f.has("list ") != 0 || f.total() != 1 {
			t.Errorf("no fallback was expected: %v\n%s", f.calls, res.Text)
		}
	})
	t.Run("cluster-scoped kind", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "get", map[string]any{"kind": "node", "name": "node-a"})
		if res.IsError || f.total() != 1 {
			t.Errorf("%v\n%s", f.calls, res.Text)
		}
		call(t, r, "get", map[string]any{"kind": "node", "name": "node-z"})
		if f.has("list ") != 0 {
			t.Errorf("a cluster-scoped kind has no fallback: %v", f.calls)
		}
	})
}

func TestLocateLogs(t *testing.T) {
	t.Run("pod in the default namespace: same reads and text", func(t *testing.T) {
		f1 := locateFixture(t)
		r1, _ := newRunner(t, f1)
		a := call(t, r1, "logs", map[string]any{"pod": "web-1"})
		f2 := locateFixture(t)
		r2, _ := newRunner(t, f2)
		b := call(t, r2, "logs", map[string]any{"pod": "web-1", "namespace": "shop"})
		if a.IsError || a.Text != b.Text || fmtCounts(f1.counts()) != fmtCounts(f2.counts()) || f1.has("list ") != 0 {
			t.Errorf("reads %s / %s\n%s\n%s", fmtCounts(f1.counts()), fmtCounts(f2.counts()), a.Text, b.Text)
		}
	})
	t.Run("pod in one other namespace", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "ledger-0"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "found in namespace bank; none in the default namespace shop", "logs pod ledger-0 · namespace bank", "ledger line")
		if f.has("get pods shop/ledger-0") != 1 || f.has("listmeta pods ") != 1 || f.has("get pods bank/ledger-0") != 1 || f.has("logs bank/ledger-0") != 1 || f.total() != 4 {
			t.Errorf("reads: %v", f.calls)
		}
	})
	t.Run("pod in two namespaces", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "twin"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "2 objects are named twin, in 2 namespaces: give namespace", "a/twin", "b/twin", "next: logs pod=twin namespace=a")
		if f.has("logs ") != 0 {
			t.Errorf("no logs were expected: %v", f.calls)
		}
	})
	t.Run("nowhere", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "ledger-1"})
		if !res.IsError {
			t.Fatalf("want an error:\n%s", res.Text)
		}
		mustContain(t, res.Text, `"ledger-1" not found`, "also looked in all namespaces", "pod bank/ledger-0")
	})
	t.Run("namespace given, pod elsewhere", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "ledger-0", "namespace": "shop"})
		if !res.IsError || f.total() != 1 {
			t.Errorf("no fallback was expected: %v\n%s", f.calls, res.Text)
		}
		// A workload name is not read as a pod name when a namespace is given.
		res = call(t, r, "logs", map[string]any{"pod": "web", "namespace": "shop"})
		if !res.IsError {
			t.Errorf("a namespace was given:\n%s", res.Text)
		}
	})
	t.Run("workload name as the pod, in the default namespace", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "web"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "no pod is named web; reading the logs of deployment/web in namespace shop", "web line", "deployment web")
	})
	t.Run("workload name as the pod, in one other namespace", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "invoice-api"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "no pod is named invoice-api; reading the logs of deployment/invoice-api in namespace billing", "invoice line")
	})
	t.Run("workload name in two namespaces", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"pod": "dup"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "no pod is named dup; 2 workloads are named so", "a/dup", "b/dup", "next: logs workload=deployment/dup namespace=a")
		res = call(t, r, "logs", map[string]any{"pod": "multi"})
		mustContain(t, res.Text, "deployment a/multi", "statefulset b/multi")
	})
	t.Run("workload reference without a namespace", func(t *testing.T) {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		res := call(t, r, "logs", map[string]any{"workload": "deployment/invoice-api"})
		if res.IsError {
			t.Fatal(res.Text)
		}
		mustContain(t, res.Text, "found in namespace billing; none in the default namespace shop", "invoice line", "deployment invoice-api")
		res = call(t, r, "logs", map[string]any{"workload": "deployment/dup"})
		mustContain(t, res.Text, "2 objects are named dup", "next: logs workload=deployment/dup namespace=a")
		res = call(t, r, "logs", map[string]any{"workload": "deployment/nope"})
		if !res.IsError {
			t.Errorf("want an error:\n%s", res.Text)
		}
		res = call(t, r, "logs", map[string]any{"workload": "deployment/ledger", "namespace": "shop"})
		if !res.IsError {
			t.Errorf("a namespace was given:\n%s", res.Text)
		}
	})
	t.Run("workload reference in the default namespace: same reads and text", func(t *testing.T) {
		f1 := locateFixture(t)
		r1, _ := newRunner(t, f1)
		a := call(t, r1, "logs", map[string]any{"workload": "deployment/web"})
		f2 := locateFixture(t)
		r2, _ := newRunner(t, f2)
		b := call(t, r2, "logs", map[string]any{"workload": "deployment/web", "namespace": "shop"})
		if a.IsError || a.Text != b.Text || fmtCounts(f1.counts()) != fmtCounts(f2.counts()) {
			t.Errorf("%s / %s\n%s\n%s", fmtCounts(f1.counts()), fmtCounts(f2.counts()), a.Text, b.Text)
		}
	})
}

func TestLocateReadCountsOnTheNotFoundPath(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		// The search reads the metadata of the kind (listmeta), and each match in
		// full with one get: no list of the objects of a whole kind.
		{"get", map[string]any{"kind": "deployment", "name": "ledger"}, "get:2 listmeta:1"},
		{"get", map[string]any{"kind": "deployment", "name": "nope"}, "get:1 listmeta:1"},
		{"describe", map[string]any{"kind": "deployment", "name": "nope"}, "get:1 listmeta:1"},
		{"logs", map[string]any{"pod": "ledger-0"}, "get:2 listmeta:1 logs:1"},
		{"logs", map[string]any{"pod": "nope"}, "get:1 listmeta:5"}, // the pod metadata, and the metadata of each workload kind
	}
	for _, c := range cases {
		f := locateFixture(t)
		r, _ := newRunner(t, f)
		call(t, r, c.tool, c.args)
		if got := fmtCounts(f.counts()); got != c.want {
			t.Errorf("%s %v: reads %s, want %s\n%v", c.tool, c.args, got, c.want, f.calls)
		}
	}
	_ = fmt.Sprint
}
