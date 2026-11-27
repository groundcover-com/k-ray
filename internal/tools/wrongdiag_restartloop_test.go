package tools

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// A Deployment can report every replica available while every one of its
// pods is in a restart loop: each pod is OOM killed, starts again, and is
// Ready until the next kill. The Deployment status alone looks healthy. The
// pods say it is not: the kills go on now, they are not history.
//
// TestSilentHealthyDeploymentGetsNoCause keeps the other side of the line:
// one pod of two that restarted 3 times, the last 2h ago, is history.

// loopingPod is a Ready pod of the workload app=web whose one container was
// OOM killed restarts times, the last kill at lastKill (it ran 30m before
// it). The container runs again since 5s after the kill.
func loopingPod(name string, restarts int, lastKill string) string {
	last := `{"exitCode":137,"reason":"OOMKilled","startedAt":"` + lastKill + `","finishedAt":"` + lastKill + `"}`
	pod := restartedPod(name, restarts, last, true)
	return strings.Replace(pod, `"startedAt":"2026-11-13T10:00:00Z"`, `"startedAt":"`+lastKill+`"`, 1)
}

// restartLoopObjs is a Deployment with 2 of 2 replicas available, and two
// pods that are OOM killed again and again: 18 kills, the last 2m ago, and
// 14 kills, the last 40m ago.
func restartLoopObjs(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	web1 := strings.Replace(loopingPod("web-1", 18, "2026-11-13T11:58:00Z"),
		`"startedAt":"2026-11-13T11:58:00Z","finishedAt"`, `"startedAt":"2026-11-13T11:28:00Z","finishedAt"`, 1)
	web2 := strings.Replace(loopingPod("web-2", 14, "2026-11-13T11:20:00Z"),
		`"startedAt":"2026-11-13T11:20:00Z","finishedAt"`, `"startedAt":"2026-11-13T10:50:00Z","finishedAt"`, 1)
	return objsFrom(t, "["+healthyWebDeployment(2)+","+web1+","+web2+"]")
}

// describe of the Deployment says it is not healthy, and gives the cause of
// its worst pod.
func TestDeploymentWithEveryPodInARestartLoopIsNotHealthy(t *testing.T) {
	r, _ := newRunner(t, &fakeReader{objs: restartLoopObjs(t), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustContain(t, out,
		"state: warning",
		"likely cause:",
		"pod/web-1: container app is OOM killed at its memory limit of 64Mi (18 restarts, last 2m ago)",
	)
	mustNotContain(t, out, "state: healthy", "looks wrong: nothing found", "earlier:")
}

// list of Deployments does not count it among the healthy ones: the row
// is shown, with the cause of its worst pod.
func TestListShowsDeploymentWithEveryPodInARestartLoop(t *testing.T) {
	r, _ := newRunner(t, &fakeReader{objs: restartLoopObjs(t), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := call(t, r, "list", map[string]any{"kind": "deployments", "namespace": "shop"}).Text
	mustContain(t, out, "1 deployment: 1 warning", "OOM killed")
	mustNotContain(t, out, "1 healthy")
}
