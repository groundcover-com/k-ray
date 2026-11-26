package readonly

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
)

// fakeAPI is a TLS server that answers a few read paths and records every
// request that reaches it.
type fakeAPI struct {
	srv *httptest.Server

	mu      sync.Mutex
	seen    []string
	auth    []string
	accepts []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI())
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.accepts = append(f.accepts, r.Header.Get("Accept"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			fmt.Fprint(w, `{"kind":"APIVersions","versions":["v1"],"serverAddressByClientCIDRs":[]}`)
		case "/apis":
			fmt.Fprint(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"apps","versions":[{"groupVersion":"apps/v1","version":"v1"}],"preferredVersion":{"groupVersion":"apps/v1","version":"v1"}}]}`)
		case "/api/v1/namespaces/shop/pods":
			fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[{"metadata":{"name":"api","namespace":"shop"}}]}`)
		case "/api/v1/configmaps":
			fmt.Fprint(w, `{"kind":"PartialObjectMetadataList","apiVersion":"meta.k8s.io/v1","metadata":{},"items":[{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"name":"big","namespace":"shop"}}]}`)
		case "/api/v1/namespaces/shop/pods/api":
			fmt.Fprint(w, `{"kind":"Pod","apiVersion":"v1","metadata":{"name":"api","namespace":"shop"}}`)
		case "/api/v1/namespaces/shop/pods/api/log":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "line one\nline two\n")
		case "/apis/metrics.k8s.io/v1beta1/namespaces/shop/pods":
			fmt.Fprint(w, `{"kind":"PodMetricsList","apiVersion":"metrics.k8s.io/v1beta1","metadata":{},"items":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeAPI) kubeconfig(t *testing.T, dir, authInfo, extraUser string) string {
	t.Helper()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	user := "    token: test-token-123\n"
	if extraUser != "" {
		user = extraUser
	}
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
    certificate-authority: %s
users:
- name: %s
  user:
%scontexts:
- name: test
  context:
    cluster: test
    user: %s
    namespace: shop
current-context: test
`, f.srv.URL, caFile, authInfo, user, authInfo)
	p := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(p, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadedClientsAllowReads(t *testing.T) {
	f := newFakeAPI(t)
	cl, err := Load(Options{Kubeconfig: f.kubeconfig(t, t.TempDir(), "tester", "")})
	if err != nil {
		t.Fatal(err)
	}
	if cl.Namespace != "shop" || cl.Context != "test" || cl.Source != SourceKubeconfig {
		t.Fatalf("unexpected cluster metadata: %+v", cl)
	}
	cfg := cl.RESTConfig()
	ctx := context.Background()

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	if _, err := dyn.Resource(pods).Namespace("shop").List(ctx, metav1.ListOptions{Limit: 500}); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if _, err := dyn.Resource(pods).Namespace("shop").Get(ctx, "api", metav1.GetOptions{}); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	metrics := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	if _, err := dyn.Resource(metrics).Namespace("shop").List(ctx, metav1.ListOptions{}); err != nil {
		t.Fatalf("list pod metrics: %v", err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tail := int64(2000)
	rc, err := cs.CoreV1().Pods("shop").GetLogs("api", &corev1.PodLogOptions{TailLines: &tail, Timestamps: true}).Stream(ctx)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !strings.Contains(string(b), "line two") {
		t.Fatalf("logs body %q", b)
	}

	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dc.ServerGroups(); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	// A metadata-only list: still a GET, with another Accept header.
	mc, err := metadata.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ml, err := mc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		t.Fatalf("metadata list: %v", err)
	}
	if len(ml.Items) != 1 || ml.Items[0].Name != "big" {
		t.Fatalf("metadata list items: %+v", ml.Items)
	}
	f.mu.Lock()
	lastAccept := f.accepts[len(f.accepts)-1]
	f.mu.Unlock()
	if !strings.Contains(lastAccept, "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1") {
		t.Fatalf("metadata list Accept header %q", lastAccept)
	}

	for i, r := range f.requests() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("request %d reached the server with a non-GET method: %s", i, r)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.auth {
		if a != "Bearer test-token-123" {
			t.Errorf("request without the bearer token: %q", a)
		}
	}
}

// Writes, secrets, exec, proxy, watch and follow never reach the server.
func TestLoadedClientsBlockEverythingElse(t *testing.T) {
	f := newFakeAPI(t)
	cl, err := Load(Options{Kubeconfig: f.kubeconfig(t, t.TempDir(), "tester", "")})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cl.RESTConfig()
	var blocked atomic.Int64
	ctx := WithBlockCounter(context.Background(), &blocked)

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := metadata.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	metaCMs := mc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"})
	pods := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("shop")
	secrets := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("shop")
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "x"}}}
	follow := true

	attempts := map[string]func() error{
		"create": func() error { _, err := pods.Create(ctx, obj, metav1.CreateOptions{}); return err },
		"update": func() error { _, err := pods.Update(ctx, obj, metav1.UpdateOptions{}); return err },
		"patch": func() error {
			_, err := pods.Patch(ctx, "api", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{})
			return err
		},
		"delete":            func() error { return pods.Delete(ctx, "api", metav1.DeleteOptions{}) },
		"delete collection": func() error { return pods.DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{}) },
		"watch": func() error {
			w, err := pods.Watch(ctx, metav1.ListOptions{})
			if w != nil {
				w.Stop()
			}
			return err
		},
		// The metadata client sends the same Accept header for its writes; the
		// header does not let a write through.
		"metadata delete": func() error { return metaCMs.Namespace("shop").Delete(ctx, "big", metav1.DeleteOptions{}) },
		"metadata delete collection": func() error {
			return metaCMs.Namespace("shop").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
		},
		"metadata patch": func() error {
			_, err := metaCMs.Namespace("shop").Patch(ctx, "big", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{})
			return err
		},
		"metadata watch": func() error {
			w, err := metaCMs.Watch(ctx, metav1.ListOptions{})
			if w != nil {
				w.Stop()
			}
			return err
		},
		"metadata list secrets": func() error {
			_, err := mc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).List(ctx, metav1.ListOptions{})
			return err
		},
		"get secret":  func() error { _, err := secrets.Get(ctx, "db", metav1.GetOptions{}); return err },
		"list secret": func() error { _, err := secrets.List(ctx, metav1.ListOptions{}); return err },
		"typed secret": func() error {
			_, err := cs.CoreV1().Secrets("shop").Get(ctx, "db", metav1.GetOptions{})
			return err
		},
		"logs follow": func() error {
			rc, err := cs.CoreV1().Pods("shop").GetLogs("api", &corev1.PodLogOptions{Follow: follow}).Stream(ctx)
			if rc != nil {
				_ = rc.Close()
			}
			return err
		},
		"exec post": func() error {
			return cs.CoreV1().RESTClient().Post().Namespace("shop").Resource("pods").Name("api").SubResource("exec").Do(ctx).Error()
		},
		"exec get": func() error {
			return cs.CoreV1().RESTClient().Get().Namespace("shop").Resource("pods").Name("api").SubResource("exec").Do(ctx).Error()
		},
		"node proxy": func() error {
			return cs.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/n/proxy/exec/shop/api/c").Do(ctx).Error()
		},
		"service proxy": func() error {
			return cs.CoreV1().RESTClient().Get().AbsPath("/api/v1/namespaces/shop/services/api/proxy/").Do(ctx).Error()
		},
		"token request": func() error {
			return cs.CoreV1().RESTClient().Post().AbsPath("/api/v1/namespaces/shop/serviceaccounts/default/token").Body([]byte(`{}`)).Do(ctx).Error()
		},
		"websocket upgrade on an allowed path": func() error {
			hc, err := rest.HTTPClientFor(cfg)
			if err != nil {
				return err
			}
			r, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/api/v1/namespaces/shop/pods/api/log", nil)
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Connection", "Upgrade")
			resp, err := hc.Do(r)
			if resp != nil {
				_ = resp.Body.Close()
			}
			return err
		},
		"other host with the same client": func() error {
			hc, err := rest.HTTPClientFor(cfg)
			if err != nil {
				return err
			}
			r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.2:1/api/v1/pods", nil)
			resp, err := hc.Do(r)
			if resp != nil {
				_ = resp.Body.Close()
			}
			return err
		},
	}
	for name, fn := range attempts {
		err := fn()
		if err == nil {
			t.Errorf("%s: succeeded, want blocked", name)
			continue
		}
		if !errors.Is(err, ErrBlocked) && !strings.Contains(err.Error(), ErrBlocked.Error()) {
			t.Errorf("%s: error %v, want ErrBlocked", name, err)
		}
	}
	if got := f.requests(); len(got) != 0 {
		t.Errorf("blocked requests reached the server: %v", got)
	}
	if blocked.Load() < int64(len(attempts)) {
		t.Errorf("block counter = %d, want at least %d", blocked.Load(), len(attempts))
	}
}

