package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// Diagnosis joins signals that kubectl shows in separate commands: usage
// with requests and limits, pod requests with node capacity, service
// selectors with pod labels, probe failures and image pull errors from
// events, and the last lines of a crashed container.
// Every input is a read through kube.Reader.

// Thresholds for resource verdicts.
const (
	nearOOMPercent     = 85 // memory use, percent of the limit
	cpuAtLimitPercent  = 90 // CPU use, percent of the limit
	aboveRequestFactor = 1.1
	overProvisioned    = 4         // request is this many times the usage
	minCPURequest      = 100       // millicores; smaller requests are not "over-provisioned"
	minMemRequest      = 128 << 20 // bytes
	crashLogTail       = 200       // lines read from the previous instance
	crashLogBytes      = 256 << 10 // bytes read from the previous instance
	crashLogBudget     = 1200      // bytes of the summary shown
	schedulingPodCap   = 5000      // pods read to compute free node capacity
)

// readPage is the answer of a list read that runs in the background.
type readPage struct {
	page kube.Page
	err  error
}

// containerUse is one container's usage from the metrics API.
type containerUse struct{ cpu, mem int64 }

// finding is one resource verdict for a container.
type finding struct {
	short string // for tables: "near OOM"
	text  string // full sentence part: "near OOM: memory at 88% of the 1024Mi limit"
	warn  bool   // true: a problem now; false: information
}

// podUsage reads pod usage. ok is false when the metrics API is not
// available; diagnosis then works without usage.
func (r *Runner) podUsage(ctx context.Context, ns, sel string) (map[string]map[string]containerUse, bool) {
	use, ok, _ := r.podUsageRead(ctx, ns, sel)
	return use, ok
}

// podUsageRead is podUsage that also returns the error of the read, so that a
// caller can say why the usage is not available (metricsReason).
func (r *Runner) podUsageRead(ctx context.Context, ns, sel string) (map[string]map[string]containerUse, bool, error) {
	items, err := r.Reader.Metrics(ctx, kube.PodMetrics, ns, sel)
	if err != nil {
		return nil, false, err
	}
	out := map[string]map[string]containerUse{}
	for _, m := range items {
		cs, _, _ := unstructured.NestedSlice(m.Object, "containers")
		per := map[string]containerUse{}
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			name, _ := cm["name"].(string)
			usage, _ := cm["usage"].(map[string]any)
			per[name] = containerUse{cpu: quantity(usage["cpu"], true), mem: quantity(usage["memory"], false)}
		}
		out[m.GetNamespace()+"/"+m.GetName()] = per
	}
	return out, true, nil
}

func newUsageRow(ns, pod, container string, use containerUse, res corev1.ResourceRequirements) usageRow {
	row := usageRow{ns: ns, pod: pod, container: container, cpu: use.cpu, mem: use.mem}
	row.cpuReq = res.Requests.Cpu().MilliValue()
	row.cpuLim = res.Limits.Cpu().MilliValue()
	row.memReq = res.Requests.Memory().Value()
	row.memLim = res.Limits.Memory().Value()
	row.cpuUtil, row.cpuBasis = util(row.cpu, row.cpuLim, row.cpuReq)
	row.memUtil, row.memBasis = util(row.mem, row.memLim, row.memReq)
	return row
}

// resourceFindings returns the verdicts for one container, problems first.
func resourceFindings(u usageRow) []finding {
	if u.mem == 0 && u.cpu == 0 {
		return nil // no sample yet
	}
	var out []finding
	if u.memLim > 0 && u.memUtil >= nearOOMPercent {
		out = append(out, finding{"near OOM", fmt.Sprintf("near OOM: memory at %.0f%% of the %s limit", u.memUtil, mib(u.memLim)), true})
	}
	if u.cpuLim > 0 && u.cpuUtil >= cpuAtLimitPercent {
		out = append(out, finding{"CPU at limit", fmt.Sprintf("CPU at %.0f%% of the %dm limit, throttling likely", u.cpuUtil, u.cpuLim), true})
	}
	if u.memReq > 0 && float64(u.mem) > float64(u.memReq)*aboveRequestFactor {
		out = append(out, finding{"above memory request", fmt.Sprintf("memory above its request (%s used, %s requested): first to be evicted when the node runs low", mib(u.mem), mib(u.memReq)), false})
	}
	switch {
	case u.memLim == 0 && u.cpuLim == 0 && u.memReq == 0 && u.cpuReq == 0:
		out = append(out, finding{"no requests or limits", "no requests or limits: the scheduler cannot plan for it and nothing caps it", false})
	case u.memLim == 0:
		out = append(out, finding{"no memory limit", "no memory limit: a leak can use all the node's memory", false})
	}
	if u.cpuReq >= minCPURequest && u.cpu*overProvisioned <= u.cpuReq {
		out = append(out, finding{"CPU over-provisioned", fmt.Sprintf("CPU over-provisioned (requests %dm, uses %dm)", u.cpuReq, u.cpu), false})
	}
	if u.memReq >= minMemRequest && u.mem*overProvisioned <= u.memReq {
		out = append(out, finding{"memory over-provisioned", fmt.Sprintf("memory over-provisioned (requests %s, uses %s)", mib(u.memReq), mib(u.mem)), false})
	}
	return out
}

func shorts(fs []finding) string {
	if len(fs) == 0 {
		return "ok"
	}
	s := make([]string, len(fs))
	for i, f := range fs {
		s[i] = f.short
	}
	return strings.Join(s, ", ")
}

// useText renders one resource: "memory 900Mi (88% of 1024Mi limit)".
func useText(what string, use, req, lim int64, cpu bool) string {
	f := func(v int64) string {
		if cpu {
			return fmt.Sprintf("%dm", v)
		}
		return mib(v)
	}
	switch {
	case lim > 0:
		return fmt.Sprintf("%s %s (%.0f%% of %s limit)", what, f(use), float64(use)/float64(lim)*100, f(lim))
	case req > 0:
		return fmt.Sprintf("%s %s (%.0f%% of %s request, no limit)", what, f(use), float64(use)/float64(req)*100, f(req))
	}
	return what + " " + f(use)
}

func toPod(u *unstructured.Unstructured) (*corev1.Pod, bool) {
	var p corev1.Pod
	if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p) != nil {
		return nil, false
	}
	return &p, true
}

// resourceLines renders the "resources" section for one pod.
func resourceLines(p *corev1.Pod, use map[string]containerUse) []string {
	var out []string
	for _, c := range p.Spec.Containers {
		u, ok := use[c.Name]
		if !ok {
			continue
		}
		row := newUsageRow(p.Namespace, p.Name, c.Name, u, c.Resources)
		out = append(out, fmt.Sprintf("container %s: %s, %s: %s", c.Name,
			useText("CPU", row.cpu, row.cpuReq, row.cpuLim, true),
			useText("memory", row.mem, row.memReq, row.memLim, false),
			strings.ReplaceAll(shorts(resourceFindings(row)), ", ", "; ")))
	}
	return out
}

