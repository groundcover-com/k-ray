package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fakeAPI is a small Kubernetes API server: it serves stored objects for
// GET and records every request, whatever its method, like an audit log.
type fakeAPI struct {
	srv     *httptest.Server
	mu      sync.Mutex
	records []record
	objs    map[string][]*unstructured.Unstructured // group/resource -> objects
	logs    map[string]string                       // ns/pod -> text
	metrics []*unstructured.Unstructured
}

type record struct {
	when   time.Time
	method string
	uri    string
}

var resourceOf = map[string]string{
	"Pod": "pods", "Deployment": "deployments", "ReplicaSet": "replicasets", "ConfigMap": "configmaps", "Service": "services",
	"Secret": "secrets", "Event": "events", "Widget": "widgets", "Node": "nodes", "Namespace": "namespaces",
	"EndpointSlice": "endpointslices", "CustomResourceDefinition": "customresourcedefinitions",
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{objs: map[string][]*unstructured.Unstructured{}, logs: map[string]string{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) add(u *unstructured.Unstructured) {
	gv, _ := schema.ParseGroupVersion(u.GetAPIVersion())
	key := gv.Group + "/" + resourceOf[u.GetKind()]
	f.objs[key] = append(f.objs[key], u)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func status(w http.ResponseWriter, code int, reason string) {
	writeJSON(w, code, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": reason, "code": code})
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.records = append(f.records, record{time.Now(), r.Method, r.URL.RequestURI()})
	f.mu.Unlock()
	if r.Method != http.MethodGet {
		status(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
		return
	}
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/api":
		writeJSON(w, 200, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		return
	case r.URL.Path == "/apis":
		gv := func(g, v string) map[string]any {
			return map[string]any{"name": g, "versions": []any{map[string]any{"groupVersion": g + "/" + v, "version": v}}, "preferredVersion": map[string]any{"groupVersion": g + "/" + v, "version": v}}
		}
		writeJSON(w, 200, map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": []any{gv("apps", "v1"), gv("example.com", "v1"), gv("metrics.k8s.io", "v1beta1")}})
		return
	}
	var group, version string
	var rest []string
	switch {
	case len(segs) >= 2 && segs[0] == "api":
		version, rest = segs[1], segs[2:]
	case len(segs) >= 3 && segs[0] == "apis":
		group, version, rest = segs[1], segs[2], segs[3:]
	default:
		status(w, 404, "NotFound")
		return
	}
	ns, res, name, sub := "", "", "", ""
	switch {
	case len(rest) >= 3 && rest[0] == "namespaces":
		ns, res = rest[1], rest[2]
		if len(rest) >= 4 {
			name = rest[3]
		}
		if len(rest) >= 5 {
			sub = rest[4]
		}
	case len(rest) == 2 && rest[0] == "namespaces" && group == "":
		res, name = "namespaces", rest[1]
	case len(rest) >= 1:
		res = rest[0]
		if len(rest) >= 2 {
			name = rest[1]
		}
	}
	if group == "metrics.k8s.io" {
		var items []any
		for _, m := range f.metrics {
			if (res == "pods" && m.GetKind() == "PodMetrics" && (ns == "" || m.GetNamespace() == ns)) || (res == "nodes" && m.GetKind() == "NodeMetrics") {
				items = append(items, m.Object)
			}
		}
		writeJSON(w, 200, map[string]any{"kind": "List", "apiVersion": "metrics.k8s.io/" + version, "metadata": map[string]any{}, "items": items})
		return
	}
	objs := f.objs[group+"/"+res]
	if sub == "log" && res == "pods" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, f.logs[ns+"/"+name])
		return
	}
	if sub != "" {
		status(w, 404, "NotFound")
		return
	}
	if name != "" {
		for _, o := range objs {
			if o.GetName() == name && (ns == "" || o.GetNamespace() == ns) {
				writeJSON(w, 200, o.Object)
				return
			}
		}
		status(w, 404, "NotFound")
		return
	}
	sel, err := labels.Parse(r.URL.Query().Get("labelSelector"))
	if err != nil {
		status(w, 400, "BadRequest")
		return
	}
	fsel, err := fields.ParseSelector(r.URL.Query().Get("fieldSelector"))
	if err != nil {
		status(w, 400, "BadRequest")
		return
	}
	items := []any{}
	for _, o := range objs {
		if ns != "" && o.GetNamespace() != ns {
			continue
		}
		if !sel.Matches(labels.Set(o.GetLabels())) {
			continue
		}
		fs := fields.Set{"metadata.name": o.GetName()}
		for _, k := range []string{"kind", "name", "uid"} {
			if v, ok, _ := unstructured.NestedString(o.Object, "involvedObject", k); ok {
				fs["involvedObject."+k] = v
			}
		}
		if !fsel.Matches(fs) {
			continue
		}
		items = append(items, o.Object)
	}
	writeJSON(w, 200, map[string]any{"kind": "List", "apiVersion": "v1", "metadata": map[string]any{}, "items": items})
}

// auditLog renders the recorded requests as Kubernetes audit events.
func (f *fakeAPI) auditLog(user string) func() ([]byte, error) {
	return func() ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var b strings.Builder
		for _, r := range f.records {
			path := strings.SplitN(r.uri, "?", 2)[0]
			segs := strings.Split(strings.Trim(path, "/"), "/")
			verb := map[string]string{"POST": "create", "PUT": "update", "PATCH": "patch", "DELETE": "delete"}[r.method]
			resource, sub := "", ""
			if i := indexOf(segs, "namespaces"); i >= 0 && len(segs) > i+2 {
				resource = segs[i+2]
				if len(segs) > i+4 {
					sub = segs[i+4]
				}
				if verb == "" {
					verb = "list"
					if len(segs) > i+3 {
						verb = "get"
					}
				}
			} else if verb == "" {
				verb = "get"
				if len(segs) >= 3 && segs[0] == "api" || len(segs) >= 4 {
					verb = "list"
				}
			}
			e := map[string]any{
				"verb": verb, "requestURI": r.uri, "user": map[string]any{"username": user},
				"requestReceivedTimestamp": r.when.UTC().Format(time.RFC3339Nano),
				"objectRef":                map[string]any{"resource": resource, "subresource": sub},
			}
			j, _ := json.Marshal(e)
			b.Write(j)
			b.WriteByte('\n')
		}
		return []byte(b.String()), nil
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func (f *fakeAPI) methods() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]int{}
	for _, r := range f.records {
		m[r.method]++
	}
	return m
}
