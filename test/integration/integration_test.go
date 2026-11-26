//go:build integration

// Package integration runs the canary harness against a real kind cluster
// with API server audit logging (scripts/kind.sh up). It checks the server
// with a cluster-admin identity (the tools must hold without RBAC help),
// then with the identity that init creates, and it checks that the harness
// fails a deliberately unsafe server.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/test/fixture"
	"github.com/groundcover-com/kray/test/harness"
)

var target = flag.String("target", "", "run the harness against this MCP server command instead (space-separated)")

const harnessNS = "mcp-harness"

func root(t *testing.T) string {
	p, _ := filepath.Abs(filepath.Join("..", ".."))
	return p
}

func out(t *testing.T) string {
	d := filepath.Join(root(t), "test", "integration", "out")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func gvrFor(u *unstructured.Unstructured) schema.GroupVersionResource {
	gv, _ := schema.ParseGroupVersion(u.GetAPIVersion())
	res := map[string]string{
		"CustomResourceDefinition": "customresourcedefinitions", "Namespace": "namespaces", "Secret": "secrets",
		"ConfigMap": "configmaps", "Deployment": "deployments", "Service": "services", "Event": "events", "Widget": "widgets",
	}[u.GetKind()]
	return gv.WithResource(res)
}

func apply(t *testing.T, dyn dynamic.Interface, u *unstructured.Unstructured) {
	t.Helper()
	ctx := context.Background()
	r := dyn.Resource(gvrFor(u))
	var ri dynamic.ResourceInterface = r
	if u.GetNamespace() != "" {
		ri = r.Namespace(u.GetNamespace())
	}
	for i := 0; i < 30; i++ {
		_, err := ri.Apply(ctx, u.GetName(), u, metav1.ApplyOptions{FieldManager: "canary-fixture", Force: true})
		if err == nil {
			return
		}
		time.Sleep(2 * time.Second) // the CRD may not be established yet
		if i == 29 {
			t.Fatalf("apply %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
}

func randSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func runCmd(t *testing.T, env []string, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return b.String(), code
}

func auditLog(t *testing.T) func() ([]byte, error) {
	return func() ([]byte, error) {
		cmd := exec.Command(filepath.Join(root(t), "scripts", "kind.sh"), "audit-log")
		cmd.Dir = root(t)
		return cmd.Output()
	}
}

// targetKubeconfig makes a service account bound to cluster-admin and
// writes a kubeconfig with a one-hour token for it.
func targetKubeconfig(t *testing.T, cs kubernetes.Interface, admin *rest.Config, server string) (string, string) {
	ctx := context.Background()
	_, _ = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: harnessNS}}, metav1.CreateOptions{})
	_, err := cs.CoreV1().ServiceAccounts(harnessNS).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "target"}}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	_, err = cs.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-harness-target-admin"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "target", Namespace: harnessNS}},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	secs := int64(3600)
	tr, err := cs.CoreV1().ServiceAccounts(harnessNS).CreateToken(ctx, "target", &authv1.TokenRequest{Spec: authv1.TokenRequestSpec{ExpirationSeconds: &secs}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kc := clientcmdapi.NewConfig()
	kc.Clusters["kind"] = &clientcmdapi.Cluster{Server: server, CertificateAuthorityData: admin.CAData}
	kc.AuthInfos["target"] = &clientcmdapi.AuthInfo{Token: tr.Status.Token}
	kc.Contexts["harness"] = &clientcmdapi.Context{Cluster: "kind", AuthInfo: "target", Namespace: "default"}
	kc.CurrentContext = "harness"
	p := filepath.Join(t.TempDir(), "target.kubeconfig")
	if err := clientcmd.WriteToFile(*kc, p); err != nil {
		t.Fatal(err)
	}
	return p, "system:serviceaccount:" + harnessNS + ":target"
}

func TestCanaryHarness(t *testing.T) {
	adminKC := filepath.Join(out(t), "kubeconfig")
	if _, err := os.Stat(adminKC); err != nil {
		t.Fatalf("no kind cluster: run `make kind-up` first (%v)", err)
	}
	admin, err := clientcmd.BuildConfigFromFlags("", adminKC)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(admin)
	dyn := dynamic.NewForConfigOrDie(admin)
	ctx := context.Background()
	server, _ := url.Parse(admin.Host)

	// The fixture.
	fx := fixture.New("fixture-" + randSuffix())
	for _, o := range fx.Objects() {
		apply(t, dyn, o)
	}
	if os.Getenv("KEEP_FIXTURE") == "" {
		t.Cleanup(func() {
			_ = cs.CoreV1().Namespaces().Delete(context.Background(), fx.Namespace, metav1.DeleteOptions{})
		})
	}
	var pod corev1.Pod
	deadline := time.Now().Add(5 * time.Minute)
	for {
		pods, err := cs.CoreV1().Pods(fx.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=api"})
		if err == nil && len(pods.Items) > 0 && pods.Items[0].Status.Phase == corev1.PodRunning {
			pod = pods.Items[0]
			logs, _ := cs.CoreV1().Pods(fx.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
			if strings.Contains(string(logs), fixture.ReadyLine) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture pod did not start: %v", err)
		}
		time.Sleep(3 * time.Second)
	}

	bin := filepath.Join(t.TempDir(), brand.Name)
	if b, err := exec.Command("go", "build", "-o", bin, root(t)).CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	targetKC, targetUser := targetKubeconfig(t, cs, admin, admin.Host)

	newHome := func() (env []string, auditFile string) {
		home := t.TempDir()
		cfg := filepath.Join(home, "config")
		state := filepath.Join(home, "state")
		_ = os.MkdirAll(filepath.Join(cfg, brand.Name), 0o700)
		_ = os.WriteFile(filepath.Join(cfg, brand.Name, "config.yaml"), []byte("customResources:\n  - {group: example.com, version: v1, resource: widgets, kind: Widget, namespaced: true}\n"), 0o600)
		return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + cfg, "XDG_STATE_HOME=" + state}, filepath.Join(state, brand.Name, "audit.jsonl")
	}

	// 1. top says plainly that there is no metrics API.
	if _, err := cs.Discovery().ServerResourcesForGroupVersion("metrics.k8s.io/v1beta1"); err != nil {
		env, _ := newHome()
		o, code := runCmd(t, append(env, "KUBECONFIG="+targetKC), "", bin, "top", "-n", fx.Namespace)
		if code != 0 || !strings.Contains(o, "not available in this cluster") {
			t.Fatalf("top without metrics-server (exit %d):\n%s", code, o)
		}
		if o, err := exec.Command(filepath.Join(root(t), "scripts", "kind.sh"), "metrics-server").CombinedOutput(); err != nil {
			t.Fatalf("install metrics-server: %v\n%s", err, o)
		}
	} else {
		t.Log("metrics-server is already installed; the no-metrics check was skipped")
	}
	podMetrics := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	for deadline := time.Now().Add(4 * time.Minute); ; {
		if _, err := dyn.Resource(podMetrics).Namespace(fx.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pod metrics did not appear")
		}
		time.Sleep(5 * time.Second)
	}
	expect := append(fx.Expect(pod.Name, pod.Spec.NodeName), harness.Expect{Tool: "top", Args: map[string]any{"namespace": fx.Namespace}, Contains: pod.Name})

	run := func(name string, tgt harness.Target, exp []harness.Expect) *harness.Report {
		t.Helper()
		// A pause, then the window starts: requests made before this run
		// (init, doctor) must not count as the target's.
		time.Sleep(2 * time.Second)
		start := time.Now()
		rep, err := harness.Run(ctx, tgt, fx.HarnessFixture(pod.Name, pod.Spec.NodeName), harness.Options{
			Expect: exp, AuditLog: auditLog(t), Started: start, OutDir: out(t),
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("\n%s", rep.String())
		return rep
	}

	// Another server, when asked.
	if *target != "" {
		env, _ := newHome()
		rep := run("custom", harness.Target{
			Name: "custom-target", Command: strings.Fields(*target), Env: append(env, "KUBECONFIG="+targetKC),
			Identity: targetUser, APIServer: server.Host, TraceNetwork: true,
		}, nil)
		if !rep.OK() {
			t.Fatalf("the target failed the canary harness:\n%s", rep)
		}
		return
	}

	// 2. The server with a cluster-admin identity.
	env, auditFile := newHome()
	rep := run("admin identity", harness.Target{
		Name: brand.Name + "-cluster-admin", Command: []string{bin, "serve"},
		Env: append(env, "KUBECONFIG="+targetKC), AuditFile: auditFile, Identity: targetUser, APIServer: server.Host, TraceNetwork: true,
	}, expect)
	if !rep.OK() {
		t.Fatalf("failed the canary harness with a cluster-admin identity:\n%s", rep)
	}

	// 3. init, doctor, and the server with the init identity.
	initEnv, initAudit := newHome()
	if o, code := runCmd(t, initEnv, "", bin, "init", "--kubeconfig", adminKC, "--dry-run"); code != 0 || !strings.Contains(o, "Dry run") {
		t.Fatalf("init --dry-run (exit %d):\n%s", code, o)
	}
	if o, code := runCmd(t, initEnv, "y\n", bin, "init", "--kubeconfig", adminKC); code != 0 {
		t.Fatalf("init (exit %d):\n%s", code, o)
	}
	initUser := "system:serviceaccount:" + brand.Name + ":" + brand.Name + "-reader"
	rep = run("init identity", harness.Target{
		Name: brand.Name + "-init-identity", Command: []string{bin, "serve"},
		Env: initEnv, AuditFile: initAudit, Identity: initUser, APIServer: server.Host, TraceNetwork: true,
	}, expect)
	if !rep.OK() {
		t.Fatalf("failed the canary harness with the init identity:\n%s", rep)
	}
	// doctor runs after the harness runs: its review calls are POSTs by the
	// same identities, and they must not fall into a harness window.
	if o, code := runCmd(t, initEnv, "", bin, "doctor"); code != 0 || !strings.Contains(o, "result:    OK") {
		t.Fatalf("doctor with the init identity (exit %d):\n%s", code, o)
	}
	if o, code := runCmd(t, append(env, "KUBECONFIG="+targetKC), "", bin, "doctor"); code != 1 || !strings.Contains(o, "can get secrets") {
		t.Fatalf("doctor with cluster-admin must warn (exit %d):\n%s", code, o)
	}

	// 4. The harness must fail an unsafe server.
	stub := filepath.Join(t.TempDir(), "unsafe-stub")
	if b, err := exec.Command("go", "build", "-o", stub, filepath.Join(root(t), "test", "harness", "stub")).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, b)
	}
	senv, _ := newHome()
	srep := run("unsafe stub", harness.Target{Name: "unsafe-stub", Command: []string{stub}, Env: append(senv, "KUBECONFIG="+targetKC), Identity: targetUser}, nil)
	if srep.OK() || len(srep.Leaks) == 0 || len(srep.BadRequests) == 0 {
		t.Fatalf("the harness did not fail the unsafe stub:\n%s", srep)
	}
	fmt.Printf("integration: canary harness passed with both identities; the unsafe stub failed with %d leaks and %d bad requests\n", len(srep.Leaks), len(srep.BadRequests))
}
