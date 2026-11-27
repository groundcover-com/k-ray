package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

func (r *Runner) describe(ctx context.Context, in DescribeInput, red *redact.Redactor) (output, error) {
	if in.LabelSelector != "" {
		return r.describeSelected(ctx, in, red)
	}
	if in.Kind == "" {
		if in.Name == "" {
			return output{}, badInput("describe needs a name, or a kind with labelSelector")
		}
		return r.describeFound(ctx, in, red)
	}
	k, err := r.kind(in.Kind)
	if err != nil {
		return output{}, err
	}
	if err := objectName("name", in.Name); err != nil {
		return output{}, err
	}
	if in.AllNamespaces && k.Namespaced() {
		return r.describeFound(ctx, in, red)
	}
	ns, err := r.namespaceFor(k, in.Namespace, false)
	if err != nil {
		return output{}, err
	}
	if in.Namespace == "" && k.Namespaced() {
		return r.describeLocated(ctx, in, k, ns, red)
	}
	return r.describeOne(ctx, k, ns, in.Name, nil, red, r.budget(false))
}

// describeOne describes one object. pre is the object when the caller has
// read it already. limit is the size of the body in bytes.
func (r *Runner) describeOne(ctx context.Context, k kinds.Kind, ns, name string, pre *unstructured.Unstructured, red *redact.Redactor, limit int) (output, error) {
	return r.describeObj(ctx, k, ns, name, pre, red, limit, false)
}

// describeBrief is the short block of one of several objects that matched a
// name: its state, what looks wrong, the likely causes and the related
// objects that have a problem. It leaves out the events, the owners and the
// spec.
func (r *Runner) describeBrief(ctx context.Context, k kinds.Kind, ns, name string, pre *unstructured.Unstructured, red *redact.Redactor) (output, error) {
	return r.describeObj(ctx, k, ns, name, pre, red, 2<<10, true)
}

// describeObj reads the object, runs the reads around it at the same time,
// diagnoses it by kind, and writes the short block (brief) or the full form.
func (r *Runner) describeObj(ctx context.Context, k kinds.Kind, ns, name string, pre *unstructured.Unstructured, red *redact.Redactor, limit int, brief bool) (output, error) {
	u, err := r.describeRead(ctx, k, ns, name, pre)
	if err != nil {
		return output{namespace: ns}, err
	}
	d := r.describeNewParts(ctx, k, ns, name, u, red, brief)
	reads := r.describeStartReads(ctx, d)
	defer reads.drop()
	r.describeKindParts(ctx, d, reads.events)
	rel := reads.rel.wait()
	describeContainers(d)
	out := output{
		namespace: ns,
		title:     fmt.Sprintf("describe %s %s%s · context %s", k.Singular(), name, inNS(ns), r.Context),
		summary:   []string{d.state},
	}
	if brief {
		d.causes = r.briefCauses(ctx, k, name, ns, d.row.State, d.badPod, d.causes, rel, red)
		return r.briefBody(out, d.problems, d.causes, rel), nil
	}
	ev := reads.events.wait()
	owners := reads.owners.wait()
	var b strings.Builder
	r.describeWriteBody(&b, d, rel, ev, owners)
	return r.describeFinish(out, &b, d, limit), nil
}

// ownerChain follows the controller of an object up to 5 hops and says what
// state each owner is in.
func (r *Runner) ownerChain(ctx context.Context, u *unstructured.Unstructured, now time.Time) []string {
	var owners []string
	cur := u
	for hop := 0; hop < 5; hop++ {
		ref := controllerRef(cur)
		if ref == nil {
			break
		}
		gv, _ := schema.ParseGroupVersion(ref.APIVersion)
		ok, found := r.Kinds.ByGroupKind(gv.Group, ref.Kind)
		if !found {
			owners = append(owners, fmt.Sprintf("%s/%s (kind not in the allowlist)", ref.Kind, ref.Name))
			break
		}
		parent, err := r.Reader.Get(ctx, ok, cur.GetNamespace(), ref.Name)
		if err != nil {
			owners = append(owners, fmt.Sprintf("%s/%s (could not read: %v)", ref.Kind, ref.Name, err))
			break
		}
		prow := shape.Inspect(parent, now)
		line := fmt.Sprintf("%s/%s: %s", ref.Kind, ref.Name, prow.State)
		if len(prow.Problems) > 0 {
			line += ": " + prow.Lines(r.rt)[0]
		}
		owners = append(owners, line)
		cur = parent
	}
	return owners
}

