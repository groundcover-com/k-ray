// Package fixture builds the canary fixture: objects that hide a unique
// canary in each place a secret can hide. The kind integration test applies
// them to a real cluster; the end-to-end test serves them from a fake API
// server. Canaries are new for every run.
package fixture

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/test/harness"
)

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// base64Alphabet is the alphabet of a key body line.
const base64Alphabet = alphabet + "+/"

// random returns n random characters of the alphabet, with at least one
// upper case letter, one lower case letter and one digit.
func random(alpha string, n int) string {
	for {
		b := make([]byte, n)
		for i := range b {
			k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alpha))))
			if err != nil {
				panic(err)
			}
			b[i] = alpha[k.Int64()]
		}
		s := string(b)
		if strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") && strings.ContainsAny(s, "0123456789") {
			return s
		}
	}
}

// Canary returns a random 40-character token with upper and lower case
// letters and digits.
func Canary() string { return random(alphabet, 40) }

// word returns one random part of a phrase: 16 characters.
func word() string { return random(alphabet, 16) }

// keyBody returns the body lines of a private key block: full random base64
// lines of 64 characters and a short last line of 22 characters and "==".
func keyBody(full int) []string {
	var out []string
	for i := 0; i < full; i++ {
		out = append(out, random(base64Alphabet, 64))
	}
	return append(out, random(base64Alphabet, 22)+"==")
}

// filler returns ordinary words of exactly n bytes that end with one space.
func filler(n int) string {
	const text = "the controller is reconciling the object again and waits for the next sync of the cache "
	s := strings.TrimRight(strings.Repeat(text, n/len(text)+1)[:n-1], " ")
	return s + strings.Repeat("a", n-1-len(s)) + " "
}

// quotedPhrase returns a message that holds {"password":"W1 W2 ...","k":"v"}
// after enough filler that a cut of the message to cut bytes falls inside the
// quoted value, 8 bytes into the second word.
func quotedPhrase(words []string, cut int) string {
	const key = `{"password":"`
	pre := cut - 8 - 1 - len(words[0]) - len(key)
	return filler(pre) + key + strings.Join(words, " ") + `","k":"v"}`
}

// Places where a canary is planted.
const (
	SecretData        = "secret-data"
	EnvLiteral        = "env-literal"
	EnvNeutral        = "env-neutral-key"
	ConfigMapKey      = "configmap-key"
	ConfigMapEmbedded = "configmap-embedded"
	LogPassword       = "log-password"
	LogBearer         = "log-bearer"
	ArgsFlag          = "args-flag"
	ArgsSplit         = "args-split"
	LastApplied       = "last-applied"
	Annotation        = "annotation"
	EventMessage      = "event-message"
	CustomResource    = "custom-resource"
	ConditionMessage  = "condition-message"   // in the status.conditions of a custom resource, which describe and list show
	LogSpacedKey      = "log-spaced-key"      // a JSON log line whose key has a space: "api key"
	LogBareSpacedKey  = "log-bare-spaced-key" // a plain log line whose key has a space and no quotes: secret key=
)

// Places with several parts (Set.Parts). A leak of any one part is a leak.
const (
	LogKeyBlock           = "log-key-block"           // a private key block over several lines in the log
	EventQuotedPhrase     = "event-quoted-phrase"     // a quoted multi-word value in an Event message, cut at 300 bytes
	ArgsPhrase            = "args-phrase"             // one container arg of several words after --password
	ConditionQuotedPhrase = "condition-quoted-phrase" // a quoted multi-word value in a condition message, cut at 200 bytes
)

// The cuts that the quoted phrases straddle: the message column of the
// events table, and a condition message (internal/tools, internal/shape).
const (
	eventMessageCut     = 300
	conditionMessageCut = 200
)

const (
	pemBegin = "-----BEGIN RSA PRIVATE KEY-----"
	pemEnd   = "-----END RSA PRIVATE KEY-----"
)

// Image is the container image of the fixture workload.
const Image = "registry.k8s.io/e2e-test-images/busybox:1.36.1-1"

