package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// One hop along the references. describe looks at the objects that the
// described object refers to, or that refer to it, in the same call, and
// prints only what is wrong there. Healthy relations are one summary line.
//
// Every read goes through r.Reader (the reader of the call: bounded, and
// each read made once). The relations of one object are read at the same
// time. A kind that is not in the allowlist, or a read that fails,
// is skipped without a word: the answer is then only smaller.

const (
	relatedLinesMax   = 8   // problem lines of the related objects, in one description
	relatedConfigMaps = 3   // ConfigMaps read for the keys they must hold; a bound on the reads
	relatedPeersMax   = 6   // names shown for a group of objects
	relatedLogLines   = 6   // lines of log shown for a container that waits
	relatedListCap    = 200 // objects read in one list of related objects
)

// relResult is what one relation found.
type relResult struct {
	problems []string // "kind/name: what is wrong"
	ok       []string // healthy things, as "kind" once each
	lines    []string // blocks that are shown whether or not there is a problem
	secrets  []string // Secret names that are referenced (never read)
}

func (x *relResult) bad(format string, args ...any) {
	x.problems = append(x.problems, fmt.Sprintf(format, args...))
}

func (x *relResult) good(kind string, n int) {
	for i := 0; i < n; i++ {
		x.ok = append(x.ok, kind)
	}
}

// related is the merged result of every relation of one object.
type related struct {
	problems []string
	okLine   string
	lines    []string
	notRead  string // "related, not read: ...": the reads that failed
}

// podShape is what a pod, or the pod template of a workload, looks like.
type podShape struct {
	labels map[string]string
	spec   corev1.PodSpec
}

func podShapeOf(u *unstructured.Unstructured) (podShape, bool) {
	if u.GetKind() == "Pod" {
		p, ok := toPod(u)
		if !ok {
			return podShape{}, false
		}
		return podShape{p.Labels, p.Spec}, true
	}
	spec, ok := templatePodSpec(u)
	if !ok {
		return podShape{}, false
	}
	path := []string{"spec", "template", "metadata", "labels"}
	if u.GetKind() == "CronJob" {
		path = []string{"spec", "jobTemplate", "spec", "template", "metadata", "labels"}
	}
	l, _, _ := unstructured.NestedStringMap(u.Object, path...)
	return podShape{l, spec}, true
}

// listOf reads the objects of a kind by its plural name. It returns nil when
// the kind is not allowed or the read fails.
func (r *Runner) listOf(ctx context.Context, resource, ns, sel string, limit int) []unstructured.Unstructured {
	k, err := r.Kinds.Lookup(resource)
	if err != nil {
		return nil
	}
	if !k.Namespaced() {
		ns = ""
	}
	page, err := r.Reader.List(ctx, k, ns, sel, limit)
	if err != nil {
		noteFailure(ctx, resource, err)
		return nil
	}
	return page.Items
}

// listPageOf is listOf that also says whether the list was cut at the cap.
func (r *Runner) listPageOf(ctx context.Context, resource, ns, sel string, limit int) ([]unstructured.Unstructured, bool) {
	k, err := r.Kinds.Lookup(resource)
	if err != nil {
		return nil, false
	}
	if !k.Namespaced() {
		ns = ""
	}
	page, err := r.Reader.List(ctx, k, ns, sel, limit)
	if err != nil {
		noteFailure(ctx, resource, err)
		return nil, false
	}
	return page.Items, page.More
}

