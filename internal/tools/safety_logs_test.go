package tools

import (
	"fmt"
	"strings"
	"testing"
)

// --- item 2: a key block that spans log lines ---

const pemLogEnd = "-----END RSA PRIVATE KEY-----"

var pemLogBody = []string{
	"MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun",
	"VTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u+qxCPDwCsXAPcVQGpGxQqYB3nNnm",
	"k3nQ0lN3jx1Yb1Xv8N0o5K1rJvZ0p3w5a2Qh3mWc8bK4d9sT7uV6xY1zA2cE5fG8",
	"Zq8sT2vB9xK3mP7wL5nR1d==",
}

const shopPod = `[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},"spec":{"containers":[{"name":"app","image":"x:1"}]},"status":{"phase":"Running"}}]`

// pemLog is a log in which an app prints a private key over many lines, each
// with a timestamp. kind "full" keeps the whole block; "noend" has no END
// line; "orphan" starts inside the block.
func pemLog(kind string) string {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "2026-11-13T11:00:%02dZ GET /health 200 %dms\n", i, i)
	}
	line := func(s string) { b.WriteString("2026-11-13T11:01:00Z " + s + "\n") }
	if kind != "orphan" {
		line("-----BEGIN RSA PRIVATE KEY-----")
	}
	for _, l := range pemLogBody {
		line(l)
	}
	if kind != "noend" {
		line(pemLogEnd)
	}
	return b.String()
}

func noPEMLeft(t *testing.T, text string) {
	t.Helper()
	mustNotContain(t, text, "Zq8sT2vB9xK3mP7wL5nR1d", "END RSA PRIVATE KEY", "k3nQ0lN3jx1Yb1Xv", "BEGIN RSA PRIVATE KEY")
}

func TestLogsMaskAMultiLineKeyBlock(t *testing.T) {
	for _, kind := range []string{"full", "noend", "orphan"} {
		for _, fullArg := range []bool{false, true} {
			r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, shopPod), logs: map[string]string{"shop/web-1/app": pemLog(kind) + "2026-11-13T11:01:01Z GET /ready 200 1ms\n"}})
			res := call(t, r, "logs", map[string]any{"pod": "web-1", "namespace": "shop", "full": fullArg})
			if res.IsError {
				t.Fatal(res.Text)
			}
			noPEMLeft(t, res.Text)
			mustContain(t, res.Text, "[REDACTED:pem]", "redactions: 1 (pem 1)")
		}
	}
}

func TestLogsKeepAnOrdinaryBase64LineWithoutAKeyHeader(t *testing.T) {
	log := "2026-11-13T11:00:00Z GET /health 200\n2026-11-13T11:00:01Z Zq8sT2vB9xK3mP7wL5nR1d==\n2026-11-13T11:00:02Z GET /ready 200\n"
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, shopPod), logs: map[string]string{"shop/web-1/app": log}})
	res := call(t, r, "logs", map[string]any{"pod": "web-1", "namespace": "shop", "full": true})
	mustContain(t, res.Text, "Zq8sT2vB9xK3mP7wL5nR1d==", "GET /health 200")
	mustNotContain(t, res.Text, "REDACTED", "redactions:")
}

func TestLogsMaskAKeyBlockWithoutTimestamps(t *testing.T) {
	var b strings.Builder
	b.WriteString("2026-11-13T11:00:00Z loading key\n-----BEGIN RSA PRIVATE KEY-----\n")
	b.WriteString(strings.Join(pemLogBody, "\n") + "\n" + pemLogEnd + "\n2026-11-13T11:00:05Z done\n")
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, shopPod), logs: map[string]string{"shop/web-1/app": b.String()}})
	res := call(t, r, "logs", map[string]any{"pod": "web-1", "namespace": "shop"})
	noPEMLeft(t, res.Text)
	mustContain(t, res.Text, "done")
}

// A crash log is summarized in describe: the same masking holds there.
func TestDescribeCrashLogMasksAMultiLineKeyBlock(t *testing.T) {
	for _, kind := range []string{"full", "noend", "orphan"} {
		f := diagFixture(t)
		f.logs["pay/crash-1/app#previous"] = pemLog(kind) + "2026-11-13T11:02:00Z FATAL config: DATABASE_URL is not set\n"
		r, _ := newRunner(t, f)
		out := describe(t, r, "pod", "crash-1", "pay")
		noPEMLeft(t, out)
	}
}

const initPod = `[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T11:50:00Z"},
"spec":{"nodeName":"n","initContainers":[{"name":"keygen","image":"x:1"}],"containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Pending","initContainerStatuses":[{"name":"keygen","ready":false,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T11:50:30Z"}}}],
"containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{"waiting":{"reason":"PodInitializing"}}}]}}]`

// The last log line of an init container is shown in the cause. When the log
// ends inside a key block, that line is the END line or the short last line.
func TestDescribeInitLastLogLineMasksAKeyBlock(t *testing.T) {
	for _, kind := range []string{"full", "noend", "orphan"} {
		r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, initPod), logs: map[string]string{"shop/web-1/keygen": pemLog(kind)}})
		out := describe(t, r, "pod", "web-1", "shop")
		mustContain(t, out, "init container keygen", "its last log line")
		noPEMLeft(t, out)
	}
}
