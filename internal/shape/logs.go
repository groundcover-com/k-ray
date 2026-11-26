package shape

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LogEntry is one log record: a head line and its continuation lines (for
// example a stack trace).
type LogEntry struct {
	Time    time.Time
	Text    string // head line, without the timestamp prefix
	More    []string
	Dropped int // continuation lines beyond maxMore, not kept
	Error   bool
	Pod     string // the pod the entry came from, when logs of several pods are merged
	// Frame is the last application frame of a condensed trace, for example
	// "at /app/main.py:42 (run)". Condense sets it.
	Frame string
}

// maxMore is how many continuation lines one entry keeps.
const maxMore = 200

var (
	levelRE    = regexp.MustCompile(`(?i)"?\b(level|severity|lvl|loglevel)"?\s*[:=]\s*"?([a-z]+)`)
	errWordRE  = regexp.MustCompile(`(?i)\b(error|errors|fatal|panic|exception|traceback|critical|segfault|segmentation fault|oomkilled|out of memory)\b`)
	klogRE     = regexp.MustCompile(`^[EF]\d{4} `)
	contRE     = regexp.MustCompile(`^(\s+\S|at |Caused by|\.\.\. \d+ more|goroutine \d+|\[signal)`)
	tsInLineRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
	uuidRE     = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	ipRE       = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	hexRE      = regexp.MustCompile(`\b(0x)?[0-9a-fA-F]{8,}\b`)
	numRE      = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
)

// IsErrorLine decides whether a log line reports an error. A level field,
// when present, decides alone.
func IsErrorLine(s string) bool {
	if m := levelRE.FindStringSubmatch(s); m != nil {
		switch strings.ToLower(m[2]) {
		case "error", "err", "fatal", "panic", "critical", "crit", "emerg", "emergency", "alert", "severe", "dpanic":
			return true
		}
		return false
	}
	return klogRE.MatchString(s) || errWordRE.MatchString(benignErrRE.ReplaceAllString(s, " "))
}

// benignErrRE matches the words of a line that says there was no error: "0
// errors", "no errors", "errors=0", "error_count: 0". They are removed before
// the line is looked at; a line that has another error word stays an error.
var benignErrRE = regexp.MustCompile(`(?i)\b(no|zero|0|without)\s+(errors|exceptions|panics|faults)\b|` +
	`\bno\s+(error|exception|panic)\s+(found|detected|occurred|reported|seen)\b|` +
	`\b(errors|errs|exceptions|panics|(error|err|exception|panic)[_-]?(count|total|num|number)|errorcount)"?\s*[:=]\s*"?0+(\.0+)?\b`)

// Template replaces the variable parts of a line, so repeated lines group.
func Template(s string) string {
	s = tsInLineRE.ReplaceAllString(s, "<ts>")
	s = uuidRE.ReplaceAllString(s, "<uuid>")
	s = ipRE.ReplaceAllString(s, "<ip>")
	s = hexRE.ReplaceAllString(s, "<hex>")
	s = numRE.ReplaceAllString(s, "<n>")
	return s
}

// causeRE matches a line that names a cause inside a multi-line entry: a
// "Caused by:" line, an exception line such as "java.io.IOException: ..." or
// "ValueError: ...", a Python "Traceback" line, a panic line.
var causeRE = regexp.MustCompile(`^(Caused by\b|Suppressed:|Traceback \(|panic:|fatal error:|\[signal |[A-Za-z_$][\w$.]*(Exception|Error|Throwable|Failure)(:|$))`)

var (
	// chainRE is the line between two Python tracebacks of chained exceptions.
	chainRE = regexp.MustCompile(`^(During handling of the above exception|The above exception was the direct cause)`)
	// goFrameRE is the function line of a Go stack frame; its file line is
	// indented and so joins by itself.
	goFrameRE = regexp.MustCompile(`^([A-Za-z_]\S*\(.*\)|created by .*|exit status \d+)$`)
)

// inGoTrace reports whether the entry is a Go panic or fatal error whose
// goroutine stack has started, so that frame lines belong to it.
func inGoTrace(e *LogEntry) bool {
	if !strings.HasPrefix(e.Text, "panic:") && !strings.HasPrefix(e.Text, "fatal error:") {
		return false
	}
	for _, m := range e.More {
		if strings.HasPrefix(m, "goroutine ") {
			return true
		}
	}
	return false
}

func isTraceback(e *LogEntry) bool {
	return strings.HasPrefix(e.Text, "Traceback (most recent call last)")
}

func isCauseLine(s string) bool { return causeRE.MatchString(strings.TrimSpace(s)) }

// joinsEntry reports whether a line that is not indented belongs to the error
// entry before it: an exception line, not a new panic or traceback.
func joinsEntry(s string) bool {
	return isCauseLine(s) && !strings.HasPrefix(s, "panic:") && !strings.HasPrefix(s, "fatal error:") && !strings.HasPrefix(s, "Traceback")
}

// ParseLogs splits raw pod log text (requested with timestamps=true) into
// entries. Continuation lines join the entry before them, and so does an
// exception line that follows an error, so a stack trace is one entry.
func ParseLogs(raw string) []LogEntry {
	var out []LogEntry
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ts time.Time
		text := line
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			if t, err := time.Parse(time.RFC3339Nano, line[:sp]); err == nil {
				ts, text = t, line[sp+1:]
			}
		} else if t, err := time.Parse(time.RFC3339Nano, line); err == nil {
			ts, text = t, "" // an empty log line has only its timestamp
		}
		if strings.TrimSpace(text) == "" {
			continue // an empty line inside a trace
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			if contRE.MatchString(text) || (joinsEntry(text) && (last.Error || len(last.More) > 0)) ||
				(isTraceback(last) && chainRE.MatchString(text)) || (inGoTrace(last) && goFrameRE.MatchString(text)) {
				if len(last.More) < maxMore {
					last.More = append(last.More, strings.TrimRight(text, " "))
				} else {
					last.Dropped++
				}
				continue
			}
		}
		out = append(out, LogEntry{Time: ts, Text: strings.TrimRight(text, " "), Error: IsErrorLine(text)})
	}
	return out
}

