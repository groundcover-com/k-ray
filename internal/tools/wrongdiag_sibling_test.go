package tools

import (
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Item 8: a fact is printed only if it holds for all failing pods and there
// are at least 2 failing pods, or all healthy pods share one single other value.

func siblingDescribe(t *testing.T, nodes []string, failing map[int]bool) string {
	t.Helper()
	x := tpl{image: "registry.example/web:1"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, revDeployment(t, 1, int32(len(nodes)), int32(len(nodes)-len(failing)), int32(len(nodes)), int32(len(nodes)), x),
		revRS(t, 1, "aaaa1", int32(len(nodes)), int32(len(nodes)-len(failing)), 5*7*24*time.Hour, x, ""))
	for i, n := range nodes {
		f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-aaaa1-%d", i), ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), node: n, image: "registry.example/web:1", digest: "a1", crash: failing[i]}.pod(t))
	}
	r, _ := newRunner(t, f)
	return describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
}

// One failing pod among 3 pods on 3 different nodes: its node is no signal.
func TestSilentOneFailingPodOnItsOwnNodePrintsNoNodeLine(t *testing.T) {
	out := siblingDescribe(t, []string{"node-a", "node-b", "node-c"}, map[int]bool{0: true})
	mustNotContain(t, out, "- node: ")
}

// One failing pod, and every healthy pod on one other node: that is a signal.
func TestSingleFailingPodWithHealthyPodsOnOneOtherNode(t *testing.T) {
	out := siblingDescribe(t, []string{"node-b", "node-a", "node-a"}, map[int]bool{0: true})
	mustContain(t, out, "- node: node-b in the failing pod, in none of the 2 healthy pods")
}

// Two failing pods on one node, the healthy ones on different nodes: a signal.
func TestTwoFailingPodsOnOneNode(t *testing.T) {
	out := siblingDescribe(t, []string{"node-b", "node-b", "node-a", "node-c"}, map[int]bool{0: true, 1: true})
	mustContain(t, out, "- node: node-b in all 2 failing pods, in none of the 2 healthy pods")
}

// The remainder of an ordinal for a divisor has no general justification: it
// is not claimed. Above or below every healthy pod still is.
func TestSilentStatefulSetOrdinalRemainderIsNotClaimed(t *testing.T) {
	bad := map[int]bool{3: true, 10: true, 17: true}
	out := statefulSetDescribe(t, 20, bad)
	mustNotContain(t, out, "ordinal mod", " mod ")
	mustNotContain(t, out, "differ from the healthy ones", "claim")
}

func TestStatefulSetOrdinalAboveEveryHealthyPod(t *testing.T) {
	out := statefulSetDescribe(t, 20, map[int]bool{17: true, 18: true, 19: true})
	mustContain(t, out, "- ordinal: all failing pods are above every healthy one (failing 17, 18, 19; the highest healthy is 16)")
}

// statefulSetDescribe describes a StatefulSet of n pods on 4 nodes; the pods
// in bad fail. The claim of each pod is its own and is not a difference.
func statefulSetDescribe(t *testing.T, n int32, bad map[int]bool) string {
	t.Helper()
	f := &fakeReader{logs: map[string]string{}}
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db", UID: "uid-sts-db", CreationTimestamp: metav1.NewTime(fixedNow.Add(-72 * time.Hour))},
		Spec:       appsv1.StatefulSetSpec{Replicas: &n, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.example/db:1"}}}}},
		Status:     appsv1.StatefulSetStatus{Replicas: n, ReadyReplicas: n - int32(len(bad))},
	}
	f.objs = append(f.objs, toObj(t, "apps/v1", "StatefulSet", &sts))
	for i := 0; i < int(n); i++ {
		f.objs = append(f.objs, podSpec{
			name: fmt.Sprintf("db-%d", i), ownerKind: "StatefulSet", owner: "db",
			labels: map[string]string{"app": "db", "controller-revision-hash": "db-7c9"},
			node:   fmt.Sprintf("node-%d", i%4), image: "registry.example/db:1", digest: "d1", crash: bad[i], claim: fmt.Sprintf("data-db-%d", i),
		}.pod(t))
	}
	r, _ := newRunner(t, f)
	return describeText(t, r, map[string]any{"kind": "statefulset", "name": "db", "namespace": "shop"})
}