// containersLine is the compact spec summary: names and images, then one
// line per container that has a command, arguments or environment. It shows
// the names of environment variables and where they come from, never their
// values, so an agent does not need the whole object to check them.
//
// With ev, it prints the evidence lines instead: for each container the
// values of the environment (redacted), the requests and limits, and the
// probes. The env line then replaces the names.
func containersLine(cs []corev1.Container, ev *evidence, red *redact.Redactor) string {
	parts := make([]string, 0, len(cs))
	var details []string
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s (%s)", c.Name, c.Image))
		var d []string
		if len(c.Command) > 0 {
			d = append(d, "command: "+cutCommand(commandText(red, c.Command)))
		}
		if len(c.Args) > 0 {
			d = append(d, "args: "+cutCommand(commandText(red, c.Args)))
		}
		if ev == nil && (len(c.Env) > 0 || len(c.EnvFrom) > 0) {
			var names []string
			for _, e := range c.Env {
				n := e.Name
				if vf := e.ValueFrom; vf != nil {
					switch {
					case vf.SecretKeyRef != nil:
						n += fmt.Sprintf(" (secret %s/%s)", vf.SecretKeyRef.Name, vf.SecretKeyRef.Key)
					case vf.ConfigMapKeyRef != nil:
						n += fmt.Sprintf(" (configmap %s/%s)", vf.ConfigMapKeyRef.Name, vf.ConfigMapKeyRef.Key)
					case vf.FieldRef != nil:
						n += " (field " + vf.FieldRef.FieldPath + ")"
					}
				}
				names = append(names, n)
			}
			for _, e := range c.EnvFrom {
				switch {
				case e.SecretRef != nil:
					names = append(names, "all keys of secret "+e.SecretRef.Name)
				case e.ConfigMapRef != nil:
					names = append(names, "all keys of configmap "+e.ConfigMapRef.Name)
				}
			}
			if len(names) > 30 {
				names = append(names[:30], fmt.Sprintf("(%d more)", len(names)-30))
			}
			d = append(d, "env: "+strings.Join(names, ", "))
		}
		if len(d) > 0 {
			details = append(details, "- "+c.Name+": "+strings.Join(d, "; "))
		}
		if ev != nil {
			lines := evidenceLines(c, ev.red, ev.hasUsage[c.Name])
			if len(d) == 0 {
				details = append(details, "- "+c.Name+":")
			}
			for _, l := range lines {
				details = append(details, "  "+l)
			}
		}
	}
	out := "containers: " + strings.Join(parts, ", ")
	if len(details) > 0 {
		out += "\n" + strings.Join(details, "\n")
	}
	return out
}

// podDiagnosis is what describe and list show for one unhealthy pod.
type podDiagnosis struct {
	causes  []string
	crashOf string   // container whose previous instance is summarized
	crash   []string // summary lines of that instance's logs
	logOf   string   // running container that is not ready, whose current log is summarized
	log     []string
}

// diagnosePod finds the likely causes for one pod. groups are the pod's
// events; nil reads them. The last error line of a crashed container goes
// into its cause.
func (r *Runner) diagnosePod(ctx context.Context, u *unstructured.Unstructured, use map[string]containerUse, groups []eventGroup, red *redact.Redactor) podDiagnosis {
	p, ok := toPod(u)
	if !ok {
		return podDiagnosis{}
	}
	if groups == nil {
		groups, _ = r.objectEventGroups(ctx, u, p.Namespace)
	}
	causes, crashed := r.podCauses(ctx, p, use, groups, false)
	d := podDiagnosis{causes: causes}
	if len(crashed) > 0 {
		d.crashOf = crashed[0]
		var lastError string
		used := false
		for _, c := range d.causes {
			used = used || strings.HasPrefix(c, "container "+d.crashOf+" exits with code")
		}
		d.crash, lastError = r.lastCrash(ctx, p.Namespace, p.Name, d.crashOf, red, used)
		if lastError != "" {
			for i, c := range d.causes {
				if strings.HasPrefix(c, "container "+d.crashOf+" exits with code") {
					d.causes[i] = c + " Last error before the exit: " + lastError
				}
			}
		}
	}
	// A container that runs and is not ready: its probe message is often empty,
	// so the last error of its log is the cause.
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Running == nil || cs.Ready || p.Status.Phase != corev1.PodRunning {
			continue
		}
		prefix := "container " + cs.Name + " is not ready because"
		for i, c := range d.causes {
			if !strings.HasPrefix(c, prefix) {
				continue
			}
			lines, lastErr, ok := r.containerLog(ctx, p.Namespace, p.Name, cs.Name, false, red, true)
			if ok {
				if lastErr != "" {
					d.causes[i] = c + " Last error in its log: " + lastErr
				}
				d.logOf, d.log = cs.Name, lines
			}
			break
		}
		if d.logOf != "" {
			break
		}
	}
	return d
}

// workloadPods is what a workload's pods say.
type workloadPods struct {
	summary  string   // "pods (selector ...): 2, 1 with problems"
	lines    []string // first problem of up to 5 pods
	worst    string   // name of the worst pod, "" when all are healthy
	worstPod *unstructured.Unstructured
	pods     []unstructured.Unstructured // the pods that were read, for the comparison of siblings
	unread   bool                        // the pod list could not be read: summary says so, and nothing depends on the pods
	// everyPodBad is true when the workload status looks healthy and every
	// pod has a problem: the workload is a warning (see everyPodBad).
	everyPodBad bool
	diagnose    podDiagnosis
}

