package tools

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// Item 1: an init container with restartPolicy Always is a native sidecar. It
// runs for the life of the pod, so a started sidecar is never "not completed".

// sidecarPod is a pod with one sidecar (init container with restartPolicy
// Always) and one app container. The statuses are JSON objects.
func sidecarPod(ns, name, sidecarStatus, appStatus, phase string) string {
	return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"` + name + `","namespace":"` + ns + `","uid":"u-` + name + `","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","initContainers":[{"name":"proxy","image":"proxy:1","restartPolicy":"Always","resources":{"requests":{"cpu":"10m","memory":"32Mi"},"limits":{"memory":"64Mi"}}}],
"containers":[{"name":"app","image":"x:1","resources":{"requests":{"cpu":"10m","memory":"32Mi"},"limits":{"memory":"64Mi"}}}]},
"status":{"phase":"` + phase + `","initContainerStatuses":[` + sidecarStatus + `],"containerStatuses":[` + appStatus + `]}}`
}

const (
	runningSidecar = `{"name":"proxy","ready":true,"started":true,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T10:00:05Z"}}}`
	readyApp       = `{"name":"app","ready":true,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T10:00:10Z"}}}`
	unreadyApp     = `{"name":"app","ready":false,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T10:00:10Z"}}}`
)

func TestSilentSidecarRunningInHealthyPod(t *testing.T) {
	out := runCall(t, sidecarPod("shop", "web-1", runningSidecar, readyApp, "Running"), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustNotContain(t, out, "init container", "has not completed", "likely cause:")
}

// A short block (two pods with one name) names the init container that has
// not completed, but never a sidecar that runs.
func TestSilentSidecarNotNamedAsCauseOfAnotherProblem(t *testing.T) {
	objs := sidecarPod("shop", "web-1", runningSidecar, unreadyApp, "Running") + "," + sidecarPod("shop2", "web-1", runningSidecar, unreadyApp, "Running")
	out := runCall(t, objs, "describe", map[string]any{"name": "web-1"})
	mustNotContain(t, out, "init container proxy", "has not completed", "sidecar")
}

// A sidecar that crashes is diagnosed like a normal container, and is not
// said to "not have completed".
func TestSidecarThatCrashesIsDiagnosed(t *testing.T) {
	bad := `{"name":"proxy","ready":false,"started":false,"restartCount":6,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"exitCode":1,"reason":"Error","finishedAt":"2026-11-13T11:58:00Z"}}}`
	f := &fakeReader{objs: objsFrom(t, "["+sidecarPod("shop", "web-1", bad, readyApp, "Running")+"]"), logs: map[string]string{
		"shop/web-1/proxy#previous": "2026-11-13T11:57:59Z FATAL cannot reach upstream\n",
	}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "likely cause:", "container proxy exits with code 1")
	mustNotContain(t, out, "has not completed")
}

// top: a sidecar has requests and limits in the pod spec.
func TestTopSidecarHasItsRequestsAndLimits(t *testing.T) {
	f := &fakeReader{
		objs: objsFrom(t, "["+sidecarPod("shop", "web-1", runningSidecar, readyApp, "Running")+"]"),
		metrics: map[kube.MetricsTarget][]unstructured.Unstructured{kube.PodMetrics: {
			podMetric("shop", "web-1", map[string][2]string{"app": {"5m", "20Mi"}, "proxy": {"5m", "20Mi"}}),
		}},
	}
	r, _ := newRunner(t, f)
	out := call(t, r, "top", map[string]any{"resource": "pods", "namespace": "shop"}).Text
	line := lineWith(t, out, "proxy")
	if strings.Contains(line, "no requests or limits") {
		t.Errorf("the sidecar has requests and limits:\n%s", out)
	}
}