// shownMore splits the continuation lines of an entry into the cause lines
// to show and the number of other lines (stack frames) left out.
func shownMore(e LogEntry) (causes []string, left int) {
	for _, m := range e.More {
		if isCauseLine(m) {
			causes = append(causes, m)
		} else {
			left++
		}
	}
	return causes, left + e.Dropped
}

type logGroup struct {
	tpl         string
	err         bool
	count       int
	first, last time.Time
	lastIdx     int
	sample      LogEntry       // the most recent entry of the group
	hit         bool           // a line of the group holds a word of the match
	pods        map[string]int // entries per pod
	podOrder    []string
	d           *mined    // set for a group of the template tree
	an          *analysis // the variables of a group of the tree
}

// LogSummary is the rendered summary and the facts for the cut note.
type LogSummary struct {
	Body                   string
	Entries, ErrorEntries  int
	Groups, ErrorGroups    int
	ShownGroups, Truncated int // groups shown; groups left out
	TruncatedLines         int // lines in the groups left out
	// With a match: the lines and groups before the match kept the ones that hold a word.
	TotalEntries, TotalGroups int
}

// otherGroupsWithErrors is how many of the largest groups without an error
// are shown when there are errors: they only say what normal looks like.
const otherGroupsWithErrors = 2

// SummarizeLogs renders error groups first (most recent first), then the
// other groups, within max bytes.
func SummarizeLogs(entries []LogEntry, max int) LogSummary {
	return SummarizeLogsPods(entries, 0, max)
}

// SummarizeLogsPods does the same for entries that come from several pods
// (LogEntry.Pod): a group says how many of the totalPods pods have it.
//
// An error entry is grouped by the template of its head line and of its cause
// lines, so a stack trace is one group however many frames it has. Other
// entries of a log with at least minMineEntries entries are grouped by
// template mining (mine.go): lines that differ in a word, a number or the
// value of a key are one group, and each variable keeps its values with
// counts (vars.go). Groups with a rare value, and groups that only some pods
// have, come first, then the largest. A shorter log is grouped by the masked
// text of its lines, as before.
func SummarizeLogsPods(entries []LogEntry, totalPods, max int) LogSummary {
	return summarizeLogs(entries, totalPods, max, len(entries) >= minMineEntries)
}

func summarizeLogs(entries []LogEntry, totalPods, max int, mine bool) LogSummary {
	return summarizeLogsMatch(entries, totalPods, max, mine, nil)
}

// SummarizeLogsMatch does the same and keeps only the groups that have a line
// with one of the words (lowercase). The match runs on the text of the entries
// as given, so the caller redacts first. It runs after the grouping and before
// the ranking and the size budget, so a match is not lost to a cut. A group
// that matches keeps its variable lines.
func SummarizeLogsMatch(entries []LogEntry, totalPods, max int, words []string) LogSummary {
	return summarizeLogsMatch(entries, totalPods, max, len(entries) >= minMineEntries, words)
}

