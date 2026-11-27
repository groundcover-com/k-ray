package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// describeParts is what the reads of one description found. The steps of
// describeObj fill it and the render writes it out, so that each step is a
// function that can be read and tested alone.
type describeParts struct {
	k     kinds.Kind
	ns    string
	name  string
	u     *unstructured.Unstructured
	row   shape.Row
	now   time.Time
	brief bool
	red   *redact.Redactor

	state    string // the state line of the summary
	problems []string
	causes   []string

	resources []string        // usage now, from the metrics API
	usageNote string          // said once when the metrics API is missing
	specOnly  []string        // requests and limits, when there is no usage
	hasUsage  map[string]bool // containers that a usage line describes

	summary string             // compact spec line, in place of the object YAML
	cs      []corev1.Container // containers of the pod or of the pod template, for summary
	hasCS   bool
	csBad   bool
	mounts  string

	crash     []string
	crashOf   string // container whose previous instance is summarized
	crashPod  string // the workload's pod whose crash is shown
	crashNote string // the heading of those lines, when it is not the usual one
	logOf     string
	logLines  []string

	firstBadPod string                     // for the next: line of a workload
	badPod      *unstructured.Unstructured // the pod that a short block reads its cause from
	podSummary  string
	podLines    []string
	earlierPod  string   // what a healthy pod did earlier
	earlierWork string   // what a healthy workload did earlier
	revLines    []string // what changed in the revision
	sibLines    []string // how failing pods differ
}

// describeEvents is the result of the read of the events about the object.
// The error is kept so that a failed read is not shown as "no events".
type describeEvents struct {
	groups  []eventGroup
	evCount int
	err     error
}

// describeReads are the reads that do not depend on the kind-specific reads:
// the events about the object, its owner chain (a chain of reads, each of
// which needs the one before) and the objects around it. They run while the
// kind-specific reads run.
type describeReads struct {
	events *future[describeEvents]
	owners *future[[]string]
	rel    *future[related]
}

// describeRead returns the object: pre when the caller has read it already.
func (r *Runner) describeRead(ctx context.Context, k kinds.Kind, ns, name string, pre *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if pre != nil {
		return pre, nil
	}
	return r.Reader.Get(ctx, k, ns, name)
}

// describeNewParts inspects the object. A CronJob reads its Jobs here, before
// the other reads start, as the result of its last run is part of the problems.
func (r *Runner) describeNewParts(ctx context.Context, k kinds.Kind, ns, name string, u *unstructured.Unstructured, red *redact.Redactor, brief bool) *describeParts {
	now := r.now()
	row := shape.Inspect(u, now)
	d := &describeParts{
		k: k, ns: ns, name: name, u: u, row: row, now: now, brief: brief, red: red,
		state:    describeStateLine(row.State),
		problems: row.Lines(r.rt),
		hasUsage: map[string]bool{},
	}
	if k.Kind() == "CronJob" {
		d.problems = r.cronResultKnown(ctx, u, ns, d.problems)
	}
	return d
}

// describeStartReads starts the reads that do not depend on each other. The
// goroutines get copies of the fields, as the caller changes d meanwhile.
func (r *Runner) describeStartReads(ctx context.Context, d *describeParts) describeReads {
	k, u, ns, now, red, brief := d.k, d.u, d.ns, d.now, d.red, d.brief
	return describeReads{
		events: start(ctx, func(c context.Context) describeEvents {
			g, n, err := r.objectEvents(c, u, ns)
			return describeEvents{g, n, err}
		}),
		owners: start(ctx, func(c context.Context) []string { return r.ownerChain(c, u, now) }),
		rel:    start(ctx, func(c context.Context) related { return r.related(c, k, u, ns, red, brief) }),
	}
}

// drop stops the reads that are not needed, last started first.
func (rd describeReads) drop() {
	rd.rel.drop()
	rd.owners.drop()
	rd.events.drop()
}

// describeKindParts runs the reads and the diagnosis that depend on the kind.
func (r *Runner) describeKindParts(ctx context.Context, d *describeParts, events *future[describeEvents]) {
	switch d.k.Kind() {
	case "Pod":
		r.describePodParts(ctx, d, events)
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
		r.describeWorkloadParts(ctx, d)
	case "Service":
		r.describeServiceParts(ctx, d)
	}
}

