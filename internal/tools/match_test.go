package tools

import (
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The match argument: words, comma between alternatives, no case, a plain
// substring test on the redacted text, before the budget and the folding.

func TestListMatchKeepsRowsWithAWord(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "IMG-1,worker"})
	t.Log("\n" + out.Text)
	mustContain(t, out.Text, "2 of 14 pods match: 2 unhealthy", "img-1", "worker-1", "cannot pull its image", "not scheduled")
	mustNotContain(t, out.Text, "crash-1", "probe-1", "healthy, not listed", "match IMG")

	// A cause and a status are searched too.
	byCause := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "configmap"}).Text
	mustContain(t, byCause, "1 of 14 pods match", "noconfig-1")
	byStatus := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "crashloopbackoff"}).Text
	mustContain(t, byStatus, "crash-1", "api-7f9c8-x2k")
	mustNotContain(t, byStatus, "img-1")
	byNS := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "shop"}).Text
	mustContain(t, byNS, "worker-1")
	mustNotContain(t, byNS, "crash-1")
}

// A healthy object that matches is what was asked for: it gets a row, even in
// a list of more than 8, and the row has no cause.
func TestListMatchShowsHealthyObjects(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "fat-1"}).Text
	mustContain(t, out, "1 of 14 pods match: 1 healthy")
	if l := lineWith(t, out, "fat-1"); !strings.HasSuffix(strings.TrimSpace(l), "-") {
		t.Errorf("healthy row has a cause: %q", l)
	}
	mustNotContain(t, out, "healthy, not listed")
}

// Healthy matches are shown up to a small number, then counted.
func TestListMatchCapsHealthyRows(t *testing.T) {
	objs, _ := largeCluster()
	r, _ := newRunner(t, &fakeReader{objs: objs})
	out := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "ok-"}).Text
	mustContain(t, out, "765 of 1,000 pods match: 765 healthy", "755 more healthy pods match, not shown")
	if n := strings.Count(out, "\nteam-"); n != matchHealthyShown {
		t.Errorf("%d healthy rows, want %d:\n%s", n, matchHealthyShown, out)
	}
}

func TestListMatchNoMatchNamesTheClosest(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "img-2"})
	if out.IsError {
		t.Fatal(out.Text)
	}
	mustContain(t, out.Text, "0 of 14 pods match", "no pods match; closest names: img-1")
	far := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "zzzzzzzzzzzz"}).Text
	mustContain(t, far, "no pods match")
	mustNotContain(t, far, "closest")
}

// A secret value is redacted before the match is made. A match on it finds
// nothing, and the value does not come back in the answer.
func TestMatchCannotFindOrConfirmASecret(t *testing.T) {
	f := diagFixture(t)
	f.logs["pay/crash-1/app#previous"] = "2026-11-13T11:57:02Z FATAL config: DATABASE_URL is not set password=listSecret77\n"
	f.objs = append(f.objs, unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"name": "e-secret", "namespace": "pay"},
		"involvedObject": map[string]any{"kind": "Pod", "name": "img-1", "namespace": "pay"},
		"type":           "Warning", "reason": "Failed", "count": int64(1),
		"message":       "login failed password=evtSecret88",
		"lastTimestamp": "2026-11-13T11:59:00Z",
	}})
	r, _ := newRunner(t, f)

	// The control: the row is found by its name, and its cause has the mark
	// where the value was.
	row := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "crash-1"}).Text
	mustContain(t, row, "1 of 14 pods match", "DATABASE_URL is not set password=[REDACTED:")
	mustNotContain(t, row, "listSecret77")

	ev := call(t, r, "events", map[string]any{"allNamespaces": true, "match": "login failed"}).Text
	mustContain(t, ev, "1 of 8 event groups match", "password=[REDACTED:")
	mustNotContain(t, ev, "evtSecret88")

	for _, c := range []struct {
		tool string
		args map[string]any
		word string
	}{
		{"list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "listSecret77"}, "listSecret77"},
		{"list", map[string]any{"kind": "pods", "allNamespaces": true, "match": "zzqq,LISTSECRET77"}, "listSecret77"},
		{"events", map[string]any{"allNamespaces": true, "match": "evtSecret88"}, "evtSecret88"},
		{"logs", map[string]any{"pod": "api-7f9c8-x2k", "match": "logPassword7"}, "logPassword7"},
		{"logs", map[string]any{"pod": "api-7f9c8-x2k", "match": "logPassword7", "full": true}, "logPassword7"},
	} {
		got := call(t, r, c.tool, c.args)
		if got.IsError {
			t.Fatalf("%s: %s", c.tool, got.Text)
		}
		mustContain(t, got.Text, " match")
		mustNotContain(t, got.Text, c.word, strings.ToLower(c.word), strings.ToUpper(c.word))
		if !strings.Contains(got.Text, "0 of ") && !strings.Contains(got.Text, "no ") {
			t.Errorf("%s: a secret value was found:\n%s", c.tool, got.Text)
		}
	}
}