// EntryHas reports whether the head line or a continuation line of the entry
// holds one of the words (lowercase).
func EntryHas(e LogEntry, words []string) bool {
	if len(words) == 0 {
		return true
	}
	t := strings.ToLower(e.Text)
	for _, w := range words {
		if strings.Contains(t, w) {
			return true
		}
	}
	for _, m := range e.More {
		t := strings.ToLower(m)
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
	}
	return false
}

func summarizeLogsMatch(entries []LogEntry, totalPods, max int, mine bool, words []string) LogSummary {
	entries = Condense(entries)
	gr := logsGroupAll(entries, mine, words)
	groups := gr.groups
	s := LogSummary{Entries: len(entries), ErrorEntries: gr.errorEntries}
	if len(words) > 0 {
		s.TotalEntries, s.TotalGroups = len(entries), len(groups)
		groups, s.Entries, s.ErrorEntries = logsKeepHits(groups)
	}
	errs, other := logsSplitErrors(groups)
	s.Groups, s.ErrorGroups = len(groups), len(errs)
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].lastIdx > errs[j].lastIdx })
	if mine {
		other = rankGroups(other, entries, gr.tr, totalPods, len(errs) > 0)
	} else {
		other = logsOrderInTime(other, len(errs) > 0)
	}
	body, shown, shownLines := logsRenderGroups(s, errs, other, entries, totalPods, max, mine)
	s.ShownGroups = shown
	s.Truncated = len(groups) - shown
	s.TruncatedLines = s.Entries - shownLines
	s.Body = body
	return s
}

// rankGroups orders the groups without an error. A group comes first when
// it has a rare value or when only some pods have it. The rest follow by
// size. When there are errors, only the largest of the rest are kept: they
// only say what normal looks like.
func rankGroups(other []*logGroup, entries []LogEntry, tr *tree, totalPods int, haveErrors bool) []*logGroup {
	var signal, rest []*logGroup
	score := map[*logGroup]int{}
	for _, g := range other {
		g.analyze(entries, tr)
		sc := 0
		if g.an != nil && g.an.hasRare {
			sc++
		}
		if totalPods > 1 && len(g.podOrder) > 0 && len(g.podOrder) < totalPods {
			sc++
		}
		score[g] = sc
		if sc > 0 {
			signal = append(signal, g)
		} else {
			rest = append(rest, g)
		}
	}
	bySize := func(l []*logGroup, key func(*logGroup) int) {
		sort.SliceStable(l, func(i, j int) bool {
			if k1, k2 := key(l[i]), key(l[j]); k1 != k2 {
				return k1 > k2
			}
			if l[i].count != l[j].count {
				return l[i].count > l[j].count
			}
			return l[i].lastIdx > l[j].lastIdx // equal size: the more recent
		})
	}
	bySize(signal, func(g *logGroup) int { return score[g] })
	bySize(rest, func(*logGroup) int { return 0 })
	if haveErrors && len(rest) > otherGroupsWithErrors {
		rest = rest[:otherGroupsWithErrors]
	}
	return append(signal, rest...)
}

// Count formats "1 line" or "2,000 lines".
func Count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return Thousands(n) + " " + noun + "s"
}

const podsNamed = 6

// templateCap is the least length shown of a template: it says what the lines
// of the group have in common, so it is cut later than a sample line.
const templateCap = 240

func renderGroup(g *logGroup, totalPods, headCap int) string {
	return renderHead(g, totalPods, headCap, g.sample.Text)
}

// renderMined renders a group of the tree: its template when it has
// variables, then what they show.
func renderMined(g *logGroup, entries []LogEntry, totalPods, headCap int) string {
	if g.an == nil {
		return renderGroup(g, totalPods, headCap)
	}
	return renderHead(g, totalPods, max(headCap, templateCap), g.templateText()) + g.varLines(entries)
}

