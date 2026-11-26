package tools

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// word turns a number into letters, so that a name has no digits that a
// template would mask: two workloads never fold into one row by accident.
func word(i int) string {
	s := ""
	for {
		s = string(rune('a'+i%26)) + s
		i = i/26 - 1
		if i < 0 {
			return s
		}
	}
}

// largeCluster builds 1,000 pods in 50 namespaces. 235 of them have a problem,
// in the first 40 namespaces. Every problem pod belongs to its own workload.
func largeCluster() (objs []unstructured.Unstructured, problem map[string]string) {
	problem = map[string]string{} // ns/name -> kind of problem
	pod := func(ns, name, wl string, status map[string]any, spec map[string]any) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{
				"name": name, "namespace": ns, "uid": "uid-" + ns + "-" + name,
				"creationTimestamp": "2026-11-10T10:00:00Z",
				"ownerReferences": []any{map[string]any{
					"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": wl + "-5d8f7c9b6", "uid": "rs-" + ns + "-" + wl, "controller": true,
				}},
			},
			"spec":   spec,
			"status": status,
		}}
	}
	spec := func(image string, mem string) map[string]any {
		return map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{
			"name": "app", "image": image,
			"resources": map[string]any{"limits": map[string]any{"memory": mem}},
		}}}
	}
	healthy := func(ns, name, wl string) unstructured.Unstructured {
		return pod(ns, name, wl, map[string]any{
			"phase":      "Running",
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "PodScheduled", "status": "True"}},
			"containerStatuses": []any{map[string]any{
				"name": "app", "ready": true, "restartCount": int64(0), "state": map[string]any{"running": map[string]any{"startedAt": "2026-11-10T10:00:05Z"}},
			}},
		}, spec("registry.example/"+wl+":1.0.0", "512Mi"))
	}
	n := 0
	for i := 0; i < 235; i++ {
		ns := fmt.Sprintf("team-%02d", i%40)
		wl := "svc-" + word(i)
		name := wl + "-5d8f7c9b6-" + word(i+20000)
		image := "registry.example/" + ns + "/" + wl + ":1.4.2"
		var status map[string]any
		switch (i + i/40) % 4 {
		case 0:
			problem[ns+"/"+name] = "image"
			status = map[string]any{
				"phase":      "Pending",
				"conditions": []any{map[string]any{"type": "Ready", "status": "False"}},
				"containerStatuses": []any{map[string]any{"name": "app", "ready": false, "restartCount": int64(0), "state": map[string]any{"waiting": map[string]any{
					"reason": "ImagePullBackOff", "message": `Back-off pulling image "` + image + `"`,
				}}}},
			}
		case 1:
			problem[ns+"/"+name] = "crash"
			status = map[string]any{
				"phase":      "Running",
				"conditions": []any{map[string]any{"type": "Ready", "status": "False"}},
				"containerStatuses": []any{map[string]any{
					"name": "app", "ready": false, "restartCount": int64(12),
					"state":     map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "back-off 5m0s restarting failed container=app pod=" + name}},
					"lastState": map[string]any{"terminated": map[string]any{"exitCode": int64(1), "reason": "Error", "finishedAt": "2026-11-13T11:55:00Z"}},
				}},
			}
		case 2:
			problem[ns+"/"+name] = "oom"
			status = map[string]any{
				"phase":      "Running",
				"conditions": []any{map[string]any{"type": "Ready", "status": "False"}},
				"containerStatuses": []any{map[string]any{
					"name": "app", "ready": false, "restartCount": int64(7),
					"state":     map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
					"lastState": map[string]any{"terminated": map[string]any{"exitCode": int64(137), "reason": "OOMKilled", "finishedAt": "2026-11-13T11:55:00Z"}},
				}},
			}
		default:
			problem[ns+"/"+name] = "pending"
			status = map[string]any{
				"phase": "Pending",
				"conditions": []any{map[string]any{
					"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
					"message": "0/5 nodes are available: 5 Insufficient memory. preemption: 0/5 nodes are available: 5 No preemption victims found for incoming pod.",
				}},
			}
		}
		sp := spec(image, "256Mi")
		if (i+i/40)%4 == 3 {
			delete(sp, "nodeName")
		}
		objs = append(objs, pod(ns, name, wl, status, sp))
		n++
	}
	for i := 0; n < 1000; i++ {
		ns := fmt.Sprintf("team-%02d", i%50)
		wl := "ok-" + word(i%120)
		objs = append(objs, healthy(ns, fmt.Sprintf("%s-5d8f7c9b6-%s", wl, word(i+30000)), wl))
		n++
	}
	return objs, problem
}

// tableRows counts the rows of each namespace in the table of a list. A row
// starts with the namespace, and no other line of the body does.
func tableRows(out string) (byNS map[string]int, names map[string]string) {
	byNS, names = map[string]int{}, map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 2 && strings.HasPrefix(f[0], "team-") && strings.HasPrefix(f[1], "svc-") {
			byNS[f[0]]++
			names[f[1]] = f[0]
		}
	}
	return byNS, names
}

