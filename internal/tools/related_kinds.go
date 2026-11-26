package tools

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// --- cron jobs ---

// relCronJobs: the last Jobs of a CronJob and the state of each.
func (r *Runner) relCronJobs(ctx context.Context, cj *unstructured.Unstructured, ns string) relResult {
	var x relResult
	var mine []unstructured.Unstructured
	for _, j := range r.listOf(ctx, "jobs", ns, "", relatedListCap) {
		if ownedBy(&j, "CronJob", cj.GetName()) {
			mine = append(mine, j)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool {
		return mine[i].GetCreationTimestamp().After(mine[j].GetCreationTimestamp().Time)
	})
	if len(mine) > 3 {
		mine = mine[:3]
	}
	now := r.now()
	for i := range mine {
		j := &mine[i]
		row := shape.Inspect(j, now)
		if row.State == shape.Unhealthy || row.State == shape.Warning {
			x.bad("%s: %s", refName("Job", j.GetName()), strings.Join(row.Lines(r.rt), "; "))
		} else {
			x.good("recent job", 1)
		}
	}
	return x
}

// --- pods ---

// relInitContainers: every init container of the pod that has not completed:
// its state, its restarts and the last lines of its log.
func (r *Runner) relInitContainers(ctx context.Context, u *unstructured.Unstructured, ns string, red *redact.Redactor) relResult {
	var x relResult
	p, ok := toPod(u)
	if !ok {
		return x
	}
	now := r.now()
	type wait struct {
		name, state string
		restarts    int32
		started     bool
	}
	var waits []wait
	for _, cs := range p.Status.InitContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode == 0 {
			continue // completed
		}
		if isSidecar(p, cs.Name) {
			continue // a sidecar is not an init step; the container checks report its problems
		}
		w := wait{name: cs.Name, restarts: cs.RestartCount}
		switch {
		case cs.State.Running != nil:
			w.state = "running for " + shape.Age(cs.State.Running.StartedAt.Time, now)
			w.started = true
		case cs.State.Terminated != nil:
			t := cs.State.Terminated
			w.state = fmt.Sprintf("exited with code %d", t.ExitCode)
			if t.Reason != "" {
				w.state += " (" + t.Reason + ")"
			}
			w.started = true
		case cs.State.Waiting != nil:
			w.state = "waiting"
			if cs.State.Waiting.Reason != "" {
				w.state += ": " + cs.State.Waiting.Reason
			}
			w.started = cs.RestartCount > 0 || cs.LastTerminationState.Terminated != nil
		default:
			w.state = "not started"
		}
		waits = append(waits, w)
		if len(waits) == 2 {
			break
		}
	}
	// A pod that is only starting its first init container has no problem to show.
	logs := make([][]string, len(waits))
	fns := make([]func(), len(waits))
	for i, w := range waits {
		fns[i] = func() {
			if !w.started {
				return
			}
			lines, _ := r.lastCrash(ctx, ns, p.Name, w.name, red, false)
			if len(lines) > relatedLogLines {
				lines = lines[len(lines)-relatedLogLines:]
			}
			logs[i] = lines
		}
	}
	parallel(fns...)
	for i, w := range waits {
		line := fmt.Sprintf("init container %s has not completed: %s", w.name, w.state)
		if w.restarts > 0 {
			line += fmt.Sprintf(", %s", plural(int(w.restarts), "restart", "restarts"))
		}
		if len(logs[i]) > 0 {
			line += "; its last log lines:\n" + "    " + strings.Join(logs[i], "\n    ")
		}
		x.lines = append(x.lines, line)
	}
	return x
}

// --- services ---

// portFinding is one target port that the containers do not declare.
type portFinding struct {
	text   string
	byName bool
}