// related finds the objects around u that have a problem. The relations that
// apply to the kind run at the same time.
func (r *Runner) related(ctx context.Context, k kinds.Kind, u *unstructured.Unstructured, ns string, red *redact.Redactor, brief bool) related {
	ctx = lowPriority(ctx) // the diagnosis of the object itself goes first
	failed := &readFailures{}
	ctx = withFailures(ctx, failed)
	var fns []func() relResult
	switch k.Kind() {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
		ps, ok := podShapeOf(u)
		if !ok {
			return related{}
		}
		if k.Kind() == "Deployment" {
			fns = append(fns, func() relResult { return r.relReplicaSets(ctx, u, ns) })
		}
		if k.Kind() == "CronJob" {
			fns = append(fns, func() relResult { return r.relCronJobs(ctx, u, ns) })
		}
		if k.Kind() == "Deployment" || k.Kind() == "StatefulSet" || k.Kind() == "ReplicaSet" {
			fns = append(fns, func() relResult { return r.relQuotas(ctx, u, ns) })
		}
		fns = append(fns,
			func() relResult { return r.relClaims(ctx, ps, ns) },
			func() relResult { return r.relConfigMaps(ctx, ps, ns) },
			func() relResult { return relSecrets(ps) },
			func() relResult { return r.relAutoscalers(ctx, u, ns) },
			func() relResult { return r.relBudgets(ctx, ps, ns) },
			func() relResult { return r.relServicesOf(ctx, ps, ns) },
			func() relResult { return r.relPolicies(ctx, ps, ns) },
		)
	case "Pod":
		ps, ok := podShapeOf(u)
		if !ok {
			return related{}
		}
		// A short block reads the init container itself (briefCauses): one log read.
		if !brief {
			fns = append(fns, func() relResult { return r.relInitContainers(ctx, u, ns, red) })
		}
		fns = append(fns, func() relResult { return r.relPolicies(ctx, ps, ns) })
	case "Service":
		fns = append(fns, func() relResult { return r.relServicePods(ctx, u, ns) })
	case "NetworkPolicy":
		fns = append(fns, func() relResult { return r.relPolicyPods(ctx, u, ns) })
	case "HorizontalPodAutoscaler":
		fns = append(fns, func() relResult { return r.relAutoscalerTarget(ctx, u, ns) })
	case "PersistentVolumeClaim":
		fns = append(fns, func() relResult { return r.relClaimStorage(ctx, u) })
	case "Ingress":
		fns = append(fns, func() relResult { return r.relIngressBackends(ctx, u, ns) })
	case "Node":
		fns = append(fns, func() relResult { return r.relNodePods(ctx, u) })
	}
	if len(fns) == 0 {
		return related{}
	}
	parts := make([]relResult, len(fns))
	run := make([]func(), len(fns))
	for i := range fns {
		run[i] = func() { parts[i] = fns[i]() }
	}
	parallel(run...)
	out := mergeRelated(parts)
	out.notRead = failed.line()
	return out
}

// mergeRelated joins the relations in their fixed order.
func mergeRelated(parts []relResult) related {
	var out related
	counts := map[string]int{}
	var order, secrets []string
	seenSecret := map[string]bool{}
	for _, p := range parts {
		out.problems = append(out.problems, p.problems...)
		out.lines = append(out.lines, p.lines...)
		for _, o := range p.ok {
			if counts[o] == 0 {
				order = append(order, o)
			}
			counts[o]++
		}
		for _, s := range p.secrets {
			if !seenSecret[s] {
				seenSecret[s] = true
				secrets = append(secrets, s)
			}
		}
	}
	var items []string
	for _, o := range order {
		items = append(items, plural(counts[o], o, o+"s"))
	}
	line := ""
	if len(items) > 0 {
		line = "related, no problem: " + strings.Join(items, ", ")
	}
	if len(secrets) > 0 {
		sort.Strings(secrets)
		more := ""
		if len(secrets) > relatedPeersMax {
			more = fmt.Sprintf(" and %d more", len(secrets)-relatedPeersMax)
			secrets = secrets[:relatedPeersMax]
		}
		s := "secrets referenced, names only, never read: " + strings.Join(secrets, ", ") + more
		if line == "" {
			line = "related: " + s
		} else {
			line += "; " + s
		}
	}
	out.okLine = line
	return out
}

// write adds the related objects to a description.
func (x related) write(b *strings.Builder) {
	if len(x.problems) > 0 {
		b.WriteString("related, with a problem:\n")
		shown := x.problems
		if len(shown) > relatedLinesMax {
			shown = shown[:relatedLinesMax]
		}
		for _, p := range shown {
			b.WriteString("- " + p + "\n")
		}
		if more := len(x.problems) - len(shown); more > 0 {
			fmt.Fprintf(b, "- (%d more)\n", more)
		}
	}
	if x.okLine != "" {
		b.WriteString(x.okLine + "\n")
	}
	if x.notRead != "" {
		b.WriteString(x.notRead + "\n")
	}
	for _, l := range x.lines {
		b.WriteString(l + "\n")
	}
}

