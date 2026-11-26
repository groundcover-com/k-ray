package tools

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

const logLimitBytes = 4 << 20

const (
	// maxLogPods is how many pods one logs call reads for a workload or a
	// selector; maxLogParallel is how many reads run at once.
	maxLogPods     = 30
	maxLogParallel = 6
)

// perPodTail is how many lines to read from each of n pods: the default
// shrinks as the pod count grows, and an explicit tail is capped so that the
// whole read stays bounded.
func perPodTail(asked, n int) (tail int, capped bool) {
	if n <= 1 {
		if asked == 0 {
			return 2000, false
		}
		return asked, false
	}
	limit := max(100, 10000/n)
	if asked == 0 {
		return min(2000, max(100, 4000/n)), false
	}
	if asked > limit {
		return limit, true
	}
	return asked, false
}

// logTarget is one pod to read.
type logTarget struct {
	pod       *unstructured.Unstructured
	container string
	raw       string
	err       error
}

// logsCall is the state of one logs call as it passes from step to step. The
// output is kept here because an error return still carries the namespace
// that the audit line records.
type logsCall struct {
	in      LogsInput
	podKind kinds.Kind
	since   time.Duration
	mt      *matcher
	out     output

	ns     string
	scope  string   // what was asked for, said in the title
	total  int      // pods that match
	header []string // said first: where the object was found
	// pod is the pod that the call named, when one has the name; nil when
	// the pods come from a workload or a selector.
	pod *unstructured.Unstructured

	targets       []*logTarget
	allContainers [][]string
	tail          int
	capped        bool
}

// setNamespace moves the call to the namespace where the object was found.
func (c *logsCall) setNamespace(ns string) {
	c.ns = ns
	c.out.namespace = ns
}

func (r *Runner) logs(ctx context.Context, in LogsInput, red *redact.Redactor) (output, error) {
	c, err := r.logsCheckInput(in)
	if err != nil {
		return output{}, err
	}
	if early, err := r.logsChooseTargets(ctx, c); err != nil {
		return c.out, err
	} else if early != nil {
		return *early, nil
	}
	if err := c.chooseContainers(); err != nil {
		return c.out, err
	}
	r.logsReadAll(ctx, c)
	entries, st, err := c.collect(red)
	if err != nil {
		return c.out, err
	}
	r.logsHeading(c, st)
	if len(entries) == 0 {
		c.out.body = "no log lines"
		return c.out, nil
	}
	if !r.logsBody(c, entries, st) {
		return c.out, nil
	}
	r.logsCutNotes(c, st)
	return c.out, nil
}

// logsCheckInput checks the arguments before any read of the cluster.
func (r *Runner) logsCheckInput(in LogsInput) (*logsCall, error) {
	given := 0
	for _, s := range []string{in.Pod, in.Workload, in.LabelSelector} {
		if s != "" {
			given++
		}
	}
	if given != 1 {
		return nil, badInput("give exactly one of pod, workload (for example deployment/NAME) or labelSelector")
	}
	if in.Pod != "" {
		if err := objectName("pod", in.Pod); err != nil {
			return nil, err
		}
	}
	podKind, err := r.kind("pods")
	if err != nil {
		return nil, err
	}
	if in.AllNamespaces && in.LabelSelector == "" {
		return nil, badInput("allNamespaces works with labelSelector only")
	}
	ns, err := r.namespaceFor(podKind, in.Namespace, in.AllNamespaces)
	if err != nil {
		return nil, err
	}
	if in.Container != "" {
		if err := labelName("container", in.Container); err != nil {
			return nil, err
		}
	}
	since, err := duration("since", in.Since)
	if err != nil {
		return nil, err
	}
	if _, err := bounded("tail", in.Tail, 2000, 1, 5000); err != nil {
		return nil, err
	}
	mt, err := parseMatch(in.Match)
	if err != nil {
		return nil, err
	}
	return &logsCall{
		in: in, podKind: podKind, since: since, mt: mt,
		out: output{namespace: ns, full: in.Full},
		ns:  ns, scope: "pod " + in.Pod, total: 1,
	}, nil
}

func containerNames(pod *unstructured.Unstructured) (regular, init []string) {
	for _, key := range []string{"containers", "initContainers"} {
		cs, _, _ := unstructured.NestedSlice(pod.Object, "spec", key)
		for _, c := range cs {
			if m, ok := c.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					if key == "containers" {
						regular = append(regular, n)
					} else {
						init = append(init, n)
					}
				}
			}
		}
	}
	return regular, init
}

func defaultContainer(pod *unstructured.Unstructured, containers []string) string {
	// The annotation is free text that anyone who can edit the pod writes.
	// Use it only when it names one of the pod's containers.
	if d := pod.GetAnnotations()["kubectl.kubernetes.io/default-container"]; d != "" {
		for _, c := range containers {
			if c == d {
				return d
			}
		}
	}
	if len(containers) > 0 {
		return containers[0]
	}
	return ""
}

func restarted(pod *unstructured.Unstructured, container string) bool {
	sts, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	for _, s := range sts {
		m, _ := s.(map[string]any)
		if m["name"] == container {
			if nestedCount(m, "restartCount") > 0 {
				return true
			}
		}
	}
	return false
}

// redactEntries redacts the lines of log entries as one sequence (the head
// line and the continuation lines of each entry, in order), so that a key
// block that spans lines is masked whole.
func redactEntries(red *redact.Redactor, es []shape.LogEntry) {
	var ptrs []*string
	var lines []string
	for i := range es {
		ptrs = append(ptrs, &es[i].Text)
		lines = append(lines, es[i].Text)
		for j := range es[i].More {
			ptrs = append(ptrs, &es[i].More[j])
			lines = append(lines, es[i].More[j])
		}
	}
	for i, l := range red.Lines(lines) {
		*ptrs[i] = l
	}
}

// readFailure says in fixed words why a read failed. The text of the error
// comes from the cluster and is not printed outside the fence.
func readFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the cluster did not answer in time"
	case errors.Is(err, context.Canceled):
		return "the call was stopped"
	case errors.Is(err, errInternal):
		return "the read stopped on an internal error"
	case apierrors.IsNotFound(err):
		return "for example: the container was not found in the pod"
	case apierrors.IsForbidden(err):
		return "for example: the cluster does not allow this read"
	case errors.Is(err, kube.ErrUnauthorized):
		return "the cluster rejected the credentials"
	}
	return "the read of the log failed; read one pod to see the error"
}