func renderHead(g *logGroup, totalPods, headCap int, head string) string {
	var b strings.Builder
	when := clock(g.last)
	if g.count > 1 && !g.first.IsZero() {
		when = clock(g.first) + "→" + clock(g.last)
	}
	who := ""
	if totalPods > 1 && len(g.podOrder) > 0 {
		names := append([]string(nil), g.podOrder...)
		sort.Slice(names, func(i, j int) bool { return NaturalLess(names[i], names[j]) })
		more := ""
		if len(names) > podsNamed {
			more = fmt.Sprintf(", +%d more", len(names)-podsNamed)
			names = names[:podsNamed]
		}
		who = fmt.Sprintf("  [%d of %d pods: %s%s]", len(g.podOrder), totalPods, strings.Join(names, ", "), more)
		if len(g.podOrder) == totalPods {
			who = fmt.Sprintf("  [all %d pods]", totalPods)
		}
	}
	fmt.Fprintf(&b, "  ×%-4d %s%s  %s\n", g.count, when, who, oneLineCap(head, headCap))
	if g.sample.Frame != "" {
		fmt.Fprintf(&b, "         %s\n", oneLineCap(g.sample.Frame, 200))
	}
	causes, left := shownMore(g.sample)
	for _, m := range causes {
		fmt.Fprintf(&b, "         %s\n", oneLineCap(strings.TrimSpace(m), 200))
	}
	if left > 0 {
		fmt.Fprintf(&b, "         (%s left out)\n", Count(left, "frame line"))
	}
	return b.String()
}

// NaturalLess orders names so that "checkout-3" comes before "checkout-10".
func NaturalLess(a, b string) bool {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	// Back up to the start of a digit run.
	j := i
	for j > 0 && a[j-1] >= '0' && a[j-1] <= '9' {
		j--
	}
	da, db := digitRun(a[j:]), digitRun(b[j:])
	if da != "" && db != "" && len(da) != len(db) {
		return len(da) < len(db)
	}
	return a < b
}

func digitRun(s string) string {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n]
}

// RenderRaw renders entries as plain lines, keeping the most recent ones
// that fit in max bytes. It returns the text and how many entries it left out.
func RenderRaw(entries []LogEntry, max int) (string, int) {
	var lines []string
	used := 0
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		var b strings.Builder
		if !e.Time.IsZero() {
			b.WriteString(e.Time.UTC().Format("2006-01-02T15:04:05Z") + " ")
		}
		b.WriteString(e.Text)
		for _, m := range e.More {
			b.WriteString("\n" + m)
		}
		l := b.String()
		if used+len(l)+1 > max {
			return reverseJoin(lines), i + 1
		}
		lines = append(lines, l)
		used += len(l) + 1
	}
	return reverseJoin(lines), 0
}

func reverseJoin(lines []string) string {
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.UTC().Format("15:04:05")
}

func oneLineCap(s string, n int) string {
	return CutWords(strings.ReplaceAll(s, "\r", ""), n)
}

var (
	jsonMessageKeys = []string{"message", "msg", "body", "log", "event", "text"}
	jsonLevelKeys   = []string{"level", "severity", "lvl", "loglevel", "severity_text", "severityText"}
	jsonTimeKeys    = map[string]bool{"time": true, "timestamp": true, "ts": true, "@timestamp": true, "t": true}
)

// ReadableJSON rewrites entries that are JSON log records as
// "LEVEL message key=value ...", so the message is at the start of the line
// and not after the record's metadata. Nested objects are left out, except
// under an error key. Entries that are not JSON records with a message stay
// as they are. Call it before redaction: the result is redacted like any
// other line.
func ReadableJSON(entries []LogEntry) {
	for i := range entries {
		if text, isErr, ok := readableJSON(entries[i].Text); ok {
			entries[i].Text, entries[i].Error = text, isErr
		}
	}
}

// plainKey writes a JSON key as one word: a run of characters other than
// letters, digits, "_", "." and "-" becomes "_". The redactor reads a key=value
// pair by its last word, so a key with a space ("api key") would hide that it
// is a secret name.
func plainKey(k string) string {
	var b strings.Builder
	gap := false
	for _, r := range k {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			b.WriteRune(r)
			gap = false
		} else if !gap {
			b.WriteByte('_')
			gap = true
		}
	}
	return b.String()
}

