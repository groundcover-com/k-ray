package tools

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/shape"
)

// listTable renders the folded rows of a list as a table.
type listTable struct {
	header []string
	drop   int // a kind column left out, or -1
	first  int // the index of the first kind column in header
	withNS bool
	large  bool
	fold   []listFolded
	cell   func(shape.Row) string
}

func listTableOf(kind string, withNS, large bool, fold []listFolded, cell func(shape.Row) string) listTable {
	t := listTable{withNS: withNS, large: large, fold: fold, cell: cell, drop: -1}
	if withNS {
		t.header = append(t.header, "NAMESPACE")
	}
	t.header = append(t.header, "NAME")
	if kind == "Pod" {
		t.drop = listColumnAt(kind, "NODE") // the node is rarely the point of a pod list
	}
	t.first = len(t.header)
	for i, c := range shape.Columns(kind) {
		if i != t.drop {
			t.header = append(t.header, c)
		}
	}
	t.header = append(t.header, "CAUSE")
	return t
}

// render renders the first n rows and the lines that go with them.
func (t listTable) render(n int) string {
	cells := make([][]string, 0, n)
	for _, f := range t.fold[:n] {
		cells = append(cells, t.rowCells(f))
	}
	h := t.header
	var same []string
	if t.large {
		h, cells, same = dropShared(t.header, cells, t.first)
	}
	b := shape.Table(h, cells)
	if len(same) > 0 {
		b += "\nsame on every row: " + strings.Join(same, ", ")
	}
	if n < len(t.fold) && t.withNS {
		var hidden []string
		for _, f := range t.fold[n:] {
			hidden = append(hidden, f.row.Namespace)
		}
		b += "\n" + notShownLine(hidden)
	}
	return b
}

func (t listTable) rowCells(f listFolded) []string {
	var c []string
	if t.withNS {
		c = append(c, f.row.Namespace)
	}
	c = append(c, listFoldedName(f))
	for i, v := range f.row.Cells {
		if i != t.drop {
			c = append(c, v)
		}
	}
	cs := t.cell(f.row)
	if t.large {
		cs = shortCause(cs, shortCauseMax)
	}
	return append(c, cs)
}

// fit renders as many of the first shown rows as fit in budget bytes and
// says how many that is.
func (t listTable) fit(shown, budget int) (string, int) {
	if len(t.fold) == 0 {
		return "", shown
	}
	if t.large {
		// The largest number of rows that fits.
		lo, hi := 1, shown
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if len(t.render(mid)) <= budget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		shown = lo
	} else {
		body := t.render(shown)
		for len(body) > budget && shown > 1 {
			shown = shown * 3 / 4
			body = t.render(shown)
		}
	}
	return t.render(shown), shown
}

// listShownMax is how many rows a list shows at most, before the size budget.
func listShownMax(limit int, full bool, rows int) int {
	if full {
		return min(500, rows)
	}
	return min(limit, rows)
}

// listBudget is the room for the table. A large list gets a lower budget.
func listBudget(base int, large bool, healthyLen int) int {
	budget := base - healthyLen
	if large {
		budget = min(budget, largeListBody) - healthyLen
	}
	return budget
}

// listJoinBody puts the healthy line under the table.
func listJoinBody(table, healthyLine string) string {
	if healthyLine == "" {
		return table
	}
	if table == "" {
		return healthyLine
	}
	return table + "\n" + healthyLine
}

// listTruncationNote says what the table left out and which arguments show it.
func (r *Runner) listTruncationNote(k kinds.Kind, shown, folded, fetched int, more bool, remaining int64, large, full bool) string {
	note := fmt.Sprintf("showing %s of %s %s (unhealthy first).", shape.Thousands(shown), shape.Thousands(folded), k.Name())
	if more {
		count := "more"
		if remaining > 0 {
			count = "about " + shape.Thousands(int(remaining)) + " more"
		}
		note += fmt.Sprintf(" The cluster has %s; sorting covers the first %s only.", count, shape.Thousands(fetched))
	}
	note += " Show the others with " + r.argName("match") + " (a name or a word) or " + r.argName("namespace") + ", or narrow with " + r.selectorName()
	switch {
	case large && !full:
		note += ", or " + r.verb() + " list with " + r.arg("full", "true")
	case shown < 500 && !full:
		note += ", or " + r.verb() + " list with " + r.arg("limit", "500")
	}
	return note + "."
}

// listNextHints names the next calls for the first unhealthy row. A row that
// already has its cause gets none: a hint would only invite a call that is
// not needed.
func (r *Runner) listNextHints(k kinds.Kind, listed []shape.Row, cause map[string]string, items []unstructured.Unstructured) []string {
	var row shape.Row
	found := false
	for _, x := range listed {
		if x.State == shape.Unhealthy {
			row, found = x, true
			break
		}
	}
	if !found || cause[row.Namespace+"/"+row.Name] != "" {
		return nil
	}
	next := []string{r.hintDescribe(k.Singular(), row.Name, row.Namespace)}
	if k.Kind() != "Pod" {
		return next
	}
	for i := range items {
		u := &items[i]
		if u.GetName() != row.Name || u.GetNamespace() != row.Namespace {
			continue
		}
		// Previous logs exist only when a container restarted; a pod that
		// never started has no logs.
		switch {
		case hasRestarted(u):
			next = append(next, r.hintLogs(row.Name, row.Namespace, "", true, false))
		case hasRun(u):
			next = append(next, r.hintLogs(row.Name, row.Namespace, "", false, false))
		}
		return next
	}
	return next
}
