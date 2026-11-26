package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func shapeFixture(t *testing.T, name string) unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile("../shape/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return unstructured.Unstructured{Object: m}
}

func TestGetPodDefaultIsShortAndSaysWhatItLeftOut(t *testing.T) {
	pod := shapeFixture(t, "pod.json")
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{pod}})
	out := call(t, r, "get", map[string]any{"kind": "pod", "name": "api-7d9f8b6c5d-x2k4m"}).Text
	mustContain(t, out,
		"conditions ok: PodReadyToStartContainers, Initialized, Ready, ContainersReady, PodScheduled",
		"imageDigest: sha256:3b7e1c0f9a5d",
		"ReplicaSet/api-7d9f8b6c5d (controller)",
		"left out: ", " default or repeated fields; full=true shows them.",
	)
	mustNotContain(t, out, "terminationMessagePath", "dnsPolicy", "containerID", "kube-api-access", "resourceVersion", "uid:", "pod-template-hash", "default-scheduler")

	full := call(t, r, "get", map[string]any{"kind": "pod", "name": "api-7d9f8b6c5d-x2k4m", "full": true}).Text
	mustContain(t, full, "terminationMessagePath: /dev/termination-log", "dnsPolicy: ClusterFirst", "containerID: containerd://", "kube-api-access-9xk7z", "uid: 4f0f7b2e", "pod-template-hash: 7d9f8b6c5d", "type: PodScheduled", "protocol: TCP")
	mustNotContain(t, full, "left out:", "conditions ok", "managedFields")
	if len(out)*2 > len(full) {
		t.Errorf("the default (%d bytes) is more than half of full (%d bytes)", len(out), len(full))
	}
}

func TestGetNoteNamesTheArgumentInTheCLI(t *testing.T) {
	pod := shapeFixture(t, "pod.json")
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{pod}})
	r.Surface = "cli"
	out := call(t, r, "get", map[string]any{"kind": "pod", "name": "api-7d9f8b6c5d-x2k4m"}).Text
	mustContain(t, out, "; --full shows them.")
}

func TestGetSaysWhenGenerationIsAhead(t *testing.T) {
	dep := shapeFixture(t, "deployment.json")
	_ = unstructured.SetNestedField(dep.Object, int64(6), "status", "observedGeneration")
	dep.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"] = map[string]any{"containers": []any{map[string]any{"name": "c", "image": "i:1"}}}
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{dep}})
	out := call(t, r, "get", map[string]any{"kind": "deployment", "name": "api"}).Text
	mustContain(t, out, "generation: 7", "observedGeneration: 6", "metadata.generation 7 differs from status.observedGeneration 6")
}

func bigConfigMap(secretBefore, secretAfter string) unstructured.Unstructured {
	var conf strings.Builder
	fmt.Fprintf(&conf, "# start\ntoken_url: https://x/y?t=%s\n", secretBefore)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&conf, "setting_%02d = value that is harmless and long enough\n", i)
	}
	fmt.Fprintf(&conf, "deploy_key: %s\n# end\n", secretAfter)
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "app-config", "namespace": "shop", "annotations": map[string]any{
			"note": "see " + secretBefore + strings.Repeat(" padding text", 40) + " " + secretAfter,
		}},
		"data": map[string]any{
			"app.conf": conf.String(), "LOG_LEVEL": "info",
			"password": "cfgPassword5",
			"json":     `{"db":{"host":"x","password":"nestedPw99xyz"},"pad":"` + strings.Repeat("p", 300) + `","api_key":"nestedKey88abc"}`,
		},
		"binaryData": map[string]any{"logo.png": strings.Repeat("iVBORw0KGgo", 40)},
	}}
}

func TestGetConfigMapOutlineCutAndRedactionBeforeTheCut(t *testing.T) {
	const (
		before = "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
		after  = "ghp_ZyXwVuTsRqPoNmLkJiHgFeDcBa9876543210"
	)
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{bigConfigMap(before, after)}})
	out := call(t, r, "get", map[string]any{"kind": "cm", "name": "app-config"}).Text
	mustContain(t, out,
		"data outline:",
		"- LOG_LEVEL: 4 bytes, 1 line",
		"- app.conf: ", ", cut",
		"- logo.png: base64 or binary, 440 bytes",
		"lines left out; full shows all]",
		"[base64, 440 chars]",
		"LOG_LEVEL: info",
		"left out: ", " long values; full=true shows them.",
		"[REDACTED:",
	)
	mustNotContain(t, out, before, after, before[:12], after[:12], "cfgPassword5", "nestedPw99xyz", "nestedKey88abc", "hunter")
	mustNotContain(t, out, "setting_39")

	full := call(t, r, "get", map[string]any{"kind": "cm", "name": "app-config", "full": true}).Text
	mustContain(t, full, "setting_39", "iVBORw0KGgoiVBORw0KGgo")
	mustNotContain(t, full, "data outline:", "left out:", before, after, "cfgPassword5", "nestedPw99xyz", "nestedKey88abc")
}

// A message of the status is evidence: it is not cut.
func TestStatusMessageIsNotCut(t *testing.T) {
	msg := strings.Repeat("panic: runtime error: index out of range ", 12)
	pod := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "p", "namespace": "shop"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "c", "image": "i:1"}}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{
			"name": "c", "ready": false, "restartCount": int64(2),
			"state": map[string]any{"terminated": map[string]any{"exitCode": int64(2), "message": msg}},
		}}},
	}}
	r, _ := newRunner(t, &fakeReader{objs: []unstructured.Unstructured{pod}})
	out := call(t, r, "get", map[string]any{"kind": "pod", "name": "p"}).Text
	if !strings.Contains(out, strings.TrimSpace(msg[:30])) {
		t.Errorf("termination message missing:\n%s", out)
	}
	mustNotContain(t, out, "full shows all]")
}
