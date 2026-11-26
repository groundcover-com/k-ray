package shape

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// The problem sentences of Inspect. They are kept apart from the checks that
// decide when to add them, so a change to the wording cannot change a decision
// and a test can read each sentence alone. Row.problem joins white space, so
// these return the text as it is assembled.

func inspectTextRollingOut(have, want int32, adjective string) string {
	return fmt.Sprintf("rolling out: %d of %d %s", have, want, adjective)
}

// inspectTextTooFew: what is for example "replicas available" or "pods ready".
func inspectTextTooFew(have, want int32, what string) string {
	return fmt.Sprintf("%d of %d %s", have, want, what)
}

func inspectTextNotProgressing(reason, message string) string {
	return fmt.Sprintf("not progressing: %s: %s", reason, message)
}

func inspectTextReplicaFailure(reason, message string) string {
	return fmt.Sprintf("replica failure: %s: %s", reason, message)
}

func inspectTextRolloutPaused() string { return "rollout paused" }

func inspectTextMisscheduled(n int32) string {
	return fmt.Sprintf("%d pods misscheduled", n)
}

func inspectTextJobFailed(reason, message string) string {
	return fmt.Sprintf("failed: %s: %s", reason, message)
}

func inspectTextFailedPodsSoFar(n int32) string {
	return fmt.Sprintf("%d failed pods so far", n)
}

func inspectTextSuspended() string { return "suspended" }

func inspectTextAtMaxReplicas(maxReplicas int32) string {
	return fmt.Sprintf("at max replicas (%d)", maxReplicas)
}

func inspectTextNoDisruptions(healthy, expected int32) string {
	return fmt.Sprintf("no disruptions allowed (%d of %d pods healthy)", healthy, expected)
}

func inspectTextClaimNotBound(phase corev1.PersistentVolumeClaimPhase) string {
	return "claim is " + dash(string(phase))
}

func inspectTextVolumeFailed(message string) string {
	return "volume failed: " + message
}

func inspectTextNamespaceTerminating() string { return "terminating" }

func inspectTextNodeNotReady(reason, message string) string {
	return fmt.Sprintf("not ready: %s %s", reason, message)
}

func inspectTextNodePressure(t corev1.NodeConditionType, message string) string {
	return fmt.Sprintf("%s: %s", t, message)
}

func inspectTextCordoned() string { return "cordoned (unschedulable)" }

func inspectTextNotScheduled(message string) string {
	return "not scheduled: " + message
}

func inspectTextContainerLabel(name string, init bool) string {
	if init {
		return "init container " + name
	}
	return "container " + name
}

// inspectTextContainerWaiting leaves out the waiting message of a crash loop:
// it only names the back-off and the container again, which the line already
// says.
func inspectTextContainerWaiting(label string, w *corev1.ContainerStateWaiting, last *corev1.ContainerStateTerminated, restarts int32) string {
	msg := label + ": " + w.Reason
	if last != nil {
		msg += fmt.Sprintf(", last exit %d (%s)", last.ExitCode, last.Reason)
	}
	if restarts > 0 {
		msg += fmt.Sprintf(", %d restarts", restarts)
	}
	if w.Message != "" && w.Reason != "CrashLoopBackOff" {
		msg += ": " + w.Message
	}
	return msg
}

func inspectTextContainerTerminated(label string, t *corev1.ContainerStateTerminated) string {
	return fmt.Sprintf("%s: terminated, exit %d (%s)", label, t.ExitCode, t.Reason)
}

func inspectTextOOMKilled(label string, restarts int32) string {
	return fmt.Sprintf("%s: last restart was OOMKilled (%d restarts)", label, restarts)
}

func inspectTextRestarts(label string, restarts int32) string {
	return fmt.Sprintf("%s: %d restarts", label, restarts)
}

func inspectTextRunningNotReady(label string) string {
	return label + ": running but not ready"
}

// inspectTextNotStarted: waiting is a container's waiting reason, or empty.
func inspectTextNotStarted(age, waiting string) string {
	if waiting == "" {
		return "not started after " + age
	}
	return "not started after " + age + " (" + waiting + ")"
}

// inspectTextPodFailed: why is the pod's reason and message, or empty.
func inspectTextPodFailed(why string) string {
	if why == "" {
		return "pod failed"
	}
	return "pod failed: " + why
}

func inspectTextTerminatingFor(age string) string {
	return "terminating for " + age
}
