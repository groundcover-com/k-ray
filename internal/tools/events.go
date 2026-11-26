package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/shape"
)

type eventGroup struct {
	typ, reason, object, namespace string
	message                        string
	fieldPath                      string // of a probe failure: spec.containers{NAME}; "" otherwise
	count                          int
	last                           time.Time
}

func (g eventGroup) less(o eventGroup) bool {
	if (g.typ == "Warning") != (o.typ == "Warning") {
		return g.typ == "Warning"
	}
	return g.last.After(o.last)
}

func eventTime(u *unstructured.Unstructured) time.Time {
	for _, path := range [][]string{{"series", "lastObservedTime"}, {"lastTimestamp"}, {"eventTime"}, {"firstTimestamp"}} {
		if s, ok, _ := unstructured.NestedString(u.Object, path...); ok && s != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if t, err := time.Parse(layout, s); err == nil {
					return t
				}
			}
		}
	}
	return u.GetCreationTimestamp().Time
}

// groupEvents groups by type, reason, object and message template.
func groupEvents(items []unstructured.Unstructured, now time.Time, warningsOnly bool, since time.Duration) []eventGroup {
	idx := map[string]*eventGroup{}
	var out []*eventGroup
	for i := range items {
		u := &items[i]
		typ, _, _ := unstructured.NestedString(u.Object, "type")
		if warningsOnly && typ != "Warning" {
			continue
		}
		t := eventTime(u)
		if since > 0 && now.Sub(t) > since {
			continue
		}
		reason, _, _ := unstructured.NestedString(u.Object, "reason")
		msg, _, _ := unstructured.NestedString(u.Object, "message")
		kind, _, _ := unstructured.NestedString(u.Object, "involvedObject", "kind")
		name, _, _ := unstructured.NestedString(u.Object, "involvedObject", "name")
		obj := strings.ToLower(kind) + "/" + name
		count := 1
		if c := nestedCount(u.Object, "series", "count"); c > 0 {
			count = c
		} else if c := nestedCount(u.Object, "count"); c > 0 {
			count = c
		}
		// A probe failure belongs to one container: two containers with the
		// same failure are two groups.
		fieldPath := ""
		if reason == "Unhealthy" {
			fieldPath, _, _ = unstructured.NestedString(u.Object, "involvedObject", "fieldPath")
		}
		key := typ + "|" + reason + "|" + u.GetNamespace() + "|" + obj + "|" + shape.Template(msg) + "|" + fieldPath
		g, ok := idx[key]
		if !ok {
			g = &eventGroup{typ: typ, reason: reason, object: obj, namespace: u.GetNamespace(), fieldPath: fieldPath}
			idx[key] = g
			out = append(out, g)
		}
		g.count += count
		if t.After(g.last) || g.last.IsZero() {
			g.last, g.message = t, msg
		}
	}
	res := make([]eventGroup, len(out))
	for i, g := range out {
		res[i] = *g
	}
	return res
}

