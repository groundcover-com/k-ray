package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

const canary = "kc7Qx2Lm9Rt4Vb8Nw1Zp6Hd3Fs5Jk0Gy2Tc7Ue"

// mustRedact: the secret must not appear in the output.
var mustRedact = []struct{ name, in, secret string }{
	{"kv equals", "password=hunter2x", "hunter2x"},
	{"kv json", `{"password":"hunter2x"}`, "hunter2x"},
	{"kv json spaced", `{"password" : "hunter2x"}`, "hunter2x"},
	{"kv yaml", "DB_PASSWORD: hunter2x", "hunter2x"},
	{"kv env export", "export API_TOKEN=tok-123456", "tok-123456"},
	{"flag equals", "--db-password=hunter2x", "hunter2x"},
	{"url creds", "postgres://app:hunter2x@db:5432/app", "hunter2x"},
	{"url creds amqp", "amqp://guest:s3cr3tpw@rabbit:5672/", "s3cr3tpw"},
	{"bearer", "Authorization: Bearer abcdefghijklmnop123", "abcdefghijklmnop123"},
	{"basic", "authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA=="},
	{"jwt", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
	{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabc\n-----END RSA PRIVATE KEY-----", "MIIEowIBAAKCAQEA"},
	{"pem unterminated", "key: -----BEGIN OPENSSH PRIVATE KEY----- b3BlbnNzaC1rZXktdjEAAAAA", "b3BlbnNzaC1rZXktdjEAAAAA"},
	{"pem escaped newlines", `{"tls":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----"}`, "MIIEvQIBADANBg"},
	{"aws key id", "AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
	{"aws secret by key", "aws_secret_access_key = wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY", "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"},
	{"github", "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789", "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"},
	{"slack", "xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
	{"stripe", "sk_live_51HaBcDeFgHiJkLmNoPq", "sk_live_51HaBcDeFgHiJkLmNoPq"},
	{"google api", "AIzaSyA1bCdEfGhIjKlMnOpQrStUvWxYz012345", "AIzaSyA1bCdEfGhIjKlMnOpQrStUvWxYz012345"},
	{"llm key", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWx", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWx"},
	{"entropy neutral key", "FEATURE_SEED=" + canary, canary},
	{"entropy bare", "connecting with " + canary + " now", canary},
	{"entropy base64", "data: c2VjcmV0LXZhbHVlLXRoYXQtaXMtbG9uZy1lbm91Z2g+/w==", "c2VjcmV0LXZhbHVlLXRoYXQtaXMtbG9uZy1lbm91Z2g"},
	{"api key yaml", "api_key: abc123def", "abc123def"},
	{"client secret", "client_secret=xyz789abc", "xyz789abc"},
	{"query token", "GET https://x.test/cb?token=abc123zz&x=1", "abc123zz"},
	{"connection string password", "conn=Server=db;Password=pw-9918;Database=app", "pw-9918"},
	{"azure account key", "DefaultEndpointsProtocol=https;AccountName=x;AccountKey=abcd+efgh/ijkl==", "abcd+efgh/ijkl=="},
	{"webhook url", "SLACK_WEBHOOK_URL=https://hooks.slack.com/services/T000/B000/XXXX", "hooks.slack.com/services/T000/B000/XXXX"},
	{"json env pair", `{"env":[{"name":"API_TOKEN","value":"tok123"}]}`, "tok123"},
	{"json env pair reversed", `[{"value":"tok123","name":"API_TOKEN"}]`, "tok123"},
	{"yaml env pair", "env:\n- name: DB_PASSWORD\n  value: hunter2x\n", "hunter2x"},
	{"yaml env pair quoted", "- name: \"DB_PASSWORD\"\n  value: \"hunter2x\"\n", "hunter2x"},
	{"embedded yaml password", "db:\n  host: x\n  password: hunter2x\n", "hunter2x"},
	{"properties", "spring.datasource.password=hunter2x", "hunter2x"},
	{"log json token", `{"level":"info","msg":"login","access_token":"at-77812345"}`, "at-77812345"},
}

// mustKeep: these must come through unchanged.
var mustKeep = []string{
	"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	"7b0c5a3e-9d2f-4c1a-8b6e-1f2a3b4c5d6e",
	"containerd://5f2c8e1a9b7d3c6e4f0a1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcd",
	"kube-controller-manager-kind-control-plane",
	"ip-10-0-1-23.eu-west-1.compute.internal",
	"api-7f9c8d4b6-x2k9z",
	"registry.k8s.io/e2e-test-images/busybox:1.36.1-1",
	`level=info msg="started server" port=8080`,
	"token expired, refreshing",
	"author: Jane Doe",
	"traceId=4bf92f3577b34da6a3ce929d0e0e4736",
	"kubernetes.io/service-account.name: default",
	"/var/lib/kubelet/pods/abc123/volumes",
	"ReplicaSetControllerManagerSomethingLong",
	"AWSLoadBalancerControllerIAMPolicy",
	"Back-off restarting failed container api in pod api-7f9c8d4b6-x2k9z_shop(7b0c5a3e-9d2f-4c1a-8b6e-1f2a3b4c5d6e)",
	"0/3 nodes are available: 3 Insufficient memory. preemption: 0/3 nodes are available",
	"key: node.kubernetes.io/not-ready",
	"secretName: tls-cert",
	"Readiness probe failed: HTTP probe failed with statuscode: 503",
}

func TestTextRedacts(t *testing.T) {
	for _, c := range mustRedact {
		r := New(nil)
		out := r.Text(c.in)
		if strings.Contains(out, c.secret) {
			t.Errorf("%s: secret survived: %q -> %q", c.name, c.in, out)
		}
		if r.Counts().Total() == 0 || !strings.Contains(out, markPrefix) {
			t.Errorf("%s: no visible marker or count: %q", c.name, out)
		}
	}
}

func TestTextKeeps(t *testing.T) {
	for _, in := range mustKeep {
		r := New(nil)
		if out := r.Text(in); out != in {
			t.Errorf("changed a safe value:\n  in:  %q\n  out: %q", in, out)
		}
	}
}

func TestMarkerNeverLeaksLength(t *testing.T) {
	r := New(nil)
	a := r.Text("password=a")
	b := r.Text("password=" + strings.Repeat("b", 200))
	if a != b {
		t.Fatalf("markers differ by value length: %q vs %q", a, b)
	}
}

func TestExtraKeyNames(t *testing.T) {
	r := New([]string{"license_code"})
	if out := r.Text("LICENSE_CODE=abc-123"); strings.Contains(out, "abc-123") {
		t.Fatalf("extra key not redacted: %q", out)
	}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestObject(t *testing.T) {
	obj := decode(t, `{
	  "kind": "Deployment",
	  "metadata": {
	    "name": "api",
	    "annotations": {
	      "example.com/webhook-token": "wh-1234567",
	      "kubectl.kubernetes.io/last-applied-configuration": "{\"spec\":{\"containers\":[{\"env\":[{\"name\":\"DB_PASSWORD\",\"value\":\"lastapplied99\"}]}]}}",
	      "deployment.kubernetes.io/revision": "3"
	    }
	  },
	  "spec": {"template": {"spec": {
	    "imagePullSecrets": [{"name": "regcred"}],
	    "volumes": [{"name": "tls", "secret": {"secretName": "tls-cert"}}],
	    "containers": [{
	      "name": "api",
	      "args": ["--db-password=argpw111", "--token", "argtok222", "--port", "8080", "--verbose"],
	      "command": ["sh", "-c", "exec app --api-key=cmdkey333"],
	      "env": [
	        {"name": "DB_PASSWORD", "value": "envpw444"},
	        {"name": "LOG_LEVEL", "value": "info"},
	        {"name": "FROM_SECRET", "valueFrom": {"secretKeyRef": {"name": "db-credentials", "key": "password"}}}
	      ],
	      "livenessProbe": {"httpGet": {"path": "/healthz", "httpHeaders": [{"name": "Authorization", "value": "hdr555"}]}}
	    }]
	  }}}
	}`)
	r := New(nil)
	r.Object(obj)
	b, _ := json.Marshal(obj)
	out := string(b)
	for _, s := range []string{"wh-1234567", "lastapplied99", "argpw111", "argtok222", "cmdkey333", "envpw444", "hdr555"} {
		if strings.Contains(out, s) {
			t.Errorf("secret %q survived: %s", s, out)
		}
	}
	for _, s := range []string{`"8080"`, `"--verbose"`, `"info"`, `"db-credentials"`, `"key":"password"`, `"tls-cert"`, `"regcred"`, `"deployment.kubernetes.io/revision":"3"`, `"/healthz"`} {
		if !strings.Contains(out, s) {
			t.Errorf("safe value %s missing: %s", s, out)
		}
	}
	if r.Counts()[RuleKeyName] < 7 {
		t.Errorf("counts = %v", r.Counts())
	}
}

func TestCountsString(t *testing.T) {
	c := Counts{"jwt": 1, "key-name": 2, "pem": 0}
	if c.String() != "jwt 1, key-name 2" || c.Total() != 3 {
		t.Fatalf("got %q total %d", c.String(), c.Total())
	}
}

// FuzzKV: a value after a sensitive key never survives.
func FuzzKV(f *testing.F) {
	f.Add("x ", "hunter2", " y")
	f.Add("", "aB3dE5gH7jK9", "")
	f.Fuzz(func(t *testing.T, prefix, secret, suffix string) {
		if len(secret) < 6 || len(secret) > 64 || strings.ContainsAny(secret, " \t\r\n\"',;&<>{}[]\\=:") {
			return
		}
		for _, c := range secret {
			if c < 0x21 || c > 0x7e {
				return
			}
		}
		if strings.Contains(prefix, secret) || strings.Contains(suffix, secret) {
			return
		}
		in := prefix + " password=" + secret + " " + suffix
		out := New(nil).Text(in)
		if strings.Contains(out, "password="+secret) {
			t.Fatalf("secret survived: %q -> %q", in, out)
		}
	})
}

// A token with a well-known prefix is a secret also when it is shorter than
// the real format: a test token, a truncated one, or an older format.
func TestShortTokensWithAKnownPrefix(t *testing.T) {
	r := New(nil)
	for _, in := range []string{
		`level=info msg="using token ghp_Ab3dEf6hIj9kLm2nOp5q for the audit"`,
		`sh -c echo using ghs_0123456789abcdefXYZ now`,
		`npm_Ab3dEf6hIj9kLm2nOp5qRs8t in the build log`,
	} {
		out := r.Text(in)
		if strings.Contains(out, "Ab3dEf6hIj9kLm2nOp5q") || strings.Contains(out, "0123456789abcdefXYZ") {
			t.Errorf("token not redacted: %q -> %q", in, out)
		}
	}
	// A short word after the prefix is not a token.
	if out := r.Text("the ghp_demo label"); !strings.Contains(out, "ghp_demo") {
		t.Errorf("a short name was redacted: %q", out)
	}
}
