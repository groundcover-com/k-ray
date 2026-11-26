package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// fetchCap is how many objects list reads to sort unhealthy first.
const fetchCap = 2000

// causesInList is how many unhealthy objects list diagnoses.
const causesInList = 3

func (r *Runner) list(ctx context.Context, in ListInput, red *redact.Redactor) (output, error) {
	q, err := r.listQueryOf(in)
	if err != nil {
		return output{}, err
	}
	out := output{namespace: q.ns, full: in.Full}

	// The object list, the usage, the events and the endpoints do not depend
	// on each other, so they are read at the same time.
	reads := r.listStartReads(ctx, q.k.Kind(), q.ns, q.sel)
	defer reads.drop()
	page, err := r.Reader.List(ctx, q.k, q.ns, q.sel, fetchCap)
	if err != nil {
		return out, err
	}

	now := r.now()
	use, ends := reads.wait(len(page.Items) > 0)
	rows, counts := r.listRows(ctx, q.k.Kind(), q.ns, page.Items, now, use, ends)
	out.title = r.listTitle(q.k, q.ns, q.sel)
	out.summary = []string{listSummary(q.k, len(rows), page.More, counts)}
	if len(rows) == 0 {
		out.body = "no objects found"
		return out, nil
	}

	byKey := listByKey(page.Items)
	cause := r.listRowCauses(ctx, q.k.Kind(), rows, page.Items, byKey, use, counts, reads, red)
	cell := func(row shape.Row) string { return listCell(cause, row, r.rt) }

	listed := rows
	if q.mt != nil {
		var line, empty string
		listed, line, empty = r.listMatchRows(q.k, q.mt, rows, byKey, cell)
		out.summary = append(out.summary, line)
		if len(listed) == 0 {
			out.body = empty
			return out, nil
		}
	}

	visible, healthy, healthyMore := listVisible(listed, q.mt != nil, in.All)
	fold := listFold(q.k.Kind(), in.All, visible, byKey, cell)
	// A list with many rows that have a problem is shown smaller: shorter
	// causes, no column that every row shares, and a lower size budget. full
	// and all give the whole.
	large := !in.Full && !in.All && listProblemRows(fold) > largeListRows
	withNS := q.ns == "" && q.k.Namespaced()
	healthyLine := listHealthyLine(q.k, healthy, healthyMore, byKey, withNS, large)

	table := listTableOf(q.k.Kind(), withNS, large, fold, cell)
	body, shown := table.fit(listShownMax(q.limit, in.Full, len(fold)), listBudget(r.budget(in.Full), large, len(healthyLine)))
	out.body = listJoinBody(body, healthyLine)
	if strings.Contains(healthyLine, " namespaces: ") {
		out.notes = append(out.notes, "healthy workloads are counted by namespace. For their names: run list with "+r.argName("namespace")+".")
	}
	if shown < len(fold) || page.More {
		out.truncated = true
		out.notes = append(out.notes, r.listTruncationNote(q.k, shown, len(fold), len(rows), page.More, page.Remaining, large, in.Full))
	}
	out.next = r.listNextHints(q.k, listed, cause, page.Items)
	return out, nil
}

// listCauses diagnoses up to causesInList unhealthy rows, worst first, and
// returns the cause text of each by namespace/name. It reads logs and the
// like, so it is for the worst few only; pods that only restarted are not
// among them. Other pods of the same owner with the same problem get the same
// text, so that they still fold into one row.
func (r *Runner) listCauses(ctx context.Context, kind string, rows []shape.Row, items []unstructured.Unstructured, use map[string]map[string]containerUse, events *eventIndex, red *redact.Redactor) map[string]string {
	if !listDiagnosable(kind) {
		return nil
	}
	byKey := listByKey(items)
	out := map[string]string{}
	owners := map[string]listSharedCause{} // owner UID -> the cause of the pod that was diagnosed

	// The rows to diagnose are the first causesInList that have a cause, in the
	// order of the rows. Their diagnoses do not depend on each other, so the
	// next few are diagnosed at the same time. If a diagnosis finds no cause,
	// the next row takes its place in the next round: the rows diagnosed, and
	// the text of each, are the same as when they were diagnosed one at a time.
	cands := &listCandidates{kind: kind, rows: rows, byKey: byKey, seenOwner: map[string]bool{}}
	done := 0
	for done < causesInList {
		batch := cands.batch(causesInList - done)
		if len(batch) == 0 {
			break
		}
		results := make([]listDiagnosis, len(batch))
		fns := make([]func(), len(batch))
		for i := range batch {
			fns[i] = func() { results[i] = r.listDiagnose(ctx, kind, batch[i], use, events, red) }
		}
		parallel(fns...)
		for i, c := range batch {
			if len(results[i].causes) == 0 {
				continue
			}
			done++
			text := rowCause(results[i].prefix, redactAll(red, results[i].causes), deepCauseMax)
			out[c.row.Namespace+"/"+c.row.Name] = text
			if ref := controllerRef(c.u); ref != nil && kind == "Pod" && len(c.row.Problems) > 0 {
				owners[string(ref.UID)] = listSharedCause{shape.Template(c.row.Problems[0]), text}
			}
		}
	}
	listShareOwnerCauses(rows, byKey, owners, out)
	return out
}