// targetPortFindings compares the target ports of a Service with the
// container ports that the selected pods declare. A name that no container
// declares is certain. A number that no container declares is a strong hint
// only, because a container can listen on a port that it does not declare.
func targetPortFindings(ports []any, declaredName map[string]bool, declaredNum map[int]bool, declared []string) []portFinding {
	declaredText := "none"
	if len(declared) > 0 {
		declaredText = strings.Join(declared, ", ")
	}
	var out []portFinding
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		label := fmt.Sprint(pm["port"])
		if n, ok := pm["name"].(string); ok && n != "" {
			label = n + " (" + label + ")"
		}
		switch tp := pm["targetPort"].(type) {
		case string:
			if tp != "" && !declaredName[tp] {
				out = append(out, portFinding{fmt.Sprintf("service port %s has targetPort %q, but the selected pods declare no container port with that name (declared: %s).", label, tp, declaredText), true})
			}
		case int64, int32, float64, int:
			n := int(reflect.ValueOf(tp).Convert(reflect.TypeOf(0.0)).Float())
			if len(declared) > 0 && !declaredNum[n] {
				out = append(out, portFinding{fmt.Sprintf("service port %s has targetPort %d, which no container port of the selected pods declares (declared: %s). Strong hint of a wrong port; it is fine only if the container listens on it without declaring it.", label, n, declaredText), false})
			}
		}
	}
	return out
}

// relServicePods: what the Service sends to. An ExternalName has no pods. A
// service whose selected pods have a problem shows it, even when some pod is
// ready.
func (r *Runner) relServicePods(ctx context.Context, svc *unstructured.Unstructured, ns string) relResult {
	var x relResult
	typ, _, _ := unstructured.NestedString(svc.Object, "spec", "type")
	if typ == "ExternalName" {
		name, _, _ := unstructured.NestedString(svc.Object, "spec", "externalName")
		x.lines = append(x.lines, fmt.Sprintf("external name: %s (a DNS alias; it has no pods and no endpoints)", name))
		return x
	}
	sel, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	if len(sel) == 0 {
		return x
	}
	pods := r.listOf(ctx, "pods", ns, labels.SelectorFromSet(labels.Set(sel)).String(), 200) // the same read as serviceCause: made once
	now := r.now()
	ready, bad := 0, 0
	for i := range pods {
		if p, ok := toPod(&pods[i]); ok && podReady(p) {
			ready++
			continue
		}
		if row := shape.Inspect(&pods[i], now); row.State == shape.Unhealthy || row.State == shape.Warning {
			bad++
			if bad <= 3 && len(row.Problems) > 0 && ready < len(pods) {
				x.bad("%s: %s", refName("Pod", row.Name), row.Lines(r.rt)[0])
			}
		}
	}
	// With no ready pod the cause already says why (serviceCause); do not say it twice.
	if ready == 0 {
		x.problems = nil
	}
	if len(pods) > 0 && bad == 0 {
		x.good("selected pod", len(pods))
	}
	return x
}

// --- network policies ---

// policyText says in a line or two what a NetworkPolicy lets in and out.
func policyText(np *unstructured.Unstructured) string {
	var p networkingv1.NetworkPolicy
	if !toTyped(np, &p) {
		return "(could not be read)"
	}
	types := map[networkingv1.PolicyType]bool{}
	for _, t := range p.Spec.PolicyTypes {
		types[t] = true
	}
	if len(types) == 0 {
		types[networkingv1.PolicyTypeIngress] = true
		if len(p.Spec.Egress) > 0 {
			types[networkingv1.PolicyTypeEgress] = true
		}
	}
	var parts []string
	if types[networkingv1.PolicyTypeIngress] {
		var rules []string
		for _, rl := range p.Spec.Ingress {
			rules = append(rules, ruleText(rl.From, rl.Ports, "from"))
		}
		parts = append(parts, directionText("ingress", rules))
	}
	if types[networkingv1.PolicyTypeEgress] {
		var rules []string
		for _, rl := range p.Spec.Egress {
			rules = append(rules, ruleText(rl.To, rl.Ports, "to"))
		}
		parts = append(parts, directionText("egress", rules))
	}
	return strings.Join(parts, "; ")
}

func directionText(name string, rules []string) string {
	switch {
	case len(rules) == 0:
		return name + ": nothing is allowed"
	case len(rules) > 3:
		return fmt.Sprintf("%s: %s; (%d more rules)", name, strings.Join(rules[:3], "; "), len(rules)-3)
	}
	return name + ": " + strings.Join(rules, "; ")
}

