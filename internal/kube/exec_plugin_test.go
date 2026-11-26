package kube

import (
	"context"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

// Reads that start at the same time share one run of the credential plugin:
// the plugin takes about as long as a read (0.4 s on a real cluster), so a
// call that ran it once for each read would not be faster.
func TestConcurrentReadsRunTheCredentialPluginOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a shell script as a credential plugin")
	}
	a := newAPI(t)
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "plugin.sh")
	body := fmt.Sprintf("#!/bin/sh\necho run >> %q\nsleep 0.3\nprintf '%%s' '{\"apiVersion\":\"client.authentication.k8s.io/v1\",\"kind\":\"ExecCredential\",\"status\":{\"token\":\"t\",\"expirationTimestamp\":\"2099-01-01T00:00:00Z\"}}'\n", runs)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(dir, "ca.crt")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.srv.Certificate().Raw}), 0o600)
	kc := filepath.Join(dir, "kubeconfig")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: %q, certificate-authority: %q}
users:
- name: u
  user:
    exec: {apiVersion: client.authentication.k8s.io/v1, command: %q, interactiveMode: Never}
contexts:
- name: x
  context: {cluster: c, user: u, namespace: shop}
current-context: x
`, a.srv.URL, ca, script)
	if err := os.WriteFile(kc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cl, err := readonly.Load(readonly.Options{Kubeconfig: kc, StdinUnavailable: true})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cl)
	if err != nil {
		t.Fatal(err)
	}
	pods, _ := kinds.Default().Lookup("pods")
	var wg sync.WaitGroup
	reads := []func() error{
		func() error { _, err := c.List(context.Background(), pods, "shop", "", 10); return err },
		func() error { _, err := c.Events(context.Background(), "shop", nil, 10); return err },
		func() error { _, err := c.Metrics(context.Background(), PodMetrics, "shop", ""); return err },
		func() error { _, err := c.Logs(context.Background(), "shop", "a", LogOptions{}); return err },
	}
	a.metricsGroup = "v1beta1"
	for _, read := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := read(); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(runs)
	if n := strings.Count(string(b), "run"); n != 1 {
		t.Errorf("the credential plugin ran %d times for %d reads that started together, want 1", n, len(reads))
	}
}
