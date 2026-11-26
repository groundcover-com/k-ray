package readonly

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var testTarget = Target{Scheme: "https", Host: "api.test:6443"}

func req(t testing.TB, method, rawURL string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return &http.Request{Method: method, URL: u, Header: http.Header{}, Host: u.Host}
}

// Every request shape that kray sends must pass.
func TestCheckAllows(t *testing.T) {
	base := "https://api.test:6443"
	for _, p := range []string{
		"/version",
		"/api",
		"/apis",
		"/api/v1",
		"/apis/apps",
		"/apis/apps/v1",
		"/apis/metrics.k8s.io/v1beta1",
		"/api/v1/namespaces",
		"/api/v1/namespaces/shop",
		"/api/v1/namespaces/shop/pods",
		"/api/v1/namespaces/shop/pods?labelSelector=app%3Dapi&limit=500",
		"/api/v1/namespaces/shop/pods?limit=500&continue=eyJ2IjoibWV0YS5rOHMuaW8vdjEifQ",
		"/api/v1/namespaces/shop/pods/api-7f9c8-x2k",
		"/api/v1/namespaces/shop/pods/api-7f9c8-x2k/log?container=api&previous=true&tailLines=2000&limitBytes=4194304&timestamps=true&sinceSeconds=600",
		"/api/v1/pods?limit=500",
		"/api/v1/nodes",
		"/api/v1/nodes/ip-10-0-1-23.eu-west-1.compute.internal",
		"/api/v1/namespaces/shop/events?fieldSelector=involvedObject.uid%3D7b0c5a",
		"/apis/apps/v1/namespaces/shop/deployments/api",
		"/apis/apps/v1/deployments?limit=500",
		"/apis/metrics.k8s.io/v1beta1/namespaces/shop/pods",
		"/apis/metrics.k8s.io/v1/nodes",
		"/apis/metrics.k8s.io/v1beta1/pods",
		"/apis/discovery.k8s.io/v1/namespaces/shop/endpointslices?labelSelector=kubernetes.io%2Fservice-name%3Dapi",
		"/apis/argoproj.io/v1alpha1/namespaces/argo/rollouts/web",
		// Whole segments only, so these names are fine.
		"/api/v1/namespaces/kube-system/pods/kube-proxy-abcde",
		"/api/v1/namespaces/external-secrets/pods",
		"/api/v1/namespaces/auth/pods/oauth2-proxy-5d4f9",
		"/api/v1/namespaces/csi/pods/secrets-store-csi-driver-x1",
		"/api/v1/namespaces/shop/configmaps/exec-hooks",
	} {
		if err := Check(req(t, http.MethodGet, base+p), testTarget); err != nil {
			t.Errorf("GET %s: blocked, want allowed: %v", p, err)
		}
	}
}

// metaListAccept is the Accept header of a metadata-only list, as the
// metadata client of client-go sends it.
const metaListAccept = "application/vnd.kubernetes.protobuf;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1,application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1,application/json"

