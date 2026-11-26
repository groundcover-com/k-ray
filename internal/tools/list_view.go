package tools

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/shape"
)

// listCell is the CAUSE column of a row: its diagnosed cause, or else its
// first problem and how many more it has.
func listCell(cause map[string]string, row shape.Row, rt func(string) string) string {
	if c := cause[row.Namespace+"/"+row.Name]; c != "" {
		return c
	}
	if len(row.Problems) == 0 {
		return "-"
	}
	p := oneLine(row.Lines(rt)[0], 200)
	if len(row.Problems) > 1 {
		p += fmt.Sprintf(" (+%d more)", len(row.Problems)-1)
	}
	return p
}

// listTitle is the title line: what was listed, where and with which selector.
func (r *Runner) listTitle(k kinds.Kind, ns, sel string) string {
	scope := "namespace " + ns
	switch {
	case !k.Namespaced():
		scope = "cluster-scoped"
	case ns == "":
		scope = "all namespaces"
	}
	title := fmt.Sprintf("list %s · %s · context %s", k.Name(), scope, r.Context)
	if sel != "" {
		title += " · selector " + sel
	}
	return title
}

// listSummary is the first summary line: how many objects, in which states.
func listSummary(k kinds.Kind, total int, more bool, counts map[shape.State]int) string {
	sum := fmt.Sprintf("%s %s", shape.Thousands(total), k.Name())
	if total == 1 {
		sum = "1 " + k.Singular()
	}
	if more {
		sum = fmt.Sprintf("first %s %s fetched (the cluster has more)", shape.Thousands(total), k.Name())
	}
	return sum + listStateCounts(counts)
}

// listStateCounts is ": 2 unhealthy, 5 healthy", worst first, or "" when all
// counts are zero.
func listStateCounts(counts map[shape.State]int) string {
	var parts []string
	for _, s := range []shape.State{shape.Unhealthy, shape.Warning, shape.Healthy} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, ", ")
}

// listColumnAt is the index of a kind column, or -1.
func listColumnAt(kind, name string) int {
	at := -1
	for i, c := range shape.Columns(kind) {
		if c == name {
			at = i
		}
	}
	return at
}

// listMatchRows keeps the rows that hold a word. It runs on redacted text,
// after the causes and before the folding and the size budget, so that a
// match is never lost to a cut or hidden inside "(+3 like it)". line is the
// summary line of the match; empty is the body when no row matches.
func (r *Runner) listMatchRows(k kinds.Kind, mt *matcher, rows []shape.Row, byKey map[string]*unstructured.Unstructured, cell func(shape.Row) string) (listed []shape.Row, line, empty string) {
	quiet := r.quietRedactor()
	statusAt := listColumnAt(k.Kind(), "STATUS")
	matched := map[shape.State]int{}
	for _, row := range rows {
		if mt.has(quiet.Text(listMatchText(row, byKey, cell, statusAt))) {
			listed = append(listed, row)
			matched[row.State]++
		}
	}
	line = fmt.Sprintf("%s of %s %s match", shape.Thousands(len(listed)), shape.Thousands(len(rows)), k.Name()) + listStateCounts(matched)
	if len(listed) == 0 {
		empty = listNoMatchBody(k, mt, rows)
	}
	return listed, line, empty
}

// listMatchText is the text of a row that a match looks in.
func listMatchText(row shape.Row, byKey map[string]*unstructured.Unstructured, cell func(shape.Row) string, statusAt int) string {
	hay := row.Namespace + " " + row.Name + " " + cell(row)
	if u := byKey[row.Namespace+"/"+row.Name]; u != nil {
		hay += " " + workloadName(u)
	}
	if statusAt >= 0 && statusAt < len(row.Cells) {
		hay += " " + row.Cells[statusAt]
	}
	return hay
}

func listNoMatchBody(k kinds.Kind, mt *matcher, rows []shape.Row) string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	body := "no " + k.Name() + " match"
	if c := closeNames(mt.words, names, suggestionsShown); len(c) > 0 {
		body += "; closest names: " + strings.Join(c, ", ")
	}
	return body
}

// listVisible picks the rows that get a line. Healthy objects are
// summarized, not listed, unless the list is short or all=true: a row for
// each of 100 healthy pods hides the 5 that matter. With a match, the healthy
// objects that match are what was asked for: they get rows, up to a small
// number, then they are counted in healthyMore.
func listVisible(listed []shape.Row, matching, all bool) (visible, healthy []shape.Row, healthyMore int) {
	switch {
	case matching && !all:
		var shownHealthy []shape.Row
		for _, row := range listed {
			if row.State != shape.Healthy {
				visible = append(visible, row)
			} else if len(shownHealthy) < matchHealthyShown {
				shownHealthy = append(shownHealthy, row)
			} else {
				healthyMore++
			}
		}
		return append(visible, shownHealthy...), nil, healthyMore
	case !matching && !all && len(listed) > listAllUnder:
		for _, row := range listed {
			if row.State == shape.Healthy {
				healthy = append(healthy, row)
			} else {
				visible = append(visible, row)
			}
		}
		return visible, healthy, 0
	}
	return listed, nil, 0
}

// listFolded is one table row that stands for n objects with the same problem.
type listFolded struct {
	row       shape.Row
	n         int
	workloads map[string]bool
}

// listFold folds pods of one namespace with the same problem into one row,
// also across workloads: twenty services that wait for the same missing image
// are one fact, not twenty.
func listFold(kind string, all bool, visible []shape.Row, byKey map[string]*unstructured.Unstructured, cell func(shape.Row) string) []listFolded {
	var fold []listFolded
	if kind != "Pod" || all {
		for _, row := range visible {
			fold = append(fold, listFolded{row: row, n: 1})
		}
		return fold
	}
	idx := map[string]int{}
	for _, row := range visible {
		key, wl := "", ""
		if u := byKey[row.Namespace+"/"+row.Name]; u != nil && len(row.Problems) > 0 {
			wl = workloadName(u)
			key = row.Namespace + "|" + string(row.State) + "|" + shape.Template(cell(row))
		}
		if i, ok := idx[key]; ok && key != "" {
			fold[i].n++
			fold[i].workloads[wl] = true
			continue
		}
		idx[key] = len(fold)
		fold = append(fold, listFolded{row: row, n: 1, workloads: map[string]bool{wl: true}})
	}
	return fold
}

// listFoldedName is the NAME cell of a folded row.
func listFoldedName(f listFolded) string {
	switch {
	case f.n > 1 && len(f.workloads) > 1:
		return f.row.Name + fmt.Sprintf(" (+%d like it, in %d workloads)", f.n-1, len(f.workloads))
	case f.n > 1:
		return f.row.Name + fmt.Sprintf(" (+%d like it)", f.n-1)
	}
	return f.row.Name
}

// listProblemRows counts the folded rows that are not healthy.
func listProblemRows(fold []listFolded) int {
	n := 0
	for _, f := range fold {
		if f.row.State != shape.Healthy {
			n++
		}
	}
	return n
}

// listHealthyLine says what the healthy objects that have no row are.
func listHealthyLine(k kinds.Kind, healthy []shape.Row, healthyMore int, byKey map[string]*unstructured.Unstructured, withNS, large bool) string {
	line := ""
	if len(healthy) > 0 {
		nsShown := healthyNamespacesShown
		if large {
			nsShown = healthyNamespacesLarge
		}
		line = healthySummary(k.Kind(), healthy, byKey, withNS, nsShown)
	}
	if healthyMore > 0 {
		line = fmt.Sprintf("%d more healthy %s match, not shown; make the match narrower", healthyMore, k.Name())
	}
	return line
}