func refName(kind, name string) string { return strings.ToLower(kind) + "/" + name }

// rowProblems is the state and the problems of an object that is not a pod.
func (r *Runner) rowProblems(u *unstructured.Unstructured) (shape.State, string) {
	row := shape.Inspect(u, r.now())
	if row.State != shape.Unhealthy && row.State != shape.Warning {
		return row.State, ""
	}
	return row.State, strings.Join(row.Lines(r.rt), "; ")
}

// ownedBy reports whether the controller of u is the named object.
func ownedBy(u *unstructured.Unstructured, kind, name string) bool {
	ref := controllerRef(u)
	return ref != nil && ref.Kind == kind && ref.Name == name
}

// --- workload relations ---

// relReplicaSets: a ReplicaSet of a Deployment that cannot create its pods
// says why in its condition and in its events (a quota, a limit range).
func (r *Runner) relReplicaSets(ctx context.Context, d *unstructured.Unstructured, ns string) relResult {
	var x relResult
	sel := workloadSelector(d)
	if sel == "" {
		return x
	}
	var active []unstructured.Unstructured
	for _, rs := range r.listOf(ctx, "replicasets", ns, sel, relatedListCap) {
		if want, _ := shape.NestedInt(rs.Object, "spec", "replicas"); want > 0 && ownedBy(&rs, "Deployment", d.GetName()) {
			active = append(active, rs)
		}
	}
	bad := 0
	for i := range active {
		rs := &active[i]
		row := shape.Inspect(rs, r.now())
		// "N of M replicas ready" only repeats the Deployment: a ReplicaSet is
		// shown when its condition says more.
		var said []string
		for _, p := range row.Lines(r.rt) {
			if !replicasReadyRE.MatchString(p) {
				said = append(said, p)
			}
		}
		text := strings.Join(said, "; ")
		if text == "" {
			if row.State == shape.Healthy {
				x.good("replicaset", 1)
			}
			continue
		}
		line := refName("ReplicaSet", rs.GetName()) + ": " + text
		if bad < 2 { // the events of a ReplicaSet say what the condition may not
			groups, _ := r.objectEventGroups(ctx, rs, ns)
			for _, g := range groups {
				if g.typ != "Warning" {
					continue
				}
				msg := oneLine(strings.TrimPrefix(cleanMessage(r.rt(g.message)), "Error creating: "), 220)
				if !strings.Contains(text, msg[:min(40, len(msg))]) {
					line += fmt.Sprintf("; event %s: %s", g.reason, msg)
					if g.count > 1 {
						line += fmt.Sprintf(" (×%d)", g.count)
					}
				}
				break
			}
		}
		bad++
		x.bad("%s", line)
	}
	return x
}

var replicasReadyRE = regexp.MustCompile(`^\d+ of \d+ replicas ready$`)

