package tools

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// podFailing reports whether a pod has a problem, as the workload summary
// counts it.
func podFailing(u *unstructured.Unstructured, now time.Time) bool {
	st := shape.Inspect(u, now).State
	return st == shape.Unhealthy || st == shape.Warning
}

// redactor puts every value that is shown through the redactor.
type redactor struct{ r *redact.Redactor }

func (x redactor) text(s string) string { return x.r.Text(s) }

// kv redacts a value by its key (a label or annotation key can mark it secret).
func (x redactor) kv(k, v string) string { return x.kvCut(k, v, diffValueMax) }

// kvCut redacts a value by its key, then cuts it to about n bytes.
func (x redactor) kvCut(k, v string, n int) string {
	m := map[string]any{k: v}
	x.r.Object(m)
	s, _ := m[k].(string)
	return oneLine(s, n)
}

// list redacts command or args: flags such as --password=x are caught by name.
func (x redactor) list(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	parts := x.r.Args(v)
	// The joined text is redacted again: a secret can run across elements.
	return x.r.Text("[" + strings.Join(parts, " ") + "]")
}

// env redacts the value of an environment variable by its name. A reference
// to a ConfigMap or a Secret is a name, never a value.
func (x redactor) env(name string, v envVal) string {
	if v.isRef {
		return x.r.Text(v.text)
	}
	if v.text == "" {
		return `""`
	}
	m := map[string]any{"name": name, "value": v.text}
	x.r.Object(m)
	s, _ := m["value"].(string)
	return s
}

// sibPod is a pod of the workload and the facts that are compared.
type sibPod struct {
	pod     *corev1.Pod
	failing bool
	facts   map[string]string
}