// diagnoseWorkload reads a workload's pods and diagnoses the worst one.
func (r *Runner) diagnoseWorkload(ctx context.Context, u *unstructured.Unstructured, ns string, red *redact.Redactor) (workloadPods, bool) {
	var w workloadPods
	sel := workloadSelector(u)
	if sel == "" || u.GetKind() == "CronJob" {
		return w, false
	}
	pk, err := r.Kinds.Lookup("pods")
	if err != nil {
		return w, false
	}
	// The usage does not depend on the list, so a workload that has a problem
	// reads it with the list. A workload with no problem usually has no pod that needs
	// it: then it is read after the list, only if some pod does need it.
	readUse := func(c context.Context) map[string]map[string]containerUse {
		use, _ := r.podUsage(c, ns, sel)
		return use
	}
	var useF *future[map[string]map[string]containerUse]
	st := shape.Inspect(u, r.now()).State
	if st == shape.Unhealthy || st == shape.Warning {
		useF = start(ctx, readUse)
		defer useF.drop()
	}
	page, err := r.Reader.List(ctx, pk, ns, sel, 200)
	if err != nil {
		// Said, not dropped: no pod block and no cause that needs the pods.
		w.summary = "pods: could not be read (" + failureReason(err) + ")"
		w.unread = true
		return w, true
	}
	now := r.now()
	bad := 0
	var worst *unstructured.Unstructured
	worstState := shape.Healthy
	for i := range page.Items {
		pr := shape.Inspect(&page.Items[i], now)
		if pr.State == shape.Unhealthy || pr.State == shape.Warning {
			bad++
			if len(w.lines) < 5 && len(pr.Problems) > 0 {
				w.lines = append(w.lines, fmt.Sprintf("pod/%s: %s", pr.Name, pr.Lines(r.rt)[0]))
			}
			if worst == nil || (pr.State == shape.Unhealthy && worstState != shape.Unhealthy) {
				worst, worstState = &page.Items[i], pr.State
			}
		}
	}
	w.pods = page.Items
	w.summary = fmt.Sprintf("pods (selector %s): %d, %d with problems", sel, len(page.Items), bad)
	if st == shape.Healthy && !page.More && everyPodBad(u.GetKind(), bad, len(page.Items)) {
		st, w.everyPodBad = shape.Warning, true
	}
	if worst != nil {
		w.worst, w.worstPod = worst.GetName(), worst
		if st == shape.Healthy {
			// A workload that is healthy has no likely cause: what one of its
			// pods did earlier is history (describe prints it on one line).
			return w, true
		}
		// The events of the worst pod are read while the usage may still be
		// on its way.
		eventsF := start(ctx, func(c context.Context) []eventGroup {
			g, _ := r.objectEventGroups(c, worst, ns)
			return groupsOrEmpty(g)
		})
		defer eventsF.drop()
		var use map[string]map[string]containerUse
		if useF != nil {
			use = useF.wait()
		} else {
			use = readUse(ctx)
		}
		w.diagnose = r.diagnosePod(ctx, worst, use[ns+"/"+worst.GetName()], eventsF.wait(), red)
	}
	return w, true
}

// neighbors names the ConfigMaps in the namespace when a pod refers to one
// that does not exist: the right one is often there under another name.
func (r *Runner) neighbors(ctx context.Context, ns, missing string) string {
	ck, err := r.Kinds.Lookup("configmaps")
	if err != nil {
		return ""
	}
	page, err := r.Reader.List(ctx, ck, ns, "", 200)
	if err != nil {
		return ""
	}
	var close, all []string
	for _, c := range page.Items {
		n := c.GetName()
		if n == "kube-root-ca.crt" {
			continue
		}
		all = append(all, n)
		if similar(n, missing) {
			close = append(close, n)
		}
	}
	if len(close) > 0 {
		all = close
	}
	sort.Strings(all)
	switch {
	case len(all) == 0:
		return " The namespace has no ConfigMaps."
	case len(all) > 8:
		all = append(all[:8], fmt.Sprintf("(%d more)", len(all)-8))
	}
	return " ConfigMaps in the namespace: " + strings.Join(all, ", ") + "."
}

var exitMeaning = map[int32]string{
	1:   "application error",
	2:   "application error or wrong arguments",
	126: "the command cannot run",
	127: "command not found",
	134: "aborted (SIGABRT)",
	137: "killed (SIGKILL)",
	139: "segmentation fault",
	143: "stopped (SIGTERM)",
}

func specImage(p *corev1.Pod, container string) string {
	for _, c := range append(append([]corev1.Container(nil), p.Spec.Containers...), p.Spec.InitContainers...) {
		if c.Name == container {
			return c.Image
		}
	}
	return ""
}

func memLimit(p *corev1.Pod, container string) int64 {
	for _, c := range append(append([]corev1.Container(nil), p.Spec.Containers...), p.Spec.InitContainers...) {
		if c.Name == container {
			return c.Resources.Limits.Memory().Value()
		}
	}
	return 0
}

var missingRefRE = regexp.MustCompile(`(secret|configmap) "([^"]+)" not found`)

