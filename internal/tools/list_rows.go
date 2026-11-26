package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/shape"
)

// listQuery is the checked arguments of one list call.
type listQuery struct {
	k     kinds.Kind
	ns    string
	sel   string
	limit int
	mt    *matcher
}

func (r *Runner) listQueryOf(in ListInput) (listQuery, error) {
	k, err := r.kind(in.Kind)
	if err != nil {
		return listQuery{}, err
	}
	ns, err := r.namespaceFor(k, in.Namespace, in.AllNamespaces)
	if err != nil {
		return listQuery{}, err
	}
	sel, err := selector(in.LabelSelector)
	if err != nil {
		return listQuery{}, err
	}
	limit, err := bounded("limit", in.Limit, 200, 1, 500)
	if err != nil {
		return listQuery{}, err
	}
	mt, err := parseMatch(in.Match)
	if err != nil {
		return listQuery{}, err
	}
	return listQuery{k: k, ns: ns, sel: sel, limit: limit, mt: mt}, nil
}

// listEndpoints is what the EndpointSlices say about the services of a list.
type listEndpoints struct {
	counts  map[string][2]int // service -> ready, total
	noPorts map[string]bool
	cut     bool // the EndpointSlice list was cut at the cap
}

// listReads are the reads that run next to the object list. Each is nil when
// the kind does not need it.
type listReads struct {
	use       *future[map[string]map[string]containerUse]
	events    *future[*eventIndex]
	endpoints *future[listEndpoints]
}

// listStartReads starts the reads before the list says they are needed, so
// that they do not wait for it; drop cancels the ones that turn out not to be
// needed (the usage when there is no pod, the events when no pod has a
// problem).
func (r *Runner) listStartReads(ctx context.Context, kind, ns, sel string) *listReads {
	reads := &listReads{}
	switch kind {
	case "Pod":
		reads.use = start(ctx, func(c context.Context) map[string]map[string]containerUse {
			use, _ := r.podUsage(c, ns, sel)
			return use
		})
		reads.events = start(ctx, func(c context.Context) *eventIndex { return r.podEventIndex(c, ns) })
	case "Service":
		reads.endpoints = start(ctx, func(c context.Context) listEndpoints {
			counts, noPorts, cut := r.endpointCounts(c, ns)
			return listEndpoints{counts, noPorts, cut}
		})
	}
	return reads
}

func (l *listReads) drop() {
	if l.events != nil {
		l.events.drop()
	}
	if l.use != nil {
		l.use.drop()
	}
	if l.endpoints != nil {
		l.endpoints.drop()
	}
}

// wait returns the usage and the endpoints. An empty list needs neither.
func (l *listReads) wait(haveItems bool) (map[string]map[string]containerUse, listEndpoints) {
	var use map[string]map[string]containerUse
	if l.use != nil && haveItems {
		use = l.use.wait()
	}
	ends := listEndpoints{counts: map[string][2]int{}, noPorts: map[string]bool{}}
	if l.endpoints != nil && haveItems {
		ends = l.endpoints.wait()
	}
	return use, ends
}

// listRows inspects each object, adds the problems that need more than the
// object to see, and sorts the rows worst first. counts is by state.
func (r *Runner) listRows(ctx context.Context, kind, ns string, items []unstructured.Unstructured, now time.Time, use map[string]map[string]containerUse, ends listEndpoints) ([]shape.Row, map[shape.State]int) {
	rows := make([]shape.Row, 0, len(items))
	counts := map[shape.State]int{}
	for i := range items {
		row := shape.Inspect(&items[i], now)
		if kind == "Service" {
			r.listServiceProblems(ctx, ns, &row, &items[i], ends)
		}
		listUsageProblems(&row, &items[i], use)
		rows = append(rows, row)
		counts[row.State]++
	}
	shape.SortRows(rows)
	if kind == "Pod" {
		// Pods that only restarted come after the other warnings.
		sort.SliceStable(rows, func(i, j int) bool { return rowTier(rows[i]) < rowTier(rows[j]) })
	}
	return rows, counts
}

// listServiceProblems judges a Service by its endpoints, which the object
// itself does not show.
func (r *Runner) listServiceProblems(ctx context.Context, ns string, row *shape.Row, u *unstructured.Unstructured, ends listEndpoints) {
	sel, _, _ := unstructured.NestedStringMap(u.Object, "spec", "selector")
	ports, _, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
	key := row.Namespace + "/" + row.Name
	c, ok := ends.counts[key]
	// With the slice list cut, a Service that shows no ready endpoint in view
	// is unknown, not "none ready".
	if len(sel) == 0 || ends.counts == nil || (ends.cut && c[0] == 0) {
		return
	}
	row.State = shape.Healthy
	switch {
	case !ok || c[0] == 0:
		if owners := r.scaledToZeroOwners(ctx, ns, row.Namespace, sel); len(owners) > 0 {
			row.AddProblem(shape.Warning, listScaledToZeroCause(c[0], c[1], owners))
		} else {
			row.AddProblem(shape.Unhealthy, listNoReadyEndpointsCause(c[0], c[1]))
		}
	case ends.noPorts[key] && len(ports) > 0:
		row.AddProblem(shape.Unhealthy, listNoPortsCause())
	}
}

// listUsageProblems joins usage with limits, so a pod near its memory limit
// or at its CPU limit shows as a warning before it crashes.
func listUsageProblems(row *shape.Row, u *unstructured.Unstructured, use map[string]map[string]containerUse) {
	cu := use[u.GetNamespace()+"/"+u.GetName()]
	if cu == nil {
		return
	}
	p, ok := toPod(u)
	if !ok {
		return
	}
	for _, c := range p.Spec.Containers {
		for _, f := range resourceFindings(newUsageRow(p.Namespace, p.Name, c.Name, cu[c.Name], c.Resources)) {
			if f.warn {
				row.AddProblem(shape.Warning, "container "+c.Name+": "+f.text)
			}
		}
	}
}

func listScaledToZeroCause(ready, total int, owners []string) string {
	return fmt.Sprintf("no ready endpoints (%d of %d): %s %s scaled to 0 replicas", ready, total, strings.Join(owners, ", "), listIsAre(len(owners)))
}

func listNoReadyEndpointsCause(ready, total int) string {
	return fmt.Sprintf("no ready endpoints (%d of %d)", ready, total)
}

func listNoPortsCause() string {
	return "endpoints have no ports: a targetPort matches no container port"
}

// listIsAre is the verb that agrees with n subjects.
func listIsAre(n int) string {
	if n > 1 {
		return "are"
	}
	return "is"
}

// listByKey indexes objects by namespace/name.
func listByKey(items []unstructured.Unstructured) map[string]*unstructured.Unstructured {
	byKey := map[string]*unstructured.Unstructured{}
	for i := range items {
		byKey[items[i].GetNamespace()+"/"+items[i].GetName()] = &items[i]
	}
	return byKey
}
