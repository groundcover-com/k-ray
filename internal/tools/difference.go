package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// Differences as signals. In a cluster the signal is often a difference:
// this revision fails and the one before it worked; these pods fail and those
// do not. Two blocks of describe state such a difference:
//
//   - what changed in the revision of a Deployment (revisionDiff), against the
//     revision before it;
//   - what the failing pods of a workload have in common that no healthy pod
//     has (siblingDiff).
//
// Both use objects that the call has read already (the pods of the workload,
// and the ReplicaSets that the related-objects code lists with the same
// arguments: the call reader makes that read once). They add no read. They
// print only when the difference is a signal; a healthy, old rollout prints
// nothing. They state what was compared, and do not guess a cause.

const (
	diffLinesMax     = 8 // lines of each block; the rest is counted
	diffYoungRollout = time.Hour
	diffMinGap       = time.Minute // the age of two pods differs by less: no signal
	diffValueMax     = 80          // bytes of one value shown
)

const (
	revisionAnnotation = "deployment.kubernetes.io/revision"
	desiredAnnotation  = "deployment.kubernetes.io/desired-replicas"
	restartedAtKey     = "kubectl.kubernetes.io/restartedAt"
)

// change is one line of a revision difference. The lines with the lowest prio
// come first.
type change struct {
	prio int
	text string
}

const (
	prioImage = iota
	prioCommand
	prioEnv
	prioProbe
	prioResources
	prioMount
	prioSecurity
	prioOther
)

// revisionOf reads the revision number of a Deployment or a ReplicaSet.
func revisionOf(u *unstructured.Unstructured) int {
	n, _ := strconv.Atoi(u.GetAnnotations()[revisionAnnotation])
	return n
}

// podsOf returns the pods that the ReplicaSet owns.
func podsOwnedBy(pods []unstructured.Unstructured, kind, name string) []*corev1.Pod {
	var out []*corev1.Pod
	for i := range pods {
		if !ownedBy(&pods[i], kind, name) {
			continue
		}
		if p, ok := toPod(&pods[i]); ok {
			out = append(out, p)
		}
	}
	return out
}

