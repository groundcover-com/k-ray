package shape

import (
	"strings"
	"testing"
	"time"
)

// TestConditionPolarity is the table of the polarity rule: which (type,
// status, reason) is healthy. To extend the rule, add a row to
// healthyWhenFalse or problemWords and a case here.
func TestConditionPolarity(t *testing.T) {
	cases := []struct {
		typ, status, reason string
		healthy             bool
	}{
		{"Ready", "True", "", true},
		{"Ready", "False", "NotReady", false},
		{"Ready", "Unknown", "", false},
		{"Available", "True", "", true},
		{"Available", "False", "MinimumReplicasUnavailable", false},
		{"Bound", "True", "", true},
		{"Accepted", "True", "", true},
		{"ResolvedRefs", "False", "RefNotPermitted", false},
		// Types that name a problem are healthy when False.
		{"ReplicaFailure", "True", "FailedCreate", false},
		{"ReplicaFailure", "False", "", true},
		{"Failed", "True", "BackoffLimitExceeded", false},
		{"Failed", "False", "", true},
		{"MemoryPressure", "True", "", false},
		{"MemoryPressure", "False", "", true},
		{"DiskPressure", "True", "", false},
		{"PIDPressure", "True", "", false},
		{"NetworkUnavailable", "True", "", false},
		{"NetworkUnavailable", "False", "", true},
		{"Stalled", "True", "", false},
		{"Degraded", "True", "", false},
		{"Degraded", "False", "", true},
		{"Conflict", "True", "", false},
		{"ReconciliationError", "True", "", false},
		{"NamespaceDeletionDiscoveryFailure", "True", "", false},
		{"NamespaceContentRemaining", "True", "", false},
		{"ScalingLimited", "True", "TooManyReplicas", false},
		{"ScalingLimited", "False", "", true},
		// Unknown is never healthy, whatever the polarity.
		{"Failed", "Unknown", "", false},
		{"Degraded", "Unknown", "", false},
		// Special cases.
		{"Complete", "True", "", true},
		{"Complete", "False", "", true},
		{"SuccessCriteriaMet", "True", "", true},
		{"SuccessCriteriaMet", "False", "", true},
		{"FailureTarget", "True", "", false},
		{"Progressing", "True", "NewReplicaSetAvailable", true},
		{"Progressing", "True", "ProgressDeadlineExceeded", false},
		{"Progressing", "False", "ProgressDeadlineExceeded", false},
		{"Progressing", "False", "", false},
		{"AbleToScale", "True", "", true},
		{"ScalingActive", "False", "FailedGetResourceMetric", false},
	}
	for _, c := range cases {
		if got := ConditionHealthy(c.typ, c.status, c.reason); got != c.healthy {
			t.Errorf("ConditionHealthy(%s, %s, %s) = %v, want %v", c.typ, c.status, c.reason, got, c.healthy)
		}
	}
}

func TestSignalsForACustomResource(t *testing.T) {
	u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w","namespace":"x"},
	  "status":{"conditions":[
	   {"type":"Ready","status":"False","reason":"DependencyMissing","message":"database \"db\" is not ready","lastTransitionTime":"2026-11-13T11:00:00Z"},
	   {"type":"Synced","status":"True","lastTransitionTime":"2026-11-13T11:30:00Z"},
	   {"type":"Stalled","status":"True","reason":"Retries","message":"gave up","lastTransitionTime":"2026-11-13T11:50:00Z"},
	   {"type":"Degraded","status":"False"}]}}`)
	sigs := Signals(u, now)
	if len(sigs) != 2 {
		t.Fatalf("signals: %+v", sigs)
	}
	// Newest first.
	if sigs[0].Text != "Stalled is True: Retries: gave up" || sigs[1].Text != `Ready is False: DependencyMissing: database "db" is not ready` {
		t.Errorf("signals: %+v", sigs)
	}
	row := Inspect(u, now)
	if row.State != Unhealthy || len(row.Problems) != 2 {
		t.Errorf("row: %+v", row)
	}
}

func TestCustomResourceWithHealthyConditionsIsHealthy(t *testing.T) {
	u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	if row := Inspect(u, now); row.State != Healthy || len(row.Problems) != 0 {
		t.Errorf("row: %+v", row)
	}
	none := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"}}`)
	if row := Inspect(none, now); row.State != NoHealth {
		t.Errorf("row: %+v", row)
	}
}