// podCauses returns the likely causes for an unhealthy pod, and the names of
// containers whose previous instance crashed (for the last-crash section).
// With light set it reads nothing from the cluster: it uses the pod and its
// events only, so that list can do it for every row.
func (r *Runner) podCauses(ctx context.Context, p *corev1.Pod, use map[string]containerUse, events []eventGroup, light bool) (causes, crashed []string) {
	now := r.now()
	eventsFor := func(reason, prefix string) (eventGroup, bool) {
		var best eventGroup
		found := false
		for _, g := range events {
			if g.reason == reason && strings.HasPrefix(g.message, prefix) && (!found || g.last.After(best.last)) {
				best, found = g, true
			}
		}
		return best, found
	}

	if p.Status.Phase == corev1.PodPending {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				s := ""
				if light {
					s = placementCause(c.Message, p, r.rt) // the scheduler's own reasons; no node or pod lists
				} else {
					s = r.schedulingCause(ctx, p, c.Message)
				}
				if s != "" {
					causes = append(causes, s)
				}
			}
		}
	}

	if g, ok := eventsFor("FailedAttachVolume", "Multi-Attach error"); ok && p.Status.Phase == corev1.PodPending {
		var claims []string
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				claims = append(claims, v.PersistentVolumeClaim.ClaimName)
			}
		}
		vol := "The volume"
		if len(claims) > 0 {
			word := "claims"
			if len(claims) == 1 {
				word = "claim"
			}
			vol = fmt.Sprintf("The volume (%s %s)", word, strings.Join(claims, ", "))
		}
		causes = append(causes, fmt.Sprintf("the pod cannot start because its volume is still attached to another node (Multi-Attach error, ×%d). %s attaches to one node at a time (ReadWriteOnce). The pod that holds it must stop first; a rolling update starts the new pod before it stops the old one.", g.count, vol))
	}

	// A probe failure event belongs to one container: the one that its
	// fieldPath names (spec.containers{NAME}). An event with no fieldPath is
	// attached only when exactly one container of the pod has that probe.
	probeEvent := func(container, probe string) (eventGroup, bool) {
		var best eventGroup
		found := false
		for _, g := range events {
			if g.reason != "Unhealthy" || !strings.HasPrefix(g.message, probe+" probe failed") {
				continue
			}
			if name, ok := containerOfFieldPath(g.fieldPath); ok {
				if name != container {
					continue
				}
			} else if !(probeOwners(p, probe) == 1 && definesProbe(p, container, probe) || probeOwners(p, probe) == 0 && len(p.Spec.Containers) == 1 && p.Spec.Containers[0].Name == container) {
				// Two or more containers could own it, or none can: say nothing.
				// A pod with one container and no probe in its spec (the spec
				// changed since): the event can only be about that container.
				continue
			}
			if !found || g.last.After(best.last) {
				best, found = g, true
			}
		}
		return best, found
	}
	type statusOf struct {
		cs   corev1.ContainerStatus
		init bool
	}
	var statuses []statusOf
	for _, cs := range p.Status.InitContainerStatuses {
		statuses = append(statuses, statusOf{cs, true})
	}
	for _, cs := range p.Status.ContainerStatuses {
		statuses = append(statuses, statusOf{cs, false})
	}
	for _, st := range statuses {
		cs := st.cs
		live, liveOK := probeEvent(cs.Name, "Liveness")
		if st.init && !isSidecar(p, cs.Name) {
			liveOK = false // an init step that ran to its end is not restarted by a probe
		}
		last := cs.LastTerminationState.Terminated
		if cs.State.Terminated != nil {
			last = cs.State.Terminated
		}
		w := cs.State.Waiting
		switch {
		case last != nil && last.Reason == "OOMKilled":
			lim := memLimit(p, cs.Name)
			lastWhen := last
			if crashOngoing(p, cs) {
				lastWhen = nil // the time is always "a moment ago"
			}
			if lim > 0 {
				s := fmt.Sprintf("container %s is OOM killed at its memory limit of %s%s.", cs.Name, mib(lim), restartsWhen(cs.RestartCount, lastWhen, now, " (", ")"))
				if u, ok := use[cs.Name]; ok && u.mem > 0 {
					s += fmt.Sprintf(" Memory use now: %s (%.0f%% of the limit).", mib(u.mem), float64(u.mem)/float64(lim)*100)
				}
				causes = append(causes, s+oomRunTime(last))
			} else {
				causes = append(causes, fmt.Sprintf("container %s is OOM killed and has no memory limit%s: the node ran out of memory.", cs.Name, restartsWhen(cs.RestartCount, lastWhen, now, " (", ")")))
			}
		case w != nil && (w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull" || w.Reason == "InvalidImageName"):
			msg := w.Message
			if g, ok := eventsFor("Failed", "Failed to pull image"); ok {
				msg += " " + g.message
			}
			causes = append(causes, fmt.Sprintf("container %s cannot pull its image: %s", cs.Name, imagePullCause(specImage(p, cs.Name), r.rt(msg), p.Spec.ImagePullSecrets)))
		case w != nil && (w.Reason == "CreateContainerConfigError" || w.Reason == "CreateContainerError"):
			if m := missingRefRE.FindStringSubmatch(w.Message); m != nil {
				s := fmt.Sprintf("container %s cannot start: it refers to the %s %q, which does not exist in namespace %s.", cs.Name, m[1], m[2], p.Namespace)
				if m[1] == "configmap" && !light {
					s += r.neighbors(ctx, p.Namespace, m[2])
				}
				causes = append(causes, s)
			}
		case last != nil && last.ExitCode != 0 && (w != nil && w.Reason == "CrashLoopBackOff" || cs.State.Terminated != nil || cs.RestartCount > 0):
			s := fmt.Sprintf("container %s exits with code %d", cs.Name, last.ExitCode)
			if m := exitMeaning[last.ExitCode]; m != "" {
				s += " (" + m + ")"
			}
			lastWhen := last
			if crashOngoing(p, cs) {
				lastWhen = nil
			}
			s += restartsWhen(cs.RestartCount, lastWhen, now, ", ", "") + "."
			if liveOK {
				s += fmt.Sprintf(" Its liveness probe fails (×%d): %s", live.count, r.cut(live.message, 200))
			}
			causes = append(causes, s)
			if cs.RestartCount > 0 {
				crashed = append(crashed, cs.Name)
			}
		case last != nil && last.ExitCode == 0 && cs.RestartCount > 0 && liveOK:
			// The kubelet stops the container after liveness failures, and a
			// container that handles the signal exits with code 0.
			msg := strings.TrimSuffix(r.cut(strings.TrimSpace(strings.TrimPrefix(live.message, "Liveness probe failed:")), 200), ".")
			s := fmt.Sprintf("container %s is restarted by the kubelet because its liveness probe fails (×%d, last %s ago)", cs.Name, live.count, shape.Age(live.last, now))
			if msg != "" {
				s += ": " + msg
			}
			s += fmt.Sprintf(". It has %s restarts and stops with exit code 0 each time, so the application itself does not crash.", shape.Thousands(int(cs.RestartCount)))
			causes = append(causes, s)
		case cs.State.Running != nil && !cs.Ready && p.Status.Phase == corev1.PodRunning:
			if g, ok := probeEvent(cs.Name, "Readiness"); ok {
				msg := strings.TrimSuffix(r.cut(strings.TrimSpace(strings.TrimPrefix(g.message, "Readiness probe failed:")), 200), ".")
				s := fmt.Sprintf("container %s is not ready because its readiness probe fails (×%d, last %s ago)", cs.Name, g.count, shape.Age(g.last, now))
				if msg != "" {
					s += ": " + msg
				}
				s += "."
				if pr := probeText(p, cs.Name); pr != "" {
					s += " Probe: " + pr + "."
				}
				causes = append(causes, s)
			} else if g, ok := probeEvent(cs.Name, "Startup"); ok {
				causes = append(causes, fmt.Sprintf("container %s is not ready because its startup probe fails (×%d): %s", cs.Name, g.count, r.cut(g.message, 200)))
			}
		}
	}
	return causes, crashed
}

