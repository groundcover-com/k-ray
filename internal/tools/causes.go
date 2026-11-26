package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// A list row carries its own cause, on the same line as the object name, so
// that a text filter that keeps the row keeps the cause. These are the sizes.
const (
	rowCauseMax  = 160 // bytes of a cause from the pod, its events and its usage
	deepCauseMax = 240 // bytes of a cause that also read logs: it quotes the last error
)

// rowTier orders pod rows: unhealthy, warning, pods that only restarted,
// then healthy.
func rowTier(row shape.Row) int {
	switch {
	case row.State == shape.Unhealthy:
		return 0
	case row.State == shape.Warning && !restartOnly(row):
		return 1
	case row.State == shape.Warning:
		return 2
	}
	return 3
}

func redactAll(red *redact.Redactor, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = red.Text(s)
	}
	return out
}

var lastErrorKeys = []string{"Last error before the exit: ", "Last error in its log: "}

// splitSentences cuts a cause at ". " followed by a capital letter. The quote
// of the last error (from "Last error before the exit: " or "Last error in its
// log: " to the end) is one piece: a full stop inside a log line does not start
// a sentence.
func splitSentences(s string) []string {
	var out []string
	start := 0
	end := len(s) // the text that may be cut: before the quote
	for _, key := range lastErrorKeys {
		if i := strings.Index(s, key); i >= 0 && (i == 0 || strings.HasSuffix(s[:i], ". ")) && i < end {
			end = i
		}
	}
	for i := 0; i+2 < len(s) && i+2 <= end; i++ {
		if s[i] == '.' && s[i+1] == ' ' && s[i+2] >= 'A' && s[i+2] <= 'Z' {
			out = append(out, s[start:i])
			start = i + 2
		}
	}
	return append(out, strings.TrimSuffix(s[start:], "."))
}

// sentenceStarters are the words that lowerFirst lowers. A name such as
// "ConfigMaps" keeps its capital.
var sentenceStarters = map[string]bool{
	"The": true, "It": true, "Its": true, "This": true, "Memory": true, "Probe": true, "Pods": true,
	"A": true, "An": true, "No": true, "Every": true, "By": true, "Cannot": true, "Nodes": true, "Node": true,
}

// lowerFirst makes "The limit is..." read as a clause after a semicolon.
func lowerFirst(s string) string {
	word, _, _ := strings.Cut(s, " ")
	if sentenceStarters[word] {
		return strings.ToLower(s[:1]) + s[1:]
	}
	return s
}

// middleCut keeps the start and the end of a long line: the end of a log
// line is often the error itself, and the middle is often ids. It cuts at
// words, so a token is kept whole or dropped whole.
func middleCut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 24 {
		return shape.CutWords(s, n)
	}
	headN, tailN := (n-5)*2/5, (n-5)*3/5
	for headN > 0 && !utf8.RuneStart(s[headN]) {
		headN-- // cut between runes
	}
	for len(s)-tailN < len(s) && !utf8.RuneStart(s[len(s)-tailN]) {
		tailN-- // the tail starts on a rune
	}
	head := s[:headN]
	if i := strings.LastIndexByte(head, ' '); i > 0 {
		head = head[:i]
	}
	tail := s[len(s)-tailN:]
	if i := strings.IndexByte(tail, ' '); i >= 0 {
		tail = tail[i+1:]
	}
	return strings.TrimSpace(head) + " ... " + strings.TrimSpace(tail)
}

// compactCause shortens a cause to one line of at most max bytes. The first
// sentence says what is wrong; the quote of the last error is kept, with the
// middle of a long quote left out; sentences that explain more come after,
// only if they fit.
func compactCause(full string, limit int) string {
	s := strings.Join(strings.Fields(full), " ")
	parts := splitSentences(s)
	head, tail := parts[0], ""
	var rest []string
	for _, x := range parts[1:] {
		found := false
		for _, key := range lastErrorKeys {
			if after, ok := strings.CutPrefix(x, key); ok {
				tail, found = "last error: "+after, true
			}
		}
		if !found && x != "" {
			rest = append(rest, lowerFirst(x))
		}
	}
	whole := strings.Join(append(append([]string{head}, rest...), tail), "; ")
	if tail == "" {
		whole = strings.Join(append([]string{head}, rest...), "; ")
	}
	if len(whole) <= limit {
		return whole
	}
	headMax := limit
	if tail != "" {
		headMax = limit * 55 / 100
	}
	if len(head) > headMax {
		head = shape.CutWords(head, headMax)
	}
	out := head
	reserve := 0
	if tail != "" {
		reserve = min(len(tail)+2, limit/2)
	}
	for _, x := range rest {
		if len(out)+2+len(x)+reserve <= limit {
			out += "; " + x
		}
	}
	if tail != "" {
		room := limit - len(out) - 2
		out += "; " + middleCut(tail, max(room, 30))
	}
	return out
}