// ReadyLine is printed by the workload when it has written its log canaries.
const ReadyLine = "fixture-ready"

// Set is one fixture.
type Set struct {
	Namespace string
	Canaries  map[string]string   // places with one token
	Parts     map[string][]string // places with several parts; every part is checked alone
}

// New makes a fixture with fresh canaries.
func New(namespace string) *Set {
	s := &Set{Namespace: namespace, Canaries: map[string]string{}, Parts: map[string][]string{}}
	for _, p := range []string{SecretData, EnvLiteral, EnvNeutral, ConfigMapKey, ConfigMapEmbedded, LogPassword, LogBearer, ArgsFlag, ArgsSplit, LastApplied, Annotation, EventMessage, CustomResource, ConditionMessage, LogSpacedKey, LogBareSpacedKey} {
		s.Canaries[p] = Canary()
	}
	s.Parts[LogKeyBlock] = keyBody(3)
	s.Parts[EventQuotedPhrase] = []string{word(), word(), word(), word()}
	s.Parts[ArgsPhrase] = []string{word(), word(), word()}
	s.Parts[ConditionQuotedPhrase] = []string{word(), word(), word(), word()}
	return s
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Script is the workload's shell script. The log canaries appear only in
// base64 in the pod spec and are decoded at run time, so the log is the
// only place they appear in clear text.
//
// The private key block is one base64 blob of the whole block, so that no
// line of the block is in the spec, not even in base64. It is decoded to
// several lines with one command, which needs no care with echo.
func (s *Set) Script() string {
	block := strings.Join(s.keyBlock(), "\n") + "\n"
	return fmt.Sprintf(`echo "starting worker"
echo "connecting with password=$(echo %s | base64 -d)"
echo "Authorization: Bearer $(echo %s | base64 -d)"
echo "ERROR upstream returned 503 for /api/orders"
echo "ERROR upstream returned 503 for /api/orders"
echo "{\"level\":\"info\",\"msg\":\"login\",\"api key\":\"$(echo %s | base64 -d)\"}"
echo "uploading with secret key=$(echo %s | base64 -d) to bucket logs"
echo "loading signing key"
echo %s | base64 -d
echo "signing key loaded"
echo "%s"
while true; do echo "heartbeat ok"; sleep 10; done`,
		b64(s.Canaries[LogPassword]), b64(s.Canaries[LogBearer]), b64(s.Canaries[LogSpacedKey]), b64(s.Canaries[LogBareSpacedKey]), b64(block), ReadyLine)
}

// logLines is what the workload prints before its heartbeat, in order.
func (s *Set) logLines() []string {
	lines := []string{
		"starting worker",
		"connecting with password=" + s.Canaries[LogPassword],
		"Authorization: Bearer " + s.Canaries[LogBearer],
		"ERROR upstream returned 503 for /api/orders",
		"ERROR upstream returned 503 for /api/orders",
		`{"level":"info","msg":"login","api key":"` + s.Canaries[LogSpacedKey] + `"}`,
		"uploading with secret key=" + s.Canaries[LogBareSpacedKey] + " to bucket logs",
		"loading signing key",
	}
	lines = append(lines, s.keyBlock()...)
	return append(lines, "signing key loaded", ReadyLine)
}

// keyBlock is the private key block of the log, line by line.
func (s *Set) keyBlock() []string {
	return append(append([]string{pemBegin}, s.Parts[LogKeyBlock]...), pemEnd)
}

// LogText is what the workload prints, for the fake API server.
func (s *Set) LogText() string {
	var b strings.Builder
	for i, l := range s.logLines() {
		fmt.Fprintf(&b, "2026-11-13T10:00:%02d.%03d000000Z %s\n", i/10, (i%10)*100, l)
	}
	b.WriteString("2026-11-13T10:00:30.000000000Z heartbeat ok\n")
	return b.String()
}

func (s *Set) container() map[string]any {
	return map[string]any{
		"name":    "api",
		"image":   Image,
		"command": []any{"sh", "-c", s.Script(), "--"},
		"args":    []any{"--db-password=" + s.Canaries[ArgsFlag], "--token", s.Canaries[ArgsSplit], "--password", strings.Join(s.Parts[ArgsPhrase], " "), "--port=8080"},
		"env": []any{
			map[string]any{"name": "DB_PASSWORD", "value": s.Canaries[EnvLiteral]},
			map[string]any{"name": "FEATURE_SEED", "value": s.Canaries[EnvNeutral]},
			map[string]any{"name": "DB_PASS_FROM_SECRET", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "db-credentials", "key": "password"}}},
			map[string]any{"name": "LOG_LEVEL", "value": "info"},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "10m", "memory": "16Mi"},
			"limits":   map[string]any{"cpu": "100m", "memory": "64Mi"},
		},
	}
}

