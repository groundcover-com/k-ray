package setup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

func TestManifestsAreReadOnly(t *testing.T) {
	ks, err := kinds.Default().With([]kinds.CustomResource{{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts", Kind: "Rollout", Namespaced: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range Rules(ks) {
		for _, v := range r["verbs"].([]any) {
			if v != "get" && v != "list" {
				t.Errorf("verb %v in %v", v, r)
			}
		}
		for _, res := range r["resources"].([]any) {
			s := res.(string)
			if strings.Contains(s, "secret") || s == "*" || strings.Contains(s, "exec") || strings.Contains(s, "proxy") || strings.Contains(s, "attach") || strings.Contains(s, "portforward") {
				t.Errorf("resource %q in %v", s, r)
			}
			if strings.Contains(s, "/") && s != "pods/log" {
				t.Errorf("subresource %q in %v", s, r)
			}
		}
		for _, g := range r["apiGroups"].([]any) {
			if g == "*" {
				t.Errorf("wildcard group in %v", r)
			}
		}
	}
	y := ManifestYAML(ks)
	for _, want := range []string{"kind: ClusterRole", "- pods/log", "- rollouts", "metrics.k8s.io", "automountServiceAccountToken: false", "name: " + ServiceAccount()} {
		if !strings.Contains(y, want) {
			t.Errorf("manifest lacks %q:\n%s", want, y)
		}
	}
	var body []string
	for _, l := range strings.Split(y, "\n") {
		if !strings.HasPrefix(l, "#") {
			body = append(body, l)
		}
	}
	if b := strings.Join(body, "\n"); strings.Contains(b, "watch") || strings.Contains(b, "secrets") {
		t.Errorf("manifest grants watch or secrets:\n%s", b)
	}
}

// fakeAPI answers the setup calls and records them.
type fakeAPI struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []string
	allowed map[string]bool // "verb resource/sub" -> allowed
	user    string
	expires time.Time
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{allowed: map[string]bool{}, user: "kubernetes-admin", expires: time.Now().Add(8 * time.Hour)}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch:
			var obj map[string]any
			_ = json.Unmarshal(body, &obj)
			_ = json.NewEncoder(w).Encode(obj)
		case strings.HasSuffix(r.URL.Path, "/token"):
			fmt.Fprintf(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenRequest","status":{"token":"tok.en.value","expirationTimestamp":%q}}`, f.expires.UTC().Format(time.RFC3339))
		case strings.HasSuffix(r.URL.Path, "/selfsubjectreviews"):
			fmt.Fprintf(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview","status":{"userInfo":{"username":%q,"groups":["system:authenticated"]}}}`, f.user)
		case strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews"):
			var rev struct {
				Spec struct {
					ResourceAttributes struct{ Verb, Resource, Subresource string }
				}
			}
			_ = json.Unmarshal(body, &rev)
			a := rev.Spec.ResourceAttributes
			key := a.Verb + " " + a.Resource
			if a.Subresource != "" {
				key += "/" + a.Subresource
			}
			fmt.Fprintf(w, `{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":%v}}`, f.allowed[key])
		case strings.HasSuffix(r.URL.Path, "/selfsubjectrulesreviews"):
			fmt.Fprint(w, `{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectRulesReview","status":{"resourceRules":[{"verbs":["get"],"resources":["pods"]}],"incomplete":false}}`)
		case r.URL.Path == "/api":
			fmt.Fprint(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case r.URL.Path == "/apis":
			fmt.Fprint(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"metrics.k8s.io","versions":[{"groupVersion":"metrics.k8s.io/v1beta1","version":"v1beta1"}],"preferredVersion":{"groupVersion":"metrics.k8s.io/v1beta1","version":"v1beta1"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":404}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) kubeconfig(t *testing.T) string {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}))
	p := filepath.Join(t.TempDir(), "person.kubeconfig")
	kc := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: %s\n    certificate-authority-data: %s\nusers:\n- name: admin\n  user:\n    token: admin-token\ncontexts:\n- name: prod\n  context: {cluster: c, user: admin, namespace: shop}\ncurrent-context: prod\n", f.srv.URL, ca)
	if err := os.WriteFile(p, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fakeAPI) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

func TestInitDryRunAppliesNothing(t *testing.T) {
	f := newFakeAPI(t)
	var out bytes.Buffer
	target := filepath.Join(t.TempDir(), "cfg", "kubeconfig")
	err := Init(context.Background(), InitOptions{Kubeconfig: f.kubeconfig(t), DryRun: true, Duration: 8 * time.Hour, Target: target, Out: &out, In: strings.NewReader("")})
	if err != nil {
		t.Fatal(err)
	}
	if f.seen() != "" {
		t.Fatalf("dry run sent requests:\n%s", f.seen())
	}
	if !strings.Contains(out.String(), "kind: ClusterRole") || !strings.Contains(out.String(), "Dry run: nothing was applied") {
		t.Fatalf("output:\n%s", out.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("dry run wrote a kubeconfig")
	}
}

func TestInitRefusesWithoutConfirmation(t *testing.T) {
	f := newFakeAPI(t)
	var out bytes.Buffer
	err := Init(context.Background(), InitOptions{Kubeconfig: f.kubeconfig(t), Duration: 8 * time.Hour, Target: filepath.Join(t.TempDir(), "k"), Out: &out, In: strings.NewReader("n\n")})
	if err == nil || f.seen() != "" {
		t.Fatalf("applied without a yes: %v\n%s", err, f.seen())
	}
}

func TestInitAppliesAndWritesReadOnlyKubeconfig(t *testing.T) {
	f := newFakeAPI(t)
	var out bytes.Buffer
	target := filepath.Join(t.TempDir(), "cfg", "kubeconfig")
	err := Init(context.Background(), InitOptions{Kubeconfig: f.kubeconfig(t), Duration: 8 * time.Hour, Target: target, Out: &out, In: strings.NewReader("y\n")})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	seen := f.seen()
	for _, want := range []string{
		"PATCH /api/v1/namespaces/" + Namespace(), "PATCH /api/v1/namespaces/" + Namespace() + "/serviceaccounts/" + ServiceAccount(),
		"PATCH /apis/rbac.authorization.k8s.io/v1/clusterroles/" + RoleName(), "PATCH /apis/rbac.authorization.k8s.io/v1/clusterrolebindings/" + RoleName(),
		"POST /api/v1/namespaces/" + Namespace() + "/serviceaccounts/" + ServiceAccount() + "/token",
	} {
		if !strings.Contains(seen, want) {
			t.Errorf("missing call %q in:\n%s", want, seen)
		}
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("kubeconfig mode %v", st.Mode().Perm())
	}
	b, _ := os.ReadFile(target)
	if !strings.Contains(string(b), "tok.en.value") || strings.Contains(string(b), "admin-token") || strings.Contains(string(b), "exec:") {
		t.Errorf("kubeconfig:\n%s", b)
	}
	cl, err := readonly.Load(readonly.Options{InitKubeconfig: target})
	if err != nil {
		t.Fatal(err)
	}
	if cl.Source != readonly.SourceInit || cl.Identity != Identity() || cl.Namespace != "shop" {
		t.Fatalf("serve would use %+v", cl)
	}
	if !strings.Contains(out.String(), "identity: "+Identity()) {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestInitTokenStdinOnlyWritesKubeconfig(t *testing.T) {
	f := newFakeAPI(t)
	exp := time.Now().Add(2 * time.Hour).Unix()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	tok := "eyJhbGciOiJSUzI1NiJ9." + payload + ".sig"
	var out bytes.Buffer
	target := filepath.Join(t.TempDir(), "kubeconfig")
	if err := Init(context.Background(), InitOptions{Kubeconfig: f.kubeconfig(t), TokenStdin: true, Duration: 8 * time.Hour, Target: target, Out: &out, In: strings.NewReader(tok + "\n")}); err != nil {
		t.Fatal(err)
	}
	if f.seen() != "" {
		t.Fatalf("token-stdin sent requests:\n%s", f.seen())
	}
	if !strings.Contains(out.String(), "(in 2h0m") {
		t.Errorf("expiry not shown:\n%s", out.String())
	}
}

func TestInitRejectsBadDuration(t *testing.T) {
	for _, d := range []time.Duration{time.Minute, 48 * time.Hour} {
		if err := Init(context.Background(), InitOptions{Duration: d}); err == nil {
			t.Errorf("duration %s accepted", d)
		}
	}
}

func TestDoctor(t *testing.T) {
	f := newFakeAPI(t)
	var out bytes.Buffer
	bad, err := Doctor(context.Background(), DoctorOptions{Kubeconfig: f.kubeconfig(t), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if bad || !strings.Contains(out.String(), "identity:  kubernetes-admin") || !strings.Contains(out.String(), "metrics:   available") || !strings.Contains(out.String(), "result:    OK") {
		t.Fatalf("clean identity:\n%s", out.String())
	}

	f.allowed["get secrets"] = true
	f.allowed["create pods/exec"] = true
	out.Reset()
	bad, err = Doctor(context.Background(), DoctorOptions{Kubeconfig: f.kubeconfig(t), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if !bad || !strings.Contains(out.String(), "can get secrets in all namespaces") || !strings.Contains(out.String(), "can create pods/exec") {
		t.Fatalf("over-privileged identity not reported:\n%s", out.String())
	}
	for _, c := range strings.Split(f.seen(), "\n") {
		if strings.HasPrefix(c, "POST ") && !strings.Contains(c, "selfsubject") {
			t.Errorf("doctor sent a POST outside the review APIs: %s", c)
		}
		if strings.HasPrefix(c, "PATCH ") || strings.HasPrefix(c, "PUT ") || strings.HasPrefix(c, "DELETE ") {
			t.Errorf("doctor sent a write: %s", c)
		}
	}
}

func TestReviewWrapperBlocksOtherPosts(t *testing.T) {
	target := readonly.Target{Scheme: "https", Host: "api.test:6443"}
	var passed []string
	rt := reviewWrapper(target)(rtFunc(func(r *http.Request) (*http.Response, error) {
		passed = append(passed, r.Method+" "+r.URL.Path)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	try := func(method, path string) error {
		u, _ := url.Parse("https://api.test:6443" + path)
		_, err := rt.RoundTrip(&http.Request{Method: method, URL: u, Header: http.Header{}})
		return err
	}
	for _, p := range []string{"/apis/authentication.k8s.io/v1/selfsubjectreviews", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"} {
		if err := try("POST", p); err != nil {
			t.Errorf("POST %s blocked: %v", p, err)
		}
	}
	if err := try("GET", "/api/v1/pods"); err != nil {
		t.Errorf("GET blocked: %v", err)
	}
	for _, c := range [][2]string{{"POST", "/api/v1/namespaces/x/pods"}, {"POST", "/apis/authorization.k8s.io/v1/subjectaccessreviews"}, {"POST", "/api/v1/namespaces/x/serviceaccounts/y/token"}, {"DELETE", "/api/v1/namespaces/x"}, {"GET", "/api/v1/secrets"}} {
		if err := try(c[0], c[1]); err == nil {
			t.Errorf("%s %s allowed", c[0], c[1])
		}
	}
	if len(passed) != 3 {
		t.Errorf("passed %v", passed)
	}
}