func TestLoadSelectsInitKubeconfig(t *testing.T) {
	f := newFakeAPI(t)
	dir := t.TempDir()
	initPath := f.kubeconfig(t, dir, "system:serviceaccount:reader-ns:reader@prod", "")

	cl, err := Load(Options{InitKubeconfig: initPath})
	if err != nil {
		t.Fatal(err)
	}
	if cl.Source != SourceInit {
		t.Fatalf("source = %q, want init", cl.Source)
	}
	if cl.Identity != "system:serviceaccount:reader-ns:reader" {
		t.Fatalf("identity = %q", cl.Identity)
	}

	// An explicit kubeconfig wins over the init kubeconfig.
	other := f.kubeconfig(t, t.TempDir(), "person", "")
	cl, err = Load(Options{Kubeconfig: other, InitKubeconfig: initPath})
	if err != nil {
		t.Fatal(err)
	}
	if cl.Source != SourceKubeconfig || cl.Identity != "" {
		t.Fatalf("explicit kubeconfig not used: %+v", cl)
	}
}

func TestLoadReportsExecPluginAndProtectsStdin(t *testing.T) {
	f := newFakeAPI(t)
	execUser := "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: aws\n      args: [eks, get-token]\n      interactiveMode: IfAvailable\n"
	p := f.kubeconfig(t, t.TempDir(), "eks-user", execUser)
	cl, err := Load(Options{Kubeconfig: p, StdinUnavailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if cl.ExecPlugin != "aws" {
		t.Fatalf("exec plugin = %q", cl.ExecPlugin)
	}
	if cfg := cl.RESTConfig(); cfg.ExecProvider == nil || !cfg.ExecProvider.StdinUnavailable {
		t.Fatal("exec provider may read stdin, which carries the MCP protocol")
	}
}

func TestLoadUnknownContext(t *testing.T) {
	f := newFakeAPI(t)
	if _, err := Load(Options{Kubeconfig: f.kubeconfig(t, t.TempDir(), "u", ""), Context: "nope"}); err == nil {
		t.Fatal("want an error for an unknown context")
	}
}