// relQuotas names the quotas of the namespace that block this workload, when
// it has fewer pods than it wants. There must be evidence: a FailedCreate event
// of its ReplicaSet (or of the workload itself) that names the quota, or usage
// at the hard limit of a resource that pods consume (pods, cpu, memory) and
// that the workload uses. A limit for objects of another kind (ConfigMaps,
// Services), or a limit of 0 on a resource that the workload does not request,
// says nothing about this workload. A quota with a scope counts only some pods:
// its usage proves nothing, only an event does.
func (r *Runner) relQuotas(ctx context.Context, u *unstructured.Unstructured, ns string) relResult {
	var x relResult
	want, ok := shape.NestedInt(u.Object, "spec", "replicas")
	have, _ := shape.NestedInt(u.Object, "status", "replicas")
	if !ok || want <= 0 || have >= want {
		return x
	}
	quotas := r.listOf(ctx, "resourcequotas", ns, "", 50)
	if len(quotas) == 0 {
		return x
	}
	spec, _ := templatePodSpec(u)
	named := r.quotaNamedByEvents(ctx, u, ns)
	for _, q := range quotas {
		hard, _, _ := unstructured.NestedStringMap(q.Object, "status", "hard")
		used, _, _ := unstructured.NestedStringMap(q.Object, "status", "used")
		scoped := false
		if sc, _, _ := unstructured.NestedSlice(q.Object, "spec", "scopes"); len(sc) > 0 {
			scoped = true
		}
		if _, found, _ := unstructured.NestedMap(q.Object, "spec", "scopeSelector"); found {
			scoped = true
		}
		var full []string
		names := make([]string, 0, len(hard))
		for n := range hard {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if scoped || !quotaResourceUsed(n, spec) {
				continue
			}
			h, err1 := resource.ParseQuantity(hard[n])
			us, err2 := resource.ParseQuantity(used[n])
			if err1 == nil && err2 == nil && us.Cmp(h) >= 0 {
				full = append(full, fmt.Sprintf("%s used %s of %s", n, used[n], hard[n]))
			}
		}
		event := named[q.GetName()]
		switch {
		case len(full) > 0 && event != "":
			x.bad("%s at its limit (the workload has %d of %d pods): %s; %s", refName("ResourceQuota", q.GetName()), have, want, strings.Join(full, ", "), event)
		case len(full) > 0:
			x.bad("%s at its limit (the workload has %d of %d pods): %s", refName("ResourceQuota", q.GetName()), have, want, strings.Join(full, ", "))
		case event != "":
			x.bad("%s: %s (the workload has %d of %d pods)", refName("ResourceQuota", q.GetName()), event, have, want)
		}
	}
	return x
}

// quotaResourceUsed reports whether a quota entry is about something that the
// pods of the workload use: the number of pods, or a CPU or memory resource
// that a container of the template requests or limits.
func quotaResourceUsed(name string, spec corev1.PodSpec) bool {
	switch name {
	case "pods", "count/pods":
		return true
	}
	kind, res := "requests", name
	if k, r, ok := strings.Cut(name, "."); ok && (k == "requests" || k == "limits") {
		kind, res = k, r
	}
	if res != "cpu" && res != "memory" {
		return false
	}
	for _, c := range append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...) {
		list := c.Resources.Requests
		if kind == "limits" {
			list = c.Resources.Limits
		}
		if q, ok := list[corev1.ResourceName(res)]; ok && !q.IsZero() {
			return true
		}
	}
	return false
}

// quotaNamedByEvents reads the FailedCreate events of the ReplicaSets of a
// Deployment (the active ones, at most two), or of the workload itself, and
// returns, by quota name, the event that names it ("exceeded quota: NAME").
func (r *Runner) quotaNamedByEvents(ctx context.Context, u *unstructured.Unstructured, ns string) map[string]string {
	targets := []*unstructured.Unstructured{u}
	if u.GetKind() == "Deployment" {
		targets = nil
		sel := workloadSelector(u)
		for _, rs := range r.listOf(ctx, "replicasets", ns, sel, relatedListCap) {
			if want, _ := shape.NestedInt(rs.Object, "spec", "replicas"); want > 0 && ownedBy(&rs, "Deployment", u.GetName()) && len(targets) < 2 {
				targets = append(targets, &rs)
			}
		}
	}
	out := map[string]string{}
	for _, t := range targets {
		groups, _ := r.objectEventGroups(ctx, t, ns)
		for _, g := range groups {
			if g.reason != "FailedCreate" {
				continue
			}
			msg := r.rt(g.message)
			i := strings.Index(msg, "exceeded quota: ")
			if i < 0 {
				continue
			}
			name := msg[i+len("exceeded quota: "):]
			if j := strings.IndexAny(name, ", "); j >= 0 {
				name = name[:j]
			}
			if _, seen := out[name]; !seen && name != "" {
				out[name] = "named by a FailedCreate event"
			}
		}
	}
	return out
}

// claimNames are the claims a pod template mounts.
func claimNames(ps podShape) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range ps.spec.Volumes {
		if v.PersistentVolumeClaim != nil && !seen[v.PersistentVolumeClaim.ClaimName] {
			seen[v.PersistentVolumeClaim.ClaimName] = true
			out = append(out, v.PersistentVolumeClaim.ClaimName)
		}
	}
	return out
}

