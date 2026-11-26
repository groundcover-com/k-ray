package shape

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Fixtures. Each returns raw log text as the cluster API gives it: a
// timestamp, a space, the line.

var (
	accessPaths     = []string{"/api/stock", "/api/prices", "/api/cart", "/api/orders"}
	accessUpstreams = []string{"catalog", "pricing", "inventory"}
)

func stamp(i int) string {
	return time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * 700 * time.Millisecond).Format("2006-01-02T15:04:05.000000000Z")
}

// accessLog has n lines; the lines at rare positions answer 503 and go to the
// inventory upstream.
func accessLog(n int, rare map[int]bool, json bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		path := accessPaths[(i*7)%len(accessPaths)]
		up := accessUpstreams[(i*5)%len(accessUpstreams)]
		status := 200
		if rare[i] {
			status, up = 503, "inventory"
		}
		dur := 3 + (i*37)%400
		if rare[i] {
			dur = 2100 + i%50
		}
		if json {
			fmt.Fprintf(&b, "%s {\"level\":\"info\",\"msg\":\"request handled\",\"path\":%q,\"status\":%d,\"upstream\":%q,\"dur_ms\":%d,\"req\":\"r-%d\"}\n", stamp(i), path, status, up, dur, i*7919)
		} else {
			fmt.Fprintf(&b, "%s request handled path=%s status=%d upstream=%s dur=%dms req=r-%d\n", stamp(i), path, status, up, dur, i*7919)
		}
	}
	return b.String()
}

func rareSet(n, count int) map[int]bool {
	m := map[int]bool{}
	for k := 0; k < count; k++ {
		m[(k+1)*n/(count+2)] = true
	}
	return m
}

func klogLog(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		result := "ok"
		if i%97 == 5 {
			result = "requeue"
		}
		fmt.Fprintf(&b, "%s I1113 10:%02d:%02d.%06d       1 sync.go:%d] \"Sync complete\" key=\"shop/api-%d\" took=%dms result=%s\n", stamp(i), i/60%60, i%60, i, 100+i%3, i%40, 2+i%17, result)
	}
	fmt.Fprintf(&b, "%s E1113 10:59:01.000001       1 sync.go:210] \"Sync failed\" err=\"context deadline exceeded\"\n", stamp(n))
	fmt.Fprintf(&b, "%s E1113 10:59:02.000001       1 sync.go:210] \"Sync failed\" err=\"context deadline exceeded\"\n", stamp(n+1))
	return b.String()
}

func javaLog(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s 2026-11-13 10:00:%02d,%03d INFO  [http-nio-%d] c.shop.OrderService - order accepted id=%d total=%d.%02d\n", stamp(i), i/50%60, i%1000, 8000+i%4, 1000+i, 10+i%90, i%100)
		if i == n/2 {
			fmt.Fprintf(&b, "%s 2026-11-13 10:00:30,001 ERROR [http-nio-8080] c.shop.PayService - payment failed\n", stamp(i))
			fmt.Fprintf(&b, "%s com.shop.PayException: gateway rejected\n\tat c.shop.PayService.pay(PayService.java:42)\n\tat c.shop.OrderService.accept(OrderService.java:17)\nCaused by: java.net.ConnectException: Connection refused\n\tat java.base/Socket.connect(Socket.java:1)\n", stamp(i))
		}
	}
	return b.String()
}

func shortLog() string {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "%s request handled path=%s status=%d\n", stamp(i), accessPaths[i%2], 200+3*(i%2))
	}
	b.WriteString(stamp(12) + " worker started id=7\n")
	return b.String()
}

func summarize(t testing.TB, raw string, pods, max int) LogSummary {
	t.Helper()
	es := ParseLogs(raw)
	ReadableJSON(es)
	return SummarizeLogsPods(es, pods, max)
}

func wantIn(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in:\n%s", w, body)
		}
	}
}

func wantNotIn(t *testing.T, body string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(body, w) {
			t.Errorf("unexpected %q in:\n%s", w, body)
		}
	}
}