// A metadata-only list is a GET of a list path with another Accept header. The
// header does not change what Check allows: a GET of a list passes, any other
// method with the same header is blocked, and so are the denied paths.
func TestCheckMetadataList(t *testing.T) {
	base := "https://api.test:6443"
	withAccept := func(method, p string) *http.Request {
		r := req(t, method, base+p)
		r.Header.Set("Accept", metaListAccept)
		return r
	}
	for _, p := range []string{
		"/api/v1/configmaps?limit=500",
		"/api/v1/pods?limit=500&continue=eyJ2IjoibWV0YS5rOHMuaW8vdjEifQ",
		"/api/v1/namespaces/shop/configmaps?limit=500",
		"/apis/apps/v1/deployments?limit=500",
		"/apis/argoproj.io/v1alpha1/rollouts?limit=500",
	} {
		if err := Check(withAccept(http.MethodGet, p), testTarget); err != nil {
			t.Errorf("GET %s with a metadata Accept header: blocked, want allowed: %v", p, err)
		}
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD"} {
		for _, p := range []string{"/api/v1/configmaps", "/api/v1/namespaces/shop/configmaps", "/api/v1/namespaces/shop/configmaps/big"} {
			if err := Check(withAccept(m, p), testTarget); !errors.Is(err, ErrBlocked) {
				t.Errorf("%s %s with a metadata Accept header: %v, want blocked", m, p, err)
			}
		}
	}
	for _, p := range []string{"/api/v1/secrets?limit=500", "/api/v1/namespaces/shop/secrets", "/api/v1/configmaps?watch=true", "/api/v1/nodes/n/proxy"} {
		if err := Check(withAccept(http.MethodGet, p), testTarget); !errors.Is(err, ErrBlocked) {
			t.Errorf("GET %s with a metadata Accept header: %v, want blocked", p, err)
		}
	}
}

