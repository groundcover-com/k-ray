package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/redact"
)

// describe also finds things: by label selector, and by a name alone. Both
// read through kube.Reader with fixed kinds and the existing fetch caps.

const (
	maxDescribed     = 3   // objects that describe shows in full; more are listed
	describeListCap  = 100 // objects read for a selector
	describeResolve  = 10  // owners looked up to find the workloads of some pods
	matchLinesShown  = 10
	suggestionsShown = 5
)

// target is one object to describe. pods counts the pods that led to a
// workload, and is 0 for an object that matched by itself.
type target struct {
	k    kinds.Kind
	u    *unstructured.Unstructured
	pods int
}

func (t target) key() string { return t.k.Kind() + "/" + t.u.GetNamespace() + "/" + t.u.GetName() }

func (t target) ref() string { return strings.ToLower(t.k.Kind()) + "/" + t.u.GetName() }

// topOwner follows the controller chain of an object up to the workload:
// a pod to its ReplicaSet to its Deployment, a Job to its CronJob.
func (r *Runner) topOwner(ctx context.Context, u *unstructured.Unstructured) (kinds.Kind, *unstructured.Unstructured, bool) {
	cur, found := u, false
	var ck kinds.Kind
	for hop := 0; hop < 4; hop++ {
		ref := controllerRef(cur)
		if ref == nil {
			break
		}
		gv, _ := schema.ParseGroupVersion(ref.APIVersion)
		ok, known := r.Kinds.ByGroupKind(gv.Group, ref.Kind)
		if !known {
			break
		}
		parent, err := r.Reader.Get(ctx, ok, cur.GetNamespace(), ref.Name)
		if err != nil {
			break
		}
		cur, ck, found = parent, ok, true
	}
	return ck, cur, found
}

// collapseToWorkloads turns pods into the workloads that own them, so that
// the pods of one Deployment are one target. A pod with no owner, or an
// owner that cannot be read, is a target itself.
func (r *Runner) collapseToWorkloads(ctx context.Context, pk kinds.Kind, items []unstructured.Unstructured) []target {
	var targets []target
	seen := map[string]int{}
	add := func(t target) {
		if i, ok := seen[t.key()]; ok {
			targets[i].pods += t.pods
			return
		}
		seen[t.key()] = len(targets)
		targets = append(targets, t)
	}
	type group struct {
		first *unstructured.Unstructured
		n     int
	}
	groups := map[string]*group{}
	var order []string
	for i := range items {
		u := &items[i]
		ref := controllerRef(u)
		if ref == nil {
			add(target{k: pk, u: u})
			continue
		}
		key := u.GetNamespace() + "/" + ref.Kind + "/" + ref.Name
		g := groups[key]
		if g == nil {
			g = &group{first: u}
			groups[key] = g
			order = append(order, key)
		}
		g.n++
	}
	// The owner chains of the groups do not depend on each other: they are
	// followed at the same time, and the targets are added in the order of the
	// groups.
	type resolved struct {
		k     kinds.Kind
		owner *unstructured.Unstructured
		ok    bool
	}
	owners := make([]resolved, min(len(order), describeResolve))
	fns := make([]func(), len(owners))
	for i := range owners {
		fns[i] = func() {
			ck, owner, ok := r.topOwner(ctx, groups[order[i]].first)
			owners[i] = resolved{ck, owner, ok}
		}
	}
	if len(fns) > 0 {
		parallel(fns...)
	}
	for i, key := range order {
		g := groups[key]
		if i >= describeResolve {
			add(target{k: pk, u: g.first}) // there are more than enough targets already
			continue
		}
		if o := owners[i]; o.ok {
			add(target{k: o.k, u: o.owner, pods: g.n})
		} else {
			add(target{k: pk, u: g.first})
		}
	}
	return targets
}