func TestMineAccessLogKeyValue(t *testing.T) {
	raw := accessLog(4000, rareSet(4000, 12), false)
	s := summarize(t, raw, 0, 16<<10)
	if s.Groups != 1 {
		t.Fatalf("groups %d:\n%s", s.Groups, s.Body)
	}
	if len(s.Body) >= 900 {
		t.Errorf("summary is %d bytes:\n%s", len(s.Body), s.Body)
	}
	wantIn(t, s.Body,
		"×4000",
		"request handled path=<*> status=<*> upstream=<*> dur=<*> req=<*>",
		"status: 200 ×3988 · 503 ×12",
		"status=503 ×12 goes with upstream=inventory ×12",
		"e.g. ", "status=503 upstream=inventory",
		"dur: min ", "median ", "max ",
	)
	// The common value has no example line.
	if strings.Count(s.Body, "e.g. ") != 1 {
		t.Errorf("one example only:\n%s", s.Body)
	}
	wantNotIn(t, s.Body, "r-7919")
}

func TestMineAccessLogJSON(t *testing.T) {
	raw := accessLog(4000, rareSet(4000, 12), true)
	s := summarize(t, raw, 0, 16<<10)
	if s.Groups != 1 {
		t.Fatalf("groups %d:\n%s", s.Groups, s.Body)
	}
	if len(s.Body) >= 900 {
		t.Errorf("summary is %d bytes:\n%s", len(s.Body), s.Body)
	}
	wantIn(t, s.Body, "INFO request handled", "status: 200 ×3988 · 503 ×12", "status=503 ×12 goes with upstream=inventory ×12", "status=503")
}

func TestMineWordThatVariesIsOneGroup(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "%s request handled path=%s status=200\n", stamp(i), accessPaths[i%2])
	}
	s := summarize(t, b.String(), 0, 4096)
	if s.Groups != 1 {
		t.Fatalf("two paths must be one group:\n%s", s.Body)
	}
	wantIn(t, s.Body, "path=<*>", "path: /api/prices ×30 · /api/stock ×30")
}

func TestMineKlog(t *testing.T) {
	s := summarize(t, klogLog(1200), 0, 16<<10)
	wantIn(t, s.Body,
		"errors: 2 lines in 1 group",
		"Sync failed", "context deadline exceeded",
		"result: ok ×", "requeue ×13",
		"e.g. ",
	)
	if s.ErrorEntries != 2 || s.ErrorGroups != 1 {
		t.Errorf("errors %d in %d groups", s.ErrorEntries, s.ErrorGroups)
	}
	if s.Groups != 2 {
		t.Errorf("groups %d:\n%s", s.Groups, s.Body)
	}
}

func TestMineJavaTraceAmongInfo(t *testing.T) {
	s := summarize(t, javaLog(400), 0, 16<<10)
	wantIn(t, s.Body,
		"errors: 1 line in 1 group",
		"payment failed",
		"com.shop.PayException: gateway rejected",
		"Caused by: java.net.ConnectException: Connection refused",
		"(3 frame lines left out)",
		"order accepted id=<*> total=<*>",
	)
	wantNotIn(t, s.Body, "PayService.java")
	if s.Groups != 2 {
		t.Errorf("groups %d:\n%s", s.Groups, s.Body)
	}
}

func TestMineTwentyPodsThreeHaveExtraPattern(t *testing.T) {
	var all []LogEntry
	for p := 0; p < 20; p++ {
		var b strings.Builder
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&b, "%s heartbeat ok seq=%d\n", stamp(i), i)
			if p%7 == 0 && i%10 == 0 {
				fmt.Fprintf(&b, "%s cache miss key=item-%d shard=%d\n", stamp(i), i, p%3)
			}
		}
		es := ParseLogs(b.String())
		for j := range es {
			es[j].Pod = fmt.Sprintf("api-%d", p)
		}
		all = append(all, es...)
	}
	s := SummarizeLogsPods(all, 20, 16<<10)
	if s.Groups != 2 {
		t.Fatalf("groups %d:\n%s", s.Groups, s.Body)
	}
	i1, i2 := strings.Index(s.Body, "cache miss"), strings.Index(s.Body, "heartbeat ok")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("the group of 3 pods must come first:\n%s", s.Body)
	}
	wantIn(t, s.Body, "[3 of 20 pods: api-0, api-7, api-14]", "[all 20 pods]", "×2000")
}