// describePodParts reads the usage of a pod and diagnoses it. A pod with no
// problem has no likely cause: what it did earlier (a restart long ago) is
// one line of history.
func (r *Runner) describePodParts(ctx context.Context, d *describeParts, events *future[describeEvents]) {
	p, ok := toPod(d.u)
	if !ok {
		return
	}
	d.cs, d.hasCS = p.Spec.Containers, true
	d.badPod = d.u
	key := d.ns + "/" + p.Name
	use, have, useErr := r.podUsageRead(ctx, d.ns, labels.SelectorFromSet(labels.Set(p.Labels)).String())
	if have {
		d.resources = resourceLines(p, use[key])
		for c := range use[key] {
			d.hasUsage[c] = true
		}
	} else {
		d.usageNote = describeUsageMissing(useErr)
		d.specOnly = specLines(p)
	}
	if d.row.State == shape.Healthy {
		d.earlierPod = podEarlier(p, d.now)
	} else {
		pd := r.diagnosePod(ctx, d.u, use[key], groupsOrEmpty(events.wait().groups), d.red)
		d.causes, d.crashOf, d.crash = pd.causes, pd.crashOf, pd.crash
		d.logOf, d.logLines = pd.logOf, pd.log
	}
	d.mounts = mountsLine(p)
}

// describeWorkloadParts reads the pods of a workload and diagnoses the worst
// one. A healthy workload gets no likely cause and no crash block.
func (r *Runner) describeWorkloadParts(ctx context.Context, d *describeParts) {
	if spec, ok := templatePodSpec(d.u); ok {
		d.cs, d.hasCS = spec.Containers, true
	} else {
		d.csBad = true
	}
	d.mounts = templateMountsLine(d.u)
	w, ok := r.diagnoseWorkload(ctx, d.u, d.ns, d.red)
	if !ok {
		return
	}
	d.podSummary, d.podLines, d.firstBadPod = w.summary, w.lines, w.worst
	d.badPod = w.worstPod
	if w.everyPodBad && d.row.State == shape.Healthy {
		d.row.AddProblem(shape.Warning, everyPodBadText(len(w.pods)))
		d.state, d.problems = describeStateLine(d.row.State), d.row.Lines(r.rt)
	}
	if !d.brief && !w.unread {
		if d.k.Kind() == "Deployment" {
			d.revLines = r.revisionDiff(ctx, d.u, d.ns, w, d.red)
		}
		d.sibLines = r.siblingDiff(w.pods, d.k.Kind() == "StatefulSet")
	}
	if d.row.State == shape.Healthy {
		d.earlierWork = workloadEarlier(d.k.Kind(), d.u, w.pods, d.now)
	}
	for _, c := range w.diagnose.causes {
		d.causes = append(d.causes, describeWorstPodCause(w.worst, c))
	}
	d.crashOf, d.crash, d.crashPod = w.diagnose.crashOf, w.diagnose.crash, w.worst
	if describeNeedsFailedLog(d, w) {
		r.describeFailedJobLog(ctx, d, w)
	}
}

// describeNeedsFailedLog reports whether the worst pod of a failed Job has no
// previous instance to show. A pod of a Job that failed without a restart has
// none; its own log holds the error.
func describeNeedsFailedLog(d *describeParts, w workloadPods) bool {
	return d.row.State != shape.Healthy && d.k.Kind() == "Job" && len(d.crash) == 0 && w.worstPod != nil
}

// describeFailedJobLog shows the log of the failed container of the worst pod
// of a Job, in place of the crash of a previous instance.
func (r *Runner) describeFailedJobLog(ctx context.Context, d *describeParts, w workloadPods) {
	if c, lines := r.failedContainerLog(ctx, w.worstPod, d.red); len(lines) > 0 {
		d.crashOf, d.crash, d.crashNote = c, lines, describeFailedLogHeading(c, w.worst)
	}
}