// rowCause is the text of the CAUSE column: the causes of one object on one
// line. With two or more, each gets a share and the rest are counted.
func rowCause(prefix string, causes []string, limit int) string {
	limit -= len(prefix)
	if len(causes) == 1 {
		return prefix + compactCause(causes[0], limit)
	}
	shown := causes
	if len(shown) > 2 {
		shown = shown[:2]
	}
	var parts []string
	for _, c := range shown {
		parts = append(parts, compactCause(c, limit/len(shown)-2))
	}
	out := prefix + strings.Join(parts, " | ")
	if more := len(causes) - len(shown); more > 0 {
		out += fmt.Sprintf(" (+%d more)", more)
	}
	return out
}

// eventIndex holds the events of one list's scope, read once, by the pod
// that they are about.
type eventIndex struct {
	byUID    map[string][]unstructured.Unstructured
	byName   map[string][]unstructured.Unstructured // namespace/name, for an event without a UID
	complete bool                                   // false when the cluster has more events than were read
}

// podEventIndex reads the events of the namespace (or of all namespaces) in
// one request and indexes those about pods. It returns nil when they cannot
// be read: causes are then found without events.
func (r *Runner) podEventIndex(ctx context.Context, ns string) *eventIndex {
	page, err := r.Reader.Events(ctx, ns, nil, fetchCap)
	if err != nil {
		return nil
	}
	x := &eventIndex{byUID: map[string][]unstructured.Unstructured{}, byName: map[string][]unstructured.Unstructured{}, complete: !page.More}
	for i := range page.Items {
		u := &page.Items[i]
		if kind, _, _ := unstructured.NestedString(u.Object, "involvedObject", "kind"); kind != "Pod" {
			continue
		}
		name, _, _ := unstructured.NestedString(u.Object, "involvedObject", "name")
		if uid, _, _ := unstructured.NestedString(u.Object, "involvedObject", "uid"); uid != "" {
			x.byUID[uid] = append(x.byUID[uid], *u)
		} else {
			key := u.GetNamespace() + "/" + name
			x.byName[key] = append(x.byName[key], *u)
		}
	}
	return x
}

// groups returns the grouped events about one pod, warnings first. It is
// empty, not nil, when there are none, so that nobody reads them again.
func (x *eventIndex) groups(r *Runner, pod *unstructured.Unstructured) []eventGroup {
	if x == nil {
		return nil
	}
	items := x.byUID[string(pod.GetUID())]
	if len(items) == 0 && pod.GetUID() == "" {
		items = x.byName[pod.GetNamespace()+"/"+pod.GetName()]
	}
	if len(items) == 0 {
		return []eventGroup{}
	}
	g := groupEvents(items, r.now(), false, 0)
	sort.SliceStable(g, func(i, j int) bool { return g[i].less(g[j]) })
	return g
}

// lightPodCauses is the diagnosis of a pod that a list row can afford: it
// uses only the pod, its events and its usage. It reads no logs, no other
// pods, no nodes and no ConfigMaps. A pod that restarted and has no event
// that says why still gets its exit code.
func (r *Runner) lightPodCauses(ctx context.Context, p *corev1.Pod, use map[string]containerUse, groups []eventGroup) []string {
	causes, _ := r.podCauses(ctx, p, use, groups, true)
	if len(causes) > 0 {
		return causes
	}
	for _, cs := range p.Status.ContainerStatuses {
		last := cs.LastTerminationState.Terminated
		if last == nil || cs.RestartCount < 1 {
			continue
		}
		s := fmt.Sprintf("container %s restarted %s; its last exit code was %d", cs.Name, plural(int(cs.RestartCount), "time", "times"), last.ExitCode)
		if m := exitMeaning[last.ExitCode]; m != "" {
			s += " (" + m + ")"
		} else if last.ExitCode == 0 {
			s += " (completed)"
		}
		if !last.FinishedAt.IsZero() {
			s += ", " + shape.Age(last.FinishedAt.Time, r.now()) + " ago"
		}
		causes = append(causes, s+". No event is left that says why.")
		break
	}
	return causes
}
