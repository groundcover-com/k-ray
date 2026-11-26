package tools

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// Items 2 and 3: a healthy object gets no "likely cause"; an old restart is
// history, said once on an "earlier:" line; a cause says when the last
// termination was.

// restartedPod is a Running pod of the workload app=web. last is the JSON of
// the last termination of its one container; ready tells if it is ready.
func restartedPod(name string, restarts int, last string, ready bool) string {
	state := `"running":{"startedAt":"2026-11-13T10:00:00Z"}`
	if !ready {
		state = `"waiting":{"reason":"CrashLoopBackOff"}`
	}
	return fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q,"namespace":"shop","uid":"u-%s","labels":{"app":"web"},"creationTimestamp":"2026-01-01T10:00:00Z",
"ownerReferences":[{"apiVersion":"apps/v1","kind":"ReplicaSet","name":"web-5d","uid":"o-rs","controller":true}]},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1","resources":{"limits":{"memory":"64Mi"}}}]},
"status":{"phase":"Running","conditions":[{"type":"Ready","status":%q}],"containerStatuses":[{"name":"app","ready":%v,"restartCount":%d,"state":{%s},"lastState":{"terminated":%s}}]}}`,
		name, name, map[bool]string{true: "True", false: "False"}[ready], ready, restarts, state, last)
}

const (
	oomRecent = `{"exitCode":137,"reason":"OOMKilled","startedAt":"2026-11-13T07:00:00Z","finishedAt":"2026-11-13T10:00:00Z"}`
	oomOld    = `{"exitCode":137,"reason":"OOMKilled","startedAt":"2026-04-26T07:00:00Z","finishedAt":"2026-04-26T10:00:00Z"}`
	exitOld   = `{"exitCode":1,"reason":"Error","startedAt":"2026-04-26T07:00:00Z","finishedAt":"2026-04-26T10:00:00Z"}`
	exitRecnt = `{"exitCode":1,"reason":"Error","startedAt":"2026-11-13T09:00:00Z","finishedAt":"2026-11-13T10:00:00Z"}`
)

func healthyWebDeployment(replicas int) string {
	return fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"shop","uid":"u-web","creationTimestamp":"2026-01-01T10:00:00Z"},
"spec":{"replicas":%d,"selector":{"matchLabels":{"app":"web"}},"template":{"metadata":{"labels":{"app":"web"}},"spec":{"containers":[{"name":"app","image":"x:1"}]}}},
"status":{"replicas":%d,"readyReplicas":%d,"availableReplicas":%d,"updatedReplicas":%d,"conditions":[{"type":"Available","status":"True"},{"type":"Progressing","status":"True","reason":"NewReplicaSetAvailable"}]}}`,
		replicas, replicas, replicas, replicas, replicas)
}

// A pod that is Running and Ready, killed by OOM once 200 days ago, is history.
func TestSilentOldOOMOfReadyPodIsNotAWarning(t *testing.T) {
	pod := restartedPod("web-1", 1, oomOld, true)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "state: healthy", "earlier: container app restarted once, last 201d ago")
	mustNotContain(t, out, "likely cause:", "looks wrong:\n- ", "last crash")
	list := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "shop"}).Text
	mustNotContain(t, list, "OOM", "restart")
}

// Three or more restarts, the last one old, on a Ready pod: still history.
func TestSilentManyOldRestartsOfReadyPodIsNotAWarning(t *testing.T) {
	pod := restartedPod("web-1", 9, exitOld, true)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "state: healthy", "earlier: container app restarted 9 times, last 201d ago")
	mustNotContain(t, out, "likely cause:")
}

// A pod in CrashLoopBackOff keeps its cause, whatever the age.
func TestOldTerminationOfCrashLoopingPodStaysAProblem(t *testing.T) {
	pod := restartedPod("web-1", 9, exitOld, false)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "state: unhealthy", "likely cause:", "container app exits with code 1")
	mustNotContain(t, out, "earlier:")
}

// A recent OOM kill of a Ready pod stays a warning, and the cause says when.
func TestOOMCauseSaysWhenTheLastKillWas(t *testing.T) {
	pod := restartedPod("web-1", 4, oomRecent, true)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "state: warning", "likely cause:", "OOM killed at its memory limit of 64Mi (4 restarts, last 2h ago)")
}

func TestExitCodeCauseSaysWhenTheLastExitWas(t *testing.T) {
	pod := restartedPod("web-1", 4, exitRecnt, false)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	// In CrashLoopBackOff the crash is ongoing: no time.
	mustContain(t, out, "container app exits with code 1 (application error), 4 restarts.")
	mustNotContain(t, out, "last 2h ago")
	// A container that runs now restarted in the past: the time stays.
	pod = restartedPod("web-1", 4, exitRecnt, true)
	r, _ = newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out = describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "container app exits with code 1 (application error), 4 restarts, last 2h ago.")
}

func TestOOMInCrashLoopHasNoTime(t *testing.T) {
	pod := restartedPod("web-1", 4, oomRecent, false)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "OOM killed at its memory limit of 64Mi (4 restarts).")
	mustNotContain(t, out, "last 2h ago")
}

