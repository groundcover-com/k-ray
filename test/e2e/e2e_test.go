// Package e2e runs the real binary over stdio, through the canary harness,
// against a fake API server. It needs no Docker; the kind test in
// test/integration covers a real API server and real RBAC.
package e2e

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/test/fixture"
	"github.com/groundcover-com/kray/test/harness"
)

const (
	podName  = "api-5d8f7c9b4-abcde"
	nodeName = "node-1"
)

func repoRoot(t *testing.T) string {
	p, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func build(t *testing.T, pkg, out string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
}

func setup(t *testing.T) (*fakeAPI, *fixture.Set, string) {
	fx := fixture.New("fixture")
	api := newFakeAPI(t)
	for _, o := range fx.Objects() {
		api.add(o)
	}
	api.add(fx.Pod(podName, nodeName))
	api.add(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": nodeName}, "status": map[string]any{
			"allocatable": map[string]any{"cpu": "2", "memory": "4Gi"},
			"conditions":  []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}})
	api.logs["fixture/"+podName] = fx.LogText()
	api.metrics = []*unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics", "metadata": map[string]any{"name": podName, "namespace": "fixture"},
			"containers": []any{map[string]any{"name": "api", "usage": map[string]any{"cpu": "5m", "memory": "8Mi"}}},
		}},
		{Object: map[string]any{
			"apiVersion": "metrics.k8s.io/v1beta1", "kind": "NodeMetrics", "metadata": map[string]any{"name": nodeName},
			"usage": map[string]any{"cpu": "500m", "memory": "1Gi"},
		}},
	}
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.srv.Certificate().Raw}), 0o600)
	kc := filepath.Join(dir, "kubeconfig")
	_ = os.WriteFile(kc, []byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: fake\n  cluster: {server: %q, certificate-authority: %q}\nusers:\n- name: admin\n  user: {token: fake-admin-token}\ncontexts:\n- name: fake\n  context: {cluster: fake, user: admin, namespace: fixture}\ncurrent-context: fake\n", api.srv.URL, ca)), 0o600)
	return api, fx, kc
}

func outDir(t *testing.T) string {
	d := filepath.Join(repoRoot(t), "test", "e2e", "out")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// TestServerPassesTheCanaryHarness is the acceptance test of the whole
// product against a fake cluster.
func TestServerPassesTheCanaryHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	api, fx, kc := setup(t)
	bin := filepath.Join(t.TempDir(), brand.Name)
	build(t, ".", bin)

	home := t.TempDir()
	cfgDir := filepath.Join(home, "config")
	stateDir := filepath.Join(home, "state")
	_ = os.MkdirAll(filepath.Join(cfgDir, brand.Name), 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, brand.Name, "config.yaml"), []byte("customResources:\n  - {group: example.com, version: v1, resource: widgets, kind: Widget, namespaced: true}\n"), 0o600)
	u, _ := url.Parse(api.srv.URL)
	target := harness.Target{
		Name:    brand.Name + "-fake-api",
		Command: []string{bin, "serve"},
		Env: []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + home, "KUBECONFIG=" + kc,
			"XDG_CONFIG_HOME=" + cfgDir, "XDG_STATE_HOME=" + stateDir,
		},
		AuditFile:    filepath.Join(stateDir, brand.Name, "audit.jsonl"),
		TraceNetwork: true,
		APIServer:    u.Host,
		Identity:     "fake-user",
	}
	start := time.Now()
	rep, err := harness.Run(context.Background(), target, fx.HarnessFixture(podName, nodeName), harness.Options{
		Expect: fx.Expect(podName, nodeName), AuditLog: api.auditLog("fake-user"), Started: start.Add(-time.Second), OutDir: outDir(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + rep.String())
	if !rep.OK() {
		t.Fatalf("the server failed the canary harness:\n%s", rep.String())
	}
	if m := api.methods(); len(m) != 1 || m["GET"] == 0 {
		t.Fatalf("methods that reached the API server: %v", m)
	}
	hostile := 0
	for _, c := range rep.Calls {
		if c.Hostile {
			hostile++
		}
	}
	if len(rep.Calls) < 100 || hostile < 50 {
		t.Fatalf("too few calls: %d (%d hostile)", len(rep.Calls), hostile)
	}
	b, err := os.ReadFile(target.AuditFile)
	_ = os.WriteFile(filepath.Join(outDir(t), target.Name+"-audit.jsonl"), b, 0o600)
	if err != nil || strings.Count(string(b), "\n") != len(rep.Calls) {
		t.Fatalf("audit lines %d for %d calls (%v)", strings.Count(string(b), "\n"), len(rep.Calls), err)
	}
}

// TestHarnessFailsAnUnsafeServer proves the harness can fail: a stub that
// returns Secrets and writes on every call must not pass.
func TestHarnessFailsAnUnsafeServer(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the stub")
	}
	api, fx, kc := setup(t)
	stub := filepath.Join(t.TempDir(), "unsafe-stub")
	build(t, "./test/harness/stub", stub)
	target := harness.Target{
		Name: "unsafe-stub", Command: []string{stub},
		Env:      []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "KUBECONFIG=" + kc},
		Identity: "fake-user",
	}
	rep, err := harness.Run(context.Background(), target, fx.HarnessFixture(podName, nodeName), harness.Options{
		AuditLog: api.auditLog("fake-user"), Started: time.Now().Add(-time.Second), OutDir: outDir(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || len(rep.Leaks) == 0 || len(rep.BadRequests) == 0 {
		t.Fatalf("the harness did not fail an unsafe server:\n%s", rep.String())
	}
	// The stub prints raw text, so every canary place must show. Each place
	// with several parts must report every part that the stub's output holds.
	leaked := map[string]map[int]bool{}
	for _, l := range rep.Leaks {
		if leaked[l.Place] == nil {
			leaked[l.Place] = map[int]bool{}
		}
		leaked[l.Place][l.Part] = true
	}
	for _, p := range []string{fixture.LogSpacedKey, fixture.LogBareSpacedKey, fixture.LogBearer, fixture.LogPassword, fixture.EventMessage, fixture.ConditionMessage, fixture.CustomResource} {
		if !leaked[p][0] {
			t.Errorf("the harness did not report the leak of place %q from the stub:\n%s", p, rep.String())
		}
	}
	for p, parts := range fx.Parts {
		for i := range parts {
			if !leaked[p][i+1] {
				t.Errorf("the harness did not report the leak of place %q part %d from the stub:\n%s", p, i+1, rep.String())
			}
		}
	}
	t.Logf("unsafe stub failed as expected: %d leaks, %d bad requests", len(rep.Leaks), len(rep.BadRequests))
}
