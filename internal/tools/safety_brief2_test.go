package tools

import (
	"strings"
	"testing"
)

// describe.go: the short block of one of several matches cuts a problem (240)
// and a cause (320) after redaction.
func TestCutBriefBlockRedactsBeforeTheCut(t *testing.T) {
	msg := leak(300 - len("container app: CreateContainerError: "))
	one := podWaiting("web-1", "CreateContainerError", msg)
	two := strings.ReplaceAll(one, `"namespace":"shop"`, `"namespace":"shop2"`)
	out := runCall(t, one+","+two, "describe", map[string]any{"name": "web-1"})
	mustContain(t, out, "CreateContainerError")
	noLeak(t, "brief block problem", out)

	// A cause: a liveness probe message in the cause of a crashing pod.
	pod := crashPod("web-1", "CrashLoopBackOff", 5, 1, "")
	pod2 := strings.ReplaceAll(pod, `"namespace":"shop"`, `"namespace":"shop2"`)
	ev := event("shop", "e1", "Warning", "Unhealthy", "Pod", "web-1", "Liveness probe failed: "+leak(200-len("Liveness probe failed: ")))
	ev2 := strings.ReplaceAll(ev, `"shop"`, `"shop2"`)
	out = runCall(t, pod+","+pod2+","+ev+","+ev2, "describe", map[string]any{"name": "web-1"})
	mustContain(t, out, "likely cause:")
	noLeak(t, "brief block cause", out)
}
