package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// The values below are secrets. None of them may reach any output.
var evidenceSecrets = []string{
	"KrayCanaryPassword1", "KrayCanaryToken2", "KrayCanaryUrlPw3",
	"KRAYCANARYhighEntropy4aZ9xQ2mP7vL5nB8cD3fG6hJ1kS0tW", "KrayCanaryJsonKey5", "KrayCanaryAnnotation6",
	"KrayCanaryProbePw7",
}

const evidenceEnv = `[
 {"name":"DB_PASSWORD","value":"KrayCanaryPassword1"},
 {"name":"GITHUB_TOKEN","value":"KrayCanaryToken2"},
 {"name":"DATABASE_ADDR","value":"postgres://app:KrayCanaryUrlPw3@db.shop.svc:5432/app"},
 {"name":"BUILD_REF","value":"KRAYCANARYhighEntropy4aZ9xQ2mP7vL5nB8cD3fG6hJ1kS0tW"},
 {"name":"SETTINGS","value":"{\"region\":\"eu\",\"api_key\":\"KrayCanaryJsonKey5\"}"},
 {"name":"MEMORY_ALLOCATION_MB","value":"100"},
 {"name":"LONG_NOTE","value":"%s"},
 {"name":"KAFKA_ADDR","value":"kafka.shop.svc:9092"}
]`

const evidenceRefs = `[
 {"name":"NS","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}},
 {"name":"MEM_LIMIT","valueFrom":{"resourceFieldRef":{"resource":"limits.memory"}}},
 {"name":"CFG_MODE","valueFrom":{"configMapKeyRef":{"name":"app-config","key":"mode"}}},
 {"name":"API_TOKEN","valueFrom":{"secretKeyRef":{"name":"api-secrets","key":"token"}}}
]`

const evidenceContainer = `{"name":"app","image":"registry.example/app:1",
 "env":%s,
 "envFrom":[{"configMapRef":{"name":"shared-config"}},{"secretRef":{"name":"shared-secrets"}}],
 "resources":{"requests":{"cpu":"10m","memory":"20Mi"},"limits":{"memory":"50Mi"}},
 "livenessProbe":{"exec":{"command":["/bin/grpc_health_probe","-addr=:7070","--password=KrayCanaryProbePw7"]},"timeoutSeconds":1,"periodSeconds":10,"failureThreshold":3},
 "readinessProbe":{"httpGet":{"path":"/ready","port":8080},"timeoutSeconds":1,"periodSeconds":5},
 "startupProbe":{"tcpSocket":{"port":8080},"periodSeconds":2,"failureThreshold":30}}`

func evidenceObjects(t *testing.T, healthy bool) []unstructured.Unstructured {
	t.Helper()
	env := strings.Replace(evidenceEnv, "%s", strings.Repeat("long-text-", 30), 1)
	c := strings.Replace(evidenceContainer, "%s", env, 1) + `,{"name":"side","image":"registry.example/side:1","env":` + evidenceRefs + `}`
	ready, restarts, avail := "false", 5, 0
	if healthy {
		ready, restarts, avail = "true", 0, 1
	}
	status := `"phase":"Running","containerStatuses":[{"name":"app","ready":` + ready + `,"restartCount":` + string(rune('0'+restarts)) + `,"image":"x","imageID":"x",
	  "state":{"running":{"startedAt":"2026-11-13T11:00:00Z"}}` + oomState(!healthy) + `}],
	  "conditions":[{"type":"Ready","status":"` + map[bool]string{true: "True", false: "False"}[healthy] + `"}]`
	raw := `[
	 {"apiVersion":"v1","kind":"Namespace","metadata":{"name":"shop"}},
	 {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"shop","labels":{"app":"web"},
	   "annotations":{"note":"password=KrayCanaryAnnotation6"}},
	  "spec":{"replicas":1,"selector":{"matchLabels":{"app":"web"}},"template":{"metadata":{"labels":{"app":"web"}},"spec":{"containers":[` + c + `]}}},
	  "status":{"replicas":1,"readyReplicas":` + string(rune('0'+avail)) + `,"availableReplicas":` + string(rune('0'+avail)) + `,"updatedReplicas":1}},
	 {"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","labels":{"app":"web"},"creationTimestamp":"2026-11-13T10:00:00Z"},
	  "spec":{"nodeName":"node-a","containers":[` + c + `]},
	  "status":{` + status + `}}
	]`
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	out := make([]unstructured.Unstructured, len(items))
	for i, m := range items {
		out[i] = unstructured.Unstructured{Object: m}
	}
	return out
}

func oomState(bad bool) string {
	if !bad {
		return ""
	}
	return `,"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"startedAt":"2026-11-13T10:30:00Z","finishedAt":"2026-11-13T10:31:00Z"}}`
}

func evidenceRunner(t *testing.T, healthy bool) *Runner {
	r, _ := newRunner(t, &fakeReader{
		objs: evidenceObjects(t, healthy),
		logs: map[string]string{}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{},
	})
	return r
}

