package shape

import (
	"strings"
	"testing"
	"time"
)

// Tests for wrong diagnoses: a healthy object must not get a problem.

func podWithRestart(ready bool, finishedAt, reason string, restarts int) string {
	state := `"running":{}`
	if !ready {
		state = `"waiting":{"reason":"CrashLoopBackOff"}`
	}
	r := "False"
	if ready {
		r = "True"
	}
	return `{"kind":"Pod","metadata":{"name":"p","creationTimestamp":"2026-01-01T10:00:00Z"},"spec":{"containers":[{"name":"app"}]},
"status":{"phase":"Running","conditions":[{"type":"Ready","status":"` + r + `"}],"containerStatuses":[{"name":"app","ready":` + map[bool]string{true: "true", false: "false"}[ready] + `,"restartCount":` + string(rune('0'+restarts)) + `,
"state":{` + state + `},"lastState":{"terminated":{"exitCode":137,"reason":"` + reason + `","finishedAt":"` + finishedAt + `"}}}]}}`
}

// Item 3: one day covers a daily load cycle; an older event is history.
func TestSilentOldRestartOfReadyPodIsHealthy(t *testing.T) {
	old := now.Add(-200 * 24 * time.Hour).Format(time.RFC3339)
	for _, reason := range []string{"OOMKilled", "Error"} {
		for _, n := range []int{1, 5} {
			r := Inspect(obj(t, podWithRestart(true, old, reason, n)), now)
			if r.State != Healthy || len(r.Problems) != 0 {
				t.Errorf("%s, %d restarts, 200 days ago: %v %v", reason, n, r.State, r.Problems)
			}
		}
	}
	// The count stays in the RESTARTS column.
	if r := Inspect(obj(t, podWithRestart(true, old, "OOMKilled", 5)), now); r.Cells[2] != "5" {
		t.Errorf("cells %v", r.Cells)
	}
}

func TestRecentRestartOfReadyPodStaysAWarning(t *testing.T) {
	recent := now.Add(-23 * time.Hour).Format(time.RFC3339)
	if r := Inspect(obj(t, podWithRestart(true, recent, "OOMKilled", 1)), now); r.State != Warning {
		t.Errorf("OOM 23h ago: %v", r.State)
	}
	if r := Inspect(obj(t, podWithRestart(true, recent, "Error", 5)), now); r.State != Warning {
		t.Errorf("5 restarts 23h ago: %v", r.State)
	}
}

func TestOldRestartOfPodThatIsNotReadyStaysAProblem(t *testing.T) {
	old := now.Add(-200 * 24 * time.Hour).Format(time.RFC3339)
	r := Inspect(obj(t, podWithRestart(false, old, "OOMKilled", 5)), now)
	if r.State != Unhealthy {
		t.Errorf("crash loop: %v %v", r.State, r.Problems)
	}
	// The pod is Running, a container runs but is not ready.
	u := obj(t, strings.Replace(podWithRestart(true, old, "OOMKilled", 5), `"ready":true`, `"ready":false`, 1))
	if r := Inspect(u, now); r.State != Warning {
		t.Errorf("running but not ready: %v %v", r.State, r.Problems)
	}
}
