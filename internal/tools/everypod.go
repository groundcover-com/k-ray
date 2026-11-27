package tools

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/groundcover-com/kray/internal/shape"
)

// A workload whose own status counts every replica available can still be
// broken. A pod in a restart loop is Ready between two kills, so the
// controller counts it as available. When every pod of such a workload has a
// problem, the workload has one too. One pod with a problem among healthy
// ones is not enough: the others serve, and describe says what that pod did
// on its "earlier:" line.

// everyPodBadApplies reports whether the rule applies to a kind: the
// workloads whose pods are meant to run without end. The pods of a Job end by
// design, and a CronJob has no pods of its own.
func everyPodBadApplies(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		return true
	}
	return false
}

// everyPodBad reports whether a workload of this kind, with bad of its total
// pods having a problem, is a warning by the rule.
func everyPodBad(kind string, bad, total int) bool {
	return everyPodBadApplies(kind) && total > 0 && bad == total
}

// everyPodBadText is the problem of a workload whose every pod has one.
func everyPodBadText(total int) string {
	if total == 1 {
		return "its only pod has a problem"
	}
	return fmt.Sprintf("every pod has a problem (%d of %d)", total, total)
}

// listPod is what a list of workloads keeps of one pod.
type listPod struct {
	labels labels.Set
	bad    bool
}

// listPods are the pods that a list of workloads judges each workload by,
// by namespace. ok is false when they could not be read, or the read was cut:
// a workload whose pods are not all in view keeps the state of its own
// status.
type listPods struct {
	byNS map[string][]listPod
	ok   bool
}

// listWorkloadPods reads the pods of the namespace ("" for all) and judges
// each one.
func (r *Runner) listWorkloadPods(ctx context.Context, ns string) listPods {
	pk, err := r.Kinds.Lookup("pods")
	if err != nil {
		return listPods{}
	}
	page, err := r.Reader.List(ctx, pk, ns, "", fetchCap)
	if err != nil || page.More {
		return listPods{}
	}
	now := r.now()
	out := listPods{byNS: map[string][]listPod{}, ok: true}
	for i := range page.Items {
		p := &page.Items[i]
		st := shape.Inspect(p, now).State
		out.byNS[p.GetNamespace()] = append(out.byNS[p.GetNamespace()], listPod{
			labels: labels.Set(p.GetLabels()),
			bad:    st == shape.Unhealthy || st == shape.Warning,
		})
	}
	return out
}

// listEveryPodBad adds the problem of a workload row that its own status
// calls healthy, when every one of its pods has a problem.
func listEveryPodBad(row *shape.Row, u *unstructured.Unstructured, pods listPods) {
	if !pods.ok || row.State != shape.Healthy || !everyPodBadApplies(u.GetKind()) {
		return
	}
	sel, ok := workloadSelectorOf(u)
	if !ok {
		return
	}
	bad, total := 0, 0
	for _, p := range pods.byNS[u.GetNamespace()] {
		if sel.Matches(p.labels) {
			total++
			if p.bad {
				bad++
			}
		}
	}
	if everyPodBad(u.GetKind(), bad, total) {
		row.AddProblem(shape.Warning, everyPodBadText(total))
	}
}
