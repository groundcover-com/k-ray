// Package kinds is the closed set of resource kinds that the tools accept.
// A Kind value can only be made inside this package, from the
// built-in allowlist or from a validated custom resource.
package kinds

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Kind is one allowed resource kind.
type Kind struct {
	resource   string // plural, for example "deployments"
	group      string // "" for the core group
	version    string
	kind       string // for example "Deployment"
	namespaced bool
	short      []string
	custom     bool
}

func (k Kind) Resource() string { return k.resource }
func (k Kind) Group() string    { return k.group }
func (k Kind) Version() string  { return k.version }
func (k Kind) Kind() string     { return k.kind }
func (k Kind) Namespaced() bool { return k.namespaced }
func (k Kind) Custom() bool     { return k.custom }
func (k Kind) IsZero() bool     { return k.resource == "" }
func (k Kind) Singular() string { return strings.ToLower(k.kind) }

// GVR is the client-go GroupVersionResource.
func (k Kind) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.group, Version: k.version, Resource: k.resource}
}

// APIVersion is the apiVersion field value, for example "apps/v1".
func (k Kind) APIVersion() string {
	if k.group == "" {
		return k.version
	}
	return k.group + "/" + k.version
}

// Name is the name that tools print and accept: the plural resource, with
// the group for custom resources.
func (k Kind) Name() string {
	if k.custom {
		return k.resource + "." + k.group
	}
	return k.resource
}

var builtins = []Kind{
	// Workloads.
	{resource: "pods", version: "v1", kind: "Pod", namespaced: true, short: []string{"po"}},
	{resource: "deployments", group: "apps", version: "v1", kind: "Deployment", namespaced: true, short: []string{"deploy"}},
	{resource: "replicasets", group: "apps", version: "v1", kind: "ReplicaSet", namespaced: true, short: []string{"rs"}},
	{resource: "statefulsets", group: "apps", version: "v1", kind: "StatefulSet", namespaced: true, short: []string{"sts"}},
	{resource: "daemonsets", group: "apps", version: "v1", kind: "DaemonSet", namespaced: true, short: []string{"ds"}},
	{resource: "jobs", group: "batch", version: "v1", kind: "Job", namespaced: true},
	{resource: "cronjobs", group: "batch", version: "v1", kind: "CronJob", namespaced: true, short: []string{"cj"}},
	{resource: "horizontalpodautoscalers", group: "autoscaling", version: "v2", kind: "HorizontalPodAutoscaler", namespaced: true, short: []string{"hpa"}},
	{resource: "poddisruptionbudgets", group: "policy", version: "v1", kind: "PodDisruptionBudget", namespaced: true, short: []string{"pdb"}},
	// Networking.
	{resource: "services", version: "v1", kind: "Service", namespaced: true, short: []string{"svc"}},
	{resource: "endpointslices", group: "discovery.k8s.io", version: "v1", kind: "EndpointSlice", namespaced: true, short: []string{"endpoints", "ep"}},
	{resource: "ingresses", group: "networking.k8s.io", version: "v1", kind: "Ingress", namespaced: true, short: []string{"ing"}},
	{resource: "ingressclasses", group: "networking.k8s.io", version: "v1", kind: "IngressClass"},
	{resource: "networkpolicies", group: "networking.k8s.io", version: "v1", kind: "NetworkPolicy", namespaced: true, short: []string{"netpol"}},
	// Storage.
	{resource: "persistentvolumeclaims", version: "v1", kind: "PersistentVolumeClaim", namespaced: true, short: []string{"pvc"}},
	{resource: "persistentvolumes", version: "v1", kind: "PersistentVolume", short: []string{"pv"}},
	{resource: "storageclasses", group: "storage.k8s.io", version: "v1", kind: "StorageClass", short: []string{"sc"}},
	// Config. ConfigMaps are redacted like everything else.
	{resource: "configmaps", version: "v1", kind: "ConfigMap", namespaced: true, short: []string{"cm"}},
	{resource: "resourcequotas", version: "v1", kind: "ResourceQuota", namespaced: true, short: []string{"quota"}},
	{resource: "limitranges", version: "v1", kind: "LimitRange", namespaced: true, short: []string{"limits"}},
	// Cluster.
	{resource: "nodes", version: "v1", kind: "Node", short: []string{"no"}},
	{resource: "namespaces", version: "v1", kind: "Namespace", short: []string{"ns"}},
	{resource: "events", version: "v1", kind: "Event", namespaced: true, short: []string{"ev"}},
}

// ErrSecrets is returned for any spelling of the Secret kind.
var ErrSecrets = errors.New("kind secrets is not available: this server never reads Secret objects; it shows secret references by name only")

