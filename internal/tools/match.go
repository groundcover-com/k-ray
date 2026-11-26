package tools

import (
	"sort"
	"strings"

	"github.com/groundcover-com/kray/internal/redact"
)

// The match argument of list, events and logs: case-insensitive words, with a
// comma between words that are alternatives. It is a plain substring test, not
// a pattern. It runs on the server, on redacted text, after the causes are
// computed and before the size budget and the folding of similar rows: a match
// is never lost to a cut or hidden inside "(+3 like it)". The words are not
// echoed in the output: a word that is a secret value must not come back.
const (
	maxMatchBytes = 256
	maxMatchWords = 16
)

type matcher struct{ words []string }

// parseMatch returns nil for an empty argument (no filter).
func parseMatch(s string) (*matcher, error) {
	if strings.TrimSpace(s) == "" {
		if s != "" {
			return nil, badInput("match is empty; give one or more words, separated by commas")
		}
		return nil, nil
	}
	if len(s) > maxMatchBytes {
		return nil, badInput("match is longer than %d characters", maxMatchBytes)
	}
	m := &matcher{}
	for _, w := range strings.Split(s, ",") {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			m.words = append(m.words, w)
		}
	}
	if len(m.words) == 0 {
		return nil, badInput("match is empty; give one or more words, separated by commas")
	}
	if len(m.words) > maxMatchWords {
		return nil, badInput("match has more than %d words", maxMatchWords)
	}
	return m, nil
}

// has reports whether the text holds any word. A nil matcher holds all.
func (m *matcher) has(text string) bool {
	if m == nil {
		return true
	}
	text = strings.ToLower(text)
	for _, w := range m.words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// quietRedactor redacts text for matching only. It is a separate redactor, so
// that its counts do not change the "redactions: N" line of the result.
func (r *Runner) quietRedactor() *redact.Redactor { return redact.New(r.ExtraKeys) }

// closeNames returns up to n of the names that are near one of the words, by
// containment and edit distance, the nearest first.
func closeNames(words, names []string, n int) []string {
	type cand struct {
		s string
		d int
	}
	var cands []cand
	seen := map[string]bool{}
	for _, name := range names {
		have := strings.ToLower(name)
		best := -1
		for _, want := range words {
			d := editDistance(have, want)
			if strings.Contains(have, want) || strings.Contains(want, have) || d <= max(2, len(want)/3) {
				if best < 0 || d < best {
					best = d
				}
			}
		}
		if best >= 0 && !seen[name] {
			seen[name] = true
			cands = append(cands, cand{name, best})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].d != cands[j].d {
			return cands[i].d < cands[j].d
		}
		return cands[i].s < cands[j].s
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.s
	}
	return out
}
