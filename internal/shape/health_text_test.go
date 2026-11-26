package shape

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestInspectTextSentences(t *testing.T) {
	waiting := &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull access denied"}
	crash := &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container"}
	last := &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}
	cases := []struct {
		name, got, want string
	}{
		{"rolling out", inspectTextRollingOut(2, 3, "available"), "rolling out: 2 of 3 available"},
		{"too few", inspectTextTooFew(0, 2, "pods ready"), "0 of 2 pods ready"},
		{"not progressing", inspectTextNotProgressing("ProgressDeadlineExceeded", "timed out"), "not progressing: ProgressDeadlineExceeded: timed out"},
		{"replica failure", inspectTextReplicaFailure("FailedCreate", "quota"), "replica failure: FailedCreate: quota"},
		{"job failed", inspectTextJobFailed("BackoffLimitExceeded", "too many"), "failed: BackoffLimitExceeded: too many"},
		{"failed pods", inspectTextFailedPodsSoFar(4), "4 failed pods so far"},
		{"at max", inspectTextAtMaxReplicas(10), "at max replicas (10)"},
		{"no disruptions", inspectTextNoDisruptions(1, 3), "no disruptions allowed (1 of 3 pods healthy)"},
		{"claim empty phase", inspectTextClaimNotBound(""), "claim is -"},
		{"claim pending", inspectTextClaimNotBound(corev1.ClaimPending), "claim is Pending"},
		{"node pressure", inspectTextNodePressure(corev1.NodeDiskPressure, "disk full"), "DiskPressure: disk full"},
		{"init label", inspectTextContainerLabel("setup", true), "init container setup"},
		{"label", inspectTextContainerLabel("app", false), "container app"},
		{"waiting bare", inspectTextContainerWaiting("container app", &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}, nil, 0), "container app: ErrImagePull"},
		{"waiting full", inspectTextContainerWaiting("container app", waiting, last, 2), "container app: ImagePullBackOff, last exit 137 (OOMKilled), 2 restarts: pull access denied"},
		{"crash loop drops message", inspectTextContainerWaiting("container app", crash, last, 5), "container app: CrashLoopBackOff, last exit 137 (OOMKilled), 5 restarts"},
		{"terminated", inspectTextContainerTerminated("container app", &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}), "container app: terminated, exit 1 (Error)"},
		{"oom", inspectTextOOMKilled("container app", 3), "container app: last restart was OOMKilled (3 restarts)"},
		{"not started", inspectTextNotStarted("5m", ""), "not started after 5m"},
		{"not started waiting", inspectTextNotStarted("5m", "ContainerCreating"), "not started after 5m (ContainerCreating)"},
		{"pod failed", inspectTextPodFailed(""), "pod failed"},
		{"pod failed why", inspectTextPodFailed("Evicted low memory"), "pod failed: Evicted low memory"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}
