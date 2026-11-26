package shape

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInspectContainerRestarts(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	term := func(reason string, ago time.Duration) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, FinishedAt: metav1.NewTime(now.Add(-ago))}}
	}
	cases := []struct {
		name     string
		last     corev1.ContainerState
		restarts int32
		podReady bool
		want     []string
	}{
		{"few restarts", corev1.ContainerState{}, 2, false, nil},
		{"many restarts", corev1.ContainerState{}, 3, false, []string{"container app: 3 restarts"}},
		{"oom", term("OOMKilled", time.Hour), 1, true, []string{"container app: last restart was OOMKilled (1 restarts)"}},
		{"old oom on a ready pod is history", term("OOMKilled", 25*time.Hour), 7, true, nil},
		{"old oom on a pod that is not ready", term("OOMKilled", 25*time.Hour), 1, false, []string{"container app: last restart was OOMKilled (1 restarts)"}},
	}
	for _, c := range cases {
		r := Row{State: Healthy}
		cs := corev1.ContainerStatus{Name: "app", RestartCount: c.restarts, LastTerminationState: c.last}
		inspectContainerRestarts(&r, cs, "container app", c.podReady, now)
		if !reflect.DeepEqual(r.Problems, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, r.Problems, c.want)
		}
	}
}

func TestInspectPodPhase(t *testing.T) {
	cases := []struct {
		name       string
		status     corev1.PodStatus
		wantStatus string
		wantState  State
		wantProbs  []string
	}{
		{"running", corev1.PodStatus{Phase: corev1.PodRunning}, "Running", Healthy, nil},
		{"reason wins", corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}, "Evicted", Unhealthy, nil},
		{"succeeded", corev1.PodStatus{Phase: corev1.PodSucceeded, Reason: "X"}, "Completed", Healthy, nil},
		{"unschedulable", corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Message: "0/3 nodes are available: 3 Insufficient cpu. preemption: 0/3 nodes are available",
		}}}, "Pending", Unhealthy, []string{"not scheduled: 0/3 nodes are available: 3 Insufficient cpu."}},
	}
	for _, c := range cases {
		r := Row{State: NoHealth}
		got := inspectPodPhase(&r, &corev1.Pod{Status: c.status})
		if got != c.wantStatus || r.State != c.wantState || !reflect.DeepEqual(r.Problems, c.wantProbs) {
			t.Errorf("%s: got %q %s %q, want %q %s %q", c.name, got, r.State, r.Problems, c.wantStatus, c.wantState, c.wantProbs)
		}
	}
}

func TestInspectPodFailed(t *testing.T) {
	cases := []struct {
		name   string
		status corev1.PodStatus
		before []string
		want   []string
	}{
		{"not failed", corev1.PodStatus{Phase: corev1.PodRunning}, nil, nil},
		{"no reason, nothing else", corev1.PodStatus{Phase: corev1.PodFailed}, nil, []string{"pod failed"}},
		{"no reason, a container explains it", corev1.PodStatus{Phase: corev1.PodFailed}, []string{"x"}, []string{"x"}},
		{"reason", corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", Message: "low memory"}, []string{"x"}, []string{"x", "pod failed: Evicted low memory"}},
	}
	for _, c := range cases {
		r := Row{State: Unhealthy, Problems: append([]string(nil), c.before...)}
		inspectPodFailed(&r, &corev1.Pod{Status: c.status})
		if !reflect.DeepEqual(r.Problems, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, r.Problems, c.want)
		}
	}
}

func TestInspectNodeRoles(t *testing.T) {
	got := inspectNodeRoles(map[string]string{
		"node-role.kubernetes.io/worker":        "",
		"node-role.kubernetes.io/control-plane": "",
		"node-role.kubernetes.io/":              "",
		"kubernetes.io/hostname":                "n1",
	})
	if want := []string{"control-plane", "worker"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
