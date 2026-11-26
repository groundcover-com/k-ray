package tools

import (
	"strings"
	"testing"
)

// initPodWith is a pod whose init container runs the given command and args
// (JSON fields) and is running.
func initPodWith(fields string) string {
	return `[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T11:50:00Z"},
"spec":{"nodeName":"n","initContainers":[{"name":"wait","image":"x:1",` + fields + `}],"containers":[{"name":"app","image":"x:1"}]},
"status":{"phase":"Pending","initContainerStatuses":[{"name":"wait","ready":false,"restartCount":0,"state":{"running":{"startedAt":"2026-11-13T11:50:30Z"}}}],
"containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{"waiting":{"reason":"PodInitializing"}}}]}}]`
}

// Two pods with one name make describe answer with short blocks. A short
// block names what a running init container runs and its last log line.
func briefInitCall(t *testing.T, fields string, logs map[string]string) string {
	t.Helper()
	two := strings.TrimSuffix(initPodWith(fields), "]") + "," + strings.TrimPrefix(strings.ReplaceAll(initPodWith(fields), `"shop"`, `"shop2"`), "[")
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, two), logs: logs})
	res := call(t, r, "describe", map[string]any{"name": "web-1"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	return res.Text
}

func TestBriefInitCauseRedactsCommandPerElement(t *testing.T) {
	for name, fields := range map[string]string{
		"separate": `"command":["/wait","--password","correct horse battery"]`,
		"equals":   `"command":["/wait","--password=correct horse battery"]`,
		"args":     `"command":["/wait"],"args":["--password","correct horse battery"]`,
	} {
		out := briefInitCall(t, fields, map[string]string{})
		mustNotContain(t, out, "correct", "horse", "battery")
		mustContain(t, out, "it runs: /wait --password")
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}
}

func TestBriefInitCauseKeepsAnOrdinaryCommand(t *testing.T) {
	out := briefInitCall(t, `"command":["sh","-c","sleep 3600"]`, map[string]string{})
	mustContain(t, out, "it runs: sh -c sleep 3600")
	mustNotContain(t, out, "REDACTED")
}

func TestBriefInitCauseLastLogLineMasksAKeyBlock(t *testing.T) {
	for _, kind := range []string{"full", "noend", "orphan"} {
		out := briefInitCall(t, `"command":["/keygen"]`, map[string]string{"shop/web-1/wait": pemLog(kind), "shop2/web-1/wait": pemLog(kind)})
		mustContain(t, out, "its last log line")
		noPEMLeft(t, out)
	}
}