func TestMineShortLogIsNotLarger(t *testing.T) {
	raw := shortLog()
	es := ParseLogs(raw)
	old := summarizeLogs(es, 0, 4096, false)
	got := SummarizeLogsPods(es, 0, 4096)
	if got.Body != old.Body {
		t.Errorf("a short log is grouped as before:\n%s\nwant\n%s", got.Body, old.Body)
	}
}

func TestMineErrorsStayVisible(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "%s request handled path=/a status=200\n", stamp(i))
		if i%100 == 50 {
			fmt.Fprintf(&b, "%s level=error msg=\"upstream failed\" code=%d\n", stamp(i), 500+i%3)
		}
	}
	s := summarize(t, b.String(), 0, 4096)
	if s.ErrorEntries != 5 {
		t.Fatalf("error entries %d", s.ErrorEntries)
	}
	wantIn(t, s.Body, "errors: 5 lines", "upstream failed")
}

func TestMineDeterministic(t *testing.T) {
	raw := accessLog(1500, rareSet(1500, 9), false) + klogLog(300)
	first := summarize(t, raw, 0, 8192).Body
	for i := 0; i < 20; i++ {
		if got := summarize(t, raw, 0, 8192).Body; got != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, got, first)
		}
	}
}

func TestMineMarksAreOneValue(t *testing.T) {
	var b strings.Builder
	rules := []string{"key-name", "high-entropy", "bearer"}
	for i := 0; i < 90; i++ {
		fmt.Fprintf(&b, "%s login ok user=u%d token=[REDACTED:%s] status=200\n", stamp(i), i%3, rules[i%3])
	}
	s := summarize(t, b.String(), 0, 4096)
	if s.Groups != 1 {
		t.Fatalf("groups %d:\n%s", s.Groups, s.Body)
	}
	wantIn(t, s.Body, "[REDACTED:")
	// The mark is constant, so it is in the template, not a variable of three
	// values.
	wantNotIn(t, s.Body, "token: ", "[REDACTED] ×")
	if n := strings.Count(s.Body, "[REDACTED:"); n != 1 {
		t.Errorf("mark shown %d times:\n%s", n, s.Body)
	}
}

func TestMineNumericRange(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "%s probe done took=%dms\n", stamp(i), 1+i)
	}
	s := summarize(t, b.String(), 0, 4096)
	wantIn(t, s.Body, "took: min 1ms", "max 100ms")
}

func TestMineLeafOverflowStillGroups(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "%s msg%c%c%c a b c\n", stamp(i), 'a'+i%26, 'a'+i/26%26, 'a'+i/676%26)
	}
	s := summarize(t, b.String(), 0, 1<<16)
	if s.Entries != 400 || s.Groups < 1 {
		t.Fatalf("entries %d groups %d", s.Entries, s.Groups)
	}
}

func BenchmarkSummarize50k(b *testing.B) {
	raw := accessLog(50000, rareSet(50000, 40), false)
	es := ParseLogs(raw)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SummarizeLogsPods(es, 0, 16<<10)
	}
	b.ReportMetric(float64(len(es))*float64(b.N)/b.Elapsed().Seconds(), "lines/s")
}

func BenchmarkSummarize50kMixed(b *testing.B) {
	raw := accessLog(20000, rareSet(20000, 40), true) + klogLog(20000) + javaLog(10000)
	es := ParseLogs(raw)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SummarizeLogsPods(es, 0, 16<<10)
	}
	b.ReportMetric(float64(len(es))*float64(b.N)/b.Elapsed().Seconds(), "lines/s")
}