// revisionDiff says what changed in the newest revision of a Deployment,
// against the revision before it. It returns nothing when the difference is
// no signal, or when there is no earlier revision to compare with.
func (r *Runner) revisionDiff(ctx context.Context, d *unstructured.Unstructured, ns string, w workloadPods, rd *redact.Redactor) []string {
	sel := workloadSelector(d)
	if sel == "" {
		return nil
	}
	// The same read as the related-objects code makes: once per call.
	all := r.listOf(ctx, "replicasets", ns, sel, relatedListCap)
	var sets []*unstructured.Unstructured
	for i := range all {
		if ownedBy(&all[i], "Deployment", d.GetName()) && revisionOf(&all[i]) > 0 {
			sets = append(sets, &all[i])
		}
	}
	sort.Slice(sets, func(i, j int) bool { return revisionOf(sets[i]) > revisionOf(sets[j]) })
	cur := 0
	if want := revisionOf(d); want > 0 {
		for i, s := range sets {
			if revisionOf(s) == want {
				cur = i
				break
			}
		}
	}
	if cur+1 >= len(sets) {
		return nil // the first revision: nothing to compare with
	}
	newRS, oldRS := sets[cur], sets[cur+1]
	newRev, oldRev := revisionOf(newRS), revisionOf(oldRS)

	now := r.now()
	curPods := podsOwnedBy(w.pods, "ReplicaSet", newRS.GetName())
	oldPods := podsOwnedBy(w.pods, "ReplicaSet", oldRS.GetName())
	curReady, oldReady := 0, 0
	newBad := false
	for _, p := range curPods {
		if podReady(p) {
			curReady++
		}
	}
	for _, p := range oldPods {
		if podReady(p) {
			oldReady++
		}
	}
	for i := range w.pods {
		if ownedBy(&w.pods[i], "ReplicaSet", newRS.GetName()) && podFailing(&w.pods[i], now) {
			newBad = true
		}
	}

	// A signal: a problem, a young rollout, or a rollout that is not complete.
	st := shape.Inspect(d, now).State
	problem := st == shape.Unhealthy || st == shape.Warning || newBad
	young := now.Sub(newRS.GetCreationTimestamp().Time) < diffYoungRollout
	if !problem && !young && rolloutComplete(d) {
		return nil
	}

	oldT, ok1 := rsTemplate(oldRS)
	newT, ok2 := rsTemplate(newRS)
	if !ok1 || !ok2 {
		return nil
	}
	changes, restarted := diffTemplates(oldT, newT, redactor{rd})
	if v := oldRS.GetAnnotations()[desiredAnnotation]; v != "" {
		if nv := newRS.GetAnnotations()[desiredAnnotation]; nv != "" && nv != v {
			changes = append(changes, change{prioOther, fmt.Sprintf("replicas: %s -> %s", v, nv)})
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].prio < changes[j].prio })
	// An old revision that changed nothing but a restart time is not a signal:
	// the pods fail for a reason that is not in the template.
	if len(changes) == 0 && !young && rolloutComplete(d) {
		return nil
	}

	head := fmt.Sprintf("changed in revision %d (%s ago), against revision %d:", newRev, ageText(newRS, now), oldRev)
	out := []string{head}
	switch {
	case restarted != "" && len(changes) == 0:
		out = append(out, "- restarted at "+restarted+", no other change")
	case len(changes) == 0:
		out = append(out, "- nothing differs in image, command, env, probes, resources, mounts, security context, labels or annotations (the pod template differs only in fields not compared)")
	default:
		var lines []string
		if restarted != "" {
			lines = append(lines, "restarted at "+restarted)
		}
		for _, c := range changes {
			lines = append(lines, c.text)
		}
		for i, l := range lines {
			if i == diffLinesMax {
				out = append(out, fmt.Sprintf("- (%d more changes not shown)", len(lines)-diffLinesMax))
				break
			}
			out = append(out, "- "+l)
		}
	}
	if oldReady > 0 && curReady < len(curPods) {
		// Pods that show no failure signal (no error, no restart) are only
		// not ready yet: say how long they have been starting, not a fault.
		wait := ""
		if t, quiet := startingQuietly(curPods); quiet {
			wait = fmt.Sprintf(" yet (started %s ago)", shape.Age(t, now))
		}
		switch {
		case curReady == 0 && wait != "":
			out = append(out, fmt.Sprintf("the pods of revision %d are not ready%s; revision %d has %s", newRev, wait, oldRev, plural(oldReady, "ready pod", "ready pods")))
		case curReady == 0:
			out = append(out, fmt.Sprintf("the pods of revision %d are not ready; revision %d has %s", newRev, oldRev, plural(oldReady, "ready pod", "ready pods")))
		case wait != "":
			out = append(out, fmt.Sprintf("only %d of %d pods of revision %d are ready, the others are not ready%s; revision %d has %s", curReady, len(curPods), newRev, wait, oldRev, plural(oldReady, "ready pod", "ready pods")))
		default:
			out = append(out, fmt.Sprintf("only %d of %d pods of revision %d are ready; revision %d has %s", curReady, len(curPods), newRev, oldRev, plural(oldReady, "ready pod", "ready pods")))
		}
	}
	return out
}

// startingQuietly reports whether the pods that are not ready show no failure
// signal: no restart, no container waiting with an error or terminated with
// one, and a pod that is scheduled and not failed. It returns the start of the
// oldest of them.
func startingQuietly(pods []*corev1.Pod) (time.Time, bool) {
	var oldest time.Time
	for _, p := range pods {
		if podReady(p) {
			continue
		}
		if p.Status.Phase == corev1.PodFailed {
			return time.Time{}, false
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				return time.Time{}, false
			}
		}
		for _, cs := range append(append([]corev1.ContainerStatus(nil), p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			if cs.RestartCount > 0 || cs.LastTerminationState.Terminated != nil {
				return time.Time{}, false
			}
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
				return time.Time{}, false
			}
			if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" && w.Reason != "PodInitializing" {
				return time.Time{}, false
			}
		}
		if t := p.CreationTimestamp.Time; oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	return oldest, !oldest.IsZero()
}

func ageText(u *unstructured.Unstructured, now time.Time) string {
	return shape.Age(u.GetCreationTimestamp().Time, now)
}

// rolloutComplete reports whether every replica is updated, available, and
// no old replica is left.
func rolloutComplete(d *unstructured.Unstructured) bool {
	want, hasWant := shape.NestedInt(d.Object, "spec", "replicas")
	if !hasWant {
		want = 1
	}
	updated, _ := shape.NestedInt(d.Object, "status", "updatedReplicas")
	total, _ := shape.NestedInt(d.Object, "status", "replicas")
	avail, _ := shape.NestedInt(d.Object, "status", "availableReplicas")
	gen, _ := shape.NestedInt(d.Object, "metadata", "generation")
	seen, _ := shape.NestedInt(d.Object, "status", "observedGeneration")
	return updated >= want && total <= updated && avail >= want && seen >= gen
}