// hasRun reports whether any container of the pod has started, so that it
// has logs to read.
func hasRun(u *unstructured.Unstructured) bool {
	p, ok := toPod(u)
	if !ok {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Running != nil || cs.State.Terminated != nil || cs.RestartCount > 0 {
			return true
		}
	}
	return false
}

func hasRestarted(u *unstructured.Unstructured) bool {
	p, ok := toPod(u)
	if !ok {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > 0 {
			return true
		}
	}
	return false
}

// templatePodSpec reads the pod template of a workload.
func templatePodSpec(u *unstructured.Unstructured) (corev1.PodSpec, bool) {
	path := []string{"spec", "template", "spec"}
	if u.GetKind() == "CronJob" {
		path = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	}
	m, _, _ := unstructured.NestedMap(u.Object, path...)
	var spec corev1.PodSpec
	ok := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &spec) == nil
	return spec, ok
}

// templateMountsLine names the ConfigMaps, Secrets and claims that the pods
// of a workload mount, as the line of a pod does.
func templateMountsLine(u *unstructured.Unstructured) string {
	spec, ok := templatePodSpec(u)
	if !ok {
		return ""
	}
	return mountsLine(&corev1.Pod{Spec: spec})
}

// groupsOrEmpty keeps "no events" from being read a second time.
func groupsOrEmpty(g []eventGroup) []eventGroup {
	if g == nil {
		return []eventGroup{}
	}
	return g
}

func controllerRef(u *unstructured.Unstructured) *metav1.OwnerReference {
	return controllerRefOf(u.GetOwnerReferences())
}

// controllerRefOf is the controller among owner references, else the first.
func controllerRefOf(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	if len(refs) > 0 {
		return &refs[0]
	}
	return nil
}

func workloadSelector(u *unstructured.Unstructured) string {
	sel, ok := workloadSelectorOf(u)
	if !ok {
		return ""
	}
	return sel.String()
}

// workloadSelectorOf is the pod selector of a workload. ok is false when it
// has none, or none that can be read, or an empty one (which would match
// every pod).
func workloadSelectorOf(u *unstructured.Unstructured) (labels.Selector, bool) {
	m, found, _ := unstructured.NestedMap(u.Object, "spec", "selector")
	if !found {
		return nil, false
	}
	sel, err := labelSelectorOf(m)
	if err != nil || sel.Empty() {
		return nil, false
	}
	return sel, true
}

// labelSelectorOf reads a Kubernetes label selector (matchLabels and
// matchExpressions) from an object field. An empty selector matches
// everything.
func labelSelectorOf(m map[string]any) (labels.Selector, error) {
	var ls metav1.LabelSelector
	if ml, ok := m["matchLabels"].(map[string]any); ok {
		ls.MatchLabels = map[string]string{}
		for k, v := range ml {
			if s, ok := v.(string); ok {
				ls.MatchLabels[k] = s
			}
		}
	}
	if exprs, ok := m["matchExpressions"].([]any); ok {
		for _, e := range exprs {
			em, _ := e.(map[string]any)
			req := metav1.LabelSelectorRequirement{}
			req.Key, _ = em["key"].(string)
			op, _ := em["operator"].(string)
			req.Operator = metav1.LabelSelectorOperator(op)
			if vals, ok := em["values"].([]any); ok {
				for _, v := range vals {
					if s, ok := v.(string); ok {
						req.Values = append(req.Values, s)
					}
				}
			}
			ls.MatchExpressions = append(ls.MatchExpressions, req)
		}
	}
	return metav1.LabelSelectorAsSelector(&ls)
}

