package tools

import (
	"fmt"
	"strings"
	"testing"
)

// Item 5: a JSON log key with a space is a key name, in the readable form and
// in the raw form (full=true).
func TestLogsJSONKeyWithSpacesIsRedacted(t *testing.T) {
	lines := []string{
		`{"level":"info","msg":"login","api key":"SECRETVALUE1234"}`,
		`{"level":"info","msg":"login","API  KEY":"SECRETVALUE5678"}`,
		`{"level":"info","msg":"login","user name":"alice"}`,
	}
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "2026-11-13T11:00:%02dZ %s\n", i, l)
	}
	for _, full := range []bool{false, true} {
		r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, shopPod), logs: map[string]string{"shop/web-1/app": b.String()}})
		out := call(t, r, "logs", map[string]any{"pod": "web-1", "namespace": "shop", "full": full}).Text
		mustNotContain(t, out, "SECRETVALUE1234", "SECRETVALUE5678")
		mustContain(t, out, "alice", "[REDACTED:key-name]", "redactions: 2")
	}
}