func ruleText(peers []networkingv1.NetworkPolicyPeer, ports []networkingv1.NetworkPolicyPort, prep string) string {
	who := prep + " anywhere"
	if len(peers) > 0 {
		var ps []string
		for _, p := range peers {
			ps = append(ps, peerText(p))
		}
		who = prep + " " + strings.Join(ps, " or ")
	}
	if len(ports) == 0 {
		return who + ", on every port"
	}
	var pt []string
	for _, p := range ports {
		s := "every port"
		if p.Port != nil {
			s = p.Port.String()
			if p.EndPort != nil {
				s += "-" + fmt.Sprint(*p.EndPort)
			}
		}
		proto := corev1.ProtocolTCP
		if p.Protocol != nil {
			proto = *p.Protocol
		}
		pt = append(pt, s+"/"+string(proto))
	}
	return who + ", on " + strings.Join(pt, ", ")
}

func peerText(p networkingv1.NetworkPolicyPeer) string {
	if p.IPBlock != nil {
		s := "block " + p.IPBlock.CIDR
		if len(p.IPBlock.Except) > 0 {
			s += " except " + strings.Join(p.IPBlock.Except, ", ")
		}
		return s
	}
	sel := func(s *metav1.LabelSelector) string {
		ls, err := metav1.LabelSelectorAsSelector(s)
		if err != nil || ls.Empty() {
			return ""
		}
		return ls.String()
	}
	var pod, ns string
	if p.PodSelector != nil {
		pod = sel(p.PodSelector)
	}
	if p.NamespaceSelector != nil {
		ns = sel(p.NamespaceSelector)
	}
	switch {
	case p.PodSelector != nil && p.NamespaceSelector != nil:
		a, b := "all pods", "all namespaces"
		if pod != "" {
			a = "pods " + pod
		}
		if ns != "" {
			b = "namespaces " + ns
		}
		return a + " in " + b
	case p.NamespaceSelector != nil:
		if ns == "" {
			return "all namespaces"
		}
		return "namespaces " + ns
	default:
		if pod == "" {
			return "all pods of the namespace"
		}
		return "pods " + pod
	}
}

// --- autoscalers ---

// relAutoscalerTarget: the target of an autoscaler exists, and how many
// replicas it has against the minimum and the maximum.
func (r *Runner) relAutoscalerTarget(ctx context.Context, h *unstructured.Unstructured, ns string) relResult {
	var x relResult
	kind, _, _ := unstructured.NestedString(h.Object, "spec", "scaleTargetRef", "kind")
	name, _, _ := unstructured.NestedString(h.Object, "spec", "scaleTargetRef", "name")
	apiVersion, _, _ := unstructured.NestedString(h.Object, "spec", "scaleTargetRef", "apiVersion")
	minR, hasMin := shape.NestedInt(h.Object, "spec", "minReplicas")
	if !hasMin {
		minR = 1
	}
	maxR, _ := shape.NestedInt(h.Object, "spec", "maxReplicas")
	gv, _ := schema.ParseGroupVersion(apiVersion)
	k, found := r.Kinds.ByGroupKind(gv.Group, kind)
	if !found {
		x.lines = append(x.lines, fmt.Sprintf("target: %s/%s (kind not in the allowlist), min %d, max %d", strings.ToLower(kind), name, minR, maxR))
	} else if t, err := r.Reader.Get(ctx, k, ns, name); err != nil {
		if isNotFound(err) {
			x.bad("%s: the target does not exist in namespace %s", refName(kind, name), ns)
		}
	} else {
		want, _ := shape.NestedInt(t.Object, "spec", "replicas")
		have, _ := shape.NestedInt(t.Object, "status", "replicas")
		ready, _ := shape.NestedInt(t.Object, "status", "readyReplicas")
		x.lines = append(x.lines, fmt.Sprintf("target %s/%s: %d replicas wanted, %d existing, %d ready; autoscaler min %d, max %d", strings.ToLower(kind), name, want, have, ready, minR, maxR))
	}
	if m := metricsLine(h); m != "" {
		x.lines = append(x.lines, m)
	}
	return x
}

