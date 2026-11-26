package shape

import (
	"strings"
	"testing"
	"time"
)

// Item 10: states that are normal are not problems.

func TestSilentAutoscalerWithOneReplicaRange(t *testing.T) {
	hpa := func(min, max, cur string) string {
		return `{"kind":"HorizontalPodAutoscaler","metadata":{"name":"h"},"spec":{"minReplicas":` + min + `,"maxReplicas":` + max + `,"scaleTargetRef":{"kind":"Deployment","name":"d"}},"status":{"currentReplicas":` + cur + `}}`
	}
	if r := Inspect(obj(t, hpa("3", "3", "3")), now); r.State != Healthy || len(r.Problems) != 0 {
		t.Errorf("min == max: %v %v", r.State, r.Problems)
	}
	if r := Inspect(obj(t, hpa("1", "1", "1")), now); r.State != Healthy {
		t.Errorf("1 and 1: %v %v", r.State, r.Problems)
	}
	if r := Inspect(obj(t, hpa("2", "5", "5")), now); r.State != Warning || r.Problems[0] != "at max replicas (5)" {
		t.Errorf("a real range: %v %v", r.State, r.Problems)
	}
}

func TestSilentBudgetThatForbidsByItsOwnSetting(t *testing.T) {
	pdb := func(spec string, healthy, allowed string) string {
		return `{"kind":"PodDisruptionBudget","metadata":{"name":"p"},"spec":{` + spec + `},"status":{"expectedPods":3,"currentHealthy":` + healthy + `,"disruptionsAllowed":` + allowed + `}}`
	}
	for name, j := range map[string]string{
		"minAvailable 3 of 3":      pdb(`"minAvailable":3`, "3", "0"),
		"minAvailable 100 percent": pdb(`"minAvailable":"100%"`, "3", "0"),
		"minAvailable more":        pdb(`"minAvailable":5`, "3", "0"),
		"maxUnavailable 0":         pdb(`"maxUnavailable":0`, "3", "0"),
		"maxUnavailable 0 percent": pdb(`"maxUnavailable":"0%"`, "3", "0"),
	} {
		if r := Inspect(obj(t, j), now); r.State != Healthy || len(r.Problems) != 0 {
			t.Errorf("%s: %v %v", name, r.State, r.Problems)
		}
	}
	// Allows one when healthy, and a pod is not: the pod is the reason.
	if r := Inspect(obj(t, pdb(`"minAvailable":2`, "2", "0")), now); r.State != Warning || !strings.Contains(r.Problems[0], "no disruptions allowed (2 of 3 pods healthy)") {
		t.Errorf("sick pod: %v %v", r.State, r.Problems)
	}
}

// A CronJob whose status records no success: the result of the last run is not
// known from the object alone. With a success recorded that is older than the
// last run, the last run did not succeed.
func TestSilentCronJobWithoutARecordedSuccessIsNotAFailure(t *testing.T) {
	created := now.Add(-5 * time.Hour).Format(time.RFC3339)
	last := now.Add(-20 * time.Minute).Format(time.RFC3339)
	u := obj(t, `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"c","creationTimestamp":"`+created+`"},"spec":{"schedule":"0 * * * *"},"status":{"lastScheduleTime":"`+last+`"}}`)
	s := Signals(u, now)
	if len(s) != 1 || s[0].Text != "result of the last run is not known: scheduled 20m ago, the status records no success" {
		t.Fatalf("signals: %+v", s)
	}
	if strings.Contains(s[0].Text, "did not succeed") {
		t.Errorf("claims a failure: %s", s[0].Text)
	}
}
