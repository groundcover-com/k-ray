package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for secrets that a security review found unredacted.

func TestReviewConfigShapesAreRedacted(t *testing.T) {
	cases := []struct{ name, in, secret string }{
		// Spaces around '=' (INI, TOML).
		{"ini spaced equals", "[client]\nuser = root\npassword = MyCnfPw77\n", "MyCnfPw77"},
		{"toml quoted", `password = "TomlPw88"`, "TomlPw88"},
		// A password as the first child of a YAML key.
		{"yaml first child", "redis:\n  password: R3disPassw0rdZ\n  host: redis\n", "R3disPassw0rdZ"},
		{"yaml nested first child", "db:\n  primary:\n    password: DbPassw0rdQ7\n", "DbPassw0rdQ7"},
		// Other formats.
		{"redis url empty user", "REDIS_URL=redis://:s3cr3tRedisPw@redis-master:6379/0", "s3cr3tRedisPw"},
		{"celery broker", "broker_url: redis://:Zx81mPq77tTv@redis:6379/0", "Zx81mPq77tTv"},
		{"authorization token", "Authorization: Token 9944b09199c62bcf9418ad846dd0e4bbdfc6ee4b", "9944b09199c62bcf9418ad846dd0e4bbdfc6ee4b"},
		{"authorization ssws", "authorization: SSWS 00QCjAl4MlV-WPXM-Vk8ACxuS6Avg8L0", "00QCjAl4MlV-WPXM-Vk8ACxuS6Avg8L0"},
		{"escaped json in a log", `msg="login {\"user\":\"bob\",\"password\":\"hunter2pwX\"}"`, "hunter2pwX"},
		{"quoted special characters", `password="Xk9#mQ2{vL8&pR4"`, "vL8&pR4"},
		{"quoted spaces", `password="correct horse battery staple"`, "battery staple"},
		{"json quoted spaces", `{"password": "correct horse battery staple"}`, "battery staple"},
		{"flag with space in a shell line", `sh -c "exec redis-server --requirepass foobaredPw9"`, "foobaredPw9"},
		{"azure sas sig", "https://acct.blob.core.windows.net/c?sv=2022-11-02&sig=AbCd%2BEfGh%2FIjKl%3D&se=2026", "AbCd%2BEfGh"},
		{"hex aes key", "AES_KEY=4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e", "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a"},
	}
	for _, c := range cases {
		r := New(nil)
		out := r.Text(c.in)
		if strings.Contains(out, c.secret) {
			t.Errorf("%s: secret survived:\n  in:  %q\n  out: %q", c.name, c.in, out)
		}
	}
}

func TestReviewAuthorizationKeepsTheScheme(t *testing.T) {
	out := New(nil).Text("Authorization: Token 9944b09199c62bcf9418ad846dd0e4bbdfc6ee4b")
	if !strings.HasPrefix(out, "Authorization: Token [REDACTED:") {
		t.Fatalf("got %q", out)
	}
}

// Ordinary names must not be redacted.
func TestReviewNamesAreKept(t *testing.T) {
	for _, in := range []string{
		"gke-prod-cluster-default-pool-1a2b3c4d-x9z8",
		"gke-production-eu-west1-highmem-pool-7b3e9f2a-k4m2",
		"payments-reconciliation-worker-7f9c8d6b5c-x2k4z",
		"checkout-service-v2-canary-6d8f7b9c4d-q8w7e",
		"cert-manager-webhook-ca-injector-5c9b8d7f6e-p2r4t",
		"arn:aws:iam::123456789012:role/eksctl-prod-addon-iamserviceaccount-Role1-1A2B3C4D5E6F",
		"eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/ExternalDNSRoleForCluster01",
		"projects/my-proj-123/serviceAccounts/gke-workload-sa@my-proj-123.iam.gserviceaccount.com",
		"/var/lib/kubelet/pods/0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b/volumes/kubernetes.io~projected/kube-api-access-x7k2q",
		"PaymentGatewayClientConfiguration2Factory",
		"time=\"2026-11-13T10:00:00Z\" level=info msg=\"ready\"",
	} {
		if out := New(nil).Text(in); out != in {
			t.Errorf("changed a safe value:\n  in:  %q\n  out: %q", in, out)
		}
	}
}

// The random canaries of the harness must still be found.
func TestReviewRandomTokensStillRedacted(t *testing.T) {
	for _, s := range []string{
		"kc7Qx2Lm9Rt4Vb8Nw1Zp6Hd3Fs5Jk0Gy2Tc7Ue",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"Zq8-Lm3_Xv9Kd2Wp7Rt5Hs1Jb4Nc6Fg0Ty3Ue8Ao",
	} {
		if out := New(nil).Text("seed " + s); strings.Contains(out, s) {
			t.Errorf("random token survived: %q", out)
		}
	}
}

func TestReviewObjectArgsInShellCommand(t *testing.T) {
	var obj map[string]any
	_ = json.Unmarshal([]byte(`{"command":["sh","-c","exec redis-server --requirepass foobaredPw9"],"env":[{"name":"REDIS_URL","value":"redis://:s3cr3tRedisPw@redis:6379"}]}`), &obj)
	New(nil).Object(obj)
	b, _ := json.Marshal(obj)
	if strings.Contains(string(b), "foobaredPw9") || strings.Contains(string(b), "s3cr3tRedisPw") {
		t.Fatalf("secret survived: %s", b)
	}
}