// Every write, upgrade, dangerous path, other host and unknown query must be
// blocked. Each case names the rule it exercises.
func TestCheckBlocks(t *testing.T) {
	base := "https://api.test:6443"
	type tc struct {
		name   string
		method string
		url    string
		mutate func(*http.Request)
	}
	var cases []tc

	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE", "get", "", "PROPFIND"} {
		cases = append(cases, tc{name: "method " + m, method: m, url: base + "/api/v1/namespaces/shop/pods"})
	}

	cases = append(cases,
		tc{name: "body", method: "GET", url: base + "/api/v1/pods", mutate: func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader("{}"))
		}},
		tc{name: "content length", method: "GET", url: base + "/api/v1/pods", mutate: func(r *http.Request) { r.ContentLength = 2 }},
		tc{name: "upgrade websocket", method: "GET", url: base + "/api/v1/namespaces/shop/pods/p/log", mutate: func(r *http.Request) {
			r.Header.Set("Upgrade", "websocket")
		}},
		tc{name: "upgrade spdy", method: "GET", url: base + "/api/v1/namespaces/shop/pods/p/log", mutate: func(r *http.Request) {
			r.Header.Set("Upgrade", "SPDY/3.1")
		}},
		tc{name: "connection upgrade", method: "GET", url: base + "/api/v1/pods", mutate: func(r *http.Request) {
			r.Header.Set("Connection", "keep-alive, Upgrade")
		}},
		tc{name: "websocket key", method: "GET", url: base + "/api/v1/pods", mutate: func(r *http.Request) {
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		}},
		// Header keys that are not in canonical form.
		tc{name: "raw lowercase upgrade", method: "GET", url: base + "/api/v1/namespaces/shop/pods/p/log", mutate: func(r *http.Request) {
			r.Header["upgrade"] = []string{"SPDY/3.1"}
		}},
		tc{name: "raw lowercase connection", method: "GET", url: base + "/api/v1/pods", mutate: func(r *http.Request) {
			r.Header["connection"] = []string{"Upgrade"}
		}},
		tc{name: "raw lowercase websocket", method: "GET", url: base + "/api/v1/pods", mutate: func(r *http.Request) {
			r.Header["sec-websocket-version"] = []string{"13"}
		}},
	)

	for _, u := range []string{
		"https://evil.test:6443/api/v1/pods",
		"https://api.test:6444/api/v1/pods",
		"http://api.test:6443/api/v1/pods",
		"https://api.test/api/v1/pods",
		"https://user:pass@api.test:6443/api/v1/pods",
		"https://api.test:6443/api/v1/pods#frag",
	} {
		cases = append(cases, tc{name: "host " + u, method: "GET", url: u})
	}

	for _, p := range []string{
		// exec, attach, port-forward and proxy in every place they appear.
		"/api/v1/namespaces/shop/pods/p/exec",
		"/api/v1/namespaces/shop/pods/p/exec?command=sh&stdin=true",
		"/api/v1/namespaces/shop/pods/p/attach",
		"/api/v1/namespaces/shop/pods/p/portforward",
		"/api/v1/namespaces/shop/pods/p/proxy",
		"/api/v1/namespaces/shop/pods/p/proxy/metrics",
		"/api/v1/namespaces/shop/services/s/proxy",
		"/api/v1/namespaces/shop/services/https:s:443/proxy/",
		"/api/v1/nodes/n/proxy",
		"/api/v1/nodes/n/proxy/exec/shop/p/c",
		"/api/v1/nodes/n/proxy/run/shop/p/c",
		"/api/v1/nodes/n/proxy/metrics/cadvisor",
		"/api/v1/proxy/nodes/n",
		// Secrets in every spelling.
		"/api/v1/secrets",
		"/api/v1/namespaces/shop/secrets",
		"/api/v1/namespaces/shop/secrets/db",
		"/api/v1/namespaces/shop/SECRETS/db",
		"/api/v1/namespaces/shop/Secrets",
		"/api/v1/namespaces/secrets",
		"/apis/v1/secrets",
		// Subresources other than pods/log.
		"/apis/apps/v1/namespaces/shop/deployments/api/scale",
		"/apis/apps/v1/namespaces/shop/deployments/api/status",
		"/api/v1/namespaces/shop/pods/p/status",
		"/api/v1/namespaces/shop/pods/p/ephemeralcontainers",
		"/api/v1/namespaces/shop/pods/p/eviction",
		"/api/v1/namespaces/shop/pods/p/binding",
		"/api/v1/namespaces/shop/serviceaccounts/default/token",
		"/api/v1/nodes/n/status",
		"/api/v1/namespaces/shop/pods/p/log/extra",
		"/api/v1/namespaces/shop/configmaps/c/log",
		// Paths outside the resource API.
		"/",
		"/logs",
		"/logs/kube-apiserver.log",
		"/metrics",
		"/healthz",
		"/livez",
		"/debug/pprof/heap",
		"/openapi/v2",
		"/openapi/v3",
		"/.well-known/openid-configuration",
		"/openid/v1/jwks",
		"/version/extra",
		// Dirty paths.
		"/api/",
		"/api//v1",
		"/api/./v1",
		"/api/v1/../v1/secrets",
		"/api/v1/namespaces/shop/pods/..",
		"/api/v1/namespaces/shop/pods/a%2Fb",
		"/api/v1/namespaces/shop/pods/%2e%2e",
		"/api/v1/namespaces/shop/pods/a%5Cb",
		"/api/v1/namespaces/shop/pods/a;b",
		"/api/v1/namespaces/shop/pods/caf%C3%A9",
		"/api/v1/namespaces/shop/pods/Pod-A",
		"/api/v1/namespaces/shop/pods/-p",
		"/api/v1/namespaces/shop/pods/--server=https:evil",
		"/api/vX/pods",
		"/apis/Apps/v1/deployments",
		"/apis/apps/latest/deployments",
		"/apis/apps/v1/namespaces/shop/deployments/api/x/y",
		"/api/v1/namespaces/shop/pods/p/log/x/y",
		// Unknown and streaming query keys.
		"/api/v1/pods?watch=true",
		"/api/v1/pods?watch=1",
		"/api/v1/pods?limit=5&watch=true",
		"/api/v1/namespaces/shop/pods/p/log?follow=true",
		"/api/v1/pods?timeoutSeconds=5",
		"/api/v1/pods?resourceVersion=0",
		"/api/v1/pods?pretty=true",
		"/api/v1/pods?labelSelector=a%3Db&labelSelector=c%3Dd",
		"/api/v1/pods?Watch=true",
		"/api/v1/pods?limit=5;watch=true",
		"/api/v1/namespaces/shop/pods/p/log?insecureSkipTLSVerifyBackend=true",
		"/api/v1/pods?dryRun=All",
	} {
		cases = append(cases, tc{name: "path " + p, method: "GET", url: base + p})
	}

	for _, c := range cases {
		r := req(t, c.method, c.url)
		if c.mutate != nil {
			c.mutate(r)
		}
		err := Check(r, testTarget)
		if err == nil {
			t.Errorf("%s: allowed, want blocked", c.name)
			continue
		}
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: error %v is not ErrBlocked", c.name, err)
		}
	}
}

