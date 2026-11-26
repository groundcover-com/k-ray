package redact

import (
	"regexp"
	"strings"
)

// A log prints a PEM key over many lines. Line by line, the header line and
// maybe the body are masked, but a short last base64 line and the END line
// stay in clear text. Lines redacts a sequence of lines with that block in
// view.

var (
	pemBeginRE = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]{1,64}-----`)
	pemEndRE   = regexp.MustCompile(`-----END [A-Z0-9 ]{1,64}-----`)
	base64Line = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

// maxBlockLines bounds how far a key block is followed: the lines searched for
// its END line, and the base64 lines masked when there is no END line.
const maxBlockLines = 200

// Lines redacts a sequence of lines, for example the lines of a log, and
// returns a new slice of the same length. A key block is masked whole:
//
//   - from a line with a PEM BEGIN header to its END line, every line becomes
//     one pem mark (the text before the header stays);
//   - with no END line within maxBlockLines, the lines after the header that
//     hold only base64 characters (up to maxBlockLines) are masked;
//   - an END line with no BEGIN in view is masked with the base64 lines
//     directly before it (the log started inside the block).
//
// A block counts as one redaction. Then every line goes through Text.
func (r *Redactor) Lines(lines []string) []string {
	out := append([]string(nil), lines...)
	done := make([]bool, len(out)) // the line is a mark already
	mask := func(i int, keep string) {
		out[i] = keep + Mark(RulePEM)
		done[i] = true
	}
	for i := 0; i < len(out); i++ {
		if done[i] {
			continue
		}
		if loc := pemBeginRE.FindStringIndex(out[i]); loc != nil {
			if pemEndRE.MatchString(out[i][loc[1]:]) {
				continue // the whole key is on this line; Text masks it
			}
			r.mark(RulePEM) // one count for the block
			mask(i, r.Text(out[i][:loc[0]]))
			end := -1
			for j := i + 1; j < len(out) && j <= i+maxBlockLines; j++ {
				if pemEndRE.MatchString(out[j]) {
					end = j
					break
				}
			}
			if end >= 0 {
				for j := i + 1; j <= end; j++ {
					mask(j, "")
				}
				i = end
				continue
			}
			j := i + 1
			for ; j < len(out) && j <= i+maxBlockLines && base64Only(out[j]); j++ {
				mask(j, "")
			}
			i = j - 1
			continue
		}
		if pemEndRE.MatchString(out[i]) {
			r.mark(RulePEM)
			mask(i, "")
			for j := i - 1; j >= 0 && j >= i-maxBlockLines && !done[j] && base64Only(out[j]); j-- {
				mask(j, "")
			}
		}
	}
	for i := range out {
		if !done[i] {
			out[i] = r.Text(out[i])
		}
	}
	return out
}

func base64Only(s string) bool { return base64Line.MatchString(strings.TrimSpace(s)) }

// CountMarks counts the marks in s as redactions of this Redactor. Use it
// when s was redacted with another Redactor and is now shown.
func (r *Redactor) CountMarks(s string) {
	for {
		i := strings.Index(s, markPrefix)
		if i < 0 {
			return
		}
		s = s[i+len(markPrefix):]
		j := strings.IndexByte(s, ']')
		if j < 0 {
			return
		}
		r.mark(s[:j])
		s = s[j+1:]
	}
}