// A list of a cluster of 1,000 pods with 235 that have a problem is small,
// and says where the rows that it does not show are.
func TestLargeListIsSmallAndNamesWhatIsHidden(t *testing.T) {
	objs, problem := largeCluster()
	if len(objs) != 1000 || len(problem) != 235 {
		t.Fatalf("fixture: %d pods, %d with a problem", len(objs), len(problem))
	}
	r, _ := newRunner(t, &fakeReader{objs: objs})
	out := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true}).Text
	if len(out) >= 7000 {
		t.Errorf("the list is %d bytes, want under 7,000:\n%s", len(out), out)
	}
	mustContain(t, out, "1,000 pods: 235 unhealthy, 765 healthy", "not shown: ", "healthy, not listed (765)")
	mustContain(t, out, "Show the others with match (a name or a word) or namespace")
	// Every cause is short, and a column that every row shares is said once.
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "team-") && strings.Contains(l, "ImagePullBackOff") {
			if cause := l[strings.Index(l, "container app"):]; len(cause) > 100 {
				t.Errorf("cause of %d bytes: %q", len(cause), l)
			}
		}
	}
	mustContain(t, out, "same on every row: READY 0/1, AGE 3d")
	mustNotContain(t, out, "Back-off pulling image", "READY  ", "RESTARTS")

	// The whole list, from full, is the old shape: long causes, every column.
	full := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "full": true}).Text
	mustContain(t, full, "RESTARTS", "Back-off pulling image")
	mustNotContain(t, full, "same on every row")
	if len(full) < 2*len(out) {
		t.Errorf("full is %d bytes, the default %d", len(full), len(out))
	}

	// Every namespace with a row that is not shown is named in the line, with
	// the number of rows.
	shownBy, shownNames := tableRows(out)
	allBy, allNames := tableRows(full)
	line := lineWith(t, out, "not shown: ")
	hidden := 0
	for ns, n := range allBy {
		if want := n - shownBy[ns]; want > 0 {
			hidden += want
			if !strings.Contains(line, fmt.Sprintf("%s %d", ns, want)) {
				t.Errorf("namespace %s has %d hidden rows; the line says: %s", ns, want, line)
			}
		}
	}
	if hidden == 0 {
		t.Fatal("no row is hidden: the test does not test anything")
	}
	if !strings.Contains(line, fmt.Sprintf("not shown: %d rows (", hidden)) {
		t.Errorf("%d rows are hidden; the line says: %s", hidden, line)
	}
	for ns := range allBy {
		if shownBy[ns] == 0 && !strings.Contains(line, ns+" ") {
			t.Errorf("namespace %s has no shown row and is not in the line", ns)
		}
	}

	// A match on the name of a hidden pod returns its row with its cause, and
	// so does a match on the namespace of a hidden row.
	var name string
	for n := range allNames {
		if _, ok := shownNames[n]; !ok {
			name = n
			break
		}
	}
	if name == "" {
		t.Fatal("no hidden pod")
	}
	one := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true, "match": name}).Text
	row := lineWith(t, one, name)
	if !strings.HasPrefix(row, allNames[name]+" ") || !regexp.MustCompile(`cannot pull its image|exits with code|OOM killed|not scheduled`).MatchString(row) {
		t.Errorf("row of %s: %q", name, row)
	}
	byNS := call(t, r, "list", map[string]any{"kind": "pods", "namespace": allNames[name], "match": "svc-"}).Text
	mustContain(t, byNS, name)
}

// The list of a namespace with a few problems is not changed by any of this.
func TestSmallListKeepsItsShape(t *testing.T) {
	objs, _ := largeCluster()
	r, _ := newRunner(t, &fakeReader{objs: objs})
	out := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "team-00"}).Text
	mustContain(t, out, "READY", "RESTARTS", "AGE")
	mustNotContain(t, out, "same on every row", "not shown")
}

func TestShortCause(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{
			`container app cannot pull its image: registry.example/team-00/svc-a:1.4.2: Back-off pulling image "registry.example/team-00/svc-a:1.4.2"`,
			"container app cannot pull its image: registry.example/team-00/svc-a:1.4.2",
		},
		{
			"container app is OOM killed at its memory limit of 256Mi (7 restarts); the limit is too low for this workload, or the container leaks memory",
			"container app is OOM killed at its memory limit of 256Mi (7 restarts)",
		},
		{
			"container api exits with code 1 (application error), 12 restarts; last error: FATAL config: DATABASE_URL is not set",
			"container api exits with code 1 (application error), 12 restarts",
		},
		{"container api exits with code 1; last error: connection refused", "container api exits with code 1; last error: connection refused"},
		{
			"container notifier cannot start: it refers to the configmap \"notifier-settings\", which does not exist in namespace pay",
			"container notifier cannot start: it refers to the configmap \"notifier-settings\"",
		},
	} {
		got := shortCause(c.in, 100)
		if got != c.want {
			t.Errorf("shortCause(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
		if len(c.in) > 100 && len(got) > 100 {
			t.Errorf("%d bytes: %q", len(got), got)
		}
	}
}
