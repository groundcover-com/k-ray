package kinds

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultHasNoSecretsAndResolvesNames(t *testing.T) {
	s := Default()
	for _, k := range s.All() {
		if strings.Contains(strings.ToLower(k.Resource()), "secret") {
			t.Fatalf("allowlist contains %s", k.Name())
		}
	}
	for in, want := range map[string]string{
		"pods": "pods", "pod": "pods", "po": "pods", "Pod": "pods", " PODS ": "pods",
		"deploy": "deployments", "Deployment": "deployments", "deployments.apps": "deployments",
		"hpa": "horizontalpodautoscalers", "cm": "configmaps", "ns": "namespaces", "ev": "events",
		"endpointslices.discovery.k8s.io": "endpointslices", "pvc": "persistentvolumeclaims",
	} {
		k, err := s.Lookup(in)
		if err != nil || k.Resource() != want {
			t.Errorf("Lookup(%q) = %v, %v; want %s", in, k.Name(), err, want)
		}
	}
	if k, _ := s.Lookup("nodes"); k.Namespaced() {
		t.Error("nodes must be cluster-scoped")
	}
}

func TestLookupRejectsSecretsInEverySpelling(t *testing.T) {
	s := Default()
	for _, in := range []string{"secret", "secrets", "Secret", "SECRETS", "secrets.v1", "v1/secrets", "secret.core", " secrets "} {
		if _, err := s.Lookup(in); !errors.Is(err, ErrSecrets) {
			t.Errorf("Lookup(%q) = %v, want ErrSecrets", in, err)
		}
	}
	for _, in := range []string{"", "pods/exec", "nodes/proxy", "--server=x", "../pods", "serviceaccounts", "roles", "tokenreviews"} {
		if _, err := s.Lookup(in); err == nil {
			t.Errorf("Lookup(%q) succeeded, want error", in)
		}
	}
}

func TestCustomResources(t *testing.T) {
	ok := CustomResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts", Kind: "Rollout", Namespaced: true}
	s, err := Default().With([]CustomResource{ok})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.Lookup("rollouts.argoproj.io")
	if err != nil || !k.Custom() || k.APIVersion() != "argoproj.io/v1alpha1" {
		t.Fatalf("custom lookup: %v %v", k, err)
	}
	if k2, err := s.Lookup("rollout"); err != nil || k2.GVR() != k.GVR() {
		t.Fatalf("custom singular lookup: %v", err)
	}
	bad := []CustomResource{
		{Group: "", Version: "v1", Resource: "secrets", Kind: "Secret"},
		{Group: "apps", Version: "v1", Resource: "things", Kind: "Thing"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles", Kind: "Role"},
		{Group: "k8s.io", Version: "v1", Resource: "x", Kind: "X"},
		{Group: "example.com", Version: "v1", Resource: "secrets", Kind: "Secret"},
		{Group: "example.com", Version: "latest", Resource: "things", Kind: "Thing"},
		{Group: "example.com", Version: "v1", Resource: "Things", Kind: "Thing"},
		{Group: "example.com", Version: "v1", Resource: "things", Kind: "thing"},
		{Group: "example", Version: "v1", Resource: "things", Kind: "Thing"},
	}
	for _, c := range bad {
		if _, err := Default().With([]CustomResource{c}); err == nil {
			t.Errorf("custom resource %+v accepted", c)
		}
	}
}
