package shape

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// A string value longer than this is cut, or replaced by a marker.
	longValue = 300
	// A cut value keeps about this many bytes of its start.
	headBytes = 200
	// binaryDataMin is the size from which a ConfigMap binaryData value is
	// replaced by a marker. Every such value is base64.
	binaryDataMin = 64
	// outlineMax is the most keys that the outline of a ConfigMap lists.
	outlineMax = 30
)

// redactedPrefix starts the marker of the redactor. A marker is never cut.
const redactedPrefix = "[REDACTED:"

// CutLong shortens the long string values of an object in place: a long text
// is cut to its first lines, and base64 or binary data is replaced by a
// marker with its length. The status is left whole (a termination message or
// a condition message is evidence).
//
// CutLong must run after redaction: redaction sees each value whole, so a
// secret is found whatever the cut does, and a cut never leaves half of a
// secret. It also gives a ConfigMap with large data an outline of its keys,
// from the redacted values, before it cuts them.
func CutLong(obj map[string]any, rep *Report) {
	if obj["kind"] == "ConfigMap" {
		rep.Outline = configMapOutline(obj)
	}
	for k, v := range obj {
		if k == "status" {
			continue
		}
		obj[k] = cutValue(v, k == "binaryData", rep)
	}
}

func cutValue(v any, binary bool, rep *Report) any {
	switch x := v.(type) {
	case string:
		return cutString(x, binary, rep)
	case map[string]any:
		for k, c := range x {
			x[k] = cutValue(c, binary, rep)
		}
		return x
	case []any:
		for i, c := range x {
			x[i] = cutValue(c, binary, rep)
		}
		return x
	}
	return v
}

func cutString(s string, binary bool, rep *Report) string {
	if strings.HasPrefix(s, redactedPrefix) && strings.HasSuffix(s, "]") {
		return s
	}
	if isBinary(s) {
		rep.CutValues++
		return fmt.Sprintf("[binary data, %s bytes]", Thousands(len(s)))
	}
	long := len(s) > longValue
	if !long && !(binary && len(s) >= binaryDataMin) {
		return s
	}
	if (binary || long) && isBase64(s) {
		rep.CutValues++
		return fmt.Sprintf("[base64, %s chars]", Thousands(len(s)))
	}
	if !long {
		return s
	}
	rep.CutValues++
	head := headOf(s)
	left := len(s) - len(head)
	lines := strings.Count(s, "\n") - strings.Count(head, "\n")
	marker := fmt.Sprintf("… [%s bytes left out; full shows all]", Thousands(left))
	if lines > 0 {
		marker = fmt.Sprintf("… [%s bytes, %s left out; full shows all]", Thousands(left), plural(lines, "line"))
	}
	if strings.Contains(head, "\n") && len(s) > len(head) && s[len(head)] == '\n' {
		return head + "\n" + marker
	}
	return head + " " + marker
}

// headOf returns the start of a long text: whole lines when it has lines,
// else whole words. It never splits a character.
func headOf(s string) string {
	p := headBytes
	for p > 0 && !utf8.RuneStart(s[p]) {
		p--
	}
	if cut := strings.LastIndex(s[:p], "\n"); cut > 0 {
		return s[:cut]
	}
	if cut := strings.LastIndexAny(s[:p], " \t"); cut > p/2 {
		return strings.TrimRight(s[:cut], " \t")
	}
	return s[:p]
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return Thousands(n) + " " + word + "s"
}

// isBinary reports whether a string is not text: it is not valid UTF-8, or
// it holds control characters other than tab and line ends.
func isBinary(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	if len(s) <= longValue {
		return false // a short text may hold a control character, for example a color code
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return true
		}
	}
	return false
}

// isBase64 reports whether a string is only base64 characters (it may be
// wrapped in lines) and so holds no text a reader could use.
func isBase64(s string) bool {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '+', c == '/', c == '=', c == '-', c == '_':
			n++
		case c == '\n' || c == '\r':
		default:
			return false
		}
	}
	return n >= binaryDataMin
}

// configMapOutline lists each key of a ConfigMap with its size and number of
// lines, when at least one value is long. It lists nothing otherwise: the
// data is short enough to read.
func configMapOutline(obj map[string]any) []string {
	type entry struct{ key, text string }
	var entries []entry
	anyLong := false
	for _, section := range []string{"data", "binaryData"} {
		m, _ := obj[section].(map[string]any)
		for k, v := range m {
			s, _ := v.(string)
			switch {
			case section == "binaryData" || isBinary(s) || len(s) > longValue && isBase64(s):
				entries = append(entries, entry{k, fmt.Sprintf("%s: base64 or binary, %s bytes", k, Thousands(len(s)))})
				if len(s) >= binaryDataMin {
					anyLong = true
				}
			case len(s) > longValue:
				anyLong = true
				entries = append(entries, entry{k, fmt.Sprintf("%s: %s bytes, %s, cut", k, Thousands(len(s)), plural(strings.Count(s, "\n")+1, "line"))})
			default:
				entries = append(entries, entry{k, fmt.Sprintf("%s: %s bytes, %s", k, Thousands(len(s)), plural(strings.Count(s, "\n")+1, "line"))})
			}
		}
	}
	if !anyLong {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	var out []string
	for i, e := range entries {
		if i == outlineMax {
			out = append(out, fmt.Sprintf("and %d more keys", len(entries)-outlineMax))
			break
		}
		out = append(out, e.text)
	}
	return out
}
