package tools

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// initWaitPod is a pod of a Deployment whose init container runs for hours
// and never completes.
func initWaitPod(ns, name string, labels map[string]any) unstructured.Unstructured {
	p := mkPod(ns, name, "ReplicaSet", "web-7f", labels, "Pending")
	p.Object["spec"].(map[string]any)["initContainers"] = []any{map[string]any{"name": "wait-db", "image": "busybox"}}
	p.Object["status"] = map[string]any{
		"phase": "Pending",
		"initContainerStatuses": []any{map[string]any{
			"name": "wait-db", "ready": false, "restartCount": int64(0), "image": "busybox", "imageID": "",
			"state": map[string]any{"running": map[string]any{"startedAt": "2026-11-13T09:00:00Z"}},
		}},
		"containerStatuses": []any{map[string]any{"name": "app", "ready": false, "restartCount": int64(0), "image": "x", "imageID": "", "state": map[string]any{"waiting": map[string]any{"reason": "PodInitializing"}}}},
	}
	return p
}

// quietObjects are what appDeployment refers to, so that no relation is a problem.
func quietObjects(nss ...string) []unstructured.Unstructured {
	var out []unstructured.Unstructured
	for _, ns := range nss {
		out = append(out,
			ob("v1", "PersistentVolumeClaim", ns, "data", map[string]any{"spec": map[string]any{"storageClassName": "standard", "volumeName": "pv-" + ns}, "status": map[string]any{"phase": "Bound"}}),
			ob("v1", "ConfigMap", ns, "web-config", map[string]any{"data": map[string]any{"LOG": "info"}}))
	}
	return out
}

func brokenDeployment(ns, name string, labels map[string]any) unstructured.Unstructured {
	d := appDeployment(ns, name, 1, 0, labels)
	_ = unstructured.SetNestedField(d.Object, int64(0), "status", "availableReplicas")
	_ = unstructured.SetNestedField(d.Object, int64(0), "status", "readyReplicas")
	return d
}

// Two workloads with one name: one fails from a ReplicaSet that cannot create
// pods, the other from an init container that never completes. Both blocks
// hold their cause. The match in the context namespace comes first. A secret
// in the log line of the init container is not shown.
func TestAmbiguousNameBlocksCarryTheirCause(t *testing.T) {
	labels := map[string]any{"app": "web"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, quietObjects("shop", "quota-ns", "init-ns")...)
	f.objs = append(f.objs,
		appDeployment("shop", "web", 1, 1, labels), // the context namespace: healthy
		brokenDeployment("quota-ns", "web", labels),
		ownedRS("quota-ns", "web-6f", "web", 1, 0, labels, map[string]any{"conditions": conds(map[string]any{
			"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate",
			"message": `pods "web-6f-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=10, limited: pods=10`,
		})}),
		brokenDeployment("init-ns", "web", labels),
		ownedRS("init-ns", "web-7f", "web", 1, 1, labels, nil),
		initWaitPod("init-ns", "web-7f-abc", labels),
	)
	f.logs["init-ns/web-7f-abc/wait-db"] = "waiting for db:5432\nwaiting for db:5432 password=hunter2hunter2\n"
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"name": "web"})
	mustContain(t, out, "found by name: 3 objects match web in 3 namespaces; all are described below (problems first)",
		"== deployment/web in namespace quota-ns", "exceeded quota: compute",
		"== deployment/web in namespace init-ns", "pod/web-7f-abc: init container wait-db has not completed: running for 3h; its last log line: waiting for db:5432 password=[REDACTED")
	mustNotContain(t, out, "hunter2hunter2", "no cause found")
	// The context namespace first, then the others.
	shop, quota, initNS := strings.Index(out, "in namespace shop\n"), strings.Index(out, "in namespace quota-ns"), strings.Index(out, "in namespace init-ns")
	if !(shop >= 0 && shop < quota && shop < initNS) {
		t.Errorf("the match in the context namespace must come first:\n%s", out)
	}
	// The answer ends with the line that says the blocks are different objects.
	body := strings.TrimRight(out[:strings.Index(out, "<<<end")], "\n")
	if !strings.HasSuffix(body, "these are different objects; the namespace decides which one is meant.") {
		t.Errorf("the closing line is missing:\n%s", out)
	}
	// Each block is short.
	for _, blk := range strings.Split(out, "\n== ")[1:] {
		if len(blk) > 600 {
			t.Errorf("block of %d bytes:\n%s", len(blk), blk)
		}
	}
	// At most one log read for each block, and none for the others.
	if n := f.counts()["logs"]; n != 1 {
		t.Errorf("%d log reads, want 1: %v", n, f.calls)
	}
	noSecretReads(t, f)
}

