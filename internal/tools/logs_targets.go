package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/shape"
)

// logsChooseTargets finds the pods to read: the named pod, or the pods of a
// workload or a selector. A non-nil output ends the call with that output, for
// example when a name is in more than one namespace.
func (r *Runner) logsChooseTargets(ctx context.Context, c *logsCall) (*output, error) {
	workload := c.in.Workload
	var preW *unstructured.Unstructured
	if c.in.Pod != "" {
		res, early, err := r.locateLogPod(ctx, c.podKind, c.ns, c.in)
		if err != nil {
			return nil, err
		}
		if early != nil {
			return early, nil
		}
		c.pod, preW, workload = res.pod, res.workload, res.workloadRef
		if res.header != "" {
			c.header = []string{res.header}
			c.setNamespace(res.ns)
		}
	}
	if c.pod != nil {
		c.targets = []*logTarget{{pod: c.pod}}
		return nil, nil
	}
	var sel string
	if workload != "" {
		var early *output
		var err error
		sel, early, err = r.logsWorkloadSelector(ctx, c, workload, preW)
		if err != nil || early != nil {
			return early, err
		}
	} else {
		var err error
		if sel, err = selector(c.in.LabelSelector); err != nil {
			return nil, err
		}
		c.scope = "selector " + sel
	}
	return r.logsListPods(ctx, c, sel)
}

// logsWorkloadSelector returns the pod selector of the workload, given as
// kind/name. found is the workload when the name lookup already read it.
func (r *Runner) logsWorkloadSelector(ctx context.Context, c *logsCall, workload string, found *unstructured.Unstructured) (string, *output, error) {
	kindName, name, ok := strings.Cut(workload, "/")
	if !ok {
		return "", nil, badInput("workload must look like deployment/NAME")
	}
	wk, err := r.kind(kindName)
	if err != nil {
		return "", nil, err
	}
	switch wk.Kind() {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
	default:
		return "", nil, badInput("workload kind must be deployment, statefulset, daemonset, job or replicaset")
	}
	if err := objectName("workload name", name); err != nil {
		return "", nil, err
	}
	w := found
	if w == nil {
		loc, err := r.locateNamed(ctx, wk, c.ns, c.in.Namespace != "", name)
		if err != nil {
			return "", nil, err
		}
		switch {
		case len(loc.matches) > 1:
			items := make([]ambiguousItem, len(loc.matches))
			for i, m := range loc.matches {
				items[i] = ambiguousItem{wk, m}
			}
			title := fmt.Sprintf("logs %s %s · all namespaces · context %s", wk.Singular(), name, r.Context)
			out := r.ambiguous(title, name, items, func(it ambiguousItem) string {
				return "logs workload=" + wk.Singular() + "/" + name + r.nsPart(it.u.GetNamespace())
			})
			return "", &out, nil
		case loc.obj == nil:
			return "", nil, loc.notFound(wk, name)
		}
		w = loc.obj
		if loc.header != "" {
			c.header = []string{loc.header}
			c.setNamespace(w.GetNamespace())
		}
	}
	sel := workloadSelector(w)
	if sel == "" {
		return "", nil, badInput("%s/%s has no pod selector", wk.Singular(), name)
	}
	c.scope = wk.Singular() + " " + name
	return sel, nil, nil
}

// logsListPods lists the pods of the selector and keeps the first
// maxLogPods, the ones with problems first. When no pod matches, the
// returned output ends the call.
func (r *Runner) logsListPods(ctx context.Context, c *logsCall, sel string) (*output, error) {
	page, err := r.Reader.List(ctx, c.podKind, c.ns, sel, 500)
	if err != nil {
		return nil, err
	}
	items := page.Items
	logsSortProblemsFirst(items, r.now())
	c.total = len(items)
	for i := range items {
		if len(c.targets) == maxLogPods {
			break
		}
		c.targets = append(c.targets, &logTarget{pod: &items[i]})
	}
	if len(c.targets) == 0 {
		c.out.title = fmt.Sprintf("logs %s%s · context %s", c.scope, inNS(c.ns), r.Context)
		c.out.summary = c.header
		c.out.body = "no pods match"
		return &c.out, nil
	}
	return nil, nil
}

// logsSortProblemsFirst puts unhealthy pods first, then pods with a warning,
// then healthy ones, each in natural name order, so that a cut at maxLogPods
// keeps the pods most likely to explain a problem.
func logsSortProblemsFirst(items []unstructured.Unstructured, now time.Time) {
	rank := func(u *unstructured.Unstructured) int {
		switch shape.Inspect(u, now).State {
		case shape.Unhealthy:
			return 0
		case shape.Warning:
			return 1
		}
		return 2
	}
	sort.SliceStable(items, func(i, j int) bool {
		if a, b := rank(&items[i]), rank(&items[j]); a != b {
			return a < b
		}
		return shape.NaturalLess(items[i].GetName(), items[j].GetName())
	})
}

// chooseContainers sets the container of each target and the tail per pod. A
// container that a pod does not have is an error for a one-pod call, and a
// failed read of that pod when there are more.
func (c *logsCall) chooseContainers() error {
	c.tail, c.capped = perPodTail(c.in.Tail, len(c.targets))
	for _, t := range c.targets {
		containers, initContainers := containerNames(t.pod)
		c.allContainers = append(c.allContainers, containers)
		t.container = c.in.Container
		if t.container == "" {
			t.container = defaultContainer(t.pod, containers)
		}
		if logsHasContainer(t.container, containers, initContainers) {
			continue
		}
		if len(c.targets) == 1 {
			return badInput("pod %s has no container %q; containers: %s", t.pod.GetName(), t.container, strings.Join(append(containers, initContainers...), ", "))
		}
		t.err = fmt.Errorf("no container %q", t.container)
	}
	return nil
}

func logsHasContainer(name string, containers, initContainers []string) bool {
	for _, c := range append(append([]string(nil), containers...), initContainers...) {
		if c == name {
			return true
		}
	}
	return false
}
