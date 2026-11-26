package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// readCase is one tool call and the reads it makes.
type readCase struct {
	name string
	tool string
	args map[string]any
	// want is the number of reads by method. A later change that adds a read
	// fails here, so that it cannot go unnoticed.
	want map[string]int
}

func fmtCounts(m map[string]int) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

var readCases = []readCase{
	{"list pods in all namespaces (3 unhealthy pods)", "list", map[string]any{"kind": "pods", "allNamespaces": true}, map[string]int{"events": 1, "list": 2, "logs": 1, "metrics": 1}},
	{"list pods in one namespace", "list", map[string]any{"kind": "pods", "namespace": "pay"}, map[string]int{"events": 1, "list": 3, "metrics": 1}},
	// No pod has a problem, so the events read is cancelled (see cancelledReads).
	{"list pods with nothing wrong", "list", map[string]any{"kind": "pods", "namespace": "pay", "labelSelector": "app=api"}, map[string]int{"list": 1, "metrics": 1}},
	{"list deployments in all namespaces", "list", map[string]any{"kind": "deployments", "allNamespaces": true}, map[string]int{"events": 1, "list": 2, "logs": 1, "metrics": 1}},
	{"list services", "list", map[string]any{"kind": "services", "allNamespaces": true}, map[string]int{"list": 5}},
	{"describe deployment", "describe", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"}, map[string]int{"events": 2, "get": 1, "list": 6, "logs": 1, "metrics": 1}},
	{"describe pod that crashed", "describe", map[string]any{"kind": "pod", "name": "web-5d4f-aaaaa", "namespace": "shop"}, map[string]int{"events": 1, "get": 3, "list": 1, "logs": 1, "metrics": 1}},
	{"describe pod that is pending", "describe", map[string]any{"kind": "pod", "name": "pend-1", "namespace": "pay"}, map[string]int{"events": 1, "get": 1, "list": 3, "metrics": 1}},
	{"describe service", "describe", map[string]any{"kind": "service", "name": "web", "namespace": "shop"}, map[string]int{"events": 1, "get": 1, "list": 2}},
	// A search by name reads the metadata of each kind (listmeta), never the
	// objects of a whole kind, and gets each match in full. The lists left are
	// all in one namespace; "list services shop" is one more of them, because
	// the metadata of all services cannot answer it, as the full list did.
	{"describe by name, a deployment", "describe", map[string]any{"name": "web"}, map[string]int{"events": 2, "get": 2, "list": 6, "listmeta": 6, "logs": 1, "metrics": 1}},
	{"describe by name, a pod", "describe", map[string]any{"name": "web-5d4f-aaaaa"}, map[string]int{"events": 2, "get": 3, "list": 6, "listmeta": 7, "logs": 1, "metrics": 1}},
	{"describe by selector", "describe", map[string]any{"kind": "pods", "namespace": "shop", "labelSelector": "app=web"}, map[string]int{"events": 2, "get": 2, "list": 6, "logs": 1, "metrics": 1}},
	{"top pods", "top", map[string]any{"resource": "pods", "allNamespaces": true}, map[string]int{"list": 1, "metrics": 1}},
	{"top nodes", "top", map[string]any{"resource": "nodes"}, map[string]int{"list": 1, "metrics": 1}},
	{"events", "events", map[string]any{"namespace": "shop"}, map[string]int{"events": 1}},
	{"logs of a workload", "logs", map[string]any{"workload": "deployment/web", "namespace": "shop"}, map[string]int{"get": 1, "list": 1, "logs": 2}},
	{"get", "get", map[string]any{"kind": "pod", "name": "web-5d4f-aaaaa", "namespace": "shop"}, map[string]int{"get": 1}},
	// No namespace, object in the default namespace: the same reads as with the namespace.
	{"describe deployment, no namespace", "describe", map[string]any{"kind": "deployment", "name": "web"}, map[string]int{"events": 2, "get": 1, "list": 6, "logs": 1, "metrics": 1}},
	{"get, no namespace", "get", map[string]any{"kind": "pod", "name": "web-5d4f-aaaaa"}, map[string]int{"get": 1}},
	{"logs of a workload, no namespace", "logs", map[string]any{"workload": "deployment/web"}, map[string]int{"get": 1, "list": 1, "logs": 2}},
	{"logs of a pod, no namespace", "logs", map[string]any{"pod": "web-5d4f-aaaaa"}, map[string]int{"get": 1, "logs": 1}},
}

// cancelledReads is, by case, the reads that the tool starts before it knows
// it needs them and cancels when it does not. A cancelled read reaches the
// API server or not, by timing, so it is counted 0 times or once.
var cancelledReads = map[string][]string{
	"list pods with nothing wrong": {"events"},
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// TestReadCounts pins how many reads each tool makes for the main cases.
func TestReadCounts(t *testing.T) {
	for _, c := range readCases {
		t.Run(c.name, func(t *testing.T) {
			f := scenarioFixture(t)
			r, _ := newRunner(t, f)
			res := call(t, r, c.tool, c.args)
			got := f.counts()
			if dir := os.Getenv("KRAY_DUMP_READS"); dir != "" {
				// For a comparison of two builds: the reads and the output.
				name := nonWord.ReplaceAllString(strings.ToLower(c.name), "-")
				_ = os.WriteFile(filepath.Join(dir, name+".txt"), []byte(res.Text), 0o644)
				fmt.Printf("READS %-50s total=%d %s err=%v\n", c.name, f.total(), fmtCounts(got), res.IsError)
				return
			}
			if res.IsError {
				t.Fatalf("the call failed: %s", res.Text)
			}
			for _, k := range cancelledReads[c.name] {
				if got[k] == 1 && c.want[k] == 0 {
					delete(got, k)
				}
			}
			if fmtCounts(got) != fmtCounts(c.want) {
				t.Errorf("reads: got %s, want %s\n%v", fmtCounts(got), fmtCounts(c.want), f.calls)
			}
		})
	}
}
