package tools

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// bigBody is the data of each object of a large kind. The objects share the
// one string, so the test itself stays small.
var bigBody = strings.Repeat("x", 256<<10)

// heavyCluster is the scenario fixture plus 2,000 ConfigMaps and 2,000
// Deployments, each with a large body, in 20 namespaces. None of them is in
// the default namespace (shop). Two ConfigMaps named "dup" are in two
// namespaces, before the others, so that the fetch cap does not hide them.
func heavyCluster(t *testing.T) *fakeReader {
	t.Helper()
	f := scenarioFixture(t)
	for _, ns := range []string{"ns-01", "ns-02"} {
		f.objs = append(f.objs, unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "dup", "namespace": ns, "uid": "uid-dup-" + ns},
			"data":     map[string]any{"values.yaml": bigBody},
		}})
	}
	for i := 0; i < 2000; i++ {
		ns := fmt.Sprintf("ns-%02d", i%20)
		app := fmt.Sprintf("app-%04d", i)
		f.objs = append(f.objs, unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": fmt.Sprintf("cm-%04d", i), "namespace": ns, "uid": fmt.Sprintf("uid-cm-%d", i)},
			"data":     map[string]any{"values.yaml": bigBody},
		}})
		f.objs = append(f.objs, unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": app, "namespace": ns, "uid": "uid-" + app},
			"spec": map[string]any{
				"replicas": int64(1),
				"selector": map[string]any{"matchLabels": map[string]any{"app": app}},
				"template": map[string]any{
					"metadata": map[string]any{"annotations": map[string]any{"blob": bigBody}},
					"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "registry.example/app:1"}}},
				},
			},
			"status": map[string]any{"replicas": int64(1), "readyReplicas": int64(1), "availableReplicas": int64(1), "updatedReplicas": int64(1)},
		}})
	}
	return f
}

// noFullListOf fails when the call read one of the kinds in full in all
// namespaces, or did not read it as metadata.
func noFullListOf(t *testing.T, f *fakeReader, kinds ...string) {
	t.Helper()
	f.mu.Lock()
	calls := append([]string(nil), f.calls...)
	f.mu.Unlock()
	for _, k := range kinds {
		meta := false
		for _, c := range calls {
			if strings.HasPrefix(c, "list "+k+"  ") {
				t.Errorf("a full list of all %s: %q\nall reads: %v", k, c, calls)
			}
			if strings.HasPrefix(c, "listmeta "+k+" ") {
				meta = true
			}
		}
		if !meta {
			t.Errorf("no metadata list of %s\nall reads: %v", k, calls)
		}
	}
}

// A search by name reads the metadata of a kind, and reads in full only the
// objects that match: never the whole kind, whose objects can be large.
func TestNameSearchReadsMetadataOnly(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		args  map[string]any
		kinds []string
		want  []string
	}{
		{
			"get, not in the default namespace", "get",
			map[string]any{"kind": "configmap", "name": "cm-1234"},
			[]string{"configmaps"},
			[]string{"found in namespace ns-14; none in the default namespace shop", "cm-1234"},
		},
		{
			"get, a name in two namespaces", "get",
			map[string]any{"kind": "configmap", "name": "dup"},
			[]string{"configmaps"},
			[]string{"2 objects are named dup, in 2 namespaces", "ns-01/dup", "ns-02/dup"},
		},
		{
			"get, a name that is nowhere", "get",
			map[string]any{"kind": "configmap", "name": "cm-12345"},
			[]string{"configmaps"},
			[]string{"also looked in all namespaces", "closest names:", "configmap ns-14/cm-1234"},
		},
		{
			"describe a kind and a name, no namespace", "describe",
			map[string]any{"kind": "configmap", "name": "cm-1234"},
			[]string{"configmaps"},
			[]string{"found in namespace ns-14; none in the default namespace shop"},
		},
		{
			"describe a kind and a name in all namespaces", "describe",
			map[string]any{"kind": "configmap", "name": "cm-1234", "allNamespaces": true},
			[]string{"configmaps"},
			[]string{"cm-1234", "ns-14"},
		},
		{
			"describe a name alone", "describe",
			map[string]any{"name": "app-0042"},
			[]string{"deployments", "statefulsets", "daemonsets", "jobs", "cronjobs", "services"},
			[]string{"app-0042", "ns-02"},
		},
		{
			"logs of a name that is a workload", "logs",
			map[string]any{"pod": "app-0042"},
			[]string{"pods", "deployments", "statefulsets", "daemonsets", "jobs"},
			[]string{"no pod is named app-0042; reading the logs of deployment/app-0042 in namespace ns-02"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := heavyCluster(t)
			r, _ := newRunner(t, f)
			res := call(t, r, c.tool, c.args)
			mustContain(t, res.Text, c.want...)
			noFullListOf(t, f, c.kinds...)
		})
	}
}
