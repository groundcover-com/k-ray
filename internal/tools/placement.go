package tools

import (
	corev1 "k8s.io/api/core/v1"
)

// The room check (roomCause) compares the pod's requests with the free room
// of the nodes. A node that the pod cannot be placed on must not count: a
// tainted node that has room is not a node for this pod. This file evaluates
// the rules that are plain to evaluate from the pod and the node object:
// taints against tolerations (NoSchedule and NoExecute), nodeSelector, and
// required node affinity with In, NotIn, Exists and DoesNotExist. A rule that
// it does not evaluate is reported by unevaluatedPlacement; then the caller
// must not say that a node "has room".

// nodeAccepts reports whether the pod passes the evaluated placement rules
// for the node.
func nodeAccepts(p *corev1.Pod, n *corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
			continue // PreferNoSchedule is a preference
		}
		tolerated := false
		for _, tol := range p.Spec.Tolerations {
			if tolerates(tol, t) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	for k, v := range p.Spec.NodeSelector {
		if got, ok := n.Labels[k]; !ok || got != v {
			return false
		}
	}
	if req := requiredNodeTerms(p); len(req) > 0 {
		any := false
		for _, term := range req {
			all := true
			for _, e := range term.MatchExpressions {
				if !expressionHolds(e, n.Labels) {
					all = false
					break
				}
			}
			if all {
				any = true
				break
			}
		}
		if !any {
			return false
		}
	}
	return true
}

func requiredNodeTerms(p *corev1.Pod) []corev1.NodeSelectorTerm {
	if a := p.Spec.Affinity; a != nil && a.NodeAffinity != nil && a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		return a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	}
	return nil
}

// tolerates is the match of a toleration with a taint.
func tolerates(tol corev1.Toleration, t corev1.Taint) bool {
	if tol.Effect != "" && tol.Effect != t.Effect {
		return false
	}
	switch tol.Operator {
	case corev1.TolerationOpExists:
		return tol.Key == "" || tol.Key == t.Key
	case corev1.TolerationOpEqual, "":
		return tol.Key == t.Key && tol.Value == t.Value
	}
	return true // a comparison operator is not evaluated: assume it matches
}

func expressionHolds(e corev1.NodeSelectorRequirement, labels map[string]string) bool {
	got, has := labels[e.Key]
	switch e.Operator {
	case corev1.NodeSelectorOpIn:
		for _, v := range e.Values {
			if has && got == v {
				return true
			}
		}
		return false
	case corev1.NodeSelectorOpNotIn:
		for _, v := range e.Values {
			if has && got == v {
				return false
			}
		}
		return true
	case corev1.NodeSelectorOpExists:
		return has
	case corev1.NodeSelectorOpDoesNotExist:
		return !has
	}
	return true // Gt and Lt are not evaluated (see unevaluatedPlacement)
}

// unevaluatedPlacement reports whether the pod has a placement rule that
// nodeAccepts does not evaluate: a field selector or a Gt or Lt expression in
// its node affinity, pod affinity or anti-affinity that is required, a
// topology spread constraint that must hold, or a toleration with a comparison
// operator. The pod may be refused by a node that nodeAccepts accepts.
func unevaluatedPlacement(p *corev1.Pod) bool {
	for _, c := range p.Spec.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == corev1.DoNotSchedule {
			return true
		}
	}
	if a := p.Spec.Affinity; a != nil {
		if a.PodAffinity != nil && len(a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			return true
		}
		if a.PodAntiAffinity != nil && len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			return true
		}
	}
	for _, term := range requiredNodeTerms(p) {
		if len(term.MatchFields) > 0 {
			return true
		}
		for _, e := range term.MatchExpressions {
			if e.Operator == corev1.NodeSelectorOpGt || e.Operator == corev1.NodeSelectorOpLt {
				return true
			}
		}
	}
	for _, tol := range p.Spec.Tolerations {
		if tol.Operator != corev1.TolerationOpExists && tol.Operator != corev1.TolerationOpEqual && tol.Operator != "" {
			return true
		}
	}
	return false
}