func TestDescribeUnhealthyPrintsEvidence(t *testing.T) {
	for _, kind := range []string{"pod", "deployment"} {
		name := map[string]string{"pod": "web-1", "deployment": "web"}[kind]
		t.Run(kind, func(t *testing.T) {
			out := describe(t, evidenceRunner(t, false), kind, name, "shop")
			mustContain(t, out,
				"- app:",
				"env: DB_PASSWORD=[REDACTED:key-name], GITHUB_TOKEN=[REDACTED:key-name], DATABASE_ADDR=postgres://app:[REDACTED:url-password]@db.shop.svc:5432/app",
				"MEMORY_ALLOCATION_MB=100", "KAFKA_ADDR=kafka.shop.svc:9092",
				"- side:", "env: NS=(field metadata.namespace)", "MEM_LIMIT=(resource limits.memory)", "CFG_MODE=(configmap app-config/mode)",
				"API_TOKEN (secret api-secrets/token)",
				"env from: configmap shared-config, secret shared-secrets",
				"resources: requests cpu 10m, memory 20Mi; limits memory 50Mi", "resources: none (no requests, no limits)",
				"liveness: exec /bin/grpc_health_probe -addr=:7070",
				"timeout 1s, period 10s, failures 3",
				"readiness: HTTP GET /ready on 8080; timeout 1s, period 5s, failures 3",
				"startup: TCP connect to 8080; timeout 1s, period 2s, failures 30",
				"bytes)", // the long value is cut and says so
			)
			mustNotContain(t, out, evidenceSecrets...)
			// The names-only form is replaced, not repeated.
			if strings.Count(out, "MEMORY_ALLOCATION_MB") != 1 {
				t.Errorf("env names printed twice:\n%s", out)
			}
			// The cause stays first and the evidence follows the containers line.
			i := strings.Index(out, "likely cause:")
			j := strings.Index(out, "containers: app")
			k := strings.Index(out, "  env: ")
			if i < 0 || j < i || k < j {
				t.Errorf("order: cause %d, containers %d, env %d:\n%s", i, j, k, out)
			}
		})
	}
}

func TestDescribeEnvLineIsBounded(t *testing.T) {
	objs := evidenceObjects(t, false)
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, `{"name":"VAR_`+strings.Repeat("N", 5)+string(rune('A'+i%26))+string(rune('a'+i/26))+`","value":"value-number-`+strings.Repeat("v", 20)+`"}`)
	}
	var env []any
	if err := json.Unmarshal([]byte("["+strings.Join(many, ",")+"]"), &env); err != nil {
		t.Fatal(err)
	}
	for i := range objs {
		if objs[i].GetKind() == "Pod" {
			cs, _, _ := unstructured.NestedSlice(objs[i].Object, "spec", "containers")
			cs[0].(map[string]any)["env"] = env
			_ = unstructured.SetNestedSlice(objs[i].Object, cs, "spec", "containers")
		}
	}
	r, _ := newRunner(t, &fakeReader{objs: objs, logs: map[string]string{}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := describe(t, r, "pod", "web-1", "shop")
	line := lineWith(t, out, "  env: VAR_")
	if len(line) > envLineMax+80 {
		t.Errorf("env line is %d bytes", len(line))
	}
	mustContain(t, line, " more)")
}

func TestDescribeHealthyKeepsShortForm(t *testing.T) {
	for _, kind := range []string{"pod", "deployment"} {
		name := map[string]string{"pod": "web-1", "deployment": "web"}[kind]
		t.Run(kind, func(t *testing.T) {
			out := describe(t, evidenceRunner(t, true), kind, name, "shop")
			mustContain(t, out, "state: healthy", "- app: env: DB_PASSWORD, GITHUB_TOKEN, DATABASE_ADDR")
			mustNotContain(t, out, "resources: requests", "liveness:", "readiness:", "startup:", "MEMORY_ALLOCATION_MB=", "kafka.shop.svc")
			mustNotContain(t, out, evidenceSecrets...)
		})
	}
}

// get -o jsonpath runs on the redacted object: a planted secret in an env
// value or in an annotation comes back as the mark.
func TestGetJSONPathIsRedacted(t *testing.T) {
	r := evidenceRunner(t, false)
	get := func(path string) string {
		res := call(t, r, "get", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop", "jsonpath": path})
		if res.IsError {
			t.Fatalf("jsonpath %s: %s", path, res.Text)
		}
		mustNotContain(t, res.Text, evidenceSecrets...)
		return res.Text
	}
	env := get("{.spec.template.spec.containers[0].env}")
	mustContain(t, env, `"DB_PASSWORD"`, "[REDACTED:key-name]", "MEMORY_ALLOCATION_MB", `"100"`, "url-password")
	mustContain(t, get("{.spec.template.spec.containers[0].env[5].value}"), "100")
	mustContain(t, get("{.metadata.annotations.note}"), "[REDACTED:key-name]")
	mustContain(t, get("{.spec.template.spec.containers[0].resources.limits.memory}"), "50Mi")
	mustContain(t, get(".spec.replicas"), "1") // a bare path is a template, as in kubectl
	mustContain(t, get("{.spec.nothing}"), "found no value")
	whole := get("{.metadata.annotations}")
	mustContain(t, whole, "[REDACTED:key-name]")
	// A path that does not parse is one line, and the template is echoed redacted.
	bad := call(t, r, "get", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop", "jsonpath": "{.spec.password=KrayCanaryPassword1[}"})
	if !bad.IsError || strings.Count(strings.TrimSpace(bad.Text), "\n") != 0 {
		t.Errorf("bad template: %q", bad.Text)
	}
	mustNotContain(t, bad.Text, evidenceSecrets...)
	long := call(t, r, "get", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop", "jsonpath": strings.Repeat("a", 2000)})
	if !long.IsError {
		t.Error("a very long template was accepted")
	}
}