type endpointsInfo struct {
	summary     string
	ready       int
	hasSelector bool
}

func (r *Runner) serviceEndpoints(ctx context.Context, svc *unstructured.Unstructured, ns string) (endpointsInfo, bool) {
	k, err := r.Kinds.Lookup("endpointslices")
	if err != nil {
		return endpointsInfo{}, false
	}
	sel, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	page, err := r.Reader.List(ctx, k, ns, "kubernetes.io/service-name="+svc.GetName(), 50)
	if err != nil {
		return endpointsInfo{}, false
	}
	ready, total := 0, 0
	for _, s := range page.Items {
		eps, _, _ := unstructured.NestedSlice(s.Object, "endpoints")
		for _, e := range eps {
			em, _ := e.(map[string]any)
			total++
			cond, _ := em["conditions"].(map[string]any)
			if r, ok := cond["ready"].(bool); !ok || r {
				ready++
			}
		}
	}
	if page.More && ready == 0 {
		return endpointsInfo{}, false // slices were cut off: "none ready" is not known
	}
	return endpointsInfo{
		summary:     fmt.Sprintf("endpoints: %d ready of %d (from EndpointSlices)", ready, total),
		ready:       ready,
		hasSelector: len(sel) > 0,
	}, true
}

// objectEventGroups groups the events about one object (by UID), warnings
// first. It also returns the number of events.
func (r *Runner) objectEventGroups(ctx context.Context, u *unstructured.Unstructured, ns string) ([]eventGroup, int) {
	g, n, _ := r.objectEvents(ctx, u, ns)
	return g, n
}

// objectEvents is objectEventGroups that also returns the error of the read,
// so that a failed read is not shown as "no events".
func (r *Runner) objectEvents(ctx context.Context, u *unstructured.Unstructured, ns string) ([]eventGroup, int, error) {
	evNS := ns
	if evNS == "" {
		evNS = "default"
	}
	page, err := r.Reader.Events(ctx, evNS, &kube.ObjectRef{UID: string(u.GetUID()), Kind: u.GetKind(), Name: u.GetName()}, 200)
	if err != nil {
		return nil, 0, err
	}
	if len(page.Items) == 0 {
		return nil, 0, nil
	}
	groups := groupEvents(page.Items, r.now(), false, 0)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].less(groups[j]) })
	return groups, len(page.Items), nil
}

