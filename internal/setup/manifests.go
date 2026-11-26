// Package setup holds init and doctor. A person runs them with the person's
// own credentials; they are not MCP tools. This is the only package
// that builds clients able to send requests other than GET, and a
// guard test proves the server never imports it.
package setup

import (
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kinds"
)

// Names of the objects that init creates. All derive from brand.Name.
func Namespace() string      { return brand.Name }
func ServiceAccount() string { return brand.Name + "-reader" }
func RoleName() string       { return brand.Name + "-reader" }

// Identity is the Kubernetes username of the init identity.
func Identity() string {
	return "system:serviceaccount:" + Namespace() + ":" + ServiceAccount()
}

func labels() map[string]any {
	return map[string]any{"app.kubernetes.io/name": brand.Name, "app.kubernetes.io/managed-by": brand.Name + "-init"}
}

// Rules returns the ClusterRole rules: get and list on the allowlisted
// kinds, get on pods/log, get and list on the metrics API. No watch, no
// Secrets, no wildcards.
func Rules(ks *kinds.Set) []map[string]any {
	byGroup := map[string][]string{}
	for _, k := range ks.All() {
		byGroup[k.Group()] = append(byGroup[k.Group()], k.Resource())
	}
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	var rules []map[string]any
	for _, g := range groups {
		res := byGroup[g]
		sort.Strings(res)
		rules = append(rules, rule(g, res, "get", "list"))
	}
	rules = append(rules, rule("", []string{"pods/log"}, "get"))
	rules = append(rules, rule("metrics.k8s.io", []string{"nodes", "pods"}, "get", "list"))
	return rules
}

func rule(group string, resources []string, verbs ...string) map[string]any {
	r := make([]any, len(resources))
	for i, s := range resources {
		r[i] = s
	}
	v := make([]any, len(verbs))
	for i, s := range verbs {
		v[i] = s
	}
	return map[string]any{"apiGroups": []any{group}, "resources": r, "verbs": v}
}

// Objects returns the four objects init applies, in apply order.
func Objects(ks *kinds.Set) []map[string]any {
	rules := Rules(ks)
	anyRules := make([]any, len(rules))
	for i, r := range rules {
		anyRules[i] = r
	}
	return []map[string]any{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": Namespace(), "labels": labels()}},
		{
			"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": ServiceAccount(), "namespace": Namespace(), "labels": labels()},
			"automountServiceAccountToken": false,
		},
		{
			"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]any{"name": RoleName(), "labels": labels()},
			"rules": anyRules,
		},
		{
			"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": RoleName(), "labels": labels()},
			"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": RoleName()},
			"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": ServiceAccount(), "namespace": Namespace()}},
		},
	}
}

// ManifestYAML renders the objects as one multi-document YAML file.
func ManifestYAML(ks *kinds.Set) string {
	var docs []string
	for _, o := range Objects(ks) {
		b, err := yaml.Marshal(o)
		if err != nil {
			panic(err) // static maps of strings always marshal
		}
		docs = append(docs, strings.TrimRight(string(b), "\n"))
	}
	return "# Read-only identity for " + brand.Name + ": get and list on allowlisted kinds only.\n# No Secrets, no watch, no exec, no proxy, no wildcards.\n---\n" + strings.Join(docs, "\n---\n") + "\n"
}