// containerOfFieldPath reads the container name from the fieldPath of an
// event: spec.containers{NAME} or spec.initContainers{NAME}.
func containerOfFieldPath(fp string) (string, bool) {
	for _, prefix := range []string{"spec.containers{", "spec.initContainers{"} {
		if rest, ok := strings.CutPrefix(fp, prefix); ok {
			if name, ok := strings.CutSuffix(rest, "}"); ok && name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// definesProbe reports whether the named container has a probe of that kind.
func definesProbe(p *corev1.Pod, container, probe string) bool {
	for _, c := range append(append([]corev1.Container(nil), p.Spec.Containers...), p.Spec.InitContainers...) {
		if c.Name == container {
			return probeOf(c, probe) != nil
		}
	}
	return false
}

// probeOwners is the number of containers of the pod that have that probe.
func probeOwners(p *corev1.Pod, probe string) int {
	n := 0
	for _, c := range append(append([]corev1.Container(nil), p.Spec.Containers...), p.Spec.InitContainers...) {
		if probeOf(c, probe) != nil {
			n++
		}
	}
	return n
}

func probeOf(c corev1.Container, probe string) *corev1.Probe {
	switch probe {
	case "Liveness":
		return c.LivenessProbe
	case "Readiness":
		return c.ReadinessProbe
	case "Startup":
		return c.StartupProbe
	}
	return nil
}

// oomRunTime says how long the container ran before the kill, and what that
// points to: a kill soon after the start is not a slow leak.
func oomRunTime(t *corev1.ContainerStateTerminated) string {
	generic := " The limit is too low for this workload, or the container leaks memory."
	if t == nil || t.StartedAt.IsZero() || t.FinishedAt.IsZero() || !t.FinishedAt.After(t.StartedAt.Time) {
		return generic
	}
	ran := t.FinishedAt.Sub(t.StartedAt.Time)
	age := shape.Age(t.StartedAt.Time, t.FinishedAt.Time)
	switch {
	case ran < 2*time.Minute: // a kill soon after the start is not a slow leak
		return fmt.Sprintf(" It ran for %s before the kill, which points to a limit that is too low for the workload, not to a slow leak.", age)
	case ran >= time.Hour:
		return fmt.Sprintf(" It ran for %s before the kill, which fits a slow leak or a load that grew.", age)
	}
	return fmt.Sprintf(" It ran for %s before the kill.", age) + generic
}

// specLines shows requests and limits when there is no usage to join.
func specLines(p *corev1.Pod) []string {
	f := func(v int64, cpu bool) string {
		switch {
		case v <= 0:
			return "-"
		case cpu:
			return fmt.Sprintf("%dm", v)
		}
		return mib(v)
	}
	var out []string
	for _, c := range p.Spec.Containers {
		cr, cl := c.Resources.Requests.Cpu().MilliValue(), c.Resources.Limits.Cpu().MilliValue()
		mr, ml := c.Resources.Requests.Memory().Value(), c.Resources.Limits.Memory().Value()
		if cr == 0 && cl == 0 && mr == 0 && ml == 0 {
			out = append(out, fmt.Sprintf("container %s: no requests or limits", c.Name))
			continue
		}
		out = append(out, fmt.Sprintf("container %s: CPU request %s, limit %s; memory request %s, limit %s", c.Name, f(cr, true), f(cl, true), f(mr, false), f(ml, false)))
	}
	return out
}

// crashOngoing reports whether a container is between two starts right now: it
// does not run, and the pod will start it again. A crashing container is
// "terminated" for a moment and then "waiting"; the answer is the same in both
// states, so the text does not depend on the moment of the call.
func crashOngoing(p *corev1.Pod, cs corev1.ContainerStatus) bool {
	if cs.State.Running != nil {
		return false
	}
	if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
		return false
	}
	return p.Spec.RestartPolicy != corev1.RestartPolicyNever
}

// restartsWhen is restartsText with the time of the last termination: "(4
// restarts, last 2h ago)". A container that never restarted says when it
// ended. It adds nothing when the termination has no time.
func restartsWhen(n int32, last *corev1.ContainerStateTerminated, now time.Time, before, after string) string {
	if last == nil || last.FinishedAt.IsZero() {
		return restartsText(n, before, after)
	}
	ago := shape.Age(last.FinishedAt.Time, now)
	if n <= 0 {
		return before + "ended " + ago + " ago" + after
	}
	return before + plural(int(n), "restart", "restarts") + ", last " + ago + " ago" + after
}

// restartsText is "" for a container that never restarted (a job's pod).
func restartsText(n int32, before, after string) string {
	if n <= 0 {
		return ""
	}
	return before + plural(int(n), "restart", "restarts") + after
}

func probeText(p *corev1.Pod, container string) string {
	for _, c := range p.Spec.Containers {
		if c.Name != container || c.ReadinessProbe == nil {
			continue
		}
		switch h := c.ReadinessProbe.ProbeHandler; {
		case h.HTTPGet != nil:
			return fmt.Sprintf("HTTP GET %s on port %s", h.HTTPGet.Path, h.HTTPGet.Port.String())
		case h.TCPSocket != nil:
			return "TCP connect to port " + h.TCPSocket.Port.String()
		case h.GRPC != nil:
			return fmt.Sprintf("gRPC health check on port %d", h.GRPC.Port)
		case h.Exec != nil:
			return "a command in the container"
		}
	}
	return ""
}

// imagePullCause classifies an image pull failure from the kubelet's text.
func imagePullCause(image, msg string, pullSecrets []corev1.LocalObjectReference) string {
	low := strings.ToLower(msg)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	secrets := "the pod has no imagePullSecrets"
	if len(pullSecrets) > 0 {
		names := make([]string, len(pullSecrets))
		for i, s := range pullSecrets {
			names[i] = s.Name
		}
		secrets = "imagePullSecrets: " + strings.Join(names, ", ")
	}
	switch {
	case has("unauthorized", "denied", "authentication required", "401", "403", "no basic auth credentials"):
		return fmt.Sprintf("the registry refused access to %s (%s).", image, secrets)
	case has("not found", "manifest unknown", "name unknown"):
		return fmt.Sprintf("the image or tag %s does not exist in the registry (not found).", image)
	case has("no such host", "i/o timeout", "connection refused", "dial tcp", "tls:", "x509"):
		return fmt.Sprintf("the registry of %s is not reachable from the node.", image)
	case has("invalid reference format", "invalidimagename"):
		return fmt.Sprintf("the image name %s is not valid.", image)
	}
	return fmt.Sprintf("%s: %s", image, shape.CutWords(strings.Join(strings.Fields(msg), " "), 200))
}

// schedulingCause compares the pod's requests with what the nodes have free
// (allocatable minus the requests of the pods already on the node).
func (r *Runner) roomCause(ctx context.Context, p *corev1.Pod) string {
	var cpu, mem int64
	for _, c := range p.Spec.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		mem += c.Resources.Requests.Memory().Value()
	}
	for _, c := range p.Spec.InitContainers {
		cpu = max(cpu, c.Resources.Requests.Cpu().MilliValue())
		mem = max(mem, c.Resources.Requests.Memory().Value())
	}
	if cpu == 0 && mem == 0 {
		return ""
	}
	nk, err := r.Kinds.Lookup("nodes")
	if err != nil {
		return ""
	}
	pk, err := r.Kinds.Lookup("pods")
	if err != nil {
		return ""
	}
	// The nodes and the pods do not depend on each other. The pods are not
	// needed when there is no node; then that read is cancelled.
	podsF := start(ctx, func(c context.Context) readPage {
		page, err := r.Reader.List(c, pk, "", "", schedulingPodCap)
		return readPage{page, err}
	})
	defer podsF.drop()
	nodes, err := r.Reader.List(ctx, nk, "", "", 2000)
	if err != nil || len(nodes.Items) == 0 {
		return ""
	}
	pr := podsF.wait()
	pods, err := pr.page, pr.err
	if err != nil {
		return ""
	}
	type node struct {
		name               string
		allocCPU, allocMem int64
		usedCPU, usedMem   int64
		cordoned           bool
		placeable          bool // the pod passes the taints, node selector and node affinity of the node
	}
	byName := map[string]*node{}
	var list []*node
	for i := range nodes.Items {
		var n corev1.Node
		if runtime.DefaultUnstructuredConverter.FromUnstructured(nodes.Items[i].Object, &n) != nil {
			continue
		}
		nd := &node{name: n.Name, allocCPU: n.Status.Allocatable.Cpu().MilliValue(), allocMem: n.Status.Allocatable.Memory().Value(), cordoned: n.Spec.Unschedulable, placeable: nodeAccepts(p, &n)}
		byName[n.Name] = nd
		list = append(list, nd)
	}
	for i := range pods.Items {
		q, ok := toPod(&pods.Items[i])
		if !ok || q.Spec.NodeName == "" || q.Status.Phase == corev1.PodSucceeded || q.Status.Phase == corev1.PodFailed {
			continue
		}
		nd := byName[q.Spec.NodeName]
		if nd == nil {
			continue
		}
		for _, c := range q.Spec.Containers {
			nd.usedCPU += c.Resources.Requests.Cpu().MilliValue()
			nd.usedMem += c.Resources.Requests.Memory().Value()
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })

	var parts []string
	fits := ""
	var bigMem, freeMem, bigCPU, freeCPU *node
	cordoned, unplaceable := 0, 0
	for _, n := range list {
		if n.cordoned {
			cordoned++
			continue
		}
		if !n.placeable {
			unplaceable++ // a node that the pod cannot be placed on offers no room to it
			continue
		}
		if bigMem == nil || n.allocMem > bigMem.allocMem {
			bigMem = n
		}
		if freeMem == nil || n.allocMem-n.usedMem > freeMem.allocMem-freeMem.usedMem {
			freeMem = n
		}
		if bigCPU == nil || n.allocCPU > bigCPU.allocCPU {
			bigCPU = n
		}
		if freeCPU == nil || n.allocCPU-n.usedCPU > freeCPU.allocCPU-freeCPU.usedCPU {
			freeCPU = n
		}
		if fits == "" && n.allocMem-n.usedMem >= mem && n.allocCPU-n.usedCPU >= cpu {
			fits = n.name
		}
	}
	if bigMem == nil && unplaceable > 0 {
		return "" // the scheduler's own reasons (taints, selectors, affinity) say it
	}
	if bigMem == nil {
		return fmt.Sprintf("every node is cordoned (%d): no node accepts new pods.", cordoned)
	}
	switch {
	case mem > bigMem.allocMem:
		parts = append(parts, fmt.Sprintf("the pod requests %s memory, more than any node can offer (largest: %s with %s allocatable). It cannot fit on the current nodes.", mib(mem), bigMem.name, mib(bigMem.allocMem)))
	case mem > freeMem.allocMem-freeMem.usedMem:
		parts = append(parts, fmt.Sprintf("the pod requests %s memory; the most free on any node is %s (%s: %s allocatable, %s requested by other pods).", mib(mem), mib(freeMem.allocMem-freeMem.usedMem), freeMem.name, mib(freeMem.allocMem), mib(freeMem.usedMem)))
	}
	switch {
	case cpu > bigCPU.allocCPU:
		parts = append(parts, fmt.Sprintf("the pod requests %dm CPU, more than any node can offer (largest: %s with %dm allocatable). It cannot fit on the current nodes.", cpu, bigCPU.name, bigCPU.allocCPU))
	case cpu > freeCPU.allocCPU-freeCPU.usedCPU:
		parts = append(parts, fmt.Sprintf("the pod requests %dm CPU; the most free on any node is %dm (%s: %dm allocatable, %dm requested by other pods).", cpu, freeCPU.allocCPU-freeCPU.usedCPU, freeCPU.name, freeCPU.allocCPU, freeCPU.usedCPU))
	}
	if len(parts) == 0 {
		if fits != "" && unevaluatedPlacement(p) {
			return "" // a rule that is not evaluated may exclude that node: say nothing about room
		}
		if fits != "" {
			return fmt.Sprintf("by requests, node %s has room for the pod (%dm CPU, %s memory). The scheduler message names another reason, such as a taint, node affinity, or a volume.", fits, cpu, mib(mem))
		}
		parts = append(parts, fmt.Sprintf("no single node has both %dm CPU and %s memory free.", cpu, mib(mem)))
	}
	s := strings.Join(parts, " ")
	if cordoned > 0 {
		s += fmt.Sprintf(" %s cordoned and not counted.", plural(cordoned, "node is", "nodes are"))
	}
	if pods.More {
		s += fmt.Sprintf(" This is an estimate: only the first %s pods were read.", shape.Thousands(len(pods.Items)))
	}
	return s
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	if p.Status.Phase != corev1.PodRunning || len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// serviceCause explains a service with no ready endpoints: its selector
// matches no pod, or the pods it matches are not ready.
func (r *Runner) serviceCause(ctx context.Context, svc *unstructured.Unstructured, ns string) []string {
	sel, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	if len(sel) == 0 {
		return []string{"the service has no selector: its endpoints are managed by hand or by another controller, and none is ready."}
	}
	pk, err := r.Kinds.Lookup("pods")
	if err != nil {
		return nil
	}
	selText := labels.SelectorFromSet(labels.Set(sel)).String()
	page, err := r.Reader.List(ctx, pk, ns, selText, 200)
	if err != nil {
		return nil
	}
	now := r.now()
	if len(page.Items) > 0 {
		ready := 0
		var lines []string
		for i := range page.Items {
			p, ok := toPod(&page.Items[i])
			if ok && podReady(p) {
				ready++
				continue
			}
			if row := shape.Inspect(&page.Items[i], now); len(lines) < 3 {
				problem := "not ready"
				if len(row.Problems) > 0 {
					problem = row.Lines(r.rt)[0]
				}
				lines = append(lines, fmt.Sprintf("pod/%s: %s", row.Name, problem))
			}
		}
		if ready > 0 {
			return nil
		}
		return []string{fmt.Sprintf("selector %s matches %s, but none is ready: %s", selText, plural(len(page.Items), "pod", "pods"), strings.Join(lines, "; "))}
	}
	s := fmt.Sprintf("selector %s matches 0 pods in namespace %s.", selText, ns)
	if owners := r.scaledToZeroOwners(ctx, ns, ns, sel); len(owners) > 0 {
		// The reason is the replica count: the labels of other pods say nothing.
		verb := "is"
		if len(owners) > 1 {
			verb = "are"
		}
		return []string{fmt.Sprintf("selector %s matches 0 pods in namespace %s: %s %s scaled to 0 replicas.", selText, ns, strings.Join(owners, ", "), verb)}
	}
	all, err := r.Reader.List(ctx, pk, ns, "", 500)
	if err != nil {
		return []string{s}
	}
	counts := map[string]int{}
	for _, u := range all.Items {
		for k := range sel {
			if v, ok := u.GetLabels()[k]; ok && v != sel[k] {
				counts[k+"="+v]++
			}
		}
	}
	if len(counts) == 0 {
		keys := make([]string, 0, len(sel))
		for k := range sel {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return []string{s + fmt.Sprintf(" No pod in the namespace has the label %s.", strings.Join(keys, " or "))}
	}
	near := make([]string, 0, len(counts))
	for k := range counts {
		key, val, _ := strings.Cut(k, "=")
		if similar(val, sel[key]) {
			near = append(near, k)
		}
	}
	if len(near) == 0 {
		for k := range counts {
			near = append(near, k)
		}
	}
	sort.Slice(near, func(i, j int) bool {
		if counts[near[i]] != counts[near[j]] {
			return counts[near[i]] > counts[near[j]]
		}
		return near[i] < near[j]
	})
	if len(near) > 3 {
		near = near[:3]
	}
	for i, k := range near {
		near[i] = fmt.Sprintf("%s (%s)", k, plural(counts[k], "pod", "pods"))
	}
	return []string{s + " Pods with other values for the same label: " + strings.Join(near, ", ") + "."}
}

// scaledToZeroOwners names the Deployments and StatefulSets of the namespace
// whose pod template labels match the selector of a Service and that are
// scaled to 0 replicas ("deployment/web"). It lists each kind once (the reader
// of the call makes the same read once).
// listNS is the namespace that the lists read ("" for all namespaces, which a
// list of Services in all namespaces uses, so that it reads each kind once);
// svcNS is the namespace of the Service.
func (r *Runner) scaledToZeroOwners(ctx context.Context, listNS, svcNS string, sel map[string]string) []string {
	if len(sel) == 0 {
		return nil
	}
	var out []string
	for _, kind := range []struct{ resource, name string }{{"deployments", "deployment"}, {"statefulsets", "statefulset"}} {
		for _, w := range r.listOf(ctx, kind.resource, listNS, "", fetchCap) {
			if w.GetNamespace() != svcNS {
				continue
			}
			if v, found, _ := unstructured.NestedFieldNoCopy(w.Object, "spec", "replicas"); !found || nestedCount(map[string]any{"v": v}, "v") != 0 {
				continue
			}
			tl, _, _ := unstructured.NestedStringMap(w.Object, "spec", "template", "metadata", "labels")
			match := true
			for k, v := range sel {
				if got, ok := tl[k]; !ok || got != v {
					match = false
					break
				}
			}
			if match {
				out = append(out, kind.name+"/"+w.GetName())
			}
		}
	}
	sort.Strings(out)
	return out
}

// similar reports whether two label values look like versions of the same
// name: one starts with the other, or they share a long prefix.
func similar(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n >= 4 && n*2 >= min(len(a), len(b))
}

// serviceLine is the compact spec summary for a service.
func serviceLine(svc *unstructured.Unstructured) string {
	typ, _, _ := unstructured.NestedString(svc.Object, "spec", "type")
	if typ == "" {
		typ = "ClusterIP"
	}
	ports, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	var ps []string
	for _, p := range ports {
		m, _ := p.(map[string]any)
		s := fmt.Sprint(m["port"])
		if tp, ok := m["targetPort"]; ok && fmt.Sprint(tp) != s {
			s += "→" + fmt.Sprint(tp)
		}
		ps = append(ps, s)
	}
	sel, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	selText := "none"
	if len(sel) > 0 {
		selText = labels.SelectorFromSet(labels.Set(sel)).String()
	}
	return fmt.Sprintf("type %s, ports %s, selector %s", typ, strings.Join(ps, ","), selText)
}

// lastCrash summarizes the previous instance's logs of one container. When
// the container has not restarted yet, the instance that ended is the current
// one, so it falls back to the current logs. It returns nil when the logs are
// not available.
func (r *Runner) lastCrash(ctx context.Context, ns, pod, container string, red *redact.Redactor, causeHasLast bool) (lines []string, lastError string) {
	lines, lastError, ok := r.containerLog(ctx, ns, pod, container, true, red, causeHasLast)
	if !ok {
		lines, lastError, _ = r.containerLog(ctx, ns, pod, container, false, red, causeHasLast)
	}
	return lines, lastError
}

// containerLog summarizes the last lines of one container's log (current or
// previous instance) and returns its last error line. When the cause already
// states that error, the summary leaves that line group out.
func (r *Runner) containerLog(ctx context.Context, ns, pod, container string, previous bool, red *redact.Redactor, causeHasLast bool) ([]string, string, bool) {
	raw, err := r.Reader.Logs(ctx, ns, pod, kube.LogOptions{Container: container, Previous: previous, TailLines: crashLogTail, LimitBytes: crashLogBytes})
	if err == nil && strings.HasPrefix(strings.TrimSpace(raw), logsBusyText) {
		// The runtime is between two instances of the container; ask once more.
		select {
		case <-ctx.Done():
		case <-time.After(400 * time.Millisecond):
		}
		raw, err = r.Reader.Logs(ctx, ns, pod, kube.LogOptions{Container: container, Previous: previous, TailLines: crashLogTail, LimitBytes: crashLogBytes})
	}
	if err != nil || strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), logsBusyText) {
		return nil, "", false
	}
	entries := shape.ParseLogs(raw)
	shape.ReadableJSON(entries)
	redactEntries(red, entries)
	entries = shape.Condense(entries) // a traceback is its exception line
	lastError := ""
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Error {
			lastError = shape.CutWords(shape.ErrorLine(entries[i]), 360)
			if causeHasLast {
				// The cause says it; the summary does not repeat it.
				tpl := shape.Template(entries[i].Text)
				kept := entries[:0:0]
				for _, e := range entries {
					if !(e.Error && shape.Template(e.Text) == tpl) {
						kept = append(kept, e)
					}
				}
				entries = kept
			}
			break
		}
	}
	s := shape.SummarizeLogs(entries, crashLogBudget)
	if s.Body == "" {
		return nil, lastError, true
	}
	return strings.Split(s.Body, "\n"), lastError, true
}