// describeTargets describes each target and joins the results.
func (r *Runner) describeTargets(ctx context.Context, targets []target, title string, summary []string, red *redact.Redactor) (output, error) {
	limit := (r.budget(false) - 300) / len(targets)
	if len(targets) == 1 {
		t := targets[0]
		out, err := r.describeOne(ctx, t.k, t.u.GetNamespace(), t.u.GetName(), t.u, red, r.budget(false))
		out.summary = append(append([]string(nil), summary...), out.summary...)
		return out, err
	}
	out := output{title: title, summary: summary}
	var b strings.Builder
	ns := targets[0].u.GetNamespace()
	seenNote := map[string]bool{}
	// The targets are described at the same time and joined in order.
	subs := make([]output, len(targets))
	errs := make([]error, len(targets))
	fns := make([]func(), len(targets))
	for i, t := range targets {
		fns[i] = func() {
			subs[i], errs[i] = r.describeOne(ctx, t.k, t.u.GetNamespace(), t.u.GetName(), t.u, red, limit)
		}
	}
	parallel(fns...)
	for i, t := range targets {
		sub, err := subs[i], errs[i]
		if err != nil {
			fmt.Fprintf(&b, "== %s%s\ncould not be described: %s\n", t.ref(), inNS(t.u.GetNamespace()), errText(err))
			continue
		}
		if t.u.GetNamespace() != ns {
			ns = ""
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "== %s%s\n", t.ref(), inNS(t.u.GetNamespace()))
		for _, s := range sub.summary {
			b.WriteString(s + "\n")
		}
		b.WriteString(strings.TrimRight(sub.body, "\n") + "\n")
		out.truncated = out.truncated || sub.truncated
		for _, n := range sub.notes {
			if !seenNote[n] {
				seenNote[n] = true
				out.notes = append(out.notes, n)
			}
		}
		out.next = append(out.next, sub.next...)
	}
	out.namespace = ns
	out.body = b.String()
	return out, nil
}

// describeSelected describes the objects that match a label selector: 1 to 3
// of them in full, more as the list.
func (r *Runner) describeSelected(ctx context.Context, in DescribeInput, red *redact.Redactor) (output, error) {
	k, err := r.kind(in.Kind)
	if err != nil {
		return output{}, err
	}
	if in.Name != "" {
		return output{}, badInput("name and labelSelector cannot be used together")
	}
	sel, err := selector(in.LabelSelector)
	if err != nil {
		return output{}, err
	}
	ns, err := r.namespaceFor(k, in.Namespace, in.AllNamespaces)
	if err != nil {
		return output{}, err
	}
	page, err := r.Reader.List(ctx, k, ns, sel, describeListCap)
	if err != nil {
		return output{namespace: ns}, err
	}
	scope := "namespace " + ns
	switch {
	case !k.Namespaced():
		scope = "cluster-scoped"
	case ns == "":
		scope = "all namespaces"
	}
	title := fmt.Sprintf("describe %s · %s · context %s · selector %s", k.Singular(), scope, r.Context, sel)
	if len(page.Items) == 0 {
		return output{title: title, namespace: ns, body: "no objects found"}, nil
	}
	var targets []target
	if k.Kind() == "Pod" {
		targets = r.collapseToWorkloads(ctx, k, page.Items)
	} else {
		for i := range page.Items {
			targets = append(targets, target{k: k, u: &page.Items[i]})
		}
	}
	if len(targets) > maxDescribed {
		out, err := r.list(ctx, ListInput{Kind: in.Kind, Namespace: in.Namespace, AllNamespaces: in.AllNamespaces, LabelSelector: in.LabelSelector}, red)
		if err != nil {
			return out, err
		}
		first := page.Items[0]
		out.notes = append(out.notes, fmt.Sprintf("%s match, more than %d: this is the list, not a description of each. To describe one: %s %s.",
			plural(len(targets), "object", "objects"), maxDescribed, r.verb(), r.hintDescribe(k.Singular(), first.GetName(), first.GetNamespace())))
		return out, nil
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].key() < targets[j].key() })
	var summary []string
	if len(targets) == 1 && targets[0].pods > 0 {
		t := targets[0]
		summary = append(summary, fmt.Sprintf("selector %s matches %s of %s%s; this describes the workload", sel, plural(t.pods, "pod", "pods"), t.ref(), inNS(t.u.GetNamespace())))
	} else if len(targets) > 1 {
		var refs []string
		for _, t := range targets {
			refs = append(refs, t.ref())
		}
		summary = append(summary, fmt.Sprintf("selector %s matches %d objects: %s", sel, len(targets), strings.Join(refs, ", ")))
	}
	out, err := r.describeTargets(ctx, targets, title, summary, red)
	if page.More {
		out.notes = append(out.notes, fmt.Sprintf("only the first %d matching objects were read.", describeListCap))
	}
	return out, err
}

// nameMatch reports whether an object name is the wanted name, or starts
// with the wanted name and a dash.
func nameMatch(name, want string) (match, exact bool) {
	switch {
	case name == want:
		return true, true
	case strings.HasPrefix(name, want+"-"):
		return true, false
	}
	return false, false
}

