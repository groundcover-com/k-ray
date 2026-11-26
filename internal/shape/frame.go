package shape

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"text/tabwriter"
)

// fencePrefix starts both fence lines. Every occurrence in cluster content
// is escaped, so content cannot open or close a fence.
const fencePrefix = "<<<"

// Nonce returns 8 random bytes as hex. Each result gets a new nonce.
func Nonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b[:])
}

// Frame is one tool result: a header written by the server, cluster content
// inside a fence, and notes and next steps written by the server.
type Frame struct {
	Header []string // first line is the title
	Body   string   // cluster content; empty means no fence
	Notes  []string
	Next   []string
}

// Render writes the frame with the given nonce.
func (f Frame) Render(nonce string) string {
	var b strings.Builder
	for _, h := range f.Header {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	if f.Body != "" {
		fmt.Fprintf(&b, "%suntrusted cluster data %s>>>\n", fencePrefix, nonce)
		b.WriteString(EscapeFence(f.Body))
		if !strings.HasSuffix(f.Body, "\n") {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%send %s>>>\n", fencePrefix, nonce)
	}
	for _, n := range f.Notes {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	if len(f.Next) > 0 {
		b.WriteString("next: ")
		b.WriteString(strings.Join(f.Next, " · "))
		b.WriteByte('\n')
	}
	return b.String()
}

// EscapeFence puts a backslash before every fence prefix in cluster
// content, wherever it appears, so content can never write a fence line.
func EscapeFence(s string) string {
	return strings.ReplaceAll(s, fencePrefix, `\`+fencePrefix)
}

// Table renders rows with aligned columns.
func Table(header []string, rows [][]string) string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// FitLines keeps whole lines from the front until max bytes. It returns the
// kept lines and how many were dropped.
func FitLines(lines []string, max int) ([]string, int) {
	used := 0
	for i, l := range lines {
		used += len(l) + 1
		if used > max {
			return lines[:i], len(lines) - i
		}
	}
	return lines, 0
}

// Cut shortens s to at most max bytes at a line boundary and says so.
func Cut(s string, max int) (string, bool) {
	if max < 0 {
		max = 0
	}
	if len(s) <= max {
		return s, false
	}
	cut := strings.LastIndex(s[:max], "\n")
	if cut < 0 {
		cut = max
	}
	return s[:cut], true
}

// CutWords shortens s to at most n bytes without splitting a word: it cuts
// at the last space before n, so a token (which may be a secret) is either
// kept whole, for the redactor to see, or dropped whole.
func CutWords(s string, n int) string {
	if n < 0 {
		n = 0 // a negative limit must not slice before the start
	}
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndexAny(s[:n+1], " \t")
	if cut <= 0 {
		return "(long value left out)"
	}
	return strings.TrimRight(s[:cut], " \t") + " ..."
}

// Thousands formats n with commas: 2,000.
func Thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}