// commandText joins the elements of a command line for display. Each element
// is redacted by itself (a flag takes the whole next element as its value),
// then the joined text is redacted again, because a secret can run across
// elements. Cut the result only after this.
func commandText(red *redact.Redactor, argv []string) string {
	return red.Text(strings.Join(red.Args(argv), " "))
}

// cmdShown is how many bytes of a container command or arguments are shown.
const cmdShown = 200

// cutCommand shortens a long command at a word and says how much is left.
func cutCommand(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= cmdShown {
		return s
	}
	cut := strings.LastIndexByte(s[:cmdShown+1], ' ')
	if cut <= 0 {
		cut = cmdShown
	}
	return fmt.Sprintf("%s ... (%d more bytes; get shows all)", s[:cut], len(s)-cut)
}

// servicePortMismatch compares the target ports of a Service (a number or a
// name) with the container ports of the pods it selects and with the ports in
// its EndpointSlices. A name that no container declares leaves the
// EndpointSlices with endpoints and no ports: nothing routes, although the
// endpoints look ready.
func (r *Runner) servicePortMismatch(ctx context.Context, svc *unstructured.Unstructured, ns string) (causes []string, noPorts bool) {
	sel, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	ports, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	if len(sel) == 0 || len(ports) == 0 {
		return nil, false
	}
	pk, err := r.Kinds.Lookup("pods")
	if err != nil {
		return nil, false
	}
	pods, err := r.Reader.List(ctx, pk, ns, labels.SelectorFromSet(labels.Set(sel)).String(), 200) // the same read as serviceCause and the related pods: made once
	if err != nil || len(pods.Items) == 0 {
		return nil, false
	}
	declaredName := map[string]bool{}
	declaredNum := map[int]bool{}
	var declared []string
	for i := range pods.Items {
		p, ok := toPod(&pods.Items[i])
		if !ok {
			continue
		}
		for _, c := range p.Spec.Containers {
			for _, cp := range c.Ports {
				if !declaredNum[int(cp.ContainerPort)] {
					declaredNum[int(cp.ContainerPort)] = true
					d := fmt.Sprint(cp.ContainerPort)
					if cp.Name != "" {
						d = cp.Name + " " + d
					}
					declared = append(declared, d)
				}
				if cp.Name != "" {
					declaredName[cp.Name] = true
				}
			}
		}
	}
	sort.Strings(declared)
	declaredText := "none"
	if len(declared) > 0 {
		declaredText = strings.Join(declared, ", ")
	}

	endpoints, slicePorts := 0, 0
	if ek, err := r.Kinds.Lookup("endpointslices"); err == nil {
		if page, err := r.Reader.List(ctx, ek, ns, "kubernetes.io/service-name="+svc.GetName(), 50); err == nil {
			for _, s := range page.Items {
				eps, _, _ := unstructured.NestedSlice(s.Object, "endpoints")
				endpoints += len(eps)
				ps, _, _ := unstructured.NestedSlice(s.Object, "ports")
				slicePorts += len(ps)
			}
		}
	}

	var out []string
	for _, f := range targetPortFindings(ports, declaredName, declaredNum, declared) {
		s := f.text
		if f.byName && endpoints > 0 && slicePorts == 0 {
			s += fmt.Sprintf(" The EndpointSlices hold %s and no ports, so no traffic reaches them although they look ready.", plural(endpoints, "endpoint", "endpoints"))
		}
		out = append(out, s)
	}
	if len(out) == 0 && endpoints > 0 && slicePorts == 0 {
		out = append(out, fmt.Sprintf("the EndpointSlices of the service hold %s and no ports, so no traffic reaches them although they look ready. Compare the service targetPort with the container ports (declared: %s).", plural(endpoints, "endpoint", "endpoints"), declaredText))
	}
	return out, endpoints > 0 && slicePorts == 0
}

