package fixture

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// The script of the kind workload must print what the fake API server
// serves as the log, line by line.
func TestScriptPrintsTheLogOfTheFakeAPI(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if out, err := exec.Command("sh", "-c", "echo aGk= | base64 -d").Output(); err != nil || string(out) != "hi" {
		t.Skip("base64 -d is not available")
	}
	s := New("fx")
	script := s.Script()
	// Run all but the endless heartbeat loop.
	head := script[:strings.Index(script, "while true")]
	out, err := exec.Command("sh", "-c", head).Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := s.logLines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the script prints\n%s\nbut the fake log has\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The fake log has the same lines, with a timestamp each.
	var fake []string
	for _, l := range strings.Split(strings.TrimRight(s.LogText(), "\n"), "\n") {
		_, text, _ := strings.Cut(l, " ")
		fake = append(fake, text)
	}
	if strings.Join(fake[:len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("LogText differs from the lines of the script:\n%s", s.LogText())
	}
}

// A canary of the log must not be in the pod spec in clear text, or the
// workload's command would be a leak of its own.
func TestLogCanariesAreNotInTheSpecInClearText(t *testing.T) {
	s := New("fx")
	var all strings.Builder
	for _, o := range s.Objects() {
		b, _ := json.Marshal(o.Object)
		all.Write(b)
	}
	b, _ := json.Marshal(s.Pod("p", "n").Object)
	all.Write(b)
	spec := all.String()
	secrets := []string{s.Canaries[LogPassword], s.Canaries[LogBearer], s.Canaries[LogSpacedKey], s.Canaries[LogBareSpacedKey]}
	secrets = append(secrets, s.Parts[LogKeyBlock]...)
	for _, c := range secrets {
		if strings.Contains(spec, c) {
			t.Errorf("log canary %q is in the spec in clear text", c)
		}
	}
	for _, c := range s.Parts[ArgsPhrase] {
		if !strings.Contains(spec, c) {
			t.Errorf("args canary %q is not in the spec", c)
		}
	}
}

// The quoted phrases must straddle the cut: the first word whole, the cut
// inside the second.
func TestQuotedPhrasesStraddleTheCut(t *testing.T) {
	s := New("fx")
	for _, c := range []struct {
		place string
		cut   int
	}{{EventQuotedPhrase, eventMessageCut}, {ConditionQuotedPhrase, conditionMessageCut}} {
		w := s.Parts[c.place]
		m := quotedPhrase(w, c.cut)
		kept := m[:c.cut]
		if !strings.Contains(kept, w[0]) || strings.Contains(kept, w[1]) || !strings.Contains(kept, w[1][:6]) {
			t.Errorf("%s: the cut at %d leaves %q", c.place, c.cut, kept[len(kept)-40:])
		}
		if !strings.Contains(m, `{"password":"`+strings.Join(w, " ")+`","k":"v"}`) || strings.Contains(m, "  ") {
			t.Errorf("%s: malformed message %q", c.place, m)
		}
	}
}