func TestSignalsCutLongMessagesAndSayEachFactOnce(t *testing.T) {
	long := strings.Repeat("word ", 100)
	u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"status":{"conditions":[{"type":"Ready","status":"False","reason":"R","message":"`+long+`"}]}}`)
	// The signal holds the whole message; the cut comes when it is shown,
	// after redaction (see Row.Lines).
	if s := Signals(u, now)[0].Text; len(s) < len(long) {
		t.Errorf("signal text is cut before redaction: %d bytes", len(s))
	}
	s := Inspect(u, now).Lines(nil)[0]
	if len(s) > conditionMessageMax+40 {
		t.Errorf("message not cut: %d bytes", len(s))
	}
	// A Deployment row already says Progressing and ReplicaFailure.
	d := obj(t, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"d"},"spec":{"replicas":2},
	  "status":{"availableReplicas":2,"readyReplicas":2,"updatedReplicas":2,"conditions":[{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded","message":"m"}]}}`)
	row := Inspect(d, now)
	n := 0
	for _, p := range row.Problems {
		if strings.Contains(p, "Progress") || strings.Contains(p, "progress") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("progress said %d times: %v", n, row.Problems)
	}
}

func TestSignalsForReplicasOfACustomResource(t *testing.T) {
	u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"spec":{"replicas":3},"status":{"readyReplicas":1}}`)
	if s := Signals(u, now); len(s) != 1 || s[0].Text != "1 of 3 replicas ready" {
		t.Errorf("signals: %+v", s)
	}
}

func TestAutoscalerSignals(t *testing.T) {
	created := now.Add(-time.Hour).Format(time.RFC3339)
	cannot := obj(t, `{"apiVersion":"autoscaling/v2","kind":"HorizontalPodAutoscaler","metadata":{"name":"h","creationTimestamp":"`+created+`"},
	  "spec":{"maxReplicas":5,"scaleTargetRef":{"kind":"Deployment","name":"web"},"metrics":[{"type":"Resource"}]},
	  "status":{"currentReplicas":2,"conditions":[{"type":"ScalingActive","status":"False","reason":"FailedGetResourceMetric","message":"the HPA was unable to compute the replica count: failed to get cpu utilization: missing request for cpu in container app of Pod web-1"}]}}`)
	row := Inspect(cannot, now)
	if row.State != Unhealthy || len(row.Problems) != 1 || !strings.Contains(row.Problems[0], "missing request for cpu in container app of Pod web-1") {
		t.Errorf("row: %+v", row)
	}
	nometric := obj(t, `{"apiVersion":"autoscaling/v2","kind":"HorizontalPodAutoscaler","metadata":{"name":"h","creationTimestamp":"`+created+`"},
	  "spec":{"maxReplicas":5,"scaleTargetRef":{"kind":"Deployment","name":"web"},"metrics":[{"type":"Resource"}]},"status":{"currentReplicas":2}}`)
	if row := Inspect(nometric, now); row.State != Warning || !strings.Contains(row.Problems[0], "no current metric") {
		t.Errorf("row: %+v", row)
	}
}

func cron(t *testing.T, spec, extra string, age time.Duration) []Signal {
	t.Helper()
	created := now.Add(-age).Format(time.RFC3339)
	u := obj(t, `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"c","creationTimestamp":"`+created+`"},"spec":{"schedule":"`+spec+`"`+extra+`},"status":{}}`)
	return Signals(u, now)
}

func TestCronJobSignals(t *testing.T) {
	if s := cron(t, "*/5 * * * *", "", 2*time.Hour); len(s) != 1 || !strings.HasPrefix(s[0].Text, "never scheduled: it is 2h old") {
		t.Errorf("never ran: %+v", s)
	}
	if s := cron(t, "*/5 * * * *", "", 30*time.Second); len(s) != 0 {
		t.Errorf("a new cron job is not late: %+v", s)
	}
	if s := cron(t, "*/5 * * * *", `,"suspend":true`, 2*time.Hour); len(s) != 0 {
		t.Errorf("a suspended cron job is not late: %+v", s)
	}
	if s := cron(t, "0 0 31 2 *", "", 2*time.Hour); len(s) != 1 || s[0].State != Unhealthy || !strings.Contains(s[0].Text, "can never fire") {
		t.Errorf("impossible date: %+v", s)
	}
	if s := cron(t, "0 0 30 2 *", "", 2*time.Hour); len(s) != 1 || !strings.Contains(s[0].Text, "can never fire") {
		t.Errorf("30 February: %+v", s)
	}
	if s := cron(t, "0 0 29 2 *", "", time.Hour); len(s) != 0 {
		t.Errorf("29 February is possible: %+v", s)
	}
	if s := cron(t, "0 0 31 4,6 *", "", time.Hour); len(s) != 1 {
		t.Errorf("31 April and June: %+v", s)
	}
	if s := cron(t, "61 * * * *", "", time.Hour); len(s) != 1 || !strings.Contains(s[0].Text, "is not valid") {
		t.Errorf("invalid: %+v", s)
	}
	if s := cron(t, "* * *", "", time.Hour); len(s) != 1 || !strings.Contains(s[0].Text, "3 fields") {
		t.Errorf("too few fields: %+v", s)
	}
	// A yearly job that is a month old has not missed anything.
	if s := cron(t, "0 0 1 1 *", "", 24*time.Hour); len(s) != 0 {
		t.Errorf("yearly: %+v", s)
	}
}

func TestCronJobLastRunFailed(t *testing.T) {
	created := now.Add(-5 * time.Hour).Format(time.RFC3339)
	last := now.Add(-20 * time.Minute).Format(time.RFC3339)
	ok := now.Add(-3 * time.Hour).Format(time.RFC3339)
	u := obj(t, `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"c","creationTimestamp":"`+created+`"},"spec":{"schedule":"0 * * * *"},"status":{"lastScheduleTime":"`+last+`","lastSuccessfulTime":"`+ok+`"}}`)
	s := Signals(u, now)
	if len(s) != 1 || s[0].Text != "last run did not succeed: scheduled 20m ago, last success 3h ago" {
		t.Errorf("signals: %+v", s)
	}
}

func TestParseCron(t *testing.T) {
	c, err := ParseCron("30 2 * * mon-fri")
	if err != nil {
		t.Fatal(err)
	}
	// Friday 2026-11-13 12:00 -> next is Monday 2026-11-16 02:30.
	next, ok := c.Next(now)
	if !ok || !next.Equal(time.Date(2026, 11, 16, 2, 30, 0, 0, time.UTC)) {
		t.Errorf("next: %v %v", next, ok)
	}
	for _, spec := range []string{"@hourly", "@daily", "*/15 0-6 1,15 * *", "0 0 * * 7", "5/10 * * jan,feb *"} {
		if _, err := ParseCron(spec); err != nil {
			t.Errorf("%s: %v", spec, err)
		}
	}
	for _, spec := range []string{"", "* * * *", "a * * * *", "*/0 * * * *", "5-1 * * * *", "* 24 * * *", "* * 0 * *"} {
		if _, err := ParseCron(spec); err == nil {
			t.Errorf("%q should be invalid", spec)
		}
	}
}