// relClaims: the claims that a template mounts are bound, and the storage
// class of a claim that is not exists.
func (r *Runner) relClaims(ctx context.Context, ps podShape, ns string) relResult {
	var x relResult
	names := claimNames(ps)
	if len(names) == 0 {
		return x
	}
	have := map[string]*unstructured.Unstructured{}
	items := r.listOf(ctx, "persistentvolumeclaims", ns, "", relatedListCap)
	for i := range items {
		have[items[i].GetName()] = &items[i]
	}
	var classes *[]unstructured.Unstructured
	for _, n := range names {
		c := have[n]
		if c == nil {
			x.bad("%s: does not exist in namespace %s", refName("PersistentVolumeClaim", n), ns)
			continue
		}
		state, text := r.rowProblems(c)
		if text == "" {
			if state == shape.Healthy {
				x.good("claim", 1)
			}
			continue
		}
		line := refName("PersistentVolumeClaim", n) + ": " + text
		line += r.storageClassNote(ctx, c, &classes)
		x.bad("%s", line)
	}
	return x
}

// storageClassNote says whether the storage class of a claim exists, and
// names the classes that do.
func (r *Runner) storageClassNote(ctx context.Context, claim *unstructured.Unstructured, cache **[]unstructured.Unstructured) string {
	class, set, _ := unstructured.NestedString(claim.Object, "spec", "storageClassName")
	if set && class == "" {
		return "; it asks for no storage class, so only a volume that already exists can be bound"
	}
	if *cache == nil {
		items := r.listOf(ctx, "storageclasses", "", "", 100)
		*cache = &items
	}
	return classNote(class, **cache)
}

func classNote(class string, scs []unstructured.Unstructured) string {
	var names []string
	def := ""
	for _, s := range scs {
		names = append(names, s.GetName())
		if s.GetAnnotations()["storageclass.kubernetes.io/is-default-class"] == "true" {
			def = s.GetName()
		}
	}
	sort.Strings(names)
	list := "storage classes that exist: " + strings.Join(names, ", ")
	if len(names) == 0 {
		list = "the cluster has no storage class"
	}
	switch {
	case class == "" && def != "":
		return "; no storage class is set, and the default class is " + def
	case class == "":
		return "; no storage class is set and there is no default class (" + list + ")"
	}
	for _, n := range names {
		if n == class {
			return fmt.Sprintf("; storage class %s exists", class)
		}
	}
	return fmt.Sprintf("; storage class %q does not exist (%s)", class, list)
}

type cmRef struct {
	name     string
	keys     map[string]bool
	optional bool
}

// configMapRefs are the ConfigMaps a pod spec refers to, with the keys that
// it needs from each.
func configMapRefs(spec corev1.PodSpec) []*cmRef {
	idx := map[string]*cmRef{}
	var order []*cmRef
	get := func(name string, optional *bool) *cmRef {
		c := idx[name]
		if c == nil {
			c = &cmRef{name: name, keys: map[string]bool{}, optional: true}
			idx[name], order = c, append(order, c)
		}
		if optional == nil || !*optional {
			c.optional = false
		}
		return c
	}
	containers := append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...)
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
				ref := e.ValueFrom.ConfigMapKeyRef
				get(ref.Name, ref.Optional).keys[ref.Key] = true
			}
		}
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				get(e.ConfigMapRef.Name, e.ConfigMapRef.Optional)
			}
		}
	}
	for _, v := range spec.Volumes {
		if v.ConfigMap != nil {
			c := get(v.ConfigMap.Name, v.ConfigMap.Optional)
			for _, it := range v.ConfigMap.Items {
				c.keys[it.Key] = true
			}
		}
	}
	return order
}