// siblingDiff says what all failing pods of a workload share and no healthy
// pod has. It looks only at facts that the pod objects hold (node, revision,
// image, image ID, age, ordinal, restart policy, claim, QoS class): nothing
// is read for it. It returns nothing unless at least one pod fails and at
// least one is healthy. When no fact separates the two groups, it says so:
// that is a signal too.
func (r *Runner) siblingDiff(pods []unstructured.Unstructured, statefulSet bool) []string {
	now := r.now()
	var fail, ok []*sibPod
	for i := range pods {
		p, good := toPod(&pods[i])
		if !good {
			continue
		}
		switch shape.Inspect(&pods[i], now).State {
		case shape.Unhealthy, shape.Warning:
			fail = append(fail, &sibPod{pod: p, failing: true, facts: podFacts(p)})
		case shape.Healthy:
			ok = append(ok, &sibPod{pod: p, facts: podFacts(p)})
		}
	}
	if len(fail) == 0 || len(ok) == 0 {
		return nil
	}
	nf, nh := len(fail), len(ok)
	all := append(append([]*sibPod(nil), fail...), ok...)

	type sline struct{ group, text, val string }
	var sl []sline
	compared := map[string]bool{}
	note := func(what string) {
		compared[what] = true
	}

	// Facts held as one value each. A fact that a pod lacks is not compared.
	var keys []string
	have := map[string]bool{}
	for _, p := range all {
		for k := range p.facts {
			if !have[k] {
				have[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return factOrder(keys[i]) < factOrder(keys[j]) || (factOrder(keys[i]) == factOrder(keys[j]) && keys[i] < keys[j])
	})
	for _, k := range keys {
		complete := true
		for _, p := range all {
			if _, found := p.facts[k]; !found {
				complete = false
			}
		}
		if !complete {
			continue
		}
		if k == "claim" {
			// A claim of each pod of its own (a StatefulSet) says nothing.
			distinct := map[string]bool{}
			for _, p := range all {
				distinct[p.facts[k]] = true
			}
			if len(distinct) == len(all) {
				continue
			}
		}
		note(factGroup(k))
		fv := map[string]bool{}
		for _, p := range fail {
			fv[p.facts[k]] = true
		}
		if len(fv) != 1 {
			continue
		}
		val := fail[0].facts[k]
		shared := false
		for _, p := range ok {
			if p.facts[k] == val {
				shared = true
			}
		}
		if shared {
			continue
		}
		// One failing pod is a pattern only when the healthy pods agree with
		// each other on one other value: with a value of its own on each pod
		// (a node each), the failing pod differs from the rest in the way that
		// any pod does.
		if nf < 2 {
			hv := map[string]bool{}
			for _, p := range ok {
				hv[p.facts[k]] = true
			}
			if len(hv) != 1 {
				continue
			}
		}
		who := fmt.Sprintf("in all %d failing pods", nf)
		if nf == 1 {
			who = "in the failing pod"
		}
		none := fmt.Sprintf("in none of the %d healthy pods", nh)
		if nh == 1 {
			none = "in the healthy pod"
		}
		sl = append(sl, sline{factGroup(k), fmt.Sprintf("%s: %s %s, %s", k, val, who, none), val})
	}

	// Age: two groups that do not overlap in time. Needs three pods and a
	// gap, or any split of a few pods would look like a pattern.
	if len(all) >= 3 {
		note("age")
		minF, maxF := fail[0].pod.CreationTimestamp.Time, fail[0].pod.CreationTimestamp.Time
		minH, maxH := ok[0].pod.CreationTimestamp.Time, ok[0].pod.CreationTimestamp.Time
		for _, p := range fail {
			t := p.pod.CreationTimestamp.Time
			if t.Before(minF) {
				minF = t
			}
			if t.After(maxF) {
				maxF = t
			}
		}
		for _, p := range ok {
			t := p.pod.CreationTimestamp.Time
			if t.Before(minH) {
				minH = t
			}
			if t.After(maxH) {
				maxH = t
			}
		}
		switch {
		case minF.Sub(maxH) > diffMinGap:
			sl = append(sl, sline{"age", fmt.Sprintf("age: every failing pod is newer than every healthy pod (failing: at most %s old; healthy: at least %s old)", shape.Age(minF, now), shape.Age(maxH, now)), ""})
		case minH.Sub(maxF) > diffMinGap:
			sl = append(sl, sline{"age", fmt.Sprintf("age: every failing pod is older than every healthy pod (failing: at least %s old; healthy: at most %s old)", shape.Age(maxF, now), shape.Age(minH, now)), ""})
		}
	}

	// The ordinal of the pods of a StatefulSet.
	if statefulSet {
		fo, fok := ordinals(fail)
		ho, hok := ordinals(ok)
		if fok && hok {
			note("ordinal")
			if l := ordinalPattern(fo, ho); l != "" {
				sl = append(sl, sline{"ordinal", l, ""})
			}
		}
	}

	// A revision that separates the groups says the image, the image ID and the
	// age already: they are its consequence. They are folded into its line.
	for i := range sl {
		if sl[i].group != "revision" {
			continue
		}
		var rest []sline
		text := sl[i].text
		for _, s := range sl {
			switch s.group {
			case "image":
				if !strings.Contains(text, "(image ") {
					text += " (image " + s.val + ")"
				}
			case "image ID", "age":
			case "revision":
				rest = append(rest, s)
			default:
				rest = append(rest, s)
			}
		}
		for j := range rest {
			if rest[j].group == "revision" {
				rest[j].text = text
			}
		}
		sl = rest
		break
	}
	if len(sl) == 0 {
		var names []string
		for _, g := range []string{"node", "revision", "image", "image ID", "claim", "ordinal", "age", "restart policy", "QoS class"} {
			if compared[g] {
				names = append(names, g)
			}
		}
		if len(names) == 0 {
			return nil
		}
		return []string{fmt.Sprintf("no difference in %s between the %d failing and the %d healthy pods", joinOr(names), nf, nh)}
	}
	out := []string{"the failing pods differ from the healthy ones in:"}
	for i, s := range sl {
		if i == diffLinesMax {
			out = append(out, fmt.Sprintf("- (%d more)", len(sl)-diffLinesMax))
			break
		}
		out = append(out, "- "+s.text)
	}
	return out
}

func joinOr(in []string) string {
	switch len(in) {
	case 1:
		return in[0]
	case 2:
		return in[0] + " or " + in[1]
	}
	return strings.Join(in[:len(in)-1], ", ") + " or " + in[len(in)-1]
}

// factOrder puts the facts in the order of their importance.
func factOrder(k string) int {
	switch {
	case k == "node":
		return 0
	case k == "revision":
		return 1
	case strings.HasPrefix(k, "image ID "):
		return 3
	case strings.HasPrefix(k, "image "):
		return 2
	case k == "claim":
		return 4
	case k == "restart policy":
		return 5
	}
	return 6
}

// factGroup is the short name of a fact for the "no difference" line.
func factGroup(k string) string {
	switch {
	case strings.HasPrefix(k, "image ID "):
		return "image ID"
	case strings.HasPrefix(k, "image "):
		return "image"
	}
	return k
}

// podFacts holds the cheap facts of a pod, as text, by name.
func podFacts(p *corev1.Pod) map[string]string {
	f := map[string]string{}
	if p.Spec.NodeName != "" {
		f["node"] = p.Spec.NodeName
	}
	for _, key := range []string{"pod-template-hash", "controller-revision-hash"} {
		if v := p.Labels[key]; v != "" {
			f["revision"] = v
		}
	}
	for _, c := range p.Spec.Containers {
		f["image "+c.Name] = c.Image
	}
	for _, cs := range p.Status.ContainerStatuses {
		if _, digest, ok := strings.Cut(cs.ImageID, "@"); ok && digest != "" {
			f["image ID "+cs.Name] = shortDigest(digest)
		}
	}
	if p.Spec.RestartPolicy != "" {
		f["restart policy"] = string(p.Spec.RestartPolicy)
	}
	var claims []string
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			claims = append(claims, v.PersistentVolumeClaim.ClaimName)
		}
	}
	sort.Strings(claims)
	if len(claims) > 0 {
		f["claim"] = strings.Join(claims, ", ")
	}
	if p.Status.QOSClass != "" {
		f["QoS class"] = string(p.Status.QOSClass)
	}
	return f
}

func shortDigest(d string) string {
	if algo, hex, ok := strings.Cut(d, ":"); ok && len(hex) > 12 {
		return algo + ":" + hex[:12]
	}
	return d
}

// ordinals reads the number at the end of the pod names ("web-7" is 7).
func ordinals(pods []*sibPod) ([]int, bool) {
	var out []int
	for _, p := range pods {
		i := strings.LastIndex(p.pod.Name, "-")
		n, err := strconv.Atoi(p.pod.Name[i+1:])
		if i < 0 || err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, true
}

// ordinalPattern finds what the failing ordinals have in common that no
// healthy ordinal has: they are all above (or all below) the healthy ones. A
// shared remainder for some divisor is not claimed: it has no general
// justification, and with enough divisors some remainder always fits.
func ordinalPattern(fail, ok []int) string {
	minF, maxF := fail[0], fail[len(fail)-1]
	minH, maxH := ok[0], ok[len(ok)-1]
	switch {
	case minF > maxH:
		return fmt.Sprintf("ordinal: all failing pods are above every healthy one (failing %s; the highest healthy is %d)", ints(fail), maxH)
	case maxF < minH:
		return fmt.Sprintf("ordinal: all failing pods are below every healthy one (failing %s; the lowest healthy is %d)", ints(fail), minH)
	}
	return ""
}

func ints(in []int) string {
	parts := make([]string, 0, len(in))
	for i, n := range in {
		if i == 6 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ", ")
}