func rsTemplate(rs *unstructured.Unstructured) (corev1.PodTemplateSpec, bool) {
	var t corev1.PodTemplateSpec
	m, found, _ := unstructured.NestedMap(rs.Object, "spec", "template")
	if !found {
		return t, false
	}
	return t, runtime.DefaultUnstructuredConverter.FromUnstructured(m, &t) == nil
}

// diffTemplates compares two pod templates and returns one change for each
// difference that is not noise. restarted is the new restart time when the
// restart annotation changed.
func diffTemplates(a, b corev1.PodTemplateSpec, red redactor) (out []change, restarted string) {
	add := func(prio int, format string, args ...any) {
		out = append(out, change{prio, fmt.Sprintf(format, args...)})
	}
	show := func(s string) string {
		s = red.text(s)
		if s == "" {
			return "none"
		}
		return oneLine(s, diffValueMax)
	}

	// Containers, matched by name.
	type named struct {
		label string
		c     corev1.Container
	}
	index := func(spec corev1.PodSpec) (map[string]named, []string) {
		m := map[string]named{}
		var order []string
		for _, c := range spec.InitContainers {
			m["init:"+c.Name] = named{"init container " + c.Name, c}
			order = append(order, "init:"+c.Name)
		}
		for _, c := range spec.Containers {
			m["c:"+c.Name] = named{"container " + c.Name, c}
			order = append(order, "c:"+c.Name)
		}
		return m, order
	}
	am, aorder := index(a.Spec)
	bm, border := index(b.Spec)
	for _, k := range border {
		nb := bm[k]
		na, found := am[k]
		if !found {
			add(prioImage, "%s added (image %s)", nb.label, show(nb.c.Image))
			continue
		}
		diffContainer(na.label, na.c, nb.c, red, show, add)
	}
	for _, k := range aorder {
		if _, found := bm[k]; !found {
			add(prioImage, "%s removed (image %s)", am[k].label, show(am[k].c.Image))
		}
	}

	// Mounted ConfigMaps, Secrets and claims, by volume, by name.
	av, bv := volumeRefs(a.Spec), volumeRefs(b.Spec)
	for _, name := range sortedKeys(bv) {
		switch old, found := av[name]; {
		case !found:
			add(prioMount, "mount added: %s (volume %s)", bv[name], name)
		case old != bv[name]: // text and kind both
			add(prioMount, "mount of volume %s: %s -> %s", name, old, bv[name])
		}
	}
	for _, name := range sortedKeys(av) {
		if _, found := bv[name]; !found {
			add(prioMount, "mount removed: %s (volume %s)", av[name], name)
		}
	}

	// Security context of the pod, and the other fields of the pod spec.
	for _, l := range flatDiff("pod securityContext", flatOf(a.Spec.SecurityContext), flatOf(b.Spec.SecurityContext), red) {
		add(prioSecurity, "%s", l)
	}
	if a.Spec.ServiceAccountName != b.Spec.ServiceAccountName {
		add(prioOther, "serviceAccountName: %s -> %s", show(a.Spec.ServiceAccountName), show(b.Spec.ServiceAccountName))
	}
	for _, l := range flatDiff("nodeSelector", flatOf(a.Spec.NodeSelector), flatOf(b.Spec.NodeSelector), red) {
		add(prioOther, "%s", l)
	}

	// Labels and annotations of the template. The hash is noise; a restart
	// is a change of its own.
	restarted = b.Annotations[restartedAtKey]
	if restarted == a.Annotations[restartedAtKey] {
		restarted = ""
	}
	noise := func(k string) bool {
		return k == "pod-template-hash" || k == restartedAtKey || k == "kubectl.kubernetes.io/last-applied-configuration"
	}
	for _, kind := range []string{"label", "annotation"} {
		x, y := a.Labels, b.Labels
		if kind == "annotation" {
			x, y = a.Annotations, b.Annotations
		}
		for _, k := range sortedKeys(y) {
			if noise(k) {
				continue
			}
			if old, found := x[k]; !found {
				add(prioOther, "%s %s added: %s", kind, k, red.kv(k, y[k]))
			} else if old != y[k] {
				if o, n := red.kv(k, old), red.kv(k, y[k]); o == n {
					add(prioOther, "%s %s changed (value hidden by redaction)", kind, k)
				} else {
					add(prioOther, "%s %s: %s -> %s", kind, k, o, n)
				}
			}
		}
		for _, k := range sortedKeys(x) {
			if _, found := y[k]; !found && !noise(k) {
				add(prioOther, "%s %s removed (was %s)", kind, k, red.kv(k, x[k]))
			}
		}
	}
	return out, restarted
}