func (r *Runner) events(ctx context.Context, in EventsInput) (output, error) {
	ns, err := r.namespaceFor(r.mustKind("events"), in.Namespace, in.AllNamespaces)
	if err != nil {
		return output{}, err
	}
	since, err := duration("since", in.Since)
	if err != nil {
		return output{}, err
	}
	limit, err := bounded("limit", in.Limit, 50, 1, 200)
	if err != nil {
		return output{}, err
	}
	if err := eventReason(in.Reason); err != nil {
		return output{}, err
	}
	mt, err := parseMatch(in.Match)
	if err != nil {
		return output{}, err
	}
	var about *kube.ObjectRef
	if in.Kind != "" || in.Name != "" {
		if in.Kind == "" || in.Name == "" {
			return output{}, badInput("kind and name go together")
		}
		k, err := r.kind(in.Kind)
		if err != nil {
			return output{}, err
		}
		if err := objectName("name", in.Name); err != nil {
			return output{}, err
		}
		about = &kube.ObjectRef{Kind: k.Kind(), Name: in.Name}
	}
	out := output{namespace: ns}
	page, err := r.Reader.Events(ctx, ns, about, fetchCap)
	if err != nil {
		return out, err
	}
	now := r.now()
	items := page.Items
	if in.Reason != "" {
		items = nil
		for i := range page.Items {
			if reason, _, _ := unstructured.NestedString(page.Items[i].Object, "reason"); reason == in.Reason {
				items = append(items, page.Items[i])
			}
		}
	}
	groups := groupEvents(items, now, in.WarningsOnly, since)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].less(groups[j]) })

	// match: keep the groups that hold a word in the reason, the object or the
	// message, as the result shows them and redacted. It runs before Normal
	// groups are set aside and before the limit and the size budget.
	matchLine := ""
	var names []string
	if mt != nil {
		quiet := r.quietRedactor()
		before := len(groups)
		var kept []eventGroup
		for _, g := range groups {
			names = append(names, g.reason, g.object[strings.IndexByte(g.object, '/')+1:])
			if mt.has(quiet.Text(g.reason + " " + g.namespace + "/" + g.object + " " + cleanMessage(g.message))) {
				kept = append(kept, g)
			}
		}
		matchLine = fmt.Sprintf("%d of %d event groups match", len(kept), before)
		groups = kept
	}

	scope := "namespace " + ns
	if ns == "" {
		scope = "all namespaces"
	}
	out.title = fmt.Sprintf("events · %s · context %s", scope, r.Context)
	if about != nil {
		out.title += fmt.Sprintf(" · about %s/%s", strings.ToLower(about.Kind), about.Name)
	}
	if in.Reason != "" {
		out.title += " · reason " + in.Reason
	}
	warn, total := 0, 0
	for _, g := range groups {
		total += g.count
		if g.typ == "Warning" {
			warn += g.count
		}
	}
	out.summary = []string{fmt.Sprintf("%s in %s (%s warnings)", plural(total, "event", "events"), plural(len(groups), "group", "groups"), shape.Thousands(warn))}
	if matchLine != "" {
		out.summary = append(out.summary, matchLine)
	}
	if len(groups) == 0 && mt != nil {
		out.body = "no event groups match"
		if c := closeNames(mt.words, names, suggestionsShown); len(c) > 0 {
			out.body += "; closest names: " + strings.Join(c, ", ")
		}
		return out, nil
	}
	if len(groups) == 0 {
		out.body = "no events found"
		if since > 0 || in.WarningsOnly || in.Reason != "" {
			out.body += " for these filters"
		}
		out.body += "; events expire after about one hour by default"
		return out, nil
	}
	hiddenLine := ""
	if !in.All && !in.WarningsOnly && mt == nil {
		groups, hiddenLine = splitNormal(groups)
	}
	shown := min(limit, len(groups))
	table, sameObj := eventTable(groups[:shown], now, ns == "", r.rt)
	lines := strings.Split(table, "\n")
	kept, _ := shape.FitLines(lines, r.budget(false))
	if len(kept) < 2 { // the header and one row
		kept = lines[:min(2, len(lines))]
	}
	// An object name and a reason are free text that anyone who can create an
	// Event controls, so the two lines that quote them go inside the fence.
	var lead []string
	if sameObj != "" && about == nil {
		lead = append(lead, "all about "+sameObj)
	}
	if hiddenLine != "" {
		lead = append(lead, hiddenLine+"; "+r.verb()+" events with "+r.arg("all", "true")+" to show them")
	}
	out.body = strings.Join(append(lead, kept...), "\n")
	shownGroups := len(kept) - 1
	if shownGroups < len(groups) || page.More {
		out.truncated = true
		note := fmt.Sprintf("showing %d of %d event groups (warnings first).", shownGroups, len(groups))
		if page.More {
			note += fmt.Sprintf(" Only the first %s events were read.", shape.Thousands(len(page.Items)))
		}
		note += " Narrow with " + r.argName("match") + ", kind and name, reason, since or warningsOnly."
		out.notes = append(out.notes, note)
	}
	return out, nil
}

