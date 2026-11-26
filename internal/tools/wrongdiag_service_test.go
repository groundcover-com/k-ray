package tools

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// Item 7: services.

func svcObj(name, sel, spec string) string {
	if spec != "" {
		spec = "," + spec
	}
	return `{"apiVersion":"v1","kind":"Service","metadata":{"name":"` + name + `","namespace":"pay","uid":"u-` + name + `","creationTimestamp":"2026-11-13T09:00:00Z"},
"spec":{"selector":{` + sel + `},"ports":[{"port":80,"targetPort":8080}]` + spec + `}}`
}

func deployObj(name string, replicas int, templateLabels string) string {
	return fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"%s","namespace":"pay","uid":"u-d-%s","creationTimestamp":"2026-11-13T09:00:00Z"},
"spec":{"replicas":%d,"selector":{"matchLabels":{%s}},"template":{"metadata":{"labels":{%s}},"spec":{"containers":[{"name":"app","image":"x:1"}]}}},
"status":{"replicas":%d,"readyReplicas":%d,"availableReplicas":%d,"updatedReplicas":%d}}`, name, name, replicas, templateLabels, templateLabels, replicas, replicas, replicas, replicas)
}

func slice(name, svc string, ready bool, withPorts bool) string {
	ports := `[]`
	if withPorts {
		ports = `[{"port":8080}]`
	}
	return fmt.Sprintf(`{"apiVersion":"discovery.k8s.io/v1","kind":"EndpointSlice","metadata":{"name":"%s","namespace":"pay","labels":{"kubernetes.io/service-name":"%s"}},
"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":%v}}],"ports":%s}`, name, svc, ready, ports)
}

func scaledToZeroObjs() string {
	return svcObj("web", `"app":"web"`, "") + "," + deployObj("web", 0, `"app":"web"`) + "," +
		`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"other-1","namespace":"pay","labels":{"app":"web-other"}},"spec":{"containers":[{"name":"c","image":"x"}]},"status":{"phase":"Running"}}`
}

func TestServiceOfAWorkloadScaledToZero(t *testing.T) {
	out := runCall(t, scaledToZeroObjs(), "describe", map[string]any{"kind": "service", "name": "web", "namespace": "pay"})
	mustContain(t, out, "selector app=web matches 0 pods in namespace pay", "deployment/web is scaled to 0 replicas")
	mustNotContain(t, out, "Pods with other values")

	list := runCall(t, scaledToZeroObjs(), "list", map[string]any{"kind": "services", "namespace": "pay"})
	row := lineWith(t, list, "web")
	mustContain(t, row, "deployment/web is scaled to 0 replicas")
	mustNotContain(t, list, "unhealthy")
	mustContain(t, list, "1 warning")
}

// A workload that is not scaled to 0, or whose labels do not match the
// selector, is not named; the old cause stays.
func TestSilentServiceNotScaledToZero(t *testing.T) {
	for name, dep := range map[string]string{
		"running":       deployObj("web", 3, `"app":"web"`),
		"other labels":  deployObj("web", 0, `"app":"web2"`),
		"partial match": deployObj("web", 0, `"tier":"front"`),
	} {
		objs := svcObj("web", `"app":"web"`, "") + "," + dep + "," +
			`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"other-1","namespace":"pay","labels":{"app":"web-v2"}},"spec":{"containers":[{"name":"c","image":"x"}]},"status":{"phase":"Running"}}`
		out := runCall(t, objs, "describe", map[string]any{"kind": "service", "name": "web", "namespace": "pay"})
		mustNotContain(t, out, "scaled to 0")
		mustContain(t, out, "matches 0 pods in namespace pay", "Pods with other values for the same label: app=web-v2")
		list := runCall(t, objs, "list", map[string]any{"kind": "services", "namespace": "pay"})
		mustContain(t, lineWith(t, list, "web"), "matches 0 pods")
		mustContain(t, list, "1 unhealthy")
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}
}

// A page of EndpointSlices that was cut says nothing about a Service with no
// slice in view.
func fillerSlices(n int) []unstructured.Unstructured {
	var out []unstructured.Unstructured
	for i := 0; i < n; i++ {
		out = append(out, objsFrom(&testing.T{}, "["+slice(fmt.Sprintf("f-%d", i), fmt.Sprintf("filler-%d", i), true, true)+"]")...)
	}
	return out
}

func TestSilentCutEndpointSliceListSaysNothingAboutAService(t *testing.T) {
	objs := objsFrom(t, "["+svcObj("web", `"app":"web"`, "")+"]")
	objs = append(objs, fillerSlices(fetchCap+1)...)
	r, _ := newRunner(t, &fakeReader{objs: objs, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := call(t, r, "list", map[string]any{"kind": "services", "namespace": "pay"}).Text
	mustNotContain(t, out, "unhealthy", "warning", "no ready endpoints")

	// Without the cut, the same Service has none.
	r, _ = newRunner(t, &fakeReader{objs: objsFrom(t, "["+svcObj("web", `"app":"web"`, "")+"]"), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out = call(t, r, "list", map[string]any{"kind": "services", "namespace": "pay"}).Text
	mustContain(t, out, "1 unhealthy")
}

func TestSilentCutEndpointSliceListSaysNothingAboutAnIngressBackend(t *testing.T) {
	ing := `{"apiVersion":"networking.k8s.io/v1","kind":"Ingress","metadata":{"name":"web","namespace":"pay","uid":"u-ing","creationTimestamp":"2026-11-13T09:00:00Z"},
"spec":{"rules":[{"host":"a.example","http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":"web","port":{"number":80}}}}]}}]}}`
	objs := objsFrom(t, "["+svcObj("web", `"app":"web"`, "")+","+ing+"]")
	cut := append(append([]unstructured.Unstructured(nil), objs...), fillerSlices(fetchCap+1)...)
	r, _ := newRunner(t, &fakeReader{objs: cut, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describeText(t, r, map[string]any{"kind": "ingress", "name": "web", "namespace": "pay"})
	mustNotContain(t, out, "no ready endpoints")

	r, _ = newRunner(t, &fakeReader{objs: append(append([]unstructured.Unstructured(nil), objs...), fillerSlices(1)...), metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out = describeText(t, r, map[string]any{"kind": "ingress", "name": "web", "namespace": "pay"})
	mustContain(t, out, "the backend service has no ready endpoints")
}

// A headless Service with no ports, and ready endpoints with no ports, is not
// "endpoints have no ports".
func TestSilentHeadlessServiceWithoutPortsInTheList(t *testing.T) {
	headless := `{"apiVersion":"v1","kind":"Service","metadata":{"name":"db","namespace":"pay","creationTimestamp":"2026-11-13T09:00:00Z"},"spec":{"clusterIP":"None","selector":{"app":"db"}}}`
	out := runCall(t, headless+","+slice("db-1", "db", true, false), "list", map[string]any{"kind": "services", "namespace": "pay", "all": true})
	mustNotContain(t, out, "unhealthy", "warning", "no ports")

	// A Service with ports whose slice has none is still unhealthy.
	out = runCall(t, svcObj("web", `"app":"web"`, "")+","+slice("web-1", "web", true, false), "list", map[string]any{"kind": "services", "namespace": "pay"})
	mustContain(t, out, "1 unhealthy")
}
