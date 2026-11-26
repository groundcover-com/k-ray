package tools

import (
	"fmt"

	"github.com/groundcover-com/kray/internal/shape"
)

// The sentences that describe says about a state, a problem or a cause are
// made here, apart from the code that decides to say them, so that the
// wording can be read and tested without the control flow.

const (
	// describeNoReadyEndpoints is the problem of a service with a selector and
	// no ready endpoint.
	describeNoReadyEndpoints = "service has no ready endpoints"
	// describeNoPortsSummary replaces the endpoint count when the
	// EndpointSlices list endpoints but no ports.
	describeNoPortsSummary = "endpoints: listed, but the EndpointSlices have no ports"
	// describeNoDisruptions is said of a disruption budget that forbids every
	// eviction by itself.
	describeNoDisruptions = "allows no disruptions by its own setting"
	// describeContainersUnread is the compact spec line when the pod template
	// cannot be read.
	describeContainersUnread = "containers: (could not be read)"
)

// describeStateLine is the first summary line of a description.
func describeStateLine(s shape.State) string {
	return "state: " + string(s)
}

// describeUsageMissing says why there are no usage lines.
func describeUsageMissing(err error) string {
	return "resources: usage not available (" + metricsReason(err) + ")"
}

// describeWorstPodCause is a cause of the worst pod of a workload, named
// after the pod.
func describeWorstPodCause(pod, cause string) string {
	return "pod/" + pod + ": " + cause
}

// describeFailedLogHeading heads the log of a failed container of a Job pod
// that has no previous instance.
func describeFailedLogHeading(container, pod string) string {
	return "last log lines of container " + container + " in the failed pod/" + pod + ":"
}

// describeCrashHeading heads the summary of the last crash of a container.
// note, when set, replaces the usual heading. pod is "" when the described
// object is the pod itself.
func describeCrashHeading(container, pod, note string) string {
	switch {
	case note != "":
		return note
	case pod != "":
		return fmt.Sprintf("last crash of container %s (previous instance of pod/%s):", container, pod)
	}
	return fmt.Sprintf("last crash of container %s (previous instance):", container)
}

// describeNotReadyLogHeading heads the current log of a running container
// that is not ready.
func describeNotReadyLogHeading(container string) string {
	return fmt.Sprintf("recent log of container %s (running, not ready):", container)
}

// describeNoEventsLine is the events section when there is no event to show:
// a failed read is not shown as "no events".
func describeNoEventsLine(err error) string {
	if err != nil {
		return "recent events: could not be read (" + failureReason(err) + ")"
	}
	return "recent events: none"
}

// describeYAMLCutNote says how much of the object YAML is shown and how to
// get the rest.
func (r *Runner) describeYAMLCutNote(shown, whole int) string {
	return fmt.Sprintf("object YAML cut: %s of %s bytes shown. For the whole object: %s get with %s.", shape.Thousands(shown), shape.Thousands(whole), r.verb(), r.arg("full", "true"))
}

// describeWorkloadOf names the pods that led to a workload.
func describeWorkloadOf(pods int) string {
	return fmt.Sprintf(" (the workload of %s)", plural(pods, "pod", "pods"))
}

// describeFoundSummary names the object that a name found.
func describeFoundSummary(t target) string {
	s := fmt.Sprintf("found by name: %s%s", t.ref(), inNS(t.u.GetNamespace()))
	if t.pods > 0 {
		s += describeWorkloadOf(t.pods)
	}
	return s
}

// describeNoObjectNamed is the summary when no object has the name.
func describeNoObjectNamed(name string) string {
	return fmt.Sprintf("no object named %s", name)
}

// describeLookedAtNote says where a search by name looked, so that "not
// found" is not read as "not in the cluster".
func describeLookedAtNote(kindsLooked, scope string) string {
	return fmt.Sprintf("looked at %s in %s, for the name and for names that start with it and a dash.", kindsLooked, scope)
}

// describePartialNote says that a list was cut, so a match can be missing.
func describePartialNote(n int, kind string) string {
	return fmt.Sprintf("only the first %s %s were read.", shape.Thousands(n), kind)
}

// describeMatchesSummary is the summary of an ambiguous name: how many
// objects match, and which are described in full.
func describeMatchesSummary(total int, name string, namespaces, shown, rest int) string {
	scope := ""
	if namespaces > 1 {
		scope = fmt.Sprintf(" in %d namespaces", namespaces)
	}
	described := "all are described below"
	if rest > 0 {
		described = fmt.Sprintf("%d are described below, and the other %d get one line each", shown, rest)
	}
	return fmt.Sprintf("found by name: %d objects match %s%s; %s (problems first)", total, name, scope, described)
}

// describeMatchesClosing says what tells the matches of a name apart.
func describeMatchesClosing(namespaces int) string {
	if namespaces > 1 {
		return "\nthese are different objects; the namespace decides which one is meant.\n"
	}
	return "\nthese are different objects; the kind decides which one is meant.\n"
}
