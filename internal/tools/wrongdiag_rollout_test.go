package tools

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Item 9: a Deployment in a normal rolling update is not unhealthy, and its
// new pods that are not ready yet are not worded as a fault.

// rollingFixture is a Deployment of 4 replicas in a rolling update: 3 pods of
// the old revision are ready, and one new pod started 40s ago is not ready yet.
// With bad, the new pod waits with an error and has restarted.
func rollingFixture(t *testing.T, bad bool) *fakeReader {
	t.Helper()
	oldT := tpl{image: "registry.example/web:1"}
	newT := tpl{image: "registry.example/web:2"}
	dep := revDeployment(t, 2, 4, 3, 2, 5, newT)
	conds := []any{map[string]any{"type": "Progressing", "status": "True", "reason": "ReplicaSetUpdated"}, map[string]any{"type": "Available", "status": "True"}}
	_ = unstructured.SetNestedSlice(dep.Object, conds, "status", "conditions")
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, dep,
		revRS(t, 1, "aaaa1", 3, 3, 48*time.Hour, oldT, ""),
		revRS(t, 2, "bbbb2", 2, 0, 40*time.Second, newT, ""),
	)
	for i := 0; i < 3; i++ {
		f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-aaaa1-%d", i), ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), image: "registry.example/web:1", digest: "a1", age: 48 * time.Hour}.pod(t))
	}
	np := podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2", age: 40 * time.Second, crash: bad}.pod(t)
	if !bad {
		_ = unstructured.SetNestedSlice(np.Object, []any{map[string]any{"type": "Ready", "status": "False"}}, "status", "conditions")
		cs, _, _ := unstructured.NestedSlice(np.Object, "status", "containerStatuses")
		cs[0].(map[string]any)["ready"] = false
		_ = unstructured.SetNestedSlice(np.Object, cs, "status", "containerStatuses")
	}
	f.objs = append(f.objs, np)
	return f
}

func TestSilentRollingDeploymentIsNotUnhealthyAndHasNoCause(t *testing.T) {
	r, _ := newRunner(t, rollingFixture(t, false))
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustContain(t, out, "state: warning", "rolling out: 3 of 4 available",
		"the pods of revision 2 are not ready yet (started 40s ago); revision 1 has 3 ready pods")
	mustNotContain(t, out, "unhealthy", "likely cause:", "are not ready;")
	list := call(t, r, "list", map[string]any{"kind": "deployments", "namespace": "shop"}).Text
	mustContain(t, list, "1 warning")
	mustNotContain(t, list, "unhealthy")
}

// New pods that restart or wait with an error keep the old wording.
func TestRevisionDiffNewPodsWithAFailureSignalKeepTheirWording(t *testing.T) {
	r, _ := newRunner(t, rollingFixture(t, true))
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustContain(t, out, "the pods of revision 2 are not ready; revision 1 has 3 ready pods")
	mustNotContain(t, out, "not ready yet")
}
