package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A change of many fields shows the first lines by importance and counts the
// rest: the image comes before the environment.
func TestRevisionDiffIsBounded(t *testing.T) {
	oldEnv, newEnv := map[string]string{}, map[string]string{}
	for i := 0; i < 12; i++ {
		oldEnv[fmt.Sprintf("VAR_%02d", i)] = "a"
		newEnv[fmt.Sprintf("VAR_%02d", i)] = "b"
	}
	oldT := tpl{image: "registry.example/web:1", env: oldEnv}
	newT := tpl{image: "registry.example/web:2", env: newEnv}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		revDeployment(t, 2, 1, 1, 1, 1, newT),
		revRS(t, 1, "aaaa1", 0, 0, 48*time.Hour, oldT, ""),
		revRS(t, 2, "bbbb2", 1, 1, 5*time.Minute, newT, ""),
		podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2"}.pod(t),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	rev := blockOf(out, "changed in revision", revTail1, revTail2)
	lines := strings.Split(rev, "\n")
	if len(lines) != 1+diffLinesMax+1 {
		t.Errorf("%d lines:\n%s", len(lines), rev)
	}
	mustContain(t, rev, "- container app image: registry.example/web:1 -> registry.example/web:2", "- (5 more changes not shown)")
}