// metricsLine shows the current value of each metric against its target.
func metricsLine(h *unstructured.Unstructured) string {
	cur, _, _ := unstructured.NestedSlice(h.Object, "status", "currentMetrics")
	if len(cur) == 0 {
		return "current metrics: none"
	}
	val := func(m map[string]any) string {
		for _, f := range []string{"averageUtilization", "averageValue", "value"} {
			if v, ok := m[f]; ok && v != nil {
				s := fmt.Sprint(v)
				if f == "averageUtilization" {
					s += "%"
				}
				return s
			}
		}
		return "?"
	}
	var parts []string
	for _, c := range cur {
		m, _ := c.(map[string]any)
		typ, _ := m["type"].(string)
		key := typ
		if typ != "" {
			key = strings.ToLower(typ[:1]) + typ[1:]
		}
		sub, _ := m[key].(map[string]any)
		name, _, _ := unstructured.NestedString(sub, "name")
		if name == "" {
			name, _, _ = unstructured.NestedString(sub, "metric", "name")
		}
		cm, _ := sub["current"].(map[string]any)
		parts = append(parts, fmt.Sprintf("%s %s", strings.ToLower(name), val(cm)))
	}
	return "current metrics: " + strings.Join(parts, ", ")
}

// --- claims ---

// relClaimStorage: the storage class and the volume of a claim.
func (r *Runner) relClaimStorage(ctx context.Context, c *unstructured.Unstructured) relResult {
	var x relResult
	class, set, _ := unstructured.NestedString(c.Object, "spec", "storageClassName")
	vol, _, _ := unstructured.NestedString(c.Object, "spec", "volumeName")
	var scs []unstructured.Unstructured
	var pv *unstructured.Unstructured
	parallel(
		func() {
			if !set || class != "" {
				scs = r.listOf(ctx, "storageclasses", "", "", 100)
			}
		},
		func() {
			if vol == "" {
				return
			}
			if k, err := r.Kinds.Lookup("persistentvolumes"); err == nil {
				if u, err := r.Reader.Get(ctx, k, "", vol); err == nil {
					pv = u
				}
			}
		},
	)
	phase, _, _ := unstructured.NestedString(c.Object, "status", "phase")
	if !set || class != "" {
		note := strings.TrimPrefix(classNote(class, scs), "; ")
		if phase != "Bound" || strings.Contains(note, "does not exist") || strings.Contains(note, "no storage class") {
			x.lines = append(x.lines, "storage class: "+note)
		}
	}
	if pv != nil {
		pvPhase, _, _ := unstructured.NestedString(pv.Object, "status", "phase")
		if _, text := r.rowProblems(pv); text != "" {
			x.bad("%s: %s", refName("PersistentVolume", vol), text)
		} else {
			x.lines = append(x.lines, fmt.Sprintf("volume %s: %s", vol, strings.ToLower(pvPhase)))
		}
	}
	return x
}

// --- ingresses ---