// logsBusyText is what the container runtime returns, with status 200, when
// a container is between two instances.
const logsBusyText = "unable to retrieve container logs for"

// schedulingCause says why no node accepts a pending pod. The scheduler's
// message is the authority for reasons that are not about room (taints, node
// selection, volumes), so those are given in full with what the pod asks
// for. Room is computed from requests only when the scheduler names it, or
// when the message has no reason that this function knows.
func (r *Runner) schedulingCause(ctx context.Context, p *corev1.Pod, msg string) string {
	place := placementCause(msg, p, r.rt)
	if place != "" && !strings.Contains(msg, "Insufficient") {
		return place
	}
	room := r.roomCause(ctx, p)
	if place == "" {
		return room
	}
	return strings.TrimSpace(place + " " + room)
}

var schedulerMsgRE = regexp.MustCompile(`^(\d+)/(\d+) nodes are available: (.*)$`)

// placementCause restates the scheduler's reasons that are not about room.
func placementCause(msg string, p *corev1.Pod, redact func(string) string) string {
	head, _, _ := strings.Cut(msg, " preemption:")
	m := schedulerMsgRE.FindStringSubmatch(strings.TrimSpace(head))
	if m == nil {
		return ""
	}
	// Reasons are joined with ", ". A part that does not start with a count
	// belongs to the reason before it.
	var reasons []string
	for _, part := range strings.Split(strings.TrimSuffix(strings.TrimSpace(m[3]), "."), ", ") {
		if len(reasons) > 0 && (part == "" || part[0] < '0' || part[0] > '9') {
			reasons[len(reasons)-1] += ", " + part
			continue
		}
		reasons = append(reasons, part)
	}
	var keep []string
	taint, selection := false, false
	for _, reason := range reasons {
		switch {
		case strings.Contains(reason, "Insufficient"):
			continue // roomCause explains it with numbers
		case strings.Contains(reason, "taint"):
			taint = true
		case strings.Contains(reason, "node affinity") || strings.Contains(reason, "node selector"):
			selection = true
		}
		keep = append(keep, reason)
	}
	if len(keep) == 0 {
		return ""
	}
	s := fmt.Sprintf("no node accepts the pod (%s of %s). The scheduler gives these reasons: %s.", m[1], m[2], shape.CutWords(redact(strings.Join(keep, "; ")), 900))
	if taint {
		if t := tolerationsText(p, redact); t != "" {
			s += " The pod tolerates only: " + t + "."
		} else {
			s += " The pod has no toleration for a taint."
		}
	}
	if selection {
		if t := nodeSelectionText(p, redact); t != "" {
			s += " The pod selects nodes with: " + t + "."
		}
	}
	return s
}

