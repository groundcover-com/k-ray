package tools

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// Item 11 (a): the quote of the last error is never a line that counts zero errors.
func TestSilentLastErrorIsNotAZeroErrorLine(t *testing.T) {
	f := diagFixture(t)
	f.logs["pay/crash-1/app#previous"] = "2026-11-13T11:57:00Z starting ledger\n" +
		"2026-11-13T11:57:02Z FATAL config: DATABASE_URL is not set\n" +
		"2026-11-13T11:57:03Z shutdown: 0 errors\n" +
		"2026-11-13T11:57:03Z stats errors=0 warnings=1\n"
	r, _ := newRunner(t, f)
	out := describe(t, r, "pod", "crash-1", "pay")
	mustContain(t, out, "Last error before the exit: FATAL config: DATABASE_URL is not set")
	mustNotContain(t, out, "Last error before the exit: 2026-11-13T11:57:03Z", "Last error before the exit: shutdown", "Last error before the exit: stats")
}

// Item 11 (b): the quote of the last error is one piece. A full stop inside it
// does not start a sentence that moves in front of the quote.
func TestCompactCauseNeverSplitsInsideTheQuote(t *testing.T) {
	for _, key := range lastErrorKeys {
		full := "container app exits with code 1 (application error), 3 restarts. " + key + "Connection refused. Retrying in 5s"
		got := compactCause(full, 240)
		want := "container app exits with code 1 (application error), 3 restarts; last error: Connection refused. Retrying in 5s"
		if got != want {
			t.Errorf("%q:\ngot  %q\nwant %q", key, got, want)
		}
		// Too long for the budget: the quote is cut in the middle, the start stays.
		short := compactCause(full, 100)
		if len(short) > 100 {
			t.Errorf("%d bytes: %q", len(short), short)
		}
		if i := indexOf(short, "Retrying"); i >= 0 && i < indexOf(short, "Connection") {
			t.Errorf("a fragment of the quote is in front of it: %q", short)
		}
	}
}

// Sentences before the quote still split as before.
func TestCompactCauseStillSplitsOtherSentences(t *testing.T) {
	full := "container app is OOM killed at its memory limit of 64Mi (3 restarts). Memory use now: 60Mi (93% of the limit). The limit is too low for this workload, or the container leaks memory."
	got := compactCause(full, 240)
	want := "container app is OOM killed at its memory limit of 64Mi (3 restarts); memory use now: 60Mi (93% of the limit); the limit is too low for this workload, or the container leaks memory"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The same, end to end: a crash whose last error holds a full stop.
func TestSilentRowCauseKeepsTheQuoteWhole(t *testing.T) {
	pod := crashPod("web-1", "CrashLoopBackOff", 5, 1, "")
	f := &fakeReader{objs: objsFrom(t, "["+pod+"]"), logs: map[string]string{
		"shop/web-1/app#previous": "2026-11-13T11:57:02Z ERROR dial tcp 10.0.0.1:5432: Connection refused. Retrying in 5s\n",
	}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}}
	r, _ := newRunner(t, f)
	list := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "shop"}).Text
	row := lineWith(t, list, "web-1")
	mustContain(t, row, "last error: ")
	if i, j := indexOf(row, "Retrying"), indexOf(row, "last error"); i >= 0 && i < j {
		t.Errorf("a fragment of the quote is in front of it: %s", row)
	}
}
