package shape

import (
	"testing"
	"time"
)

// Item 11 (a): a line that says there were no errors is not an error line.
func TestSilentLinesThatCountZeroErrorsAreNotErrors(t *testing.T) {
	for _, s := range []string{
		"sync finished: 0 errors",
		"sync finished with 0 errors, 3 warnings",
		"2026-11-13T11:00:00Z stats errors=0 warnings=2",
		"error_count: 0",
		`{"msg":"batch done","errors":0}`,
		"errors: 0",
		"no errors",
		"completed, no errors found",
		"done without errors",
		"failures=0 exceptions=0",
		"Errors: 0, Failures: 0",
		"panics_total=0",
	} {
		if IsErrorLine(s) {
			t.Errorf("%q is not an error line", s)
		}
	}
}

func TestIsErrorLineStillFindsErrors(t *testing.T) {
	for _, s := range []string{
		"ERROR cannot connect",
		"sync finished: 3 errors",
		"errors=3 warnings=0",
		"0 errors but FATAL: config missing",
		"fatal error: out of memory",
		"error: 0 rows returned",
		"no space left on device error",
		"panic: runtime error",
		"E1113 12:00:00.000000 1 reflector.go:1] failed to list",
	} {
		if !IsErrorLine(s) {
			t.Errorf("%q is an error line", s)
		}
	}
}

// Item 11 (c): every descriptor that Kubernetes accepts is a valid schedule.
func TestEveryCronDescriptorIsValid(t *testing.T) {
	for _, spec := range []string{"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly", "@every 1h30m", "@every 30s", "@every 5m", "@YEARLY", "CRON_TZ=Europe/Paris @daily", "@every   2h"} {
		if _, err := ParseCron(spec); err != nil {
			t.Errorf("%q: %v", spec, err)
		}
		created := now.Add(-10 * time.Second).Format(time.RFC3339)
		u := obj(t, `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"c","creationTimestamp":"`+created+`"},"spec":{"schedule":"`+spec+`"}}`)
		for _, s := range Signals(u, now) {
			t.Errorf("%q: a valid schedule gets a signal: %s", spec, s.Text)
		}
	}
	for _, spec := range []string{"@every", "@every soon", "@every -1h", "@every 0s", "@sometimes", "@every 1h 30m"} {
		if _, err := ParseCron(spec); err == nil {
			t.Errorf("%q should be invalid", spec)
		}
	}
}

func TestEveryScheduleFiresAtItsInterval(t *testing.T) {
	c, err := ParseCron("@every 1h30m")
	if err != nil || !c.CanFire() {
		t.Fatalf("%v", err)
	}
	next, ok := c.Next(now)
	if !ok || !next.Equal(now.Add(90*time.Minute)) {
		t.Errorf("next: %v %v", next, ok)
	}
	// A CronJob with that schedule that was never scheduled, 5h old, is late.
	created := now.Add(-5 * time.Hour).Format(time.RFC3339)
	u := obj(t, `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":"c","creationTimestamp":"`+created+`"},"spec":{"schedule":"@every 1h30m"},"status":{}}`)
	if s := Signals(u, now); len(s) != 1 || s[0].State != Warning {
		t.Errorf("signals: %+v", s)
	}
}