// relConfigMaps: the ConfigMaps that a template refers to exist and hold the
// keys that it asks for. Only the key names are looked at.
func (r *Runner) relConfigMaps(ctx context.Context, ps podShape, ns string) relResult {
	var x relResult
	refs := configMapRefs(ps.spec)
	k, err := r.Kinds.Lookup("configmaps")
	if err != nil || len(refs) == 0 {
		return x
	}
	if len(refs) > relatedConfigMaps {
		refs = refs[:relatedConfigMaps]
	}
	type result struct {
		keys map[string]bool
		err  error
	}
	res := make([]result, len(refs))
	fns := make([]func(), len(refs))
	for i, ref := range refs {
		fns[i] = func() {
			u, err := r.Reader.Get(ctx, k, ns, ref.name)
			if err != nil || u == nil {
				res[i].err = err
				return
			}
			res[i].keys = map[string]bool{}
			for _, f := range []string{"data", "binaryData"} {
				m, _, _ := unstructured.NestedMap(u.Object, f)
				for key := range m {
					res[i].keys[key] = true
				}
			}
		}
	}
	parallel(fns...)
	for i, ref := range refs {
		switch {
		case res[i].err != nil && apierrors.IsNotFound(res[i].err):
			if !ref.optional {
				x.bad("%s: does not exist in namespace %s, and the template needs it", refName("ConfigMap", ref.name), ns)
			}
		case res[i].err != nil:
			// Not readable: say nothing.
		default:
			var missing []string
			for key := range ref.keys {
				if !res[i].keys[key] {
					missing = append(missing, key)
				}
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				x.bad("%s: exists, but has no key %s", refName("ConfigMap", ref.name), strings.Join(missing, ", "))
			} else {
				x.good("configmap", 1)
			}
		}
	}
	return x
}

// relSecrets names the Secrets that a template refers to. No Secret is
// read: the names come from the references.
func relSecrets(ps podShape) relResult {
	var x relResult
	add := func(n string) {
		if n != "" {
			x.secrets = append(x.secrets, n)
		}
	}
	containers := append(append([]corev1.Container(nil), ps.spec.InitContainers...), ps.spec.Containers...)
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				add(e.ValueFrom.SecretKeyRef.Name)
			}
		}
		for _, e := range c.EnvFrom {
			if e.SecretRef != nil {
				add(e.SecretRef.Name)
			}
		}
	}
	for _, v := range ps.spec.Volumes {
		if v.Secret != nil {
			add(v.Secret.SecretName)
		}
	}
	for _, s := range ps.spec.ImagePullSecrets {
		add(s.Name)
	}
	return x
}

// relAutoscalers: the autoscalers that target the workload.
func (r *Runner) relAutoscalers(ctx context.Context, u *unstructured.Unstructured, ns string) relResult {
	var x relResult
	for _, h := range r.listOf(ctx, "horizontalpodautoscalers", ns, "", relatedListCap) {
		kind, _, _ := unstructured.NestedString(h.Object, "spec", "scaleTargetRef", "kind")
		name, _, _ := unstructured.NestedString(h.Object, "spec", "scaleTargetRef", "name")
		if kind != u.GetKind() || name != u.GetName() {
			continue
		}
		state, text := r.rowProblems(&h)
		if text != "" {
			x.bad("%s: %s", refName("HorizontalPodAutoscaler", h.GetName()), text)
		} else if state == shape.Healthy {
			x.good("autoscaler", 1)
		}
	}
	return x
}

// relBudgets: the disruption budgets that select the pods.
func (r *Runner) relBudgets(ctx context.Context, ps podShape, ns string) relResult {
	var x relResult
	for _, b := range r.listOf(ctx, "poddisruptionbudgets", ns, "", relatedListCap) {
		m, found, _ := unstructured.NestedMap(b.Object, "spec", "selector")
		if !found {
			continue
		}
		sel, err := labelSelectorOf(m)
		if err != nil || !sel.Matches(labels.Set(ps.labels)) {
			continue
		}
		state, text := r.rowProblems(&b)
		if text != "" {
			x.bad("%s: %s", refName("PodDisruptionBudget", b.GetName()), text)
		} else if state == shape.Healthy {
			x.good("disruption budget", 1)
			if shape.BudgetForbidsByItself(&b) {
				x.lines = append(x.lines, fmt.Sprintf("%s allows no disruptions by its own setting", refName("PodDisruptionBudget", b.GetName())))
			}
		}
	}
	return x
}