// relIngressBackends: the Services that an Ingress sends to exist, have the
// port, and have ready endpoints.
func (r *Runner) relIngressBackends(ctx context.Context, ing *unstructured.Unstructured, ns string) relResult {
	var x relResult
	type backend struct{ svc, port string }
	var backends []backend
	seen := map[backend]bool{}
	add := func(m map[string]any) {
		svc, _, _ := unstructured.NestedString(m, "service", "name")
		if svc == "" {
			return
		}
		port := ""
		if n, ok, _ := unstructured.NestedFieldNoCopy(m, "service", "port", "number"); ok && n != nil {
			port = fmt.Sprint(n)
		} else if s, _, _ := unstructured.NestedString(m, "service", "port", "name"); s != "" {
			port = s
		}
		b := backend{svc, port}
		if !seen[b] && len(backends) < 10 {
			seen[b] = true
			backends = append(backends, b)
		}
	}
	if m, found, _ := unstructured.NestedMap(ing.Object, "spec", "defaultBackend"); found {
		add(m)
	}
	rules, _, _ := unstructured.NestedSlice(ing.Object, "spec", "rules")
	for _, rl := range rules {
		rm, _ := rl.(map[string]any)
		paths, _, _ := unstructured.NestedSlice(rm, "http", "paths")
		for _, p := range paths {
			pm, _ := p.(map[string]any)
			if bm, ok := pm["backend"].(map[string]any); ok {
				add(bm)
			}
		}
	}
	if len(backends) == 0 {
		return x
	}
	var svcs, slices []unstructured.Unstructured
	slicesCut := false
	parallel(
		func() { svcs = r.listOf(ctx, "services", ns, "", relatedListCap) },
		func() { slices, slicesCut = r.listPageOf(ctx, "endpointslices", ns, "", fetchCap) },
	)
	if svcs == nil {
		return x
	}
	byName := map[string]*unstructured.Unstructured{}
	for i := range svcs {
		byName[svcs[i].GetName()] = &svcs[i]
	}
	ready := map[string]int{}
	for _, s := range slices {
		name := s.GetLabels()["kubernetes.io/service-name"]
		eps, _, _ := unstructured.NestedSlice(s.Object, "endpoints")
		for _, e := range eps {
			em, _ := e.(map[string]any)
			cond, _ := em["conditions"].(map[string]any)
			if rd, ok := cond["ready"].(bool); !ok || rd {
				ready[name]++
			}
		}
	}
	for _, b := range backends {
		s := byName[b.svc]
		if s == nil {
			x.bad("%s: the backend service does not exist in namespace %s", refName("Service", b.svc), ns)
			continue
		}
		if b.port != "" && !servicePortHas(s, b.port) {
			x.bad("%s: the service has no port %s (it has %s)", refName("Service", b.svc), b.port, servicePortsText(s))
			continue
		}
		sel, _, _ := unstructured.NestedStringMap(s.Object, "spec", "selector")
		if typ, _, _ := unstructured.NestedString(s.Object, "spec", "type"); slices != nil && len(sel) > 0 && typ != "ExternalName" && ready[b.svc] == 0 && !slicesCut {
			x.bad("%s: the backend service has no ready endpoints", refName("Service", b.svc))
			continue
		}
		x.good("backend service", 1)
	}
	return x
}

func servicePortHas(s *unstructured.Unstructured, port string) bool {
	ports, _, _ := unstructured.NestedSlice(s.Object, "spec", "ports")
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		if fmt.Sprint(pm["port"]) == port {
			return true
		}
		if n, _ := pm["name"].(string); n != "" && n == port {
			return true
		}
	}
	return false
}

func servicePortsText(s *unstructured.Unstructured) string {
	ports, _, _ := unstructured.NestedSlice(s.Object, "spec", "ports")
	var out []string
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		t := fmt.Sprint(pm["port"])
		if n, _ := pm["name"].(string); n != "" {
			t = n + " " + t
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// --- nodes ---

// relNodePods: how many pods run on a node and how many wait for it.
func (r *Runner) relNodePods(ctx context.Context, node *unstructured.Unstructured) relResult {
	var x relResult
	pods := r.listOf(ctx, "pods", "", "", fetchCap)
	if pods == nil {
		return x
	}
	total := 0
	var waiting []string
	for i := range pods {
		p, ok := toPod(&pods[i])
		if !ok || p.Spec.NodeName != node.GetName() {
			continue
		}
		total++
		// A pod that was created a moment ago has not had time to start.
		if p.Status.Phase == corev1.PodPending && r.now().Sub(p.CreationTimestamp.Time) >= 2*time.Minute {
			waiting = append(waiting, p.Namespace+"/"+p.Name)
		}
	}
	sort.Strings(waiting)
	line := fmt.Sprintf("pods on this node: %d", total)
	if len(waiting) > 0 {
		shown := waiting
		if len(shown) > relatedPeersMax {
			shown = shown[:relatedPeersMax]
		}
		x.bad("%s: %s assigned to it and still pending: %s", refName("Node", node.GetName()), plural(len(waiting), "pod is", "pods are"), strings.Join(shown, ", "))
	}
	x.lines = append(x.lines, line)
	return x
}

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }

// isSidecar reports whether the named init container of the pod is a native
// sidecar: an init container with restartPolicy Always. A sidecar runs for the
// life of the pod, so it is never "an init container that has not completed".
// A sidecar that crashes or waits with an error is a normal container problem:
// the container checks and podCauses report it.
func isSidecar(p *corev1.Pod, name string) bool {
	for _, c := range p.Spec.InitContainers {
		if c.Name == name {
			return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
		}
	}
	return false
}