// Objects returns the fixture objects in apply order (the CRD first).
func (s *Set) Objects() []*unstructured.Unstructured {
	ns := s.Namespace
	lastApplied, _ := json.Marshal(map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "api", "namespace": ns},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
			"name": "api", "env": []any{map[string]any{"name": "DB_PASSWORD", "value": s.Canaries[LastApplied]}},
		}}}}},
	})
	objs := []map[string]any{
		{
			"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": "widgets.example.com"},
			"spec": map[string]any{
				"group": "example.com", "scope": "Namespaced",
				"names": map[string]any{"plural": "widgets", "singular": "widget", "kind": "Widget"},
				"versions": []any{map[string]any{
					"name": "v1", "served": true, "storage": true,
					"schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object", "x-kubernetes-preserve-unknown-fields": true}},
				}},
			},
		},
		{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns}},
		{
			"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "db-credentials", "namespace": ns},
			"type": "Opaque", "data": map[string]any{"password": b64(s.Canaries[SecretData])},
		},
		{
			"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "app-config", "namespace": ns},
			"data": map[string]any{
				"api_key":     s.Canaries[ConfigMapKey],
				"config.yaml": "server:\n  port: 8080\ndatabase:\n  host: db\n  password: " + s.Canaries[ConfigMapEmbedded] + "\nlog: info\n",
			},
		},
		{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{
				"name": "api", "namespace": ns, "labels": map[string]any{"app": "api"},
				"annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": string(lastApplied)},
			},
			"spec": map[string]any{
				"replicas": 1, "selector": map[string]any{"matchLabels": map[string]any{"app": "api"}},
				"template": map[string]any{
					"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
					"spec":     map[string]any{"terminationGracePeriodSeconds": 1, "containers": []any{s.container()}},
				},
			},
		},
		{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": "api", "namespace": ns, "annotations": map[string]any{"example.com/webhook-token": s.Canaries[Annotation]}},
			"spec":     map[string]any{"selector": map[string]any{"app": "api"}, "ports": []any{map[string]any{"port": 80, "targetPort": 8080}}},
		},
		{
			"apiVersion": "v1", "kind": "Event",
			"metadata":       map[string]any{"name": "api.fixture", "namespace": ns},
			"involvedObject": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api", "namespace": ns},
			"reason":         "ConfigReloaded", "type": "Normal", "message": "reloaded config with token=" + s.Canaries[EventMessage],
			"source": map[string]any{"component": "fixture"},
		},
		{
			"apiVersion": "v1", "kind": "Event",
			"metadata":       map[string]any{"name": "api.fixture-phrase", "namespace": ns},
			"involvedObject": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api", "namespace": ns},
			"reason":         "ConfigRejected", "type": "Warning", "message": quotedPhrase(s.Parts[EventQuotedPhrase], eventMessageCut),
			"source": map[string]any{"component": "fixture"},
		},
		{
			"apiVersion": "example.com/v1", "kind": "Widget",
			"metadata": map[string]any{"name": "w1", "namespace": ns},
			"spec":     map[string]any{"size": 3, "credentials": map[string]any{"user": "app", "password": s.Canaries[CustomResource]}},
			"status": map[string]any{"conditions": []any{
				map[string]any{
					"type": "Ready", "status": "False", "reason": "UpstreamRejected",
					"message": "upstream rejected the request with token=" + s.Canaries[ConditionMessage], "lastTransitionTime": "2026-11-13T10:00:00Z",
				},
				map[string]any{
					"type": "Synced", "status": "False", "reason": "CredentialsRejected",
					"message": quotedPhrase(s.Parts[ConditionQuotedPhrase], conditionMessageCut), "lastTransitionTime": "2026-11-13T10:00:00Z",
				},
			}},
		},
	}
	out := make([]*unstructured.Unstructured, len(objs))
	for i, o := range objs {
		out[i] = &unstructured.Unstructured{Object: o}
	}
	return out
}