// declaredPorts are the container ports a pod template declares.
func declaredPorts(spec corev1.PodSpec) (names map[string]bool, nums map[int]bool, text []string) {
	names, nums = map[string]bool{}, map[int]bool{}
	for _, c := range append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...) {
		for _, p := range c.Ports {
			if !nums[int(p.ContainerPort)] {
				nums[int(p.ContainerPort)] = true
				d := fmt.Sprint(p.ContainerPort)
				if p.Name != "" {
					d = p.Name + " " + d
				}
				text = append(text, d)
			}
			if p.Name != "" {
				names[p.Name] = true
			}
		}
	}
	sort.Strings(text)
	return names, nums, text
}

// relServicesOf: the Services that select the pods. A service whose
// targetPort the containers do not declare is a problem.
func (r *Runner) relServicesOf(ctx context.Context, ps podShape, ns string) relResult {
	var x relResult
	names, nums, text := declaredPorts(ps.spec)
	for _, s := range r.listOf(ctx, "services", ns, "", relatedListCap) {
		sel, _, _ := unstructured.NestedStringMap(s.Object, "spec", "selector")
		if len(sel) == 0 || !labels.SelectorFromSet(labels.Set(sel)).Matches(labels.Set(ps.labels)) {
			continue
		}
		ports, _, _ := unstructured.NestedSlice(s.Object, "spec", "ports")
		finds := targetPortFindings(ports, names, nums, text)
		if len(finds) == 0 {
			x.good("service", 1)
			continue
		}
		for _, f := range finds {
			x.bad("%s: %s", refName("Service", s.GetName()), f.text)
		}
	}
	return x
}

// --- network policies ---

// relPolicies lists the NetworkPolicies that select the pods, with what
// each one lets in and out. A caller that is blocked is seen from the policies
// of the target.
func (r *Runner) relPolicies(ctx context.Context, ps podShape, ns string) relResult {
	var x relResult
	items := r.listOf(ctx, "networkpolicies", ns, "", relatedListCap)
	var lines []string
	for _, np := range items {
		m, found, _ := unstructured.NestedMap(np.Object, "spec", "podSelector")
		if !found {
			continue
		}
		sel, err := labelSelectorOf(m)
		if err != nil || !sel.Matches(labels.Set(ps.labels)) {
			continue
		}
		lines = append(lines, "- "+np.GetName()+": "+policyText(&np))
	}
	if len(lines) > 0 {
		if len(lines) > relatedPeersMax {
			lines = append(lines[:relatedPeersMax], fmt.Sprintf("- (%d more)", len(lines)-relatedPeersMax))
		}
		x.lines = append(x.lines, "network policies that select these pods:\n"+strings.Join(lines, "\n"))
	}
	return x
}

// relPolicyPods: the pods that a NetworkPolicy selects.
func (r *Runner) relPolicyPods(ctx context.Context, np *unstructured.Unstructured, ns string) relResult {
	var x relResult
	m, _, _ := unstructured.NestedMap(np.Object, "spec", "podSelector")
	sel, err := labelSelectorOf(m)
	if err != nil {
		return x
	}
	items := r.listOf(ctx, "pods", ns, sel.String(), relatedListCap)
	var names []string
	for _, p := range items {
		names = append(names, p.GetName())
	}
	sort.Strings(names)
	line := fmt.Sprintf("selects %s", plural(len(names), "pod", "pods"))
	if sel.Empty() {
		line = "selects every pod in the namespace: " + plural(len(names), "pod", "pods")
	}
	if len(names) > 0 {
		shown := names
		if len(shown) > relatedPeersMax {
			shown = shown[:relatedPeersMax]
		}
		line += ": " + strings.Join(shown, ", ")
		if len(names) > len(shown) {
			line += fmt.Sprintf(" and %d more", len(names)-len(shown))
		}
	}
	x.lines = append(x.lines, line, "rules: "+policyText(np))
	return x
}

// toTyped converts an object into its Kubernetes type.
func toTyped(u *unstructured.Unstructured, into any) bool {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into) == nil
}
