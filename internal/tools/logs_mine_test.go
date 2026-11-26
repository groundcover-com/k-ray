package tools

import (
	"fmt"
	"strings"
	"testing"
)

const minePod = "shop/api-7f9c8-x2k/api"

func mineStamp(i int) string {
	return fmt.Sprintf("2026-11-13T11:%02d:%02d.%03dZ", i/3600%60, i/60%60, i%60*10)
}

// A variable that carries a secret is redacted before the line is cut into
// a template, so no histogram holds the secret.
func TestLogsHistogramsNeverHoldSecrets(t *testing.T) {
	f := fixture(t)
	var b strings.Builder
	for i := 0; i < 300; i++ {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&b, "%s login ok user=u%d token=tokSecret%dAbCdEfGh status=%d\n", mineStamp(i), i%3, i, 200+i%2*3)
		case 1:
			fmt.Fprintf(&b, "%s login ok user=u%d Authorization: Bearer bearerSecret%dXyZ status=%d\n", mineStamp(i), i%3, i, 200+i%2*3)
		case 2:
			fmt.Fprintf(&b, "%s login ok user=u%d url=https://svc:pw%dsecret@db.internal/app status=%d\n", mineStamp(i), i%3, i, 200+i%2*3)
		default:
			fmt.Fprintf(&b, "%s login ok user=u%d password=pwVar%dsecret status=%d\n", mineStamp(i), i%3, i, 200+i%2*3)
		}
	}
	f.logs[minePod] = b.String()
	r, _ := newRunner(t, f)
	out := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k"}).Text
	mustContain(t, out, "login ok", "[REDACTED")
	mustNotContain(t, out, "tokSecret", "bearerSecret", "secret@db", "pwVar", ":pw")
	noSecrets(t, out)
}

func TestLogsRareValueWithItsPartnerAndExample(t *testing.T) {
	f := fixture(t)
	var b strings.Builder
	ups := []string{"catalog", "pricing", "inventory"}
	for i := 0; i < 4000; i++ {
		status, up := 200, ups[i%3]
		if i%331 == 7 {
			status, up = 503, "inventory"
		}
		fmt.Fprintf(&b, "%s request handled path=/api/p%c status=%d upstream=%s dur=%dms\n", mineStamp(i), 'a'+i%4, status, up, 5+i*13%300)
	}
	f.logs[minePod] = b.String()
	r, _ := newRunner(t, f)
	out := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k", "tail": 5000}).Text
	mustContain(t, out,
		"status: 200 ×3987 · 503 ×13",
		"status=503 ×13 goes with upstream=inventory ×13",
		"e.g. ",
		"4,000 lines (0 errors) in 1 group",
	)
	if len(out) > 1700 {
		t.Errorf("output is %d bytes:\n%s", len(out), out)
	}
}

func TestLogsNotShownNoteSaysHowToGetThem(t *testing.T) {
	f := fixture(t)
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		w := fmt.Sprintf("%c%c%c", 'a'+i%26, 'a'+i/26%26, 'a'+i/676%26)
		fmt.Fprintf(&b, "%s p%s q%s r%s\n", mineStamp(i), w, w, w)
	}
	f.logs[minePod] = b.String()
	r, _ := newRunner(t, f)
	r.Budget = 3000
	out := call(t, r, "logs", map[string]any{"pod": "api-7f9c8-x2k", "tail": 5000}).Text
	mustContain(t, out, " not shown. To see them: narrow with match, since or tail.", "logs with full=true")
}
