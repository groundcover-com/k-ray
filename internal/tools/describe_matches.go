package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// describeMatchRank orders states: the ones with problems first.
func describeMatchRank(s shape.State) int {
	switch s {
	case shape.Unhealthy:
		return 0
	case shape.Warning:
		return 1
	case shape.Healthy:
		return 2
	}
	return 3
}

// describeDefaultNSRank puts the objects of the context namespace first.
func (r *Runner) describeDefaultNSRank(t target) int {
	if r.Namespace != "" && t.u.GetNamespace() == r.Namespace {
		return 0
	}
	return 1
}

// describeSortMatches sorts the matches before the blocks are chosen. The
// objects themselves say which have a problem; the services need their
// endpoints to say it, so with few matches the blocks decide the order.
func (r *Runner) describeSortMatches(targets []target, now time.Time) {
	sort.SliceStable(targets, func(i, j int) bool {
		if a, b := r.describeDefaultNSRank(targets[i]), r.describeDefaultNSRank(targets[j]); a != b {
			return a < b
		}
		return describeMatchRank(shape.Inspect(targets[i].u, now).State) < describeMatchRank(shape.Inspect(targets[j].u, now).State)
	})
}

// describeBriefs describes the targets at the same time, as short blocks.
func (r *Runner) describeBriefs(ctx context.Context, shown []target, red *redact.Redactor) ([]output, []error) {
	subs := make([]output, len(shown))
	errs := make([]error, len(shown))
	fns := make([]func(), len(shown))
	for i, t := range shown {
		fns[i] = func() {
			subs[i], errs[i] = r.describeBrief(ctx, t.k, t.u.GetNamespace(), t.u.GetName(), t.u, red)
		}
	}
	parallel(fns...)
	return subs, errs
}

// describeStateOf is the state that a short block found, or "-".
func describeStateOf(sub output) string {
	for _, s := range sub.summary {
		if v, ok := strings.CutPrefix(s, "state: "); ok {
			return v
		}
	}
	return "-"
}

// describeBlockOrder is the order in which the blocks are written: the
// context namespace first, then by the state that each block found.
func (r *Runner) describeBlockOrder(shown []target, subs []output) []int {
	order := make([]int, len(shown))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if x, y := r.describeDefaultNSRank(shown[order[a]]), r.describeDefaultNSRank(shown[order[b]]); x != y {
			return x < y
		}
		return describeMatchRank(shape.State(describeStateOf(subs[order[a]]))) < describeMatchRank(shape.State(describeStateOf(subs[order[b]])))
	})
	return order
}

// describeNamespaceCount counts the namespaces of the matches.
func describeNamespaceCount(targets []target) int {
	namespaces := map[string]bool{}
	for _, t := range targets {
		namespaces[t.u.GetNamespace()] = true
	}
	return len(namespaces)
}

// describeWriteBlocks writes the short blocks in order, each cut to budget.
func describeWriteBlocks(b *strings.Builder, out *output, shown []target, order []int, subs []output, errs []error, budget int) {
	for n, i := range order {
		t := shown[i]
		if n > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "== %s%s\n", t.ref(), describeInNamespace(t.u.GetNamespace()))
		if errs[i] != nil {
			fmt.Fprintf(b, "could not be described: %s\n", callError(errs[i], request{tool: "describe", kind: t.k.Name(), name: t.u.GetName(), namespace: t.u.GetNamespace()}))
			continue
		}
		fmt.Fprintf(b, "state: %s", describeStateOf(subs[i]))
		if t.pods > 0 {
			b.WriteString(describeWorkloadOf(t.pods))
		}
		b.WriteString("\n")
		body, wasCut := shape.Cut(strings.TrimRight(subs[i].body, "\n")+"\n", budget)
		b.WriteString(body)
		if wasCut {
			out.truncated = true
		}
		out.next = append(out.next, subs[i].next...)
	}
}

// describeWriteRest gives each match that has no block one line, up to
// matchListMax; the rest are counted. No match is dropped without a word.
func (r *Runner) describeWriteRest(b *strings.Builder, out *output, rest []target, now time.Time) {
	if len(rest) == 0 {
		return
	}
	b.WriteString("\nnot described here (one line each):\n")
	for n, t := range rest {
		if n == matchListMax {
			fmt.Fprintf(b, "(%d more matches, not named: narrow with %s)\n", len(rest)-n, r.argName("namespace"))
			break
		}
		b.WriteString(r.describeTargetLine(t, now) + "\n")
		if n < maxDescribed {
			out.next = append(out.next, r.hintDescribe(t.k.Singular(), t.u.GetName(), t.u.GetNamespace()))
		}
	}
}