// tolerationsText lists the pod's tolerations, without the two that every
// pod gets for a node that is not ready or not reachable.
func tolerationsText(p *corev1.Pod, redact func(string) string) string {
	var out []string
	for _, t := range p.Spec.Tolerations {
		if t.Key == "node.kubernetes.io/not-ready" || t.Key == "node.kubernetes.io/unreachable" {
			continue
		}
		s := t.Key
		switch {
		case t.Key == "" && t.Operator == corev1.TolerationOpExists:
			s = "every taint"
		case t.Operator == corev1.TolerationOpExists:
			// the key alone
		default:
			s += "=" + t.Value
		}
		if t.Effect != "" {
			s += ":" + string(t.Effect)
		}
		out = append(out, s)
	}
	return shape.CutWords(redact(strings.Join(out, ", ")), 300)
}

// nodeSelectionText gives the pod's node selector and required node
// affinity in one line.
func nodeSelectionText(p *corev1.Pod, redact func(string) string) string {
	var out []string
	keys := make([]string, 0, len(p.Spec.NodeSelector))
	for k := range p.Spec.NodeSelector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+p.Spec.NodeSelector[k])
	}
	if a := p.Spec.Affinity; a != nil && a.NodeAffinity != nil && a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		var terms []string
		for _, term := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			var exprs []string
			for _, e := range append(append([]corev1.NodeSelectorRequirement(nil), term.MatchExpressions...), term.MatchFields...) {
				x := e.Key + " " + string(e.Operator)
				if len(e.Values) > 0 {
					x += " [" + strings.Join(e.Values, ", ") + "]"
				}
				exprs = append(exprs, x)
			}
			if len(exprs) > 0 {
				terms = append(terms, strings.Join(exprs, " and "))
			}
		}
		if len(terms) > 0 {
			out = append(out, "node affinity "+strings.Join(terms, " or "))
		}
	}
	return shape.CutWords(redact(strings.Join(out, ", ")), 400)
}