// One or two plain restarts of a Ready pod print nothing, not even a reason
// of Unknown, for a pod or for the workload.
func TestSilentPlainRestartsPrintNoEarlierLine(t *testing.T) {
	unknown := `{"exitCode":255,"reason":"Unknown","startedAt":"2026-11-12T07:00:00Z","finishedAt":"2026-11-12T10:00:00Z"}`
	for _, n := range []int{1, 2} {
		pod := restartedPod("web-1", n, unknown, true)
		r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
		out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
		mustNotContain(t, out, "earlier:", "(Unknown)")
		objs := healthyWebDeployment(1) + "," + restartedPod("web-1", n, unknown, true)
		r, _ = newRunner(t, &fakeReader{objs: objsFrom(t, "["+objs+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
		out = describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
		mustNotContain(t, out, "earlier:", "(Unknown)")
	}
}

// A Deployment with all replicas available, and one pod that restarted
// recently: the Deployment is healthy, so it gets no cause and no crash block.
func TestSilentHealthyDeploymentGetsNoCause(t *testing.T) {
	objs := healthyWebDeployment(2) + "," + restartedPod("web-1", 3, oomRecent, true) + "," + restartedPod("web-2", 0, `{}`, true)
	f := &fakeReader{
		objs: objsFrom(t, "["+objs+"]"), logs: map[string]string{"shop/web-1/app#previous": "2026-11-13T09:59:00Z FATAL out of memory\n"},
		metrics: map[kube.MetricsTarget][]unstructured.Unstructured{},
	}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustContain(t, out, "state: healthy", "earlier: pod/web-1: container app restarted 3 times, last 2h ago")
	mustNotContain(t, out, "likely cause:", "last crash")
}

// A Job that completed, with failed attempts before the run that completed.
func TestSilentCompletedJobGetsNoCause(t *testing.T) {
	job := `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"mig","namespace":"shop","uid":"u-mig","creationTimestamp":"2026-11-13T09:00:00Z"},
"spec":{"completions":1,"selector":{"matchLabels":{"job-name":"mig"}},"template":{"spec":{"containers":[{"name":"app","image":"x:1"}]}}},
"status":{"succeeded":1,"failed":2,"conditions":[{"type":"Complete","status":"True"}]}}`
	pod := func(name, phase string, exit int) string {
		term := fmt.Sprintf(`"terminated":{"exitCode":%d,"reason":"Error","finishedAt":"2026-11-13T09:10:00Z"}`, exit)
		if phase == "Succeeded" {
			term = `"terminated":{"exitCode":0,"reason":"Completed","finishedAt":"2026-11-13T09:40:00Z"}`
		}
		return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"` + name + `","namespace":"shop","uid":"u-` + name + `","labels":{"job-name":"mig"},"creationTimestamp":"2026-11-13T09:00:00Z",
"ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":"mig","uid":"u-mig","controller":true}]},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"` + phase + `","containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{` + term + `}}]}}`
	}
	objs := job + "," + pod("mig-a", "Failed", 1) + "," + pod("mig-b", "Failed", 1) + "," + pod("mig-c", "Succeeded", 0)
	f := &fakeReader{objs: objsFrom(t, "["+objs+"]"), logs: map[string]string{"shop/mig-a/app": "FATAL boom\n"}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "job", "name": "mig", "namespace": "shop"})
	mustContain(t, out, "earlier attempts: 2 pods failed before the run that completed")
	mustNotContain(t, out, "likely cause:", "last log lines", "last crash")
	if strings.Contains(out, "state: unhealthy") {
		t.Errorf("a completed Job is not unhealthy:\n%s", out)
	}
}

// A Deployment that is short of replicas keeps the cause of its worst pod.
func TestUnhealthyDeploymentKeepsItsCause(t *testing.T) {
	dep := strings.Replace(healthyWebDeployment(2), `"availableReplicas":2`, `"availableReplicas":1`, 1)
	objs := dep + "," + restartedPod("web-1", 6, exitRecnt, false) + "," + restartedPod("web-2", 0, `{}`, true)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+objs+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustContain(t, out, "likely cause:", "pod/web-1: container app exits with code 1")
	mustNotContain(t, out, "earlier:")
}

// Between two starts a crashing container is "terminated" for a moment, not
// "waiting". The crash is ongoing in both states, so the time is left out in
// both: the text must not depend on the moment of the call.
func TestCrashBetweenRestartsHasNoTimeInTheTerminatedState(t *testing.T) {
	pod := strings.Replace(restartedPod("web-1", 4, exitRecnt, false),
		`"waiting":{"reason":"CrashLoopBackOff"}`,
		`"terminated":{"exitCode":1,"reason":"Error","startedAt":"2026-11-13T09:59:00Z","finishedAt":"2026-11-13T09:59:30Z"}`, 1)
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, "["+pod+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "exits with code 1 (application error), 4 restarts.")
	mustNotContain(t, out, "restarts, last ")
}