// CustomResource is an opt-in custom resource from the config file.
type CustomResource struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Resource   string `json:"resource"`
	Kind       string `json:"kind"`
	Namespaced bool   `json:"namespaced"`
}

var (
	groupRE    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)
	versionRE  = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)
	resourceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	kindRE     = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// builtinGroups are API groups that config may not use for custom resources.
var builtinGroups = map[string]bool{
	"apps": true, "batch": true, "policy": true, "autoscaling": true, "extensions": true,
}

// Validate checks a custom resource from the config file.
func (c CustomResource) Validate() error {
	switch {
	case c.Group == "" || !groupRE.MatchString(c.Group) || len(c.Group) > 253:
		return fmt.Errorf("custom resource group %q: must be a DNS name with a dot (the core group is not allowed)", c.Group)
	case c.Group == "k8s.io" || strings.HasSuffix(c.Group, ".k8s.io") || c.Group == "x-k8s.io" || builtinGroups[c.Group]:
		return fmt.Errorf("custom resource group %q: built-in Kubernetes groups are not allowed", c.Group)
	case !versionRE.MatchString(c.Version):
		return fmt.Errorf("custom resource %q: version %q is not valid", c.Resource, c.Version)
	case !resourceRE.MatchString(c.Resource) || len(c.Resource) > 63:
		return fmt.Errorf("custom resource %q: resource must be the lowercase plural name", c.Resource)
	case strings.EqualFold(c.Resource, "secrets"):
		return ErrSecrets
	case !kindRE.MatchString(c.Kind):
		return fmt.Errorf("custom resource %q: kind %q is not valid", c.Resource, c.Kind)
	}
	return nil
}

// Set is an immutable set of allowed kinds.
type Set struct {
	list  []Kind
	index map[string]Kind
}

// Default returns the built-in allowlist.
func Default() *Set {
	s, err := build(builtins)
	if err != nil {
		panic(err) // the built-in table is a constant; a test covers it
	}
	return s
}

// With returns a new set with the custom resources added.
func (s *Set) With(crs []CustomResource) (*Set, error) {
	list := append([]Kind(nil), s.list...)
	for _, c := range crs {
		if err := c.Validate(); err != nil {
			return nil, err
		}
		list = append(list, Kind{
			resource: c.Resource, group: c.Group, version: c.Version, kind: c.Kind,
			namespaced: c.Namespaced, custom: true,
		})
	}
	return build(list)
}

func build(list []Kind) (*Set, error) {
	s := &Set{list: list, index: map[string]Kind{}}
	add := func(key string, k Kind) error {
		key = strings.ToLower(key)
		if prev, ok := s.index[key]; ok && prev.GVR() != k.GVR() {
			return fmt.Errorf("kind name %q is ambiguous (%s and %s)", key, prev.Name(), k.Name())
		}
		s.index[key] = k
		return nil
	}
	for _, k := range list {
		names := []string{k.Name(), k.resource + "." + k.group, k.Singular() + "." + k.group}
		if !k.custom {
			names = append(names, k.resource, k.Singular())
			names = append(names, k.short...)
		} else {
			// Custom resources also answer to the plain plural and kind when no
			// built-in uses them.
			if _, taken := s.index[k.resource]; !taken {
				names = append(names, k.resource, k.Singular())
			}
		}
		for _, n := range names {
			if err := add(n, k); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(s.list, func(i, j int) bool { return s.list[i].Name() < s.list[j].Name() })
	return s, nil
}

// Lookup resolves a kind name as an agent or a person writes it: plural,
// singular, short name, Kind, or name.group. Any form of "secret" returns
// ErrSecrets.
func (s *Set) Lookup(name string) (Kind, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if isSecretName(n) {
		return Kind{}, ErrSecrets
	}
	if k, ok := s.index[n]; ok {
		return k, nil
	}
	return Kind{}, fmt.Errorf("unknown kind %q; allowed kinds: %s", name, strings.Join(s.Names(), ", "))
}

func isSecretName(n string) bool {
	if n == "" {
		return false
	}
	for _, f := range strings.FieldsFunc(n, func(r rune) bool { return r == '/' || r == '.' || r == ' ' }) {
		if f == "secret" || f == "secrets" {
			return true
		}
	}
	return false
}

// ByGroupKind finds an allowed kind by API group and Kind (for owner
// references).
func (s *Set) ByGroupKind(group, kind string) (Kind, bool) {
	for _, k := range s.list {
		if k.group == group && k.kind == kind {
			return k, true
		}
	}
	return Kind{}, false
}

// All returns every kind in the set, sorted by name.
func (s *Set) All() []Kind { return append([]Kind(nil), s.list...) }

// Names returns the canonical names, sorted.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.list))
	for _, k := range s.list {
		out = append(out, k.Name())
	}
	return out
}