func TestCheckPrefix(t *testing.T) {
	u, _ := url.Parse("https://rancher.test/k8s/clusters/c-1")
	target, err := TargetFor(u)
	if err != nil {
		t.Fatal(err)
	}
	ok := []string{"/k8s/clusters/c-1/api", "/k8s/clusters/c-1/api/v1/pods", "/k8s/clusters/c-1/version"}
	bad := []string{"/api/v1/pods", "/k8s/clusters/c-2/api/v1/pods", "/k8s/clusters/c-1", "/k8s/clusters/c-1x/api", "/k8s/clusters/c-1/api/v1/secrets"}
	for _, p := range ok {
		if err := Check(req(t, "GET", "https://rancher.test"+p), target); err != nil {
			t.Errorf("%s: blocked: %v", p, err)
		}
	}
	for _, p := range bad {
		if err := Check(req(t, "GET", "https://rancher.test"+p), target); err == nil {
			t.Errorf("%s: allowed, want blocked", p)
		}
	}
}

func TestTargetFor(t *testing.T) {
	for _, s := range []string{"ftp://api.test", "https://", "https://api.test/a/../b", "https://api.test/a b"} {
		u, err := url.Parse(s)
		if err != nil {
			continue
		}
		if _, err := TargetFor(u); err == nil {
			t.Errorf("TargetFor(%q): want error", s)
		}
	}
	if _, err := TargetFor(nil); err == nil {
		t.Error("TargetFor(nil): want error")
	}
}

// FuzzCheck checks the invariants of every request that Check allows, with
// an oracle that does not share code with Check.
func FuzzCheck(f *testing.F) {
	f.Add("GET", "/api/v1/namespaces/shop/pods", "limit=5", "")
	f.Add("GET", "/api/v1/namespaces/shop/pods/p/exec", "", "websocket")
	f.Add("POST", "/api/v1/namespaces/shop/pods", "", "")
	f.Add("GET", "/api/v1/nodes/n/proxy", "", "")
	f.Add("GET", "/api/v1/namespaces/shop/pods/p/log", "follow=true", "")
	f.Fuzz(func(t *testing.T, method, p, query, upgrade string) {
		u := &url.URL{Scheme: "https", Host: "api.test:6443", Path: p, RawQuery: query}
		r := &http.Request{Method: method, URL: u, Header: http.Header{}}
		if upgrade != "" {
			r.Header.Set("Upgrade", upgrade)
		}
		if Check(r, testTarget) != nil {
			return
		}
		if method != "GET" {
			t.Fatalf("allowed method %q", method)
		}
		if upgrade != "" && strings.TrimSpace(upgrade) != "" {
			t.Fatalf("allowed upgrade %q", upgrade)
		}
		if !(p == "/version" || p == "/api" || p == "/apis" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/apis/")) {
			t.Fatalf("allowed path %q", p)
		}
		for _, seg := range strings.Split(p, "/") {
			l := strings.ToLower(seg)
			if l == ".." || l == "." || l == "proxy" || l == "exec" || l == "attach" || l == "portforward" || l == "secrets" {
				t.Fatalf("allowed segment %q in %q", seg, p)
			}
		}
		q, _ := url.ParseQuery(query)
		if _, ok := q["watch"]; ok {
			t.Fatalf("allowed watch in %q", query)
		}
		if _, ok := q["follow"]; ok {
			t.Fatalf("allowed follow in %q", query)
		}
	})
}
