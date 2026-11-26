package tools

import (
	"fmt"
	"testing"
)

// The "found by name" line is left out when it repeats the title: one object,
// matched by its own name, and no other match.
func TestFoundByNameLineIsLeftOutWhenItRepeatsTheTitle(t *testing.T) {
	r, _ := newRunner(t, webFixture(t))
	out := describeText(t, r, map[string]any{"name": "web"})
	mustContain(t, out, "describe deployment web · namespace shop")
	mustNotContain(t, out, "found by name")
}

// It stays when the match is the workload of pods that the name matched.
func TestFoundByNameLineStaysForTheWorkloadOfPods(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	labels := map[string]any{"app": "phantom"}
	for i := 0; i < 2; i++ {
		f.objs = append(f.objs, mkPod("shop", fmt.Sprintf("ghost-5d7c9-%02d", i), "ReplicaSet", "ghost-5d7c9", labels, "Running"))
	}
	f.objs = append(f.objs, mkReplicaSet("shop", "ghost-5d7c9", "phantom"), mkDeployment("shop", "phantom", labels, labels))
	r, _ := newRunner(t, f)
	mustContain(t, describeText(t, r, map[string]any{"name": "ghost"}),
		"found by name: deployment/phantom · namespace shop (the workload of 2 pods)")
}

// It stays when another object has the name too.
func TestFoundByNameLineStaysWithOtherMatches(t *testing.T) {
	f := webFixture(t)
	f.objs = append(f.objs, ob("v1", "Service", "shop", "web", map[string]any{"spec": map[string]any{"selector": map[string]any{"app": "web"}}}))
	r, _ := newRunner(t, f)
	mustContain(t, describeText(t, r, map[string]any{"name": "web"}),
		"found by name: deployment/web · namespace shop", "also matched:", "service/web in namespace shop")
}

// It stays in the form for several objects.
func TestFoundByNameLineStaysForSeveralObjects(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	workload(f, "shop", "web", 1, map[string]any{"app": "web"})
	workload(f, "other", "web", 1, map[string]any{"app": "web"})
	r, _ := newRunner(t, f)
	mustContain(t, describeText(t, r, map[string]any{"name": "web"}),
		"found by name: 2 objects match web in 2 namespaces")
}
