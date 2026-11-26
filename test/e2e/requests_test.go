package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/groundcover-com/kray/internal/brand"
)

// requestsOf runs the binary once and returns the paths of the HTTP requests
// that reached the fake API server, without the query, sorted.
func requestsOf(t *testing.T, bin, kc string, api *fakeAPI, args ...string) (stdout string, paths []string) {
	t.Helper()
	home := t.TempDir()
	api.mu.Lock()
	before := len(api.records)
	api.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "KUBECONFIG=" + kc,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_STATE_HOME=" + filepath.Join(home, "state"),
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", brand.Name, args, err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	for _, r := range api.records[before:] {
		paths = append(paths, r.method+" "+strings.SplitN(r.uri, "?", 2)[0])
	}
	sort.Strings(paths)
	return string(out), paths
}

// The command line makes no request before the work of the command: there is
// no discovery and no version call. These are all the requests of two common
// commands, so that a later change that adds a request is seen.
func TestCLIRequestCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	api, _, kc := setup(t)
	bin := filepath.Join(t.TempDir(), brand.Name)
	build(t, ".", bin)

	cases := []struct {
		name string
		args []string
		want []string
		// optional requests may or may not reach the server: a call starts
		// reads at the same time as the read that says if they are needed
		// (events and usage with the list of pods), and cancels them when they
		// are not. A read can be cancelled before its request is sent.
		optional []string
	}{
		{"get pods -A", []string{"get", "pods", "-A"}, []string{
			"GET /api/v1/events",
			"GET /api/v1/pods",
			"GET /apis",
			"GET /apis/metrics.k8s.io/v1beta1/pods",
		}, []string{"GET /api/v1/events"}},
		{"describe deploy NAME", []string{"describe", "deploy", "api", "-n", "fixture"}, []string{
			"GET /api/v1/namespaces/fixture/events",
			"GET /api/v1/namespaces/fixture/pods",
			"GET /api/v1/namespaces/fixture/resourcequotas", // the fixture deployment has no status.replicas: fewer pods than wanted
			"GET /api/v1/namespaces/fixture/services",
			"GET /apis/apps/v1/namespaces/fixture/deployments/api",
			// The objects around the deployment (see related.go): one list for each.
			"GET /apis/apps/v1/namespaces/fixture/replicasets",
			"GET /apis/autoscaling/v2/namespaces/fixture/horizontalpodautoscalers",
			"GET /apis/networking.k8s.io/v1/namespaces/fixture/networkpolicies",
			"GET /apis/policy/v1/namespaces/fixture/poddisruptionbudgets",
		}, []string{"GET /apis", "GET /apis/metrics.k8s.io/v1beta1/namespaces/fixture/pods"}}, // the pods of the fixture deployment are healthy: usage is not needed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got := requestsOf(t, bin, kc, api, c.args...)
			t.Logf("%d requests: %v", len(got), got)
			want := append([]string(nil), c.want...)
			drop := func(in []string) []string {
				var kept []string
				for _, g := range in {
					skip := false
					for _, o := range c.optional {
						skip = skip || g == o
					}
					if !skip {
						kept = append(kept, g)
					}
				}
				return kept
			}
			got, want = drop(got), drop(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