// dropCovered removes the problems about a container that a likely cause
// already explains: each fact is said once.
func dropCovered(problems, causes []string) []string {
	covered := map[string]bool{}
	for _, c := range causes {
		if i := strings.Index(c, ": container "); strings.HasPrefix(c, "pod/") && i > 0 {
			c = c[i+2:]
		}
		if rest, ok := strings.CutPrefix(c, "container "); ok {
			name, _, _ := strings.Cut(rest, " ")
			covered[name] = true
		}
	}
	var out []string
	for _, p := range problems {
		if rest, ok := strings.CutPrefix(p, "container "); ok {
			if name, _, ok := strings.Cut(rest, ":"); ok && covered[name] {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// mountsLine names the ConfigMaps, Secrets and claims that a pod mounts and
// where, so that the object that holds a config file is found without the
// whole pod. Secrets are named, never read.
func mountsLine(p *corev1.Pod) string {
	paths := map[string][]string{}
	for _, c := range p.Spec.Containers {
		for _, m := range c.VolumeMounts {
			paths[m.Name] = append(paths[m.Name], m.MountPath)
		}
	}
	var parts []string
	for _, v := range p.Spec.Volumes {
		var what string
		switch {
		case v.ConfigMap != nil:
			what = "configmap " + v.ConfigMap.Name
		case v.Secret != nil:
			what = "secret " + v.Secret.SecretName
		case v.PersistentVolumeClaim != nil:
			what = "claim " + v.PersistentVolumeClaim.ClaimName
		default:
			continue
		}
		if at := paths[v.Name]; len(at) > 0 {
			what += " at " + strings.Join(at, ", ")
		}
		parts = append(parts, what)
	}
	if len(parts) == 0 {
		return ""
	}
	return "mounts: " + strings.Join(parts, "; ")
}

// dropStated removes the warning groups that a likely cause already states:
// the cause names the same count ("×161"), so the event adds nothing.
func dropStated(groups []eventGroup, causes []string) []eventGroup {
	var out []eventGroup
	for _, g := range groups {
		stated := false
		if g.typ == "Warning" && g.count > 1 {
			mark := fmt.Sprintf("×%d", g.count)
			for _, c := range causes {
				if i := strings.Index(c, mark); i >= 0 && (i+len(mark) == len(c) || c[i+len(mark)] < '0' || c[i+len(mark)] > '9') {
					stated = true
				}
			}
		}
		if !stated {
			out = append(out, g)
		}
	}
	return out
}

// briefBody is the body of a short block (see describeBrief).
func (r *Runner) briefBody(out output, problems, causes []string, rel related) output {
	problems = dropCovered(problems, causes)
	var b strings.Builder
	if len(problems) > 0 {
		b.WriteString("looks wrong:\n")
		for _, p := range shape.ProblemLines(trimLines(problems, 3)) {
			b.WriteString("- " + oneLine(r.rt(p), 240) + "\n")
		}
	}
	if len(causes) > 0 {
		b.WriteString("likely cause:\n")
		for _, c := range trimLines(causes, 2) {
			b.WriteString("- " + oneLine(r.rt(c), 320) + "\n")
		}
	}
	rel.write(&b)
	out.body = b.String()
	return out
}

// trimLines keeps the first n lines and counts the rest.
func trimLines(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(append([]string(nil), in[:n]...), fmt.Sprintf("(%d more)", len(in)-n))
}

// failedContainerLog reads the log of the first container of a pod that ended
// with a failure and has not been restarted.
func (r *Runner) failedContainerLog(ctx context.Context, pod *unstructured.Unstructured, red *redact.Redactor) (string, []string) {
	p, ok := toPod(pod)
	if !ok {
		return "", nil
	}
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 && cs.RestartCount == 0 {
			lines, _, _ := r.containerLog(ctx, p.Namespace, p.Name, cs.Name, false, red, false)
			return cs.Name, lines
		}
	}
	return "", nil
}

// noCauseText is what a short block of a workload or pod with a problem says
// when no cause can be found, so that a block with little text is not read as
// "nothing wrong".
func (r *Runner) noCauseText(k kinds.Kind, name, ns string) string {
	return "no cause found from pod state and events. For more: " + r.verb() + " " + r.hintDescribe(k.Singular(), name, ns) + "."
}

// briefCauses makes sure that the short block of a workload or pod that has a
// problem carries a cause, as the full form would: the cause of its worst pod
// from the pod and its events (the same diagnosis as a list row), else the
// init container that has not completed, with its last log line (at most one
// log read), else the exit code of a container that restarted. When nothing
// is found the block says so.
func (r *Runner) briefCauses(ctx context.Context, k kinds.Kind, name, ns string, state shape.State, pod *unstructured.Unstructured, causes []string, rel related, red *redact.Redactor) []string {
	switch k.Kind() {
	case "Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
	default:
		return causes
	}
	if state != shape.Unhealthy && state != shape.Warning {
		return causes
	}
	if len(causes) == 0 && pod != nil {
		if p, ok := toPod(pod); ok {
			prefix := ""
			if k.Kind() != "Pod" {
				prefix = "pod/" + p.Name + ": "
			}
			found, _ := r.podCauses(ctx, p, nil, nil, true)
			if len(found) == 0 {
				if c := r.briefInitCause(ctx, p, red); c != "" {
					found = []string{c}
				}
			}
			if len(found) == 0 {
				found = r.lightPodCauses(ctx, p, nil, nil)
			}
			for _, c := range found {
				causes = append(causes, prefix+c)
			}
		}
	}
	if len(causes) == 0 && len(rel.problems) == 0 && len(rel.lines) == 0 {
		causes = []string{r.noCauseText(k, name, ns)}
	}
	return causes
}

// briefInitCause names the first init container of the pod that has not
// completed: its state, its restarts and its last log line. It reads one log.
func (r *Runner) briefInitCause(ctx context.Context, p *corev1.Pod, red *redact.Redactor) string {
	now := r.now()
	for _, cs := range p.Status.InitContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode == 0 {
			continue // completed
		}
		if isSidecar(p, cs.Name) {
			continue // a sidecar runs for the life of the pod; podCauses diagnoses a crash
		}
		state, started, previous := "not started", false, false
		switch {
		case cs.State.Running != nil:
			state, started = "running for "+shape.Age(cs.State.Running.StartedAt.Time, now), true
		case cs.State.Terminated != nil:
			t := cs.State.Terminated
			state, started = fmt.Sprintf("exited with code %d", t.ExitCode), true
			if t.Reason != "" {
				state += " (" + t.Reason + ")"
			}
		case cs.State.Waiting != nil:
			state = "waiting"
			if cs.State.Waiting.Reason != "" {
				state += ": " + cs.State.Waiting.Reason
			}
			previous = cs.LastTerminationState.Terminated != nil
			started = cs.RestartCount > 0 || previous
		}
		line := fmt.Sprintf("init container %s has not completed: %s", cs.Name, state)
		if cs.RestartCount > 0 {
			line += ", " + plural(int(cs.RestartCount), "restart", "restarts")
		}
		// What the init container runs says what it waits for: show the
		// addresses in its command, and the start of the command.
		for _, c := range p.Spec.InitContainers {
			if c.Name != cs.Name {
				continue
			}
			cmd := commandText(red, append(append([]string(nil), c.Command...), c.Args...))
			if urls := commandURLs(cmd, 3); len(urls) > 0 {
				line += "; it calls: " + strings.Join(urls, ", ")
			} else if strings.TrimSpace(cmd) != "" {
				line += "; it runs: " + middleCut(oneLine(cmd, 400), 160)
			}
		}
		if started {
			if text := r.lastLogLine(ctx, p.Namespace, p.Name, cs.Name, previous, red); text != "" {
				line += "; its last log line: " + middleCut(oneLine(text, 400), 160)
			}
		}
		return line
	}
	return ""
}

// lastLogLine is the last line of the log of one container, redacted, in one
// read of a few lines. It is "" when the log cannot be read or is empty.
func (r *Runner) lastLogLine(ctx context.Context, ns, pod, container string, previous bool, red *redact.Redactor) string {
	raw, err := r.Reader.Logs(ctx, ns, pod, kube.LogOptions{Container: container, Previous: previous, TailLines: 20, LimitBytes: 16 << 10})
	if err != nil || strings.HasPrefix(strings.TrimSpace(raw), logsBusyText) {
		return ""
	}
	entries := shape.ParseLogs(raw)
	shape.ReadableJSON(entries)
	// All lines are redacted, as one sequence, so that a key block is masked
	// whole. Only the line that is shown counts as a redaction.
	quiet := r.quietRedactor()
	redactEntries(quiet, entries)
	for i := len(entries) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(entries[i].Text); t != "" {
			red.CountMarks(t)
			return t
		}
	}
	return ""
}

var commandURLRE = regexp.MustCompile(`[a-z][a-z0-9+.-]{1,15}://[^\s"'<>\\)\]}|;]+`)

// commandURLs returns the distinct addresses written in a command line, at
// most n. The text must already be redacted.
func commandURLs(cmd string, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range commandURLRE.FindAllString(cmd, -1) {
		u = strings.TrimRight(u, ".,")
		if seen[u] || len(u) > 200 {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if len(out) == n {
			break
		}
	}
	return out
}

// timesText says how often: "once", "2 times".
func timesText(n int32) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%s times", shape.Thousands(int(n)))
}

// podEarlier is what a pod with no problem did earlier: the containers that
// restarted, how often, and when the last time was. It is "" when no
// container restarted.
func podEarlier(p *corev1.Pod, now time.Time) string {
	var parts []string
	statuses := append(append([]corev1.ContainerStatus(nil), p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
	for _, cs := range statuses {
		last := cs.LastTerminationState.Terminated
		// Only what the old rules showed as a warning is history worth a line:
		// an OOM kill, or 3 or more restarts. One or two plain restarts are not.
		if cs.RestartCount <= 0 || !(cs.RestartCount >= 3 || last != nil && last.Reason == "OOMKilled") {
			continue
		}
		s := fmt.Sprintf("container %s restarted %s", cs.Name, timesText(cs.RestartCount))
		if t := last; t != nil && !t.FinishedAt.IsZero() {
			s += ", last " + shape.Age(t.FinishedAt.Time, now) + " ago"
			if t.Reason != "" && t.Reason != "Completed" && t.Reason != "Error" && t.Reason != "Unknown" {
				s += " (" + t.Reason + ")"
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// workloadEarlier is the one line of history of a workload that is healthy:
// the pod that restarted most, or, for a Job, the attempts that failed before
// the run that completed. It is "" when there is nothing to say.
func workloadEarlier(kind string, u *unstructured.Unstructured, pods []unstructured.Unstructured, now time.Time) string {
	if kind == "Job" {
		failed := 0
		for i := range pods {
			if p, ok := toPod(&pods[i]); ok && p.Status.Phase == corev1.PodFailed {
				failed++
			}
		}
		if n, _, _ := unstructured.NestedInt64(u.Object, "status", "failed"); int(n) > failed {
			failed = int(n)
		}
		if failed == 0 {
			return ""
		}
		return fmt.Sprintf("earlier attempts: %s failed before the run that completed", plural(failed, "pod", "pods"))
	}
	best, bestN, withRestarts := "", int32(0), 0
	for i := range pods {
		p, ok := toPod(&pods[i])
		if !ok {
			continue
		}
		n := int32(0)
		for _, cs := range p.Status.ContainerStatuses {
			n += cs.RestartCount
		}
		if n == 0 || podEarlier(p, now) == "" {
			continue
		}
		withRestarts++
		if n > bestN {
			best, bestN = "pod/"+p.Name+": "+podEarlier(p, now), n
		}
	}
	if best == "" {
		return ""
	}
	if withRestarts > 1 {
		best += fmt.Sprintf(" (+%d more pods restarted)", withRestarts-1)
	}
	return "earlier: " + best
}

var cronUnknownRE = regexp.MustCompile(`^result of the last run is not known: scheduled (\S+) ago, the status records no success$`)

// cronResultKnown completes the line of a CronJob whose status records no
// success: when the newest Job of the CronJob is found and failed, the last run
// is known to have failed. Otherwise the result stays "not known". The Jobs are
// the list that the related objects read (made once).
func (r *Runner) cronResultKnown(ctx context.Context, cj *unstructured.Unstructured, ns string, problems []string) []string {
	idx := -1
	var age string
	for i, p := range problems {
		if m := cronUnknownRE.FindStringSubmatch(p); m != nil {
			idx, age = i, m[1]
		}
	}
	if idx < 0 {
		return problems
	}
	var newest *unstructured.Unstructured
	jobs := r.listOf(ctx, "jobs", ns, "", relatedListCap)
	for i := range jobs {
		if ownedBy(&jobs[i], "CronJob", cj.GetName()) && (newest == nil || jobs[i].GetCreationTimestamp().After(newest.GetCreationTimestamp().Time)) {
			newest = &jobs[i]
		}
	}
	if newest == nil || shape.Inspect(newest, r.now()).State != shape.Unhealthy {
		return problems
	}
	out := append([]string(nil), problems...)
	out[idx] = "last run did not succeed: scheduled " + age + " ago, last success never"
	return out
}