// diffContainer compares two containers of the same name.
func diffContainer(label string, a, b corev1.Container, red redactor, show func(string) string, add func(int, string, ...any)) {
	if a.Image != b.Image {
		add(prioImage, "%s image: %s -> %s", label, show(a.Image), show(b.Image))
	}
	for _, f := range []struct {
		name string
		x, y []string
	}{{"command", a.Command, b.Command}, {"args", a.Args, b.Args}} {
		if strings.Join(f.x, "\x00") == strings.Join(f.y, "\x00") {
			continue
		}
		o, n := red.list(f.x), red.list(f.y)
		if o == n {
			add(prioCommand, "%s %s changed (value hidden by redaction)", label, f.name)
			continue
		}
		add(prioCommand, "%s %s: %s -> %s", label, f.name, oneLine(o, 160), oneLine(n, 160))
	}

	// Env: by name. A value is shown only through the redactor.
	av, bv := envMap(a), envMap(b)
	for _, name := range sortedKeys(bv) {
		switch old, found := av[name]; {
		case !found:
			add(prioEnv, "%s env %s added: %s", label, name, oneLine(red.env(name, bv[name]), diffValueMax))
		case old != bv[name]:
			if o, n := red.env(name, old), red.env(name, bv[name]); o == n {
				add(prioEnv, "%s env %s changed (value hidden by redaction)", label, name)
			} else {
				add(prioEnv, "%s env %s: %s -> %s", label, name, oneLine(o, diffValueMax), oneLine(n, diffValueMax))
			}
		}
	}
	for _, name := range sortedKeys(av) {
		if _, found := bv[name]; !found {
			add(prioEnv, "%s env %s removed (was %s)", label, name, oneLine(red.env(name, av[name]), diffValueMax))
		}
	}
	ae, be := envFromRefs(a), envFromRefs(b)
	for _, r := range sortedKeys(be) {
		if !ae[r] {
			add(prioEnv, "%s env from %s added", label, r)
		}
	}
	for _, r := range sortedKeys(ae) {
		if !be[r] {
			add(prioEnv, "%s env from %s removed", label, r)
		}
	}

	// Probes.
	for _, p := range []struct {
		name string
		x, y *corev1.Probe
	}{{"liveness", a.LivenessProbe, b.LivenessProbe}, {"readiness", a.ReadinessProbe, b.ReadinessProbe}, {"startup", a.StartupProbe, b.StartupProbe}} {
		if o, n := probeSummary(p.x, red), probeSummary(p.y, red); o != n {
			add(prioProbe, "%s %s probe: %s -> %s", label, p.name, o, n)
		}
	}

	// Resources.
	var res []string
	for _, set := range []struct {
		name string
		x, y corev1.ResourceList
	}{{"requests", a.Resources.Requests, b.Resources.Requests}, {"limits", a.Resources.Limits, b.Resources.Limits}} {
		names := map[string]bool{}
		for k := range set.x {
			names[string(k)] = true
		}
		for k := range set.y {
			names[string(k)] = true
		}
		for _, k := range sortedKeys(names) {
			ox, oy := "none", "none"
			qx, hx := set.x[corev1.ResourceName(k)]
			qy, hy := set.y[corev1.ResourceName(k)]
			if hx {
				ox = qx.String()
			}
			if hy {
				oy = qy.String()
			}
			if hx != hy || (hx && qx.Cmp(qy) != 0) {
				res = append(res, fmt.Sprintf("%s %s %s -> %s", set.name, k, ox, oy))
			}
		}
	}
	if len(res) > 0 {
		add(prioResources, "%s resources: %s", label, strings.Join(res, "; "))
	}

	for _, l := range flatDiff(label+" securityContext", flatOf(a.SecurityContext), flatOf(b.SecurityContext), red) {
		add(prioSecurity, "%s", l)
	}
}

// envVal is the value of an environment variable, or a description of where
// it comes from. A reference is a name, never a value, so it is not redacted
// by the name of the variable; a value always is. The flag is typed on
// purpose: a text marker inside the value could be written by the cluster.
type envVal struct {
	text  string
	isRef bool
}