// nestedCount reads a number that may be decoded as int64 or float64.
func nestedCount(obj map[string]any, path ...string) int {
	v, ok, _ := unstructured.NestedFieldNoCopy(obj, path...)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func (r *Runner) mustKind(name string) kinds.Kind {
	k, _ := r.Kinds.Lookup(name)
	return k
}

func oneLine(s string, n int) string {
	return shape.CutWords(strings.Join(strings.Fields(s), " "), n)
}

var eventSuffixRE = regexp.MustCompile(`\s+in pod \S+_\S+\([0-9a-fA-F-]{36}\)`)

// cleanMessage removes text the row already says: the suffix that names the
// pod, its namespace and UID, and a long quoted name that the message says
// more than once (an image reference, for example).
func cleanMessage(msg string) string {
	msg = eventSuffixRE.ReplaceAllString(msg, "")
	for _, m := range quotedRE.FindAllString(msg, -1) {
		if len(m) < 24 {
			continue
		}
		first := strings.Index(msg, m)
		rest := msg[first+len(m):]
		if strings.Contains(rest, strings.Trim(m, `"`)) {
			msg = msg[:first+len(m)] + strings.ReplaceAll(rest, strings.Trim(m, `"`), "(same)")
			// the quotes around a repeat stay: "(same)" in quotes reads fine
		}
	}
	return msg
}

var quotedRE = regexp.MustCompile(`"[^"\s]+"`)

// eventTable renders groups as a table with columns sized from the data. A
// column that would hold one value on every row is left out: the type when
// all are warnings, the object when all are about one object. It returns the
// table and, when the object column is left out, that object. A message is
// redacted before it is cleaned and cut.
func eventTable(groups []eventGroup, now time.Time, withNS bool, redact func(string) string) (string, string) {
	sameType, sameObj := true, true
	for _, g := range groups {
		if g.typ != groups[0].typ {
			sameType = false
		}
		if g.object != groups[0].object || g.namespace != groups[0].namespace {
			sameObj = false
		}
	}
	var header []string
	if !sameType {
		header = append(header, "TYPE")
	}
	header = append(header, "REASON")
	if !sameObj {
		header = append(header, "OBJECT")
	}
	header = append(header, "COUNT", "LAST", "MESSAGE")
	var rows [][]string
	for _, g := range groups {
		var c []string
		if !sameType {
			c = append(c, g.typ)
		}
		c = append(c, g.reason)
		if !sameObj {
			obj := g.object
			if withNS && g.namespace != "" {
				obj = g.namespace + "/" + obj
			}
			c = append(c, obj)
		}
		last := "-"
		if !g.last.IsZero() {
			last = shape.Age(g.last, now) + " ago"
		}
		c = append(c, fmt.Sprintf("×%d", g.count), last, oneLine(cleanMessage(redact(g.message)), 300))
		rows = append(rows, c)
	}
	obj := ""
	if sameObj && len(groups) > 0 {
		obj = groups[0].object
	}
	return shape.Table(header, rows), obj
}

// splitNormal separates Normal groups from the others, when there are
// others. It returns the groups to show and a line that counts the hidden
// ones by reason ("" when none is hidden).
func splitNormal(groups []eventGroup) ([]eventGroup, string) {
	var shown, hidden []eventGroup
	for _, g := range groups {
		if g.typ == "Normal" {
			hidden = append(hidden, g)
		} else {
			shown = append(shown, g)
		}
	}
	if len(shown) == 0 || len(hidden) == 0 {
		return groups, ""
	}
	byReason := map[string]int{}
	var order []string
	for _, g := range hidden {
		if byReason[g.reason] == 0 {
			order = append(order, g.reason)
		}
		byReason[g.reason] += g.count
	}
	var parts []string
	for _, r := range order {
		parts = append(parts, fmt.Sprintf("%s ×%d", r, byReason[r]))
	}
	return shown, fmt.Sprintf("%d Normal groups hidden: %s", len(hidden), strings.Join(parts, ", "))
}
