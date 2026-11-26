package shape

import (
	"strconv"
	"testing"
)

// Item 9: a rolling update inside its budget is a warning, not a failure.

func deployment(replicas, available, updated, total int, strategy, reason string) string {
	cond := ""
	if reason != "" {
		cond = `{"type":"Progressing","status":"True","reason":"` + reason + `"}`
	}
	if reason == "ProgressDeadlineExceeded" {
		cond = `{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded","message":"deadline"}`
	}
	return `{"kind":"Deployment","metadata":{"name":"d"},"spec":{"replicas":` + itoa(replicas) + strategy + `},
"status":{"replicas":` + itoa(total) + `,"readyReplicas":` + itoa(available) + `,"availableReplicas":` + itoa(available) + `,"updatedReplicas":` + itoa(updated) + `,"conditions":[` + cond + `]}}`
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestSilentRollingDeploymentInsideItsBudgetIsNotUnhealthy(t *testing.T) {
	r := Inspect(obj(t, deployment(4, 3, 2, 5, "", "ReplicaSetUpdated")), now)
	if r.State != Warning || len(r.Problems) != 1 || r.Problems[0] != "rolling out: 3 of 4 available" {
		t.Errorf("row: %v %v", r.State, r.Problems)
	}
	// 50 percent of 4, rounded down, is 2.
	strat := `,"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":"50%"}}`
	if r := Inspect(obj(t, deployment(4, 2, 2, 5, strat, "ReplicaSetUpdated")), now); r.State != Warning {
		t.Errorf("50 percent: %v %v", r.State, r.Problems)
	}
	// An absolute budget.
	strat = `,"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":2}}`
	if r := Inspect(obj(t, deployment(6, 4, 2, 7, strat, "ReplicaSetUpdated")), now); r.State != Warning {
		t.Errorf("absolute: %v %v", r.State, r.Problems)
	}
}

func TestRollingDeploymentOutsideItsBudgetStaysUnhealthy(t *testing.T) {
	cases := map[string]string{
		"below the default budget": deployment(4, 2, 2, 5, "", "ReplicaSetUpdated"),
		"one replica":              deployment(1, 0, 1, 2, "", "ReplicaSetUpdated"), // 25 percent of 1 rounds down to 0
		"budget 0":                 deployment(4, 3, 2, 5, `,"strategy":{"rollingUpdate":{"maxUnavailable":0}}`, "ReplicaSetUpdated"),
		"recreate":                 deployment(4, 3, 2, 5, `,"strategy":{"type":"Recreate"}`, "ReplicaSetUpdated"),
		"deadline exceeded":        deployment(4, 3, 2, 5, "", "ProgressDeadlineExceeded"),
		"rollout is complete":      deployment(4, 3, 4, 4, "", "NewReplicaSetAvailable"),
		"no progressing":           deployment(4, 3, 2, 5, "", ""),
	}
	for name, j := range cases {
		if r := Inspect(obj(t, j), now); r.State != Unhealthy {
			t.Errorf("%s: %v %v", name, r.State, r.Problems)
		}
	}
}

func TestRollingStatefulSet(t *testing.T) {
	sts := func(replicas, ready, updated int, strategy string) string {
		return `{"kind":"StatefulSet","metadata":{"name":"s"},"spec":{"replicas":` + itoa(replicas) + strategy + `},"status":{"replicas":` + itoa(replicas) + `,"readyReplicas":` + itoa(ready) + `,"updatedReplicas":` + itoa(updated) + `}}`
	}
	if r := Inspect(obj(t, sts(3, 2, 1, "")), now); r.State != Warning || r.Problems[0] != "rolling out: 2 of 3 ready" {
		t.Errorf("one pod at a time: %v %v", r.State, r.Problems)
	}
	for name, j := range map[string]string{
		"two missing":      sts(3, 1, 1, ""),
		"no update":        sts(3, 2, 3, ""),
		"on delete":        sts(3, 2, 1, `,"updateStrategy":{"type":"OnDelete"}`),
		"budget of zero":   sts(3, 2, 1, `,"updateStrategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":0}}`),
		"nothing is ready": sts(3, 0, 0, ""),
	} {
		if r := Inspect(obj(t, j), now); r.State != Unhealthy {
			t.Errorf("%s: %v %v", name, r.State, r.Problems)
		}
	}
}

// A StatefulSet whose current and update revision are the same is not rolling,
// whatever the count of updated pods.
func TestStatefulSetWithOneRevisionIsNotRolling(t *testing.T) {
	j := `{"kind":"StatefulSet","metadata":{"name":"s"},"spec":{"replicas":3},"status":{"replicas":3,"readyReplicas":2,"updatedReplicas":2,"currentRevision":"s-1","updateRevision":"s-1"}}`
	if r := Inspect(obj(t, j), now); r.State != Unhealthy {
		t.Errorf("%v %v", r.State, r.Problems)
	}
}

func TestRollingDaemonSet(t *testing.T) {
	ds := func(desired, ready, updated int, strategy string) string {
		return `{"kind":"DaemonSet","metadata":{"name":"s"},"spec":{` + strategy + `"template":{}},"status":{"desiredNumberScheduled":` + itoa(desired) + `,"numberReady":` + itoa(ready) + `,"numberAvailable":` + itoa(ready) + `,"updatedNumberScheduled":` + itoa(updated) + `,"numberUnavailable":` + itoa(desired-ready) + `}}`
	}
	if r := Inspect(obj(t, ds(10, 9, 5, "")), now); r.State != Warning || r.Problems[0] != "rolling out: 9 of 10 ready" {
		t.Errorf("one node at a time: %v %v", r.State, r.Problems)
	}
	for name, j := range map[string]string{
		"two missing": ds(10, 8, 5, ""),
		"no update":   ds(10, 9, 10, ""),
		"on delete":   ds(10, 9, 5, `"updateStrategy":{"type":"OnDelete"},`),
	} {
		if r := Inspect(obj(t, j), now); r.State != Unhealthy {
			t.Errorf("%s: %v %v", name, r.State, r.Problems)
		}
	}
	if r := Inspect(obj(t, ds(10, 8, 5, `"updateStrategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":"20%"}},`)), now); r.State != Warning {
		t.Errorf("20 percent of 10 is 2: %v %v", r.State, r.Problems)
	}
}
