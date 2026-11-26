package tools

import (
	"strings"
	"testing"
)

// Regression tests for rough edges found on a real cluster (2026-11-15).

// A failed pod with no reason must not print an empty "pod failed:" line,
// and its container's problem must be the first one.
func TestFailedPodWithoutReason(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}).Text
	var row string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "job-1 ") {
			row = l
		}
	}
	if !strings.Contains(row, "container report is OOM killed at its memory limit of 1024Mi") || strings.Contains(row, "pod failed:") {
		t.Errorf("job-1 row: %q", row)
	}
	d := describe(t, r, "pod", "job-1", "pay")
	mustNotContain(t, d, "pod failed:\n", "(0 restarts)")
	mustContain(t, d, "container report is OOM killed at its memory limit of 1024Mi. The limit is too low")
}

// A volume that one node holds blocks a pod on another node.
func TestDiagnoseMultiAttach(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "vol-1", "pay")
	mustContain(t, out,
		"likely cause:",
		"the pod cannot start because its volume is still attached to another node (Multi-Attach error, ×3)",
		"claim portal-data",
		"ReadWriteOnce",
	)
}

// JSON log records show their level and message, not the start of the JSON.
func TestLogsShowJSONMessages(t *testing.T) {
	f := diagFixture(t)
	long := strings.Repeat("x", 300)
	f.logs["pay/hot-1/render"] = `2026-11-13T11:00:00Z {"time":"2026-11-13T11:00:00.299Z","resource":{"service.name":"render","pad":"` + long + `"},"kind":"log","level":"info","message":"job started.","job_id":"25dbec4d-32e8-4292-bb7e-72b89a63ed66","job_type":"new-accounts"}
2026-11-13T11:00:01Z {"time":"2026-11-13T11:00:01.000Z","level":"error","msg":"render failed","error":"out of memory","api_key":"jsonSecretKey1"}
2026-11-13T11:00:02Z {"level":"info","message":"retrying after error"}
2026-11-13T11:00:03Z {"not":"a log record"}
`
	r, _ := newRunner(t, f)
	out := call(t, r, "logs", map[string]any{"pod": "hot-1", "namespace": "pay"}).Text
	mustContain(t, out,
		"4 lines read",
		"errors: 1 line in 1 group",
		"ERROR render failed",
		"error=\"out of memory\"",
		"INFO retrying after error",
		`{"not":"a log record"}`,
	)
	mustNotContain(t, out, "jsonSecretKey1", "service.name", long)

	// Without an error, every group is shown, so the first record is readable too.
	f.logs["pay/hot-1/render"] = strings.SplitN(f.logs["pay/hot-1/render"], "\n", 2)[0] + "\n"
	info := call(t, r, "logs", map[string]any{"pod": "hot-1", "namespace": "pay"}).Text
	mustContain(t, info, "INFO job started. job_id=25dbec4d-32e8-4292-bb7e-72b89a63ed66 job_type=new-accounts")

	// full=true keeps the raw lines, still redacted.
	raw := call(t, r, "logs", map[string]any{"pod": "hot-1", "namespace": "pay", "full": true}).Text
	mustContain(t, raw, `"message":"job started."`, `"service.name":"render"`)
	mustNotContain(t, raw, "jsonSecretKey1")
}

func TestLogsGrammar(t *testing.T) {
	f := diagFixture(t)
	f.logs["pay/hot-1/render"] = "2026-11-13T11:00:00Z only line\n"
	r, _ := newRunner(t, f)
	out := call(t, r, "logs", map[string]any{"pod": "hot-1", "namespace": "pay"}).Text
	mustContain(t, out, "1 line read", "1 line (0 errors) in 1 group")
}
