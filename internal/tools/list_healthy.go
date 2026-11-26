package tools

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/shape"
)

// listSplitCompleted puts completed pods apart from the running objects.
func listSplitCompleted(kind string, healthy []shape.Row) (running, done []shape.Row) {
	for _, row := range healthy {
		if kind == "Pod" && len(row.Cells) > 1 && row.Cells[1] == "Completed" {
			done = append(done, row)
		} else {
			running = append(running, row)
		}
	}
	return running, done
}

// listHealthyGroup is a name and how many healthy objects have it.
type listHealthyGroup struct {
	name string
	n    int
}

// listHealthyGroups groups rows by name, pods by workload, most numerous
// first. withNS puts the namespace before each name.
func listHealthyGroups(kind string, rows []shape.Row, byKey map[string]*unstructured.Unstructured, withNS bool) []*listHealthyGroup {
	idx := map[string]*listHealthyGroup{}
	var gs []*listHealthyGroup
	for _, row := range rows {
		name := row.Name
		if kind == "Pod" {
			if u := byKey[row.Namespace+"/"+row.Name]; u != nil {
				name = workloadName(u)
			}
		}
		if withNS && row.Namespace != "" {
			name = row.Namespace + "/" + name
		}
		g := idx[name]
		if g == nil {
			g = &listHealthyGroup{name: name}
			idx[name] = g
			gs = append(gs, g)
		}
		g.n++
	}
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].n != gs[j].n {
			return gs[i].n > gs[j].n
		}
		return gs[i].name < gs[j].name
	})
	return gs
}

// listHealthyNames names the groups of rows, the first healthyNamesShown of
// them, and counts the rest.
func listHealthyNames(kind string, rows []shape.Row, byKey map[string]*unstructured.Unstructured, withNS bool) string {
	gs := listHealthyGroups(kind, rows, byKey, withNS)
	var parts []string
	for i, g := range gs {
		if i == healthyNamesShown {
			rest := 0
			for _, x := range gs[i:] {
				rest += x.n
			}
			parts = append(parts, fmt.Sprintf("+%d more (%d)", len(gs)-i, rest))
			break
		}
		if kind == "Pod" && g.n > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", g.name, g.n))
		} else {
			parts = append(parts, g.name)
		}
	}
	return strings.Join(parts, ", ")
}

// listHealthyByNamespace says what the running objects are, one line for
// each namespace, so a text search for a workload name returns one short line
// that also gives its namespace. When the names take more than
// healthyDetailMax, they would take the room of the problems, so each
// namespace gets a count only.
func listHealthyByNamespace(kind string, running []shape.Row, byKey map[string]*unstructured.Unstructured, nsShown int) []string {
	byNS := map[string][]shape.Row{}
	var order []string
	for _, row := range running {
		if _, ok := byNS[row.Namespace]; !ok {
			order = append(order, row.Namespace)
		}
		byNS[row.Namespace] = append(byNS[row.Namespace], row)
	}
	sort.Strings(order)
	detail := []string{fmt.Sprintf("healthy, not listed (%d), by namespace:", len(running))}
	size := 0
	for _, ns := range order {
		// Brackets, not "name: value": a namespace such as
		// external-secrets must not look like a secret key to the redactor.
		l := "  [" + ns + "] " + listHealthyNames(kind, byNS[ns], byKey, false)
		size += len(l) + 1
		detail = append(detail, l)
	}
	if size <= healthyDetailMax {
		return detail
	}
	return []string{listHealthyNamespaceCounts(len(running), byNS, order, nsShown)}
}

// listHealthyNamespaceCounts counts the running objects of each namespace,
// the most numerous namespaces first.
func listHealthyNamespaceCounts(running int, byNS map[string][]shape.Row, order []string, nsShown int) string {
	sort.SliceStable(order, func(i, j int) bool { return len(byNS[order[i]]) > len(byNS[order[j]]) })
	var parts []string
	for i, ns := range order {
		if i == nsShown {
			parts = append(parts, fmt.Sprintf("+%d more namespaces", len(order)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", ns, len(byNS[ns])))
	}
	return fmt.Sprintf("healthy, not listed (%d) in %d namespaces: %s", running, len(order), strings.Join(parts, ", "))
}