func envMap(c corev1.Container) map[string]envVal {
	m := map[string]envVal{}
	for _, e := range c.Env {
		switch {
		case e.ValueFrom == nil:
			m[e.Name] = envVal{text: e.Value}
		case e.ValueFrom.SecretKeyRef != nil:
			m[e.Name] = envVal{"from secret " + e.ValueFrom.SecretKeyRef.Name + " key " + e.ValueFrom.SecretKeyRef.Key, true}
		case e.ValueFrom.ConfigMapKeyRef != nil:
			m[e.Name] = envVal{"from configmap " + e.ValueFrom.ConfigMapKeyRef.Name + " key " + e.ValueFrom.ConfigMapKeyRef.Key, true}
		case e.ValueFrom.FieldRef != nil:
			m[e.Name] = envVal{"from field " + e.ValueFrom.FieldRef.FieldPath, true}
		default:
			m[e.Name] = envVal{"from a resource field", true}
		}
	}
	return m
}

func envFromRefs(c corev1.Container) map[string]bool {
	m := map[string]bool{}
	for _, e := range c.EnvFrom {
		switch {
		case e.SecretRef != nil:
			m["secret "+e.SecretRef.Name] = true
		case e.ConfigMapRef != nil:
			m["configmap "+e.ConfigMapRef.Name] = true
		}
	}
	return m
}

// volumeRefs maps a volume name to the ConfigMap, Secret or claim it holds,
// by name. Secrets are named, never read.
func volumeRefs(spec corev1.PodSpec) map[string]string {
	m := map[string]string{}
	for _, v := range spec.Volumes {
		switch {
		case v.ConfigMap != nil:
			m[v.Name] = "configmap " + v.ConfigMap.Name
		case v.Secret != nil:
			m[v.Name] = "secret " + v.Secret.SecretName
		case v.PersistentVolumeClaim != nil:
			m[v.Name] = "claim " + v.PersistentVolumeClaim.ClaimName
		}
	}
	return m
}

// probeText says what a probe does in one short phrase; "none" for no probe.
// Timings that the API server fills in are written as their defaults, so that
// a probe that spells them out and one that does not compare equal.
func probeSummary(p *corev1.Probe, red redactor) string {
	if p == nil {
		return "none"
	}
	var what string
	switch {
	case p.HTTPGet != nil:
		what = fmt.Sprintf("http %s:%s", red.text(p.HTTPGet.Path), p.HTTPGet.Port.String())
	case p.TCPSocket != nil:
		what = "tcp :" + p.TCPSocket.Port.String()
	case p.Exec != nil:
		what = "exec " + oneLine(red.list(p.Exec.Command), 80)
	case p.GRPC != nil:
		what = fmt.Sprintf("grpc :%d", p.GRPC.Port)
	default:
		what = "probe"
	}
	def := func(v, d int32) int32 {
		if v == 0 {
			return d
		}
		return v
	}
	return fmt.Sprintf("%s (delay %ds, period %ds, timeout %ds, failures %d)", what,
		p.InitialDelaySeconds, def(p.PeriodSeconds, 10), def(p.TimeoutSeconds, 1), def(p.FailureThreshold, 3))
}

// flatOf turns a struct or a map into "path" -> "value", skipping empty values.
func flatOf(v any) map[string]string {
	out := map[string]string{}
	b, err := json.Marshal(v)
	if err != nil {
		return out
	}
	var x any
	if json.Unmarshal(b, &x) != nil {
		return out
	}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case nil:
		case map[string]any:
			for k, vv := range t {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, vv)
			}
		case string:
			out[prefix] = t
		default:
			s, _ := json.Marshal(t)
			out[prefix] = string(s)
		}
	}
	walk("", x)
	return out
}

func flatDiff(label string, a, b map[string]string, red redactor) []string {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var out []string
	for _, k := range sortedKeys(keys) {
		x, hx := a[k]
		y, hy := b[k]
		if hx && hy && x == y {
			continue
		}
		// A value is redacted (by its path too) before it is cut.
		if !hx {
			x = "unset"
		} else {
			x = red.kvCut(k, x, 60)
		}
		if !hy {
			y = "unset"
		} else {
			y = red.kvCut(k, y, 60)
		}
		out = append(out, fmt.Sprintf("%s.%s: %s -> %s", label, k, x, y))
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