// endpointCounts reads the EndpointSlices and counts ready and total
// endpoints per service. It returns nil when they cannot be read.
// more is true when the list was cut at the cap: a Service that has no slice
// in view is then not known to have none.
func (r *Runner) endpointCounts(ctx context.Context, ns string) (counts map[string][2]int, noPorts map[string]bool, more bool) {
	k, err := r.Kinds.Lookup("endpointslices")
	if err != nil {
		return nil, nil, false
	}
	page, err := r.Reader.List(ctx, k, ns, "", fetchCap)
	if err != nil {
		return nil, nil, false
	}
	out := map[string][2]int{}
	noPorts = map[string]bool{}
	portsOf := map[string]int{}
	for _, s := range page.Items {
		svc := s.GetLabels()["kubernetes.io/service-name"]
		if svc == "" {
			continue
		}
		key := s.GetNamespace() + "/" + svc
		c := out[key]
		eps, _, _ := unstructured.NestedSlice(s.Object, "endpoints")
		ps, _, _ := unstructured.NestedSlice(s.Object, "ports")
		portsOf[key] += len(ps)
		for _, e := range eps {
			em, _ := e.(map[string]any)
			c[1]++
			cond, _ := em["conditions"].(map[string]any)
			if ready, ok := cond["ready"].(bool); !ok || ready {
				c[0]++
			}
		}
		out[key] = c
	}
	for key, c := range out {
		if c[1] > 0 && portsOf[key] == 0 {
			noPorts[key] = true
		}
	}
	return out, noPorts, page.More
}

// listAllUnder is the size of a list that keeps a row for every object.
const listAllUnder = 8

// workloadName names the workload a pod belongs to: the owner, without the
// hash of a ReplicaSet or the run number of a Job.
func workloadName(u *unstructured.Unstructured) string {
	return workloadNameOf(u.GetName(), u.GetOwnerReferences())
}

// workloadNameOf is workloadName for a pod's name and owner references.
func workloadNameOf(name string, refs []metav1.OwnerReference) string {
	ref := controllerRefOf(refs)
	if ref == nil {
		return name
	}
	i := strings.LastIndexByte(ref.Name, '-')
	if i <= 0 {
		return ref.Name
	}
	switch ref.Kind {
	case "ReplicaSet":
		return ref.Name[:i]
	case "Job":
		if strings.Trim(ref.Name[i+1:], "0123456789") == "" {
			return ref.Name[:i]
		}
	}
	return ref.Name
}

const healthyNamesShown = 10

// healthyDetailMax is the room, in bytes, for the names of healthy workloads.
// Above it the summary counts each namespace instead.
const healthyDetailMax = 1500

// healthyNamespacesShown is how many namespaces the count form names.
const healthyNamespacesShown = 20

// healthySummary says in one or two lines what the healthy objects are. Pods
// count by workload, most numerous first; completed pods count apart.
func healthySummary(kind string, healthy []shape.Row, byKey map[string]*unstructured.Unstructured, withNS bool, nsShown int) string {
	if len(healthy) == 0 {
		return ""
	}
	running, done := listSplitCompleted(kind, healthy)
	var lines []string
	switch {
	case len(running) > 0 && withNS:
		lines = append(lines, listHealthyByNamespace(kind, running, byKey, nsShown)...)
	case len(running) > 0:
		lines = append(lines, fmt.Sprintf("healthy, not listed (%d): %s", len(running), listHealthyNames(kind, running, byKey, false)))
	}
	if len(done) > 0 {
		lines = append(lines, fmt.Sprintf("completed (%d): %s", len(done), listHealthyNames(kind, done, byKey, withNS)))
	}
	return strings.Join(lines, "\n")
}

var restartsOnlyRE = regexp.MustCompile(`^container [^:]+: \d+ restarts$`)

// restartOnly reports whether a pod row is a warning only because its
// containers restarted in the past: it runs and all its containers are ready.
func restartOnly(row shape.Row) bool {
	if row.State != shape.Warning || len(row.Problems) == 0 || len(row.Cells) < 3 || row.Cells[1] != "Running" {
		return false
	}
	have, want, ok := strings.Cut(row.Cells[0], "/")
	if !ok || have != want {
		return false
	}
	for _, p := range row.Problems {
		if !restartsOnlyRE.MatchString(p) {
			return false
		}
	}
	return true
}
