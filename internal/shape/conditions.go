package shape

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Signals are what an object says about itself that is wrong, found in a way
// that works for any kind, custom resources included: the entries of
// status.conditions that are not in their healthy state, and a few status
// fields that say more than the conditions (see statusFacts).

// Signal is one thing that is wrong with an object.
type Signal struct {
	State State // Unhealthy or Warning
	Text  string
	// Max is the length to cut Text to when it is shown, after redaction
	// (see Row.Lines); 0 means DefaultProblemMax.
	Max int
}

// Condition messages are cut to about this many bytes.
const (
	conditionMessageMax = 200
	// An autoscaler names the reason that a metric cannot be computed in its
	// condition message. It is the point, so it gets room.
	autoscalerMessageMax = 500
)

// Condition polarity. Most conditions are healthy when True (Ready,
// Available, Bound, Accepted). Some name a problem and are healthy when
// False. This is the whole rule; add a row to extend it, and a case to
// TestConditionPolarity.
//
// A type is matched by its lowercase name, first against the exact rows, then
// against the words that a type contains.
var (
	// healthyWhenFalse are types that name a problem: True is bad.
	healthyWhenFalse = map[string]bool{
		"replicafailure": true, "failed": true, "failuretarget": true,
		"memorypressure": true, "diskpressure": true, "pidpressure": true,
		"networkunavailable": true, "stalled": true, "degraded": true,
		"conflict": true, "unschedulable": true, "nonstructuralschema": true,
		"terminating": true, "scalinglimited": true, "suspended": true,
		"disruptiontarget": true, "podresizepending": true, "podresizeinprogress": true,
		"resizing": true, "filesystemresizepending": true,
		// Node conditions of a node problem detector: True is the fault.
		"kerneldeadlock": true, "readonlyfilesystem": true, "frequentkubeletrestart": true,
		"frequentdockerrestart": true, "frequentcontainerdrestart": true, "frequentunregisternetdevice": true,
		"corruptdockeroverlay2": true, "ntpproblem": true, "kubeletproblem": true,
		"containerruntimeproblem": true, "filesystemcorruptionproblem": true, "vmeventscheduled": true,
	}
	// problemWords: a type that contains one of these names a problem too.
	problemWords = []string{
		"failure", "failed", "pressure", "unavailable", "degraded", "stalled", "conflict", "error", "remaining",
		"problem", "deadlock", "corrupt", "readonly", "frequent",
	}
	// healthyWhenTrue are the types whose polarity is known to be the usual
	// one: True is healthy, so False is a fault. A type that is in neither
	// table and has none of the words has an unknown polarity: its False is
	// only a warning, because it may be the healthy value.
	healthyWhenTrue = map[string]bool{
		"ready": true, "available": true, "bound": true, "accepted": true, "initialized": true,
		"progressing": true, "containersready": true, "podscheduled": true, "resolvedrefs": true,
		"programmed": true, "established": true, "namesaccepted": true, "abletoscale": true,
		"scalingactive": true, "healthy": true, "synced": true, "reconciled": true, "approved": true,
		"complete": true, "successcriteriamet": true, "readyforquery": true, "certificatesigned": true,
	}
	healthyWords = []string{"ready", "available", "healthy", "synced", "accepted", "bound", "initialized", "established", "programmed", "reconciled"}
	// notAProblemWhenFalse are types whose False means "not yet" or "not
	// this way": a running Job is not Complete, and has not met its success
	// criteria.
	notAProblemWhenFalse = map[string]bool{"complete": true, "successcriteriamet": true}
	// softTypes are bad news of a lesser kind: a warning, not a failure.
	softTypes = map[string]bool{"scalinglimited": true, "suspended": true}
	// informational types say a fact, not a state to judge: whatever the
	// status, they make no problem line (a claim that is Unused=False is in use).
	informational = map[string]bool{"unused": true}
)

// ConditionHealthy reports whether a condition is in its healthy state.
// Unknown is never healthy.
func ConditionHealthy(typ, status, reason string) bool {
	t := strings.ToLower(typ)
	if t == "progressing" && reason == "ProgressDeadlineExceeded" {
		return false // a Deployment that gave up: bad whatever the status says
	}
	switch status {
	case "True":
		return !namesProblem(t)
	case "False":
		return namesProblem(t) || notAProblemWhenFalse[t]
	}
	return false
}