// describeServiceParts reads the endpoints of a service and checks its ports.
// The check of the ports runs while the endpoints are read.
func (r *Runner) describeServiceParts(ctx context.Context, d *describeParts) {
	d.summary = serviceLine(d.u)
	type mismatch struct {
		causes  []string
		noPorts bool
	}
	u, ns := d.u, d.ns
	mismatchF := start(ctx, func(c context.Context) mismatch {
		mm, noPorts := r.servicePortMismatch(c, u, ns)
		return mismatch{mm, noPorts}
	})
	defer mismatchF.drop()
	if p, ok := r.serviceEndpoints(ctx, d.u, d.ns); ok {
		d.podSummary = p.summary
		if p.ready == 0 && p.hasSelector {
			d.problems = append(d.problems, describeNoReadyEndpoints)
			d.causes = r.serviceCause(ctx, d.u, d.ns)
		}
	}
	if m := mismatchF.wait(); len(m.causes) > 0 {
		d.causes = append(d.causes, m.causes...)
		if m.noPorts {
			d.state = describeStateLine(shape.Unhealthy)
			d.podSummary = describeNoPortsSummary
		}
	}
}

// describeContainers makes the compact line of the containers. When the
// problems need evidence, the container blocks state the requests and limits,
// so the lines of the spec alone are dropped.
func describeContainers(d *describeParts) {
	switch {
	case d.csBad:
		d.summary = describeContainersUnread
	case d.hasCS:
		var ev *evidence
		if !d.brief && needsEvidence(d.problems, d.causes, d.row.State, hasRestarted(d.u), d.firstBadPod) {
			ev = &evidence{red: d.red, hasUsage: d.hasUsage}
			d.specOnly = nil
		}
		d.summary = containersLine(d.cs, ev, d.red)
	}
}

// describeNext is the hint for the next call. When the cause is in the body, a
// hint would only invite a call that is not needed.
func (r *Runner) describeNext(d *describeParts) []string {
	switch {
	case len(d.causes) > 0:
		return nil
	case d.k.Kind() == "Pod" && d.row.State != shape.Healthy:
		var next []string
		switch {
		case len(d.crash) > 0:
			next = append(next, r.hintLogs(d.name, d.ns, d.crashOf, true, true))
		case hasRestarted(d.u): // the previous instance has the story
			next = append(next, r.hintLogs(d.name, d.ns, "", true, false))
		case hasRun(d.u):
			next = append(next, r.hintLogs(d.name, d.ns, "", false, false))
		}
		return append(next, r.hintEvents("pod", d.name, d.ns))
	case d.firstBadPod != "":
		return []string{r.hintDescribe("pod", d.firstBadPod, d.ns)}
	}
	return nil
}

// describeWriteList writes each line on its own line, after prefix.
func describeWriteList(b *strings.Builder, prefix string, lines []string) {
	for _, l := range lines {
		b.WriteString(prefix + l + "\n")
	}
}

// describeWriteBody writes the sections of a full description in their fixed
// order. Each fact is said once: the problems that a cause explains are left
// out.
func (r *Runner) describeWriteBody(b *strings.Builder, d *describeParts, rel related, ev describeEvents, owners []string) {
	d.problems = dropCovered(d.problems, d.causes)
	r.describeWriteDiagnosis(b, d)
	rel.write(b)
	describeWriteList(b, "", d.revLines)
	describeWriteSpec(b, d, owners)
	r.describeWritePods(b, d)
	r.describeWriteEvents(b, d, ev)
	describeWriteCrash(b, d)
}

// describeWriteDiagnosis writes what looks wrong and the likely causes.
func (r *Runner) describeWriteDiagnosis(b *strings.Builder, d *describeParts) {
	if len(d.problems) > 0 {
		b.WriteString("looks wrong:\n")
		describeWriteList(b, "- ", shape.ProblemLines(d.problems))
	} else if len(d.causes) == 0 {
		b.WriteString("looks wrong: nothing found\n")
	}
	if d.earlierPod != "" {
		b.WriteString(r.rt("earlier: "+d.earlierPod) + "\n")
	}
	if shape.BudgetForbidsByItself(d.u) {
		b.WriteString(describeNoDisruptions + "\n")
	}
	if len(d.causes) > 0 {
		b.WriteString("likely cause:\n")
		describeWriteList(b, "- ", shape.ProblemLines(d.causes))
	}
}