// describeFound finds an object by name, in the allowed workload kinds and
// services first, then in pods: the exact name first, then names that start
// with the name and a dash. One match, or one workload-level match, is
// described. Several are named, one line each.
func (r *Runner) describeFound(ctx context.Context, in DescribeInput, red *redact.Redactor) (output, error) {
	ns, err := describeFoundNamespace(in)
	if err != nil {
		return output{}, err
	}
	stages, err := r.describeFoundStages(in)
	if err != nil {
		return output{}, err
	}
	s := r.describeSearchByName(ctx, stages, ns, in.Name)
	if s.read == 0 && s.firstErr != nil {
		return output{namespace: ns}, s.firstErr
	}
	matches := describeKeepExact(s.matches)
	scope := describeFoundScope(ns)
	title := fmt.Sprintf("describe %s · %s · context %s", in.Name, scope, r.Context)
	var notes []string
	if s.partial != "" {
		notes = append(notes, s.partial)
	}
	if len(matches) == 0 {
		return describeNotFound(in, title, ns, scope, s, notes), nil
	}
	targets := r.describeFoundTargets(ctx, matches, s)
	primary, services := describeSplitServices(targets)
	now := r.now()
	describeSortTargets(targets)
	best := describeBestTarget(primary, services)
	if nr := s.notRead(); nr != "" {
		notes = append(notes, nr+".")
	}
	if best == nil {
		return r.describeMatches(ctx, in.Name, title, ns, targets, notes, red)
	}
	return r.describeFoundOne(ctx, *best, targets, notes, now, red)
}

// joinAnd joins names as "a, b and c".
func joinAnd(names []string) string {
	switch len(names) {
	case 0:
		return "no kind"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// matchBlocks is how many of the objects that match a name are described in
// the same answer. The others get one line each.
const matchBlocks = 4

// matchListMax is how many further matches are named, one line each. Beyond
// it they are counted.
const matchListMax = 30

// describeMatches describes each object that matches an ambiguous name, up to
// matchBlocks, the ones with problems first, as short blocks. Every other
// match gets a line. No match is dropped without a word.
func (r *Runner) describeMatches(ctx context.Context, name, title, ns string, targets []target, notes []string, red *redact.Redactor) (output, error) {
	now := r.now()
	r.describeSortMatches(targets, now)
	shown := targets[:min(matchBlocks, len(targets))]
	rest := targets[len(shown):]
	subs, errs := r.describeBriefs(ctx, shown, red)
	order := r.describeBlockOrder(shown, subs)
	namespaces := describeNamespaceCount(targets)

	out := output{title: title, namespace: ns, notes: notes}
	out.summary = []string{describeMatchesSummary(len(targets), name, namespaces, len(shown), len(rest))}
	budget := (r.budget(false) - 400) / max(len(shown), 1)
	var b strings.Builder
	describeWriteBlocks(&b, &out, shown, order, subs, errs, budget)
	r.describeWriteRest(&b, &out, rest, now)
	b.WriteString(describeMatchesClosing(namespaces))
	out.body = b.String()
	return out, nil
}

// editDistance is the Levenshtein distance of two short names.
func editDistance(a, b string) int {
	if len(a) > 64 || len(b) > 64 {
		return max(len(a), len(b))
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// seenName is the kind, namespace and name of an object that was read.
type seenName struct{ kind, ns, name string }

// closestNames returns up to suggestionsShown labels of the names that are
// closest to want, by edit distance and containment.
func closestNames(names []seenName, want string) []string {
	type cand struct {
		s string
		d int
	}
	var cands []cand
	seen := map[string]bool{}
	want = strings.ToLower(want)
	for _, n := range names {
		have := strings.ToLower(n.name)
		d := editDistance(have, want)
		if !(strings.Contains(have, want) || strings.Contains(want, have) || d <= max(2, len(want)/3)) {
			continue
		}
		label := fmt.Sprintf("%s %s", strings.ToLower(n.kind), n.name)
		if n.ns != "" {
			label = fmt.Sprintf("%s %s/%s", strings.ToLower(n.kind), n.ns, n.name)
		}
		if !seen[label] {
			seen[label] = true
			cands = append(cands, cand{label, d})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].d != cands[j].d {
			return cands[i].d < cands[j].d
		}
		return cands[i].s < cands[j].s
	})
	if len(cands) > suggestionsShown {
		cands = cands[:suggestionsShown]
	}
	var lines []string
	for _, c := range cands {
		lines = append(lines, c.s)
	}
	return lines
}