// namesProblem reports whether a condition type is a problem when True.
func namesProblem(lower string) bool {
	if healthyWhenFalse[lower] {
		return true
	}
	for _, w := range problemWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// polarityKnown reports whether the healthy value of a condition type is
// known: it names a problem (healthy when False) or is a known type that is
// healthy when True.
func polarityKnown(typ string) bool {
	t := strings.ToLower(typ)
	if namesProblem(t) || healthyWhenTrue[t] {
		return true
	}
	for _, w := range healthyWords {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

type condition struct {
	typ, status, reason, message string
	at                           time.Time
}

func conditionsOf(u *unstructured.Unstructured) []condition {
	list, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var out []condition
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		var c condition
		c.typ, _ = m["type"].(string)
		c.status = fmt.Sprint(m["status"])
		c.reason, _ = m["reason"].(string)
		c.message, _ = m["message"].(string)
		for _, key := range []string{"lastTransitionTime", "lastUpdateTime", "lastProbeTime"} {
			if s, ok := m[key].(string); ok {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					c.at = t
					break
				}
			}
		}
		if c.typ != "" {
			out = append(out, c)
		}
	}
	return out
}

// HasConditions reports whether the object has any status.conditions.
func HasConditions(u *unstructured.Unstructured) bool { return len(conditionsOf(u)) > 0 }

// coveredByRow says which condition types a kind's list row already reports,
// so that a fact is said once. A pod is covered whole: its containers say it.
func coveredByRow(kind, typ string) bool {
	t := strings.ToLower(typ)
	switch kind {
	case "Pod":
		return true
	case "Node":
		switch t {
		case "ready", "memorypressure", "diskpressure", "pidpressure", "networkunavailable":
			return true
		}
	case "Deployment":
		return t == "progressing" || t == "replicafailure" || t == "available"
	case "ReplicaSet":
		return t == "replicafailure"
	case "Job":
		return t == "failed" || t == "suspended" || t == "complete"
	case "PodDisruptionBudget":
		return t == "disruptionallowed"
	}
	return false
}

// Signals returns what is wrong with the object, worst first within each
// group: its conditions that are not healthy (newest first), then the facts
// of statusFacts. It reports nothing that the list row of the kind already
// says.
func Signals(u *unstructured.Unstructured, now time.Time) []Signal {
	kind := u.GetKind()
	if kind == "Pod" || kind == "Event" || kind == "ConfigMap" {
		return nil
	}
	var out []Signal
	conds := conditionsOf(u)
	sort.SliceStable(conds, func(i, j int) bool { return conds[i].at.After(conds[j].at) })
	limit := conditionMessageMax
	if kind == "HorizontalPodAutoscaler" {
		limit = autoscalerMessageMax
	}
	for _, c := range conds {
		if ConditionHealthy(c.typ, c.status, c.reason) || coveredByRow(kind, c.typ) || informational[strings.ToLower(c.typ)] {
			continue
		}
		st := Unhealthy
		parts := []string{fmt.Sprintf("%s is %s", c.typ, c.status)}
		if c.status == "False" && !polarityKnown(c.typ) {
			// Its healthy value is not known: say what is read, claim no fault.
			st = Warning
			parts = []string{fmt.Sprintf("condition %s is False", c.typ)}
		}
		if c.reason != "" {
			parts = append(parts, c.reason)
		}
		max := 0
		if m := strings.Join(strings.Fields(c.message), " "); m != "" {
			max = len(strings.Join(parts, ": ")) + 2 + limit // the message keeps limit bytes
			parts = append(parts, m)
		}
		if softTypes[strings.ToLower(c.typ)] && c.status == "True" {
			st = Warning
		}
		out = append(out, Signal{State: st, Text: strings.Join(parts, ": "), Max: max})
	}
	return append(out, statusFacts(u, kind, now, conds)...)
}

// statusFacts are the status fields that say more than the conditions.
func statusFacts(u *unstructured.Unstructured, kind string, now time.Time, conds []condition) []Signal {
	var out []Signal
	switch kind {
	case "HorizontalPodAutoscaler":
		metrics, _, _ := unstructured.NestedSlice(u.Object, "status", "currentMetrics")
		want, _, _ := unstructured.NestedSlice(u.Object, "spec", "metrics")
		scalingFalse := false
		for _, c := range conds {
			scalingFalse = scalingFalse || strings.EqualFold(c.typ, "ScalingActive") && c.status != "True"
		}
		if len(metrics) == 0 && len(want) > 0 && !scalingFalse && now.Sub(u.GetCreationTimestamp().Time) > 3*time.Minute {
			out = append(out, Signal{State: Warning, Text: "no current metric value: status.currentMetrics is empty, so the autoscaler cannot decide"})
		}
	case "CronJob":
		out = append(out, cronFacts(u, now)...)
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "Pod":
	default:
		// Anything else with replicas: desired against ready.
		want, found := NestedInt(u.Object, "spec", "replicas")
		if !found || want <= 0 {
			break
		}
		for _, f := range [][2]string{{"readyReplicas", "ready"}, {"availableReplicas", "available"}} {
			if have, ok := NestedInt(u.Object, "status", f[0]); ok {
				if have < want {
					out = append(out, Signal{State: Unhealthy, Text: fmt.Sprintf("%d of %d replicas %s", have, want, f[1])})
				}
				break
			}
		}
	}
	return out
}

// cronFacts checks a CronJob against its own schedule and status.
func cronFacts(u *unstructured.Unstructured, now time.Time) []Signal {
	spec, _, _ := unstructured.NestedString(u.Object, "spec", "schedule")
	suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	tz, _, _ := unstructured.NestedString(u.Object, "spec", "timeZone")
	c, err := ParseCron(spec)
	if err != nil {
		return []Signal{{State: Unhealthy, Text: fmt.Sprintf("schedule %q is not valid: %v", spec, err)}}
	}
	if !c.CanFire() {
		return []Signal{{State: Unhealthy, Text: fmt.Sprintf("schedule %q can never fire: no day it names exists in the months it names", spec)}}
	}
	var out []Signal
	last, hasLast, _ := unstructured.NestedString(u.Object, "status", "lastScheduleTime")
	lastT, errLast := time.Parse(time.RFC3339, last)
	if !suspended && (!hasLast || last == "") {
		created := u.GetCreationTimestamp().Time
		grace := 2 * time.Minute
		if tz != "" {
			grace += 26 * time.Hour // the schedule is read in that zone; this reader is not
		}
		if next, ok := c.Next(created); ok && now.Sub(next) > grace {
			out = append(out, Signal{State: Warning, Text: fmt.Sprintf("never scheduled: it is %s old and the schedule %q should have fired %s ago", Age(created, now), spec, Age(next, now))})
		}
	}
	if hasLast && errLast == nil {
		active, _, _ := unstructured.NestedSlice(u.Object, "status", "active")
		ok, hasOK, _ := unstructured.NestedString(u.Object, "status", "lastSuccessfulTime")
		okT, errOK := time.Parse(time.RFC3339, ok)
		if len(active) == 0 && now.Sub(lastT) > 2*time.Minute && (!hasOK || ok == "" || errOK == nil && okT.Before(lastT)) {
			if hasOK && ok != "" && errOK == nil {
				// A success is recorded and it is older than the last run.
				out = append(out, Signal{State: Warning, Text: "last run did not succeed: scheduled " + Age(lastT, now) + " ago, last success " + Age(okT, now) + " ago"})
			} else {
				// No success is recorded. The Job of the last run is not in
				// this object, and an older cluster does not record it: the
				// result is not known from here.
				out = append(out, Signal{State: Warning, Text: "result of the last run is not known: scheduled " + Age(lastT, now) + " ago, the status records no success"})
			}
		}
	}
	return out
}

// NestedInt reads a number that may be an int64 (from the API) or a float64
// (from JSON decoded by hand). It reports false when the field is missing or
// is not a number.
func NestedInt(obj map[string]any, path ...string) (int64, bool) {
	v, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if !found || err != nil {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}