// Pod returns a running pod of the Deployment, for the fake API server.
func (s *Set) Pod(name, node string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": s.Namespace, "uid": "pod-uid-1", "labels": map[string]any{"app": "api"},
			"creationTimestamp": "2026-11-13T09:59:00Z",
		},
		"spec":   map[string]any{"nodeName": node, "containers": []any{s.container()}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "api", "ready": true, "restartCount": int64(0), "state": map[string]any{"running": map[string]any{}}}}},
	}}
}

// HarnessFixture lists the objects for the harness calls.
func (s *Set) HarnessFixture(pod, node string) harness.Fixture {
	ns := s.Namespace
	return harness.Fixture{
		Namespace: ns,
		Canaries:  s.Canaries,
		Parts:     s.Parts,
		Objects: []harness.Object{
			{Kind: "pods", KindName: "Pod", Name: pod, Namespace: ns, Container: "api"},
			{Kind: "deployments", KindName: "Deployment", Name: "api", Namespace: ns},
			{Kind: "configmaps", KindName: "ConfigMap", Name: "app-config", Namespace: ns},
			{Kind: "services", KindName: "Service", Name: "api", Namespace: ns},
			{Kind: "secrets", KindName: "Secret", Name: "db-credentials", Namespace: ns},
			{Kind: "events", KindName: "Event", Name: "api.fixture", Namespace: ns},
			{Kind: "events", KindName: "Event", Name: "api.fixture-phrase", Namespace: ns},
			{Kind: "widgets.example.com", KindName: "Widget", Name: "w1", Namespace: ns},
			{Kind: "nodes", KindName: "Node", Name: node},
			{Kind: "namespaces", KindName: "Namespace", Name: ns},
		},
	}
}

// Expect lists the usefulness checks for a server with the six tools.
func (s *Set) Expect(pod, node string) []harness.Expect {
	ns := s.Namespace
	return []harness.Expect{
		{Tool: "list", Args: map[string]any{"kind": "pods", "namespace": ns}, Contains: pod},
		{Tool: "get", Args: map[string]any{"kind": "deployments", "name": "api", "namespace": ns}, Contains: "name: api"},
		{Tool: "describe", Args: map[string]any{"kind": "deployments", "name": "api", "namespace": ns}, Contains: "pods (selector app=api)"},
		{Tool: "get", Args: map[string]any{"kind": "configmaps", "name": "app-config", "namespace": ns}, Contains: "log: info"},
		{Tool: "describe", Args: map[string]any{"kind": "services", "name": "api", "namespace": ns}, Contains: "endpoints:"},
		{Tool: "logs", Args: map[string]any{"pod": pod, "namespace": ns}, Contains: "ERROR upstream returned 503"},
		{Tool: "logs", Args: map[string]any{"pod": pod, "namespace": ns, "full": true}, Contains: ReadyLine},
		{Tool: "events", Args: map[string]any{"namespace": ns}, Contains: "ConfigReloaded"},
		{Tool: "get", Args: map[string]any{"kind": "widgets.example.com", "name": "w1", "namespace": ns}, Contains: "size: 3"},
		{Tool: "list", Args: map[string]any{"kind": "nodes"}, Contains: node},
	}
}
