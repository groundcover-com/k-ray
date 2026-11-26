package redact

import (
	"strings"
	"testing"
)

var keyBody = []string{
	"MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun",
	"VTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u+qxCPDwCsXAPcVQGpGxQqYB3nNnm",
	"k3nQ0lN3jx1Yb1Xv8N0o5K1rJvZ0p3w5a2Qh3mWc8bK4d9sT7uV6xY1zA2cE5fG8",
	"Zq8sT2vB9xK3mP7wL5nR1d==",
}

const (
	pemBegin = "-----BEGIN RSA PRIVATE KEY-----"
	pemEnd   = "-----END RSA PRIVATE KEY-----"
)

func noKeyMaterial(t *testing.T, lines []string) {
	t.Helper()
	all := strings.Join(lines, "\n")
	for _, l := range keyBody {
		if strings.Contains(all, l) || strings.Contains(all, l[:12]) {
			t.Errorf("key material %q left in output:\n%s", l, all)
		}
	}
	if strings.Contains(all, "END RSA") || strings.Contains(all, "BEGIN RSA") {
		t.Errorf("a PEM line is left in output:\n%s", all)
	}
}

func TestLinesMasksAWholePEMBlock(t *testing.T) {
	in := append([]string{"GET /health 200", pemBegin}, keyBody...)
	in = append(in, pemEnd, "GET /health 200")
	r := New(nil)
	out := r.Lines(in)
	if len(out) != len(in) {
		t.Fatalf("the number of lines changed: %d to %d", len(in), len(out))
	}
	noKeyMaterial(t, out)
	if out[0] != "GET /health 200" || out[len(out)-1] != "GET /health 200" {
		t.Errorf("the lines around the block changed: %q", out)
	}
	for _, l := range out[1 : len(out)-1] {
		if l != Mark(RulePEM) {
			t.Errorf("a block line is %q, want one pem mark", l)
		}
	}
	if got := r.Counts()[RulePEM]; got != 1 {
		t.Errorf("pem count %d, want 1 (one block)", got)
	}
}

func TestLinesKeepsTheTextBeforeTheHeader(t *testing.T) {
	in := append([]string{"loaded key: " + pemBegin}, keyBody...)
	in = append(in, pemEnd)
	out := New(nil).Lines(in)
	noKeyMaterial(t, out)
	if out[0] != "loaded key: "+Mark(RulePEM) {
		t.Errorf("first line: %q", out[0])
	}
}

func TestLinesMasksABlockWhoseEndIsMissing(t *testing.T) {
	in := append([]string{"start", pemBegin}, keyBody...)
	in = append(in, "GET /health 200", "GET /ready 200")
	r := New(nil)
	out := r.Lines(in)
	noKeyMaterial(t, out)
	if out[len(out)-1] != "GET /ready 200" || out[len(out)-2] != "GET /health 200" {
		t.Errorf("ordinary lines after the base64 run were changed: %q", out)
	}
	if got := r.Counts()[RulePEM]; got != 1 {
		t.Errorf("pem count %d, want 1", got)
	}
}

func TestLinesMissingEndIsBounded(t *testing.T) {
	in := []string{pemBegin}
	for i := 0; i < 500; i++ {
		in = append(in, "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo")
	}
	out := New(nil).Lines(in)
	marked := 0
	for _, l := range out {
		if l == Mark(RulePEM) {
			marked++
		}
	}
	if marked < 100 || marked > 202 {
		t.Errorf("masked %d lines, want a bounded run of at most 200 after the header", marked)
	}
	if out[len(out)-1] == Mark(RulePEM) {
		t.Errorf("the mask ran past the bound")
	}
}

func TestLinesMasksAnEndWithoutABegin(t *testing.T) {
	in := append([]string{"GET /health 200", "QUJDREVGR0hJSktM"}, keyBody...)
	in = append(in, pemEnd, "GET /ready 200")
	r := New(nil)
	out := r.Lines(in)
	noKeyMaterial(t, out)
	if strings.Contains(strings.Join(out, "\n"), "QUJDREVGR0hJSktM") {
		t.Errorf("a base64 line directly before the END line is left: %q", out)
	}
	if out[0] != "GET /health 200" || out[len(out)-1] != "GET /ready 200" {
		t.Errorf("ordinary lines changed: %q", out)
	}
	if got := r.Counts()[RulePEM]; got != 1 {
		t.Errorf("pem count %d, want 1", got)
	}
}

func TestLinesLeavesOrdinaryLinesAlone(t *testing.T) {
	in := []string{
		"GET /health 200 12ms",
		"user logged in",
		"Zq8sT2vB9xK3mP7wL5nR1d==", // looks like base64, no PEM header near it
		"GET /ready 200 3ms",
	}
	r := New(nil)
	out := r.Lines(in)
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("line %d changed: %q to %q", i, in[i], out[i])
		}
	}
	if r.Counts().Total() != 0 {
		t.Errorf("counts: %v", r.Counts())
	}
}

func TestLinesStillRedactsEachLine(t *testing.T) {
	out := New(nil).Lines([]string{`login password=hunter2`, "ok"})
	if strings.Contains(out[0], "hunter2") || out[1] != "ok" {
		t.Errorf("out: %q", out)
	}
}

// The text before a key header on the same line is cluster text too: it goes
// through Text like every other line.
func TestLinesRedactsTheTextBeforeTheHeader(t *testing.T) {
	in := append([]string{"password=Kq7vLm2xZp9wTr4s loaded key: " + pemBegin}, keyBody...)
	in = append(in, pemEnd)
	out := New(nil).Lines(in)
	noKeyMaterial(t, out)
	if strings.Contains(out[0], "Kq7vLm2xZp9wTr4s") {
		t.Errorf("the secret before the header stayed: %q", out[0])
	}
	if !strings.HasSuffix(out[0], Mark(RulePEM)) {
		t.Errorf("first line: %q", out[0])
	}
}

// A flag value that is a mark already stays as it is, also when punctuation
// follows the mark: the text is not changed and nothing is counted again.
func TestTextKeepsAMarkedFlagValueBeforePunctuation(t *testing.T) {
	r := New(nil)
	in := "command: /app --password " + Mark(RuleKeyName) + "; args: --port 8080"
	if out := r.Text(in); out != in {
		t.Errorf("out: %q", out)
	}
	if n := r.Counts()[RuleKeyName]; n != 0 {
		t.Errorf("counted %d redactions for text that had only a mark", n)
	}
}
