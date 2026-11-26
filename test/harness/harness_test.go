package harness

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPlanCoversObjectsAndHostileInputs(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "get", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"kind": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "namespace": map[string]any{"type": "string"},
		}, "required": []any{"kind", "name"}}},
		{Name: "logs", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"pod": map[string]any{"type": "string"}, "tail": map[string]any{"type": "integer"},
		}}},
	}
	f := Fixture{Namespace: "fx", Objects: []Object{
		{Kind: "pods", KindName: "Pod", Name: "api-1", Namespace: "fx"},
		{Kind: "configmaps", KindName: "ConfigMap", Name: "cfg", Namespace: "fx"},
	}}
	plans := Plan(tools, f)
	var sawSecretByName, sawFlagName, sawHugeTail, sawContextArg, sawLogsForCM bool
	for _, p := range plans {
		if p.tool == "get" && p.args["kind"] == "secrets" && p.args["name"] == "db-credentials" {
			sawSecretByName = true
		}
		if p.args["name"] == "--server=https://evil.example" || p.args["pod"] == "--server=https://evil.example" {
			sawFlagName = true
		}
		if p.tool == "logs" && p.args["tail"] == 1e12 {
			sawHugeTail = true
		}
		if p.args["context"] != nil {
			sawContextArg = true
		}
		if p.tool == "logs" && p.args["pod"] == "cfg" {
			sawLogsForCM = true
		}
	}
	if !sawSecretByName || !sawFlagName || !sawHugeTail || !sawContextArg {
		t.Fatalf("plan misses hostile inputs: secret=%v flag=%v tail=%v context=%v", sawSecretByName, sawFlagName, sawHugeTail, sawContextArg)
	}
	if sawLogsForCM {
		t.Fatal("logs called with a non-pod object")
	}
}

func TestFindCanariesInEveryForm(t *testing.T) {
	c := map[string]string{"secret": "kcAbC123dEf456gHi789jKl012mNo345pQr678sT"}
	for _, text := range []string{c["secret"], base64.StdEncoding.EncodeToString([]byte(c["secret"])), "x" + base64.RawURLEncoding.EncodeToString([]byte(c["secret"])) + "y"} {
		if len(findCanaries(c, text, "t")) != 1 {
			t.Errorf("missed canary in %q", text)
		}
	}
	if len(findCanaries(c, "[REDACTED:high-entropy]", "t")) != 0 {
		t.Error("false positive")
	}
}

func TestCheckAudit(t *testing.T) {
	log := strings.Join([]string{
		`{"verb":"list","requestURI":"/api/v1/namespaces/fx/pods?limit=500","user":{"username":"me"},"objectRef":{"resource":"pods"},"requestReceivedTimestamp":"2026-11-13T10:00:00Z"}`,
		`{"verb":"get","requestURI":"/api/v1/namespaces/fx/pods/p/log?tailLines=10","user":{"username":"me"},"objectRef":{"resource":"pods","subresource":"log"},"requestReceivedTimestamp":"2026-11-13T10:00:00Z"}`,
		`{"verb":"get","requestURI":"/api/v1/namespaces/fx/secrets/db","user":{"username":"me"},"objectRef":{"resource":"secrets"},"requestReceivedTimestamp":"2026-11-13T10:00:00Z"}`,
		`{"verb":"create","requestURI":"/api/v1/namespaces/fx/configmaps","user":{"username":"me"},"objectRef":{"resource":"configmaps"},"requestReceivedTimestamp":"2026-11-13T10:00:00Z"}`,
		`{"verb":"get","requestURI":"/api/v1/nodes/n/proxy/exec/fx/p/c","user":{"username":"me"},"objectRef":{"resource":"nodes","subresource":"proxy"},"requestReceivedTimestamp":"2026-11-13T10:00:00Z"}`,
		`{"verb":"create","requestURI":"/api/v1/namespaces/fx/configmaps","user":{"username":"someone-else"},"requestReceivedTimestamp":"2026-11-13T10:00:00Z"}`,
		`{"verb":"delete","requestURI":"/api/v1/namespaces/fx/pods/p","user":{"username":"me"},"requestReceivedTimestamp":"2026-11-13T09:00:00Z"}`,
	}, "\n")
	n, bad := CheckAudit([]byte(log), "me", time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC))
	if n != 5 || len(bad) != 3 {
		t.Fatalf("events %d, bad %v", n, bad)
	}
}

func TestCheckNetwork(t *testing.T) {
	trace := `123 connect(7, {sa_family=AF_INET, sin_port=htons(6443), sin_addr=inet_addr("127.0.0.1")}, 16) = 0
124 connect(8, {sa_family=AF_INET, sin_port=htons(443), sin_addr=inet_addr("1.2.3.4")}, 16) = 0
125 listen(9, 4096) = 0
126 connect(3, {sa_family=AF_UNIX, sun_path="/var/run/x"}, 110) = -1`
	bad := CheckNetwork(trace, "127.0.0.1:6443")
	if len(bad) != 2 {
		t.Fatalf("bad %v", bad)
	}
}
