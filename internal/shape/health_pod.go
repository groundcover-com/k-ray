package shape

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// inspectRestartHistory is how old a last termination must be before a ready
// pod no longer warns for it. One day covers a daily load cycle (a nightly job
// that fills the memory, for example); an older single event is history.
const inspectRestartHistory = 24 * time.Hour

func podRow(r *Row, p *corev1.Pod, now time.Time) {
	status := inspectPodPhase(r, p)
	podReady := inspectPodAllReady(p)
	ready, restarts := 0, int32(0)
	for _, cs := range p.Status.InitContainerStatuses {
		restarts += cs.RestartCount
		if w := inspectContainer(r, p, cs, true, podReady, now); w != "" {
			status = w
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
		if w := inspectContainer(r, p, cs, false, podReady, now); w != "" {
			status = w
		}
	}
	inspectPodNotStarted(r, p, now)
	inspectPodFailed(r, p)
	if p.DeletionTimestamp != nil {
		status = "Terminating"
		if now.Sub(p.DeletionTimestamp.Time) > 5*time.Minute {
			r.AddProblem(Warning, inspectTextTerminatingFor(Age(p.DeletionTimestamp.Time, now)))
		}
	}
	r.Cells = []string{fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers)), dash(status), fmt.Sprint(restarts), Age(p.CreationTimestamp.Time, now), dash(p.Spec.NodeName)}
}

// inspectPodPhase sets the state from the phase and returns the first STATUS
// cell; a waiting container or a deletion can replace it later.
func inspectPodPhase(r *Row, p *corev1.Pod) string {
	status := string(p.Status.Phase)
	if p.Status.Reason != "" {
		status = p.Status.Reason
	}
	r.State = Healthy
	switch p.Status.Phase {
	case corev1.PodFailed:
		r.State = Unhealthy // the reason line is added after the container checks
	case corev1.PodPending:
		r.State = Warning
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				// The preemption part repeats the node count and adds no reason.
				msg, _, _ := strings.Cut(c.Message, " preemption:")
				r.AddProblem(Unhealthy, inspectTextNotScheduled(msg))
			}
		}
	case corev1.PodSucceeded:
		status = "Completed"
	}
	return status
}

// inspectPodAllReady: a pod that is Running with every container ready, whose
// last termination is older than inspectRestartHistory, is not a warning for
// its restarts alone. The RESTARTS column still shows the count, and describe
// prints it on an "earlier:" line. A pod that is not ready, or is in a crash
// loop, keeps the warning.
func inspectPodAllReady(p *corev1.Pod) bool {
	ready := p.Status.Phase == corev1.PodRunning && len(p.Status.ContainerStatuses) > 0
	for _, cs := range p.Status.ContainerStatuses {
		ready = ready && cs.Ready
	}
	return ready
}

// inspectContainer adds the problems of one container. It returns the
// waiting reason when that reason replaces the pod's STATUS cell, or "".
func inspectContainer(r *Row, p *corev1.Pod, cs corev1.ContainerStatus, init, podReady bool, now time.Time) string {
	label := inspectTextContainerLabel(cs.Name, init)
	if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" && w.Reason != "PodInitializing" {
		r.AddProblem(Unhealthy, inspectTextContainerWaiting(label, w, cs.LastTerminationState.Terminated, cs.RestartCount))
		return w.Reason
	}
	if t := cs.State.Terminated; t != nil && t.ExitCode != 0 && !init {
		r.AddProblem(Unhealthy, inspectTextContainerTerminated(label, t))
		return ""
	}
	inspectContainerRestarts(r, cs, label, podReady, now)
	if !init && cs.State.Running != nil && !cs.Ready && p.Status.Phase == corev1.PodRunning {
		r.AddProblem(Warning, inspectTextRunningNotReady(label))
	}
	return ""
}

// inspectContainerRestarts warns for an OOM kill or for repeated restarts,
// unless the pod is ready and the last termination is history.
func inspectContainerRestarts(r *Row, cs corev1.ContainerStatus, label string, podReady bool, now time.Time) {
	t := cs.LastTerminationState.Terminated
	switch {
	case podReady && t != nil && !t.FinishedAt.IsZero() && now.Sub(t.FinishedAt.Time) > inspectRestartHistory:
		// history
	case t != nil && t.Reason == "OOMKilled":
		r.AddProblem(Warning, inspectTextOOMKilled(label, cs.RestartCount))
	case cs.RestartCount >= 3:
		r.AddProblem(Warning, inspectTextRestarts(label, cs.RestartCount))
	}
}

// inspectPodNotStarted warns for a pending pod that nothing else explains
// after two minutes, which is longer than a normal image pull and start.
func inspectPodNotStarted(r *Row, p *corev1.Pod, now time.Time) {
	if p.Status.Phase != corev1.PodPending || len(r.Problems) != 0 || now.Sub(p.CreationTimestamp.Time) <= 2*time.Minute {
		return
	}
	waiting := ""
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			waiting = w.Reason
			break
		}
	}
	r.AddProblem(Warning, inspectTextNotStarted(Age(p.CreationTimestamp.Time, now), waiting))
}

// inspectPodFailed adds the pod line of a failed pod. The containers' problems
// come first; they say why. The pod line is added when it has a reason, or
// when nothing else explains it.
func inspectPodFailed(r *Row, p *corev1.Pod) {
	if p.Status.Phase != corev1.PodFailed {
		return
	}
	why := strings.TrimSpace(p.Status.Reason + " " + p.Status.Message)
	if why != "" || len(r.Problems) == 0 {
		r.AddProblem(Unhealthy, inspectTextPodFailed(why))
	}
}