func readableJSON(text string) (string, bool, bool) {
	t := strings.TrimSpace(text)
	if len(t) < 2 || t[0] != '{' || t[len(t)-1] != '}' {
		return "", false, false
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || dec.More() {
		return "", false, false
	}
	pick := func(keys []string) (string, string) {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return k, v
			}
		}
		return "", ""
	}
	msgKey, msg := pick(jsonMessageKeys)
	if msgKey == "" {
		return "", false, false
	}
	levelKey, level := pick(jsonLevelKeys)

	keys := make([]string, 0, len(m))
	for k := range m {
		if k != msgKey && k != levelKey && !jsonTimeKeys[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	if level != "" {
		b.WriteString(strings.ToUpper(level) + " ")
	}
	b.WriteString(strings.Join(strings.Fields(msg), " "))
	const maxExtras = 400
	extras := 0
	for _, k := range keys {
		var val string
		switch v := m[k].(type) {
		case string:
			val = v
			if val == "" || strings.ContainsAny(val, " \t\n\"=") {
				val = fmt.Sprintf("%q", v)
			}
		case json.Number:
			val = v.String()
		case bool:
			val = fmt.Sprint(v)
		case map[string]any, []any:
			lk := strings.ToLower(k)
			if lk != "error" && lk != "err" && lk != "exception" {
				continue // metadata such as resource attributes
			}
			j, err := json.Marshal(v)
			if err != nil {
				continue
			}
			val = string(j)
		default:
			continue
		}
		part := " " + plainKey(k) + "=" + val
		if extras+len(part) > maxExtras {
			b.WriteString(" ...")
			break
		}
		extras += len(part)
		b.WriteString(part)
	}
	out := b.String()
	isErr := IsErrorLine(out)
	if level != "" {
		switch strings.ToLower(level) {
		case "error", "err", "fatal", "panic", "critical", "crit", "emerg", "emergency", "alert", "severe", "dpanic":
			isErr = true
		default:
			isErr = false
		}
	}
	return out, isErr, true
}

var (
	pyFileRE   = regexp.MustCompile(`^\s*File "([^"]+)", line (\d+), in (\S+)`)
	goFileRE   = regexp.MustCompile(`^\s+(/\S+?\.go):(\d+)`)
	traceSufRE = regexp.MustCompile(` \(traceback, \d+ lines\)$`)
)

// libraryPath reports whether a frame's file belongs to a dependency or to
// the language, not to the application.
func libraryPath(p string) bool {
	for _, part := range []string{"site-packages", "dist-packages", "/lib/python", "<frozen", "<string>", "/usr/local/go/", "/usr/lib/go", "/go/src/", "/pkg/mod/", "/vendor/", "/runtime/"} {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}

// Condense gives the entries that are Python tracebacks or Go panics the
// line a person calls the error. A Python traceback becomes its exception
// line, the last line of the traceback, with a suffix that counts its lines;
// the frame lines are dropped and the last application frame is kept. A Go
// panic keeps its lines and gets the first application frame. It returns a
// copy and leaves its argument unchanged, so raw output is not affected.
func Condense(entries []LogEntry) []LogEntry {
	out := make([]LogEntry, len(entries))
	copy(out, entries)
	for i := range out {
		e := &out[i]
		switch {
		case isTraceback(e):
			exc := ""
			for j := len(e.More) - 1; j >= 0; j-- {
				m := e.More[j]
				if strings.TrimSpace(m) != "" && m[0] != ' ' && m[0] != '\t' && !chainRE.MatchString(m) {
					exc = strings.TrimSpace(m)
					break
				}
			}
			if exc == "" {
				continue // a trace that was cut: keep what there is
			}
			frame := ""
			for _, m := range e.More {
				if f := pyFileRE.FindStringSubmatch(m); f != nil && !libraryPath(f[1]) {
					frame = fmt.Sprintf("at %s:%s (%s)", f[1], f[2], f[3])
				}
			}
			lines := 1 + len(e.More) + e.Dropped
			e.Text = fmt.Sprintf("%s (traceback, %d lines)", exc, lines)
			e.More, e.Dropped, e.Frame = nil, 0, frame
		case inGoTrace(e):
			for _, m := range e.More {
				if f := goFileRE.FindStringSubmatch(m); f != nil && !libraryPath(f[1]) {
					e.Frame = fmt.Sprintf("at %s:%s", f[1], f[2])
					break
				}
			}
		}
	}
	return out
}

// ErrorLine is the one line that says what an error entry is about, for a
// cause that quotes it: the head line, the exception line and the last
// "Caused by:" line of a Java trace; the exception line of a Python
// traceback; the panic line, the signal line and the first application frame
// of a Go panic. It has no frame lines of Java.
func ErrorLine(e LogEntry) string {
	c := Condense([]LogEntry{e})[0]
	s := traceSufRE.ReplaceAllString(c.Text, "")
	causes, _ := shownMore(c)
	var parts []string
	if n := len(causes); n > 0 {
		first, last := strings.TrimSpace(causes[0]), strings.TrimSpace(causes[n-1])
		if !strings.Contains(s, first) {
			parts = append(parts, first)
		}
		if last != first && !strings.Contains(s, last) {
			parts = append(parts, last)
		}
	}
	if len(parts) > 0 {
		s += " | " + strings.Join(parts, " | ")
	}
	if c.Frame != "" {
		s += " " + c.Frame
	}
	return s
}