// describeWriteSpec writes the resources, the compact spec, the mounts and
// the owner chain.
func describeWriteSpec(b *strings.Builder, d *describeParts, owners []string) {
	if len(d.resources) > 0 {
		b.WriteString("resources (usage now, from the metrics API):\n")
		describeWriteList(b, "- ", d.resources)
	} else if d.usageNote != "" {
		b.WriteString(d.usageNote + "\n")
		describeWriteList(b, "- ", d.specOnly)
	}
	if d.summary != "" {
		b.WriteString(d.summary + "\n")
	}
	if d.mounts != "" {
		b.WriteString(d.mounts + "\n")
	}
	if len(owners) > 0 {
		b.WriteString("owner chain:\n")
		describeWriteList(b, "- ", owners)
	}
}

// describeWritePods writes the pods of a workload or the endpoints of a
// service, and how the failing pods differ from the others.
func (r *Runner) describeWritePods(b *strings.Builder, d *describeParts) {
	if d.podSummary != "" {
		b.WriteString(d.podSummary + "\n")
		describeWriteList(b, "- ", d.podLines)
		if d.earlierWork != "" {
			b.WriteString(r.rt(d.earlierWork) + "\n")
		}
	}
	describeWriteList(b, "", d.sibLines)
}

// describeShownEvents is how many event groups the full form shows.
const describeShownEvents = 8

// describeWriteEvents writes the recent events, warnings first. The warnings
// that a cause already states are left out.
func (r *Runner) describeWriteEvents(b *strings.Builder, d *describeParts, ev describeEvents) {
	if ev.evCount == 0 {
		b.WriteString(describeNoEventsLine(ev.err) + "\n")
		return
	}
	shownEv, hidden := splitNormal(ev.groups)
	shownEv = dropStated(shownEv, d.causes)
	more := 0
	if len(shownEv) > describeShownEvents {
		more = len(shownEv) - describeShownEvents
		shownEv = shownEv[:describeShownEvents]
	}
	fmt.Fprintf(b, "recent events (%d, warnings first):\n", ev.evCount)
	if len(shownEv) > 0 {
		table, _ := eventTable(shownEv, d.now, false, r.rt)
		describeWriteList(b, "  ", strings.Split(table, "\n"))
	}
	if more > 0 {
		fmt.Fprintf(b, "  (%d more groups; %s events with %s)\n", more, r.verb(), r.eventsFilter())
	}
	if hidden != "" {
		fmt.Fprintf(b, "  (%s)\n", hidden)
	}
}

// describeWriteCrash writes the last lines of the crashed or failed container,
// and the log of a running container that is not ready.
func describeWriteCrash(b *strings.Builder, d *describeParts) {
	if len(d.crash) > 0 {
		b.WriteString(describeCrashHeading(d.crashOf, d.crashPod, d.crashNote) + "\n")
		describeWriteList(b, "  ", d.crash)
	}
	if len(d.logLines) > 0 {
		b.WriteString(describeNotReadyLogHeading(d.logOf) + "\n")
		describeWriteList(b, "  ", d.logLines)
	}
}

// describeFinish adds the object YAML for kinds without a compact summary,
// cuts the body to the size budget and adds the hint for the next call.
func (r *Runner) describeFinish(out output, b *strings.Builder, d *describeParts, limit int) output {
	if d.summary == "" {
		out = r.describeWriteObject(out, b, d, limit)
	} else {
		out.notes = append(out.notes, "Spec and status: "+r.verb()+" "+r.hintGet(d.k.Singular(), d.name, d.ns)+".")
	}
	body, wasCut := shape.Cut(b.String(), limit)
	if wasCut {
		out.truncated = true
		out.notes = append(out.notes, "the result was cut at the size budget; "+r.verb()+" events or logs for the parts left out.")
	}
	out.body = body
	out.next = r.describeNext(d)
	return out
}

// describeWriteObject writes the trimmed object, in the room the body leaves.
func (r *Runner) describeWriteObject(out output, b *strings.Builder, d *describeParts, limit int) output {
	b.WriteString("object:\n")
	yaml := objectYAML(d.u, false, d.red)
	cut, wasCut := shape.Cut(yaml, limit-b.Len())
	b.WriteString(cut)
	if wasCut {
		out.truncated = true
		out.notes = append(out.notes, r.describeYAMLCutNote(len(cut), len(yaml)))
	}
	return out
}
