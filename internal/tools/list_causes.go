package tools

import (
	"context"
	"maps"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// listRowCauses returns the cause text of the rows that get one, by
// namespace/name. The cause goes on the row of the object, so that any text
// filter that keeps the row keeps the cause. Pods get a light cause from the
// pod, its events (read once for the whole list) and its usage; the worst few
// get the deeper cause, which also reads logs.
func (r *Runner) listRowCauses(ctx context.Context, kind string, rows []shape.Row, items []unstructured.Unstructured, byKey map[string]*unstructured.Unstructured, use map[string]map[string]containerUse, counts map[shape.State]int, reads *listReads, red *redact.Redactor) map[string]string {
	var events *eventIndex
	cause := map[string]string{}
	if kind == "Pod" && counts[shape.Unhealthy]+counts[shape.Warning] > 0 {
		events = reads.events.wait()
		maps.Copy(cause, r.listLightCauses(ctx, rows, byKey, use, events, red))
	}
	maps.Copy(cause, r.listCauses(ctx, kind, rows, items, use, events, red))
	return cause
}

// listLightCauses diagnoses every pod row with a problem from what the list
// already read.
func (r *Runner) listLightCauses(ctx context.Context, rows []shape.Row, byKey map[string]*unstructured.Unstructured, use map[string]map[string]containerUse, events *eventIndex, red *redact.Redactor) map[string]string {
	out := map[string]string{}
	for _, row := range rows {
		if row.State != shape.Unhealthy && row.State != shape.Warning {
			continue
		}
		key := row.Namespace + "/" + row.Name
		u := byKey[key]
		p, ok := toPod(u)
		if u == nil || !ok {
			continue
		}
		if cs := r.lightPodCauses(ctx, p, use[key], events.groups(r, u)); len(cs) > 0 {
			out[key] = rowCause("", redactAll(red, cs), rowCauseMax)
		}
	}
	return out
}

// listDiagnosable reports whether listCauses has a deep diagnosis for a kind.
func listDiagnosable(kind string) bool {
	switch kind {
	case "Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "Service":
		return true
	}
	return false
}

// listCandidate is a row that listCauses may diagnose.
type listCandidate struct {
	row shape.Row
	u   *unstructured.Unstructured
}

// listCandidates hands out the rows to diagnose in the order of the rows: the
// ones with a problem, not only restarted, one for each owner.
type listCandidates struct {
	kind      string
	rows      []shape.Row
	byKey     map[string]*unstructured.Unstructured
	next      int
	seenOwner map[string]bool
}

func (c *listCandidates) take() (listCandidate, bool) {
	for c.next < len(c.rows) {
		row := c.rows[c.next]
		c.next++
		// Rows are sorted worst first, with pods that only restarted last,
		// so the first row that is neither ends the candidates.
		if (row.State != shape.Unhealthy && row.State != shape.Warning) || (c.kind == "Pod" && restartOnly(row)) {
			c.next = len(c.rows)
			return listCandidate{}, false
		}
		u := c.byKey[row.Namespace+"/"+row.Name]
		if u == nil {
			continue
		}
		if ref := controllerRef(u); ref != nil {
			if c.seenOwner[string(ref.UID)] {
				continue
			}
			c.seenOwner[string(ref.UID)] = true
		}
		return listCandidate{row, u}, true
	}
	return listCandidate{}, false
}

// batch takes up to n candidates.
func (c *listCandidates) batch(n int) []listCandidate {
	var b []listCandidate
	for len(b) < n {
		x, ok := c.take()
		if !ok {
			break
		}
		b = append(b, x)
	}
	return b
}

// listDiagnosis is the deep cause of one row. prefix names the pod a workload
// cause comes from.
type listDiagnosis struct {
	causes []string
	prefix string
}

func (r *Runner) listDiagnose(ctx context.Context, kind string, c listCandidate, use map[string]map[string]containerUse, events *eventIndex, red *redact.Redactor) listDiagnosis {
	row, u := c.row, c.u
	switch kind {
	case "Service":
		causes := r.serviceCause(ctx, u, row.Namespace)
		if len(causes) == 0 {
			causes, _ = r.servicePortMismatch(ctx, u, row.Namespace)
		}
		return listDiagnosis{causes: causes}
	case "Pod":
		// Events came with the list; they are read again only when the
		// list could not hold them all.
		var groups []eventGroup
		if events != nil && events.complete {
			groups = events.groups(r, u)
		}
		return listDiagnosis{causes: r.diagnosePod(ctx, u, use[row.Namespace+"/"+row.Name], groups, red).causes}
	}
	if w, ok := r.diagnoseWorkload(ctx, u, row.Namespace, red); ok && w.worst != "" {
		return listDiagnosis{causes: w.diagnose.causes, prefix: "pod/" + w.worst + ": "}
	}
	return listDiagnosis{}
}

// listSharedCause is the cause of a diagnosed pod, kept for the other pods of
// its owner that have the same problem.
type listSharedCause struct{ problem, text string }

// listShareOwnerCauses gives the other pods of a diagnosed owner, with the
// same problem, the same text, so that they still fold into one row.
func listShareOwnerCauses(rows []shape.Row, byKey map[string]*unstructured.Unstructured, owners map[string]listSharedCause, out map[string]string) {
	if len(owners) == 0 {
		return
	}
	for _, row := range rows {
		u := byKey[row.Namespace+"/"+row.Name]
		if u == nil || len(row.Problems) == 0 || out[row.Namespace+"/"+row.Name] != "" {
			continue
		}
		if ref := controllerRef(u); ref != nil {
			if sh, ok := owners[string(ref.UID)]; ok && sh.problem == shape.Template(row.Problems[0]) {
				out[row.Namespace+"/"+row.Name] = sh.text
			}
		}
	}
}
