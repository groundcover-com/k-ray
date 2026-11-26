package cli

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groundcover-com/kray/internal/brand"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestVersionAndUsage(t *testing.T) {
	code, out, _ := run("version")
	if code != 0 || !strings.HasPrefix(out, brand.Name+" ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, errs := run("frobnicate"); code != 1 || strings.TrimSpace(errs) != "This command is not supported." {
		t.Fatalf("unknown: %d %q", code, errs)
	}
	if code, _, errs := run(); code != 2 || !strings.Contains(errs, "serve") {
		t.Fatalf("usage: %d %q", code, errs)
	}
}

func fakeCluster(t *testing.T) string {
	t.Helper()
	return fakeClusterAnswering(t, map[string]string{
		"/api/v1/namespaces/shop/pods": `{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop"},"status":{"phase":"Running"}}]}`,
	})
}

// fakeClusterAnswering serves the given paths (GET only) and answers 404 for
// any other. The kubeconfig context namespace is shop.
func fakeClusterAnswering(t *testing.T, answers map[string]string) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request reached the cluster: %s %s", r.Method, r.URL)
		}
		if body, ok := answers[r.URL.Path]; ok {
			fmt.Fprint(w, body)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":404}`)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	kc := filepath.Join(dir, "kubeconfig")
	_ = os.WriteFile(kc, []byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster: {server: %q, certificate-authority: %q}\nusers:\n- name: u\n  user: {token: t}\ncontexts:\n- name: dev\n  context: {cluster: c, user: u, namespace: shop}\ncurrent-context: dev\n", srv.URL, ca)), 0o600)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	return kc
}

func TestListCommandUsesTheRunnerAndAudits(t *testing.T) {
	kc := fakeCluster(t)
	code, out, errs := run("list", "pods", "--kubeconfig", kc)
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "<<<untrusted cluster data ") || !strings.Contains(out, "context dev") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(errs, "not a read-only identity") {
		t.Fatalf("identity warning on a stderr that is not a terminal: %q", errs)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), brand.Name, "audit.jsonl"))
	if err != nil || !strings.Contains(string(b), `"surface":"cli"`) || !strings.Contains(string(b), `"tool":"list"`) {
		t.Fatalf("audit: %v %s", err, b)
	}
	code, out, _ = run("get", "secret", "db", "--kubeconfig", kc)
	if code != 1 || !strings.Contains(out, "never reads Secret objects") {
		t.Fatalf("get secret: %d %s", code, out)
	}
}

func TestServeFailsClosedWithoutAuditLog(t *testing.T) {
	kc := fakeCluster(t)
	bad := filepath.Join(t.TempDir(), "dir-not-file")
	if err := os.Mkdir(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("serve", "--kubeconfig", kc, "--audit-log", bad)
	if code == 0 || !strings.Contains(errs, "audit log") {
		t.Fatalf("serve ran without an audit log: %d %q", code, errs)
	}
}

// The identity warning is for a person. A one-shot command prints it only
// when stderr is a terminal, so an agent that captures stderr does not pay
// for the same line on every call.
func TestIdentityWarningOnlyOnATerminal(t *testing.T) {
	kc := fakeCluster(t)
	for _, args := range [][]string{
		{"list", "pods", "--kubeconfig", kc},
		{"get", "pods", "--kubeconfig", kc},
		{"kubectl", "get", "pods", "--kubeconfig", kc},
	} {
		_, _, errs := run(args...)
		if strings.Contains(errs, "not a read-only identity") {
			t.Fatalf("%v: warning without a terminal: %q", args, errs)
		}
	}
	old := stderrIsTerminal
	stderrIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { stderrIsTerminal = old })
	for _, args := range [][]string{
		{"list", "pods", "--kubeconfig", kc},
		{"get", "pods", "--kubeconfig", kc},
	} {
		_, _, errs := run(args...)
		if !strings.Contains(errs, "not a read-only identity") {
			t.Fatalf("%v: no warning on a terminal: %q", args, errs)
		}
	}
}