// A block with a problem and no cause that can be found says so.
func TestAmbiguousNameBlockWithoutCauseSaysSo(t *testing.T) {
	labels := map[string]any{"app": "web"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, quietObjects("a", "b")...)
	f.objs = append(f.objs,
		appDeployment("a", "web", 1, 1, labels),
		brokenDeployment("b", "web", labels),
		ownedRS("b", "web-7f", "web", 1, 1, labels, nil),
		func() unstructured.Unstructured { // running, not ready, and no event that says why
			p := mkPod("b", "web-7f-abc", "ReplicaSet", "web-7f", labels, "Running")
			p.Object["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["ready"] = false
			return p
		}(),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"name": "web"})
	mustContain(t, out, "== deployment/web in namespace b", "no cause found from pod state and events")
	if n := strings.Count(out, "no cause found"); n != 1 {
		t.Errorf("%d blocks say that no cause was found, want 1 (the healthy one has no problem):\n%s", n, out)
	}
	if n := f.counts()["logs"]; n != 0 {
		t.Errorf("no log is needed: %v", f.calls)
	}
}

// The pinned reads of the ambiguous case: for two workloads in two namespaces,
// with a failing init container in one, there is one log read, and none for a
// block that has no init container.
func TestAmbiguousNameReadsAreBounded(t *testing.T) {
	labels := map[string]any{"app": "web"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, quietObjects("a", "b")...)
	f.objs = append(f.objs,
		appDeployment("a", "web", 1, 1, labels),
		brokenDeployment("b", "web", labels),
		ownedRS("b", "web-7f", "web", 1, 1, labels, nil),
		initWaitPod("b", "web-7f-abc", labels),
	)
	f.logs["b/web-7f-abc/wait-db"] = "waiting\n"
	r, _ := newRunner(t, f)
	describeText(t, r, map[string]any{"name": "web"})
	if n := f.counts()["logs"]; n != 1 {
		t.Errorf("%d log reads, want 1: %v", n, f.calls)
	}
	// The search reads the metadata of the six kinds (listmeta: 6) and the two
	// matches with one get each (get: 2 + the 2 ConfigMaps of the blocks).
	// The services of each namespace are one list each (list: 13 + 2): the
	// metadata of all services cannot answer them, as the full list did.
	want := map[string]int{"events": 3, "get": 4, "list": 15, "listmeta": 6, "logs": 1, "metrics": 1}
	if got := f.counts(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reads %v, want %v: %v", got, want, f.calls)
	}
}

// The addresses in a command are what an init container waits for.
func TestCommandURLs(t *testing.T) {
	got := commandURLs(`sh -c until wget -q -O- http://feature-flags:8080/ready/v2; do echo wait; sleep 2; done; nc db:5432; curl https://a.example/x, https://a.example/x`, 3)
	want := []string{"http://feature-flags:8080/ready/v2", "https://a.example/x"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("commandURLs = %q, want %q", got, want)
	}
	if got := commandURLs("sleep 3600", 3); len(got) != 0 {
		t.Fatalf("no address expected, got %q", got)
	}
}
