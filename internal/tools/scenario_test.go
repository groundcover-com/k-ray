package tools

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/groundcover-com/kray/internal/kube"
)

// The scenario is a small cluster with a broken Deployment (a crash loop and
// a pod that cannot be scheduled), a pod that cannot pull its image, a
// service with no ready endpoints and a node. It is the cluster of the tests
// that pin the reads, the wall time and the determinism of the tools.

func toObj(t *testing.T, apiVersion, kind string, v any) unstructured.Unstructured {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		var err error
		if m, err = runtime.DefaultUnstructuredConverter.ToUnstructured(v); err != nil {
			t.Fatal(err)
		}
	}
	m["apiVersion"], m["kind"] = apiVersion, kind
	return unstructured.Unstructured{Object: m}
}

func scenarioPod(ns, name, app string, owner *metav1.OwnerReference, phase corev1.PodPhase, mem string) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns, Name: name, UID: types.UID("uid-" + name), Labels: map[string]string{"app": app},
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-2 * time.Hour)),
		},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{{
				Name: "app", Image: "registry.example/" + app + ":1",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse(mem),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
	if owner != nil {
		p.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return p
}

func scenarioEvent(ns, name, podName, reason, msg string, typ string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"name": name, "namespace": ns},
		"involvedObject": map[string]any{"kind": "Pod", "name": podName, "namespace": ns, "uid": "uid-" + podName},
		"reason":         reason, "message": msg, "type": typ, "count": int64(3),
		"lastTimestamp": fixedNow.Add(-time.Minute).Format(time.RFC3339),
	}}
}

// scenarioFixture builds the reader. Every case gets its own copy.
func scenarioFixture(t *testing.T) *fakeReader {
	t.Helper()
	var objs []unstructured.Unstructured
	add := func(o unstructured.Unstructured) { objs = append(objs, o) }

	rsOwner := &metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-5d4f", UID: types.UID("uid-rs-web"), Controller: boolPtr(true)}
	add(toObj(t, "v1", "Node", &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}))
	add(toObj(t, "apps/v1", "Deployment", map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-dep-web", "labels": map[string]any{"app": "web"}},
		"spec": map[string]any{
			"replicas": int64(3), "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "registry.example/web:1"}}}},
		},
		"status": map[string]any{"replicas": int64(3), "readyReplicas": int64(1), "availableReplicas": int64(1), "updatedReplicas": int64(3)},
	}))
	add(toObj(t, "apps/v1", "ReplicaSet", map[string]any{
		"metadata": map[string]any{
			"name": "web-5d4f", "namespace": "shop", "uid": "uid-rs-web", "labels": map[string]any{"app": "web"},
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "uid": "uid-dep-web", "controller": true}},
		},
		"spec":   map[string]any{"replicas": int64(3), "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}},
		"status": map[string]any{"replicas": int64(3), "readyReplicas": int64(1)},
	}))

	crash := scenarioPod("shop", "web-5d4f-aaaaa", "web", rsOwner, corev1.PodRunning, "256Mi")
	crash.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", RestartCount: 5,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(fixedNow.Add(-time.Minute))}},
	}}
	add(toObj(t, "v1", "Pod", &crash))

	ok := scenarioPod("shop", "web-5d4f-ccccc", "web", rsOwner, corev1.PodRunning, "256Mi")
	ok.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(fixedNow.Add(-time.Hour))}}}}
	ok.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	add(toObj(t, "v1", "Pod", &ok))

	pull := scenarioPod("pay", "img-1", "pay", nil, corev1.PodPending, "128Mi")
	pull.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
		Reason: "ImagePullBackOff", Message: "Back-off pulling image \"registry.example/pay:1\"",
	}}}}
	add(toObj(t, "v1", "Pod", &pull))

	pend := scenarioPod("pay", "pend-1", "pend", nil, corev1.PodPending, "6Gi")
	pend.Spec.NodeName = ""
	pend.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
		Message: "0/1 nodes are available: 1 Insufficient memory. preemption: 0/1 nodes are available: 1 No preemption victims found for incoming pod.",
	}}
	add(toObj(t, "v1", "Pod", &pend))

	add(toObj(t, "v1", "Service", map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "shop"},
		"spec":     map[string]any{"selector": map[string]any{"app": "web"}, "ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080)}}, "type": "ClusterIP"},
	}))
	add(toObj(t, "discovery.k8s.io/v1", "EndpointSlice", map[string]any{
		"metadata":  map[string]any{"name": "web-abc", "namespace": "shop", "labels": map[string]any{"kubernetes.io/service-name": "web"}},
		"endpoints": []any{map[string]any{"addresses": []any{"10.0.0.1"}, "conditions": map[string]any{"ready": false}}},
		"ports":     []any{map[string]any{"port": int64(8080)}},
	}))

	add(scenarioEvent("shop", "aaaaa.1", "web-5d4f-aaaaa", "BackOff", "Back-off restarting failed container app", "Warning"))
	add(scenarioEvent("pay", "img-1.1", "img-1", "Failed", "Failed to pull image \"registry.example/pay:1\": manifest unknown", "Warning"))
	add(scenarioEvent("pay", "pend-1.1", "pend-1", "FailedScheduling", "0/1 nodes are available: 1 Insufficient memory.", "Warning"))

	// More healthy pods, so that the all-namespaces list has a size.
	for i := 0; i < 12; i++ {
		p := scenarioPod("pay", fmt.Sprintf("api-6c7d-%05d", i), "api", nil, corev1.PodRunning, "64Mi")
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(fixedNow.Add(-time.Hour))}}}}
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		add(toObj(t, "v1", "Pod", &p))
	}

	return &fakeReader{
		objs: objs,
		logs: map[string]string{
			"shop/web-5d4f-aaaaa/app#previous": "2026-11-13T11:57:00Z starting web\n2026-11-13T11:57:02Z FATAL config: DATABASE_URL is not set\n",
		},
		metrics: map[kube.MetricsTarget][]unstructured.Unstructured{
			kube.PodMetrics:  {podMetric("shop", "web-5d4f-ccccc", map[string][2]string{"app": {"20m", "100Mi"}})},
			kube.NodeMetrics: {{Object: map[string]any{"metadata": map[string]any{"name": "node-a"}, "usage": map[string]any{"cpu": "500m", "memory": "2Gi"}}}},
		},
	}
}

func boolPtr(b bool) *bool { return &b }
