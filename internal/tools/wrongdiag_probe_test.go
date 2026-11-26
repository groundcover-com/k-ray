package tools

import (
	"testing"
)

// Item 6: a probe event belongs to the container that its fieldPath names.

const liveProbe = `"livenessProbe":{"httpGet":{"path":"/live","port":8080}}`

// twoContainerPod has containers a and b; initPart is the JSON of the init
// containers (spec) and initStatus the JSON of their statuses.
func twoContainerPod(aSpec, bSpec, aStatus, bStatus, initSpec, initStatus string) string {
	init := ""
	if initSpec != "" {
		init = `"initContainers":[` + initSpec + `],`
	}
	return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","uid":"u-web-1","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n",` + init + `"containers":[{"name":"a","image":"x:1"` + aSpec + `},{"name":"b","image":"x:1"` + bSpec + `}]},
"status":{"phase":"Running","initContainerStatuses":[` + initStatus + `],"containerStatuses":[` + aStatus + `,` + bStatus + `]}}`
}

const (
	aCrash = `{"name":"a","ready":false,"restartCount":4,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"exitCode":1,"reason":"Error","finishedAt":"2026-11-13T11:55:00Z"}}}`
	bLive  = `{"name":"b","ready":true,"restartCount":3,"state":{"running":{"startedAt":"2026-11-13T11:59:00Z"}},"lastState":{"terminated":{"exitCode":0,"reason":"Completed","finishedAt":"2026-11-13T11:58:00Z"}}}`
)

func probeEvent(fieldPath string) string {
	fp := ""
	if fieldPath != "" {
		fp = `,"fieldPath":"` + fieldPath + `"`
	}
	return `{"apiVersion":"v1","kind":"Event","metadata":{"name":"e1","namespace":"shop"},"type":"Warning","reason":"Unhealthy","message":"Liveness probe failed: HTTP probe failed with statuscode: 500","count":9,"lastTimestamp":"2026-11-13T11:58:00Z",
"involvedObject":{"kind":"Pod","name":"web-1","uid":"u-web-1","namespace":"shop"` + fp + `}}`
}

func TestProbeEventIsAttachedToTheContainerItNames(t *testing.T) {
	pod := twoContainerPod(","+liveProbe, ","+liveProbe, aCrash, bLive, "", "")
	out := runCall(t, pod+","+probeEvent("spec.containers{b}"), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	line := lineWith(t, out, "container a exits with code 1")
	mustNotContain(t, line, "liveness", "probe")
	mustContain(t, lineWith(t, out, "container b is restarted by the kubelet"), "liveness probe fails (×9")
}

func TestProbeEventOfTheCrashingContainerIsItsOwn(t *testing.T) {
	pod := twoContainerPod(","+liveProbe, ","+liveProbe, aCrash, bLive, "", "")
	out := runCall(t, pod+","+probeEvent("spec.containers{a}"), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, lineWith(t, out, "container a exits with code 1"), "Its liveness probe fails (×9)")
	mustNotContain(t, out, "container b is restarted by the kubelet")
}

// No fieldPath: the event goes to a container only when exactly one container
// of the pod defines that probe type.
func TestProbeEventWithoutFieldPath(t *testing.T) {
	both := twoContainerPod(","+liveProbe, ","+liveProbe, aCrash, bLive, "", "")
	out := runCall(t, both+","+probeEvent(""), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustNotContain(t, out, "liveness probe fails", "restarted by the kubelet")

	one := twoContainerPod("", ","+liveProbe, aCrash, bLive, "", "")
	out = runCall(t, one+","+probeEvent(""), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustNotContain(t, lineWith(t, out, "container a exits with code 1"), "liveness", "probe")
	mustContain(t, lineWith(t, out, "container b is restarted by the kubelet"), "liveness probe fails")
}

// A completed init container with restarts is not "restarted by the kubelet
// because its liveness probe fails" because another container has the event.
func TestSilentCompletedInitContainerAndALivenessEventElsewhere(t *testing.T) {
	initSpec := `{"name":"migrate","image":"x:1"}`
	initStatus := `{"name":"migrate","ready":true,"restartCount":2,"state":{"terminated":{"exitCode":0,"reason":"Completed","finishedAt":"2026-11-13T10:01:00Z"}}}`
	pod := twoContainerPod(","+liveProbe, "", aCrash, `{"name":"b","ready":true,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T10:02:00Z"}}}`, initSpec, initStatus)
	for _, fp := range []string{"spec.containers{a}", ""} {
		out := runCall(t, pod+","+probeEvent(fp), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
		mustNotContain(t, out, "container migrate")
		mustContain(t, out, "container a exits with code 1")
	}
}