func TestEventsMatch(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "events", map[string]any{"allNamespaces": true, "match": "probe-1, BackOff"})
	t.Log("\n" + out.Text)
	mustContain(t, out.Text, "2 of 7 event groups match", "probe-1", "api-7f9c8-x2k")
	mustNotContain(t, out.Text, "img-1", "Normal groups hidden")

	// By message.
	byMsg := call(t, r, "events", map[string]any{"allNamespaces": true, "match": "insufficient memory"}).Text
	mustContain(t, byMsg, "1 of 7 event groups match", "worker-1")

	// A Normal group that matches is shown, not set aside.
	normal := call(t, r, "events", map[string]any{"allNamespaces": true, "match": "Pulled"}).Text
	mustContain(t, normal, "Pulled")
	mustNotContain(t, normal, "hidden")

	none := call(t, r, "events", map[string]any{"allNamespaces": true, "match": "img-2"}).Text
	mustContain(t, none, "0 of 7 event groups match", "no event groups match; closest names: img-1")
}

func TestLogsMatch(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k", "match": "REFUSED"})
	t.Log("\n" + out.Text)
	mustContain(t, out.Text, "2 of 305 lines match, in 1 of 5 groups", "db connection refused")
	mustNotContain(t, out.Text, "heartbeat")

	// A group matches on a variable value, and keeps its variable lines.
	hb := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k", "match": "seq=299"}).Text
	mustContain(t, hb, "300 of 305 lines match, in 1 of 5 groups", "heartbeat ok seq=<*>", "seq: min 0")
	mustNotContain(t, hb, "refused")

	none := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k", "match": "nothing-like-this"}).Text
	mustContain(t, none, "0 of 305 lines match", "no log line matches")

	raw := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k", "match": "refused", "full": true}).Text
	mustContain(t, raw, "2 of 305 log entries match", "db connection refused")
	mustNotContain(t, raw, "heartbeat")
}

func TestMatchArgumentIsChecked(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	for _, m := range []string{" ", ",", " , ,", strings.Repeat("a", 300), strings.Repeat("a,", 17)} {
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"list", map[string]any{"kind": "pods", "match": m}},
			{"events", map[string]any{"match": m}},
			{"logs", map[string]any{"pod": "api-7f9c8-x2k", "match": m}},
		} {
			if got := call(t, r, tc.tool, tc.args); !got.IsError || !strings.Contains(got.Text, "match") {
				t.Errorf("%s match %q: %+v", tc.tool, m, got)
			}
		}
	}
	// The other tools do not know it: the decoding is strict.
	if got := call(t, r, "top", map[string]any{"match": "x"}); !got.IsError {
		t.Error("top accepted match")
	}
	if got := call(t, r, "get", map[string]any{"kind": "pod", "name": "x", "match": "x"}); !got.IsError {
		t.Error("get accepted match")
	}
}

// The audit line has the argument, like the others.
func TestMatchIsInTheAuditArgs(t *testing.T) {
	r, path := newRunner(t, diagFixture(t))
	call(t, r, "list", map[string]any{"kind": "pods", "match": "img-1"})
	b, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b, `"match":"img-1"`) {
		t.Errorf("audit line: %s", b)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
