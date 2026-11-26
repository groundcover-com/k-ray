package tools

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kube"
)

func nextLine(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "next: ") {
			return l
		}
	}
	return ""
}

// noCausePods replaces every pod with two that have a problem but no known
// cause, so the result carries a next: line.
func noCausePods(f *fakeReader) {
	var kept []unstructured.Unstructured
	for _, o := range f.objs {
		if o.GetKind() != "Pod" {
			kept = append(kept, o)
		}
	}
	pod := func(ns, name, reason string, restarts int64) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": name, "namespace": ns, "uid": "uid-" + name, "creationTimestamp": "2026-11-13T11:00:00Z"},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "x"}}},
			"status": map[string]any{"phase": "Failed", "reason": reason, "containerStatuses": []any{map[string]any{
				"name": "app", "ready": false, "restartCount": restarts, "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "reason": "Completed"}},
			}}},
		}}
	}
	// evicted-1 ran once and never restarted; expired-1 restarted twice.
	f.objs = append(kept, pod("pay", "evicted-1", "Evicted", 0), pod("shop", "expired-1", "DeadlineExceeded", 2))
}

// A hint must work when it is run as written: it names the namespace when
// that is not the default, and it suggests previous logs only for a
// container that restarted.
func TestNextHintsAreCorrect(t *testing.T) {
	f := diagFixture(t)
	noCausePods(f)
	r, _ := newRunner(t, f) // default namespace: shop
	got := nextLine(t, call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}).Text)
	if got != "next: describe pod evicted-1 namespace=pay · logs pod=evicted-1 namespace=pay" {
		t.Errorf("list next: %q", got)
	}
	got = nextLine(t, call(t, r, "list", map[string]any{"kind": "pods"}).Text)
	if got != "next: describe pod expired-1 · logs pod=expired-1 previous=true" {
		t.Errorf("list next (default namespace): %q", got)
	}
}

// When the cause is given, there is no next: line. A hint would only invite
// a call that is not needed.
func TestNoNextLineWhenTheCauseIsKnown(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	for _, out := range []string{
		call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}).Text,
		call(t, r, "describe", map[string]any{"kind": "pod", "name": "crash-1", "namespace": "pay"}).Text,
		call(t, r, "describe", map[string]any{"kind": "deployment", "name": "ledger", "namespace": "pay"}).Text,
	} {
		mustContain(t, out, "exits with code 1")
		if n := nextLine(t, out); n != "" {
			t.Errorf("next line with a known cause: %q", n)
		}
	}
}

// On the command line, hints use flags, so a person can paste them after
// the command name. They never name the product.
func TestCLIHintsUseFlags(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	r.Surface = "cli"
	logs := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k"}).Text
	mustContain(t, logs, "For raw lines: run logs with --full.", "next: logs api-7f9c8-x2k -c api --previous")
	crash := call(t, r, "describe", map[string]any{"kind": "pod", "name": "crash-1", "namespace": "pay"}).Text
	mustContain(t, crash, "Spec and status: run get pod crash-1 -n pay.")

	noCausePods(r.Reader.(*fakeReader))
	list := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true}).Text
	if got := nextLine(t, list); got != "next: describe pod evicted-1 -n pay · logs evicted-1 -n pay" {
		t.Errorf("list next: %q", got)
	}
	d := call(t, r, "describe", map[string]any{"kind": "pod", "name": "expired-1"}).Text
	if got := nextLine(t, d); got != "next: logs expired-1 --previous · events --for pod/expired-1" {
		t.Errorf("describe next: %q", got)
	}
	for _, out := range []string{list, d, logs, crash} {
		mustNotContain(t, out, "=true", "pod=", "namespace=", strings.ToLower(brand.Name)+" ")
	}
}

// The OOM cause says how long the container ran: a kill seconds after the
// start is not a slow leak.
func TestOOMCauseShowsRunTime(t *testing.T) {
	f := diagFixture(t)
	for i := range f.objs {
		if f.objs[i].GetName() == "job-1" {
			sts, _, _ := unstructured.NestedSlice(f.objs[i].Object, "status", "containerStatuses")
			sts[0].(map[string]any)["state"] = map[string]any{"terminated": map[string]any{
				"exitCode": int64(137), "reason": "OOMKilled", "startedAt": "2026-11-13T11:45:08Z", "finishedAt": "2026-11-13T11:45:18Z",
			}}
			_ = unstructured.SetNestedSlice(f.objs[i].Object, sts, "status", "containerStatuses")
		}
	}
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "job-1", "pay")
	mustContain(t, out, "container report is OOM killed at its memory limit of 1024Mi (ended 14m ago). It ran for 10s before the kill, which points to a limit that is too low for the workload, not to a slow leak.")
}

// Without the metrics API, requests and limits are still shown: for a pod
// that has a problem, in the evidence lines of each container.
func TestResourcesWithoutMetrics(t *testing.T) {
	f := diagFixture(t)
	f.metricsErr = kube.ErrNoMetrics
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "api-7f9c8-x2k", "shop")
	mustContain(t, out,
		"resources: usage not available (no metrics API)",
		"resources: requests cpu 500m, memory 512Mi; limits cpu 1, memory 1Gi",
		"resources: none (no requests, no limits)",
	)
	mustNotContain(t, out, "- container api: CPU request")
}
