package harness_test

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/groundcover-com/kray/test/fixture"
	"github.com/groundcover-com/kray/test/harness"
)

// leaksOf runs the harness check that Run applies to every call text.
func leaksOf(f harness.Fixture, text string) []harness.Leak {
	return harness.FindLeaks(f, text, "t")
}

func hasLeak(ls []harness.Leak, place string, part int) bool {
	for _, l := range ls {
		if l.Place == place && l.Part == part {
			return true
		}
	}
	return false
}

func TestEveryPartOfEveryPlaceIsFoundAlone(t *testing.T) {
	fx := fixture.New("fx")
	f := fx.HarnessFixture("pod", "node")
	for place, parts := range fx.Parts {
		for i, part := range parts {
			ls := leaksOf(f, "before "+part+" after")
			if !hasLeak(ls, place, i+1) || len(ls) != 1 {
				t.Errorf("part %d of %s alone: leaks %+v", i+1, place, ls)
			}
		}
	}
	for place, c := range fx.Canaries {
		ls := leaksOf(f, "x "+c+" y")
		if !hasLeak(ls, place, 0) || len(ls) != 1 {
			t.Errorf("canary %s alone: leaks %+v", place, ls)
		}
	}
}

// A partial leak is the point of the parts: the short last line of a key,
// the first word of a phrase.
func TestPartialLeaksAreFoundAndNamed(t *testing.T) {
	fx := fixture.New("fx")
	f := fx.HarnessFixture("pod", "node")
	key := fx.Parts[fixture.LogKeyBlock]
	short := key[len(key)-1]
	if len(short) != 24 || !strings.HasSuffix(short, "==") {
		t.Fatalf("the last key line is %q, want 22 characters and ==", short)
	}
	for _, c := range []struct {
		name  string
		text  string
		place string
		part  int
	}{
		{"only the short last key line", "2026-11-13T10:00:01Z " + short + "\n[REDACTED:pem]", fixture.LogKeyBlock, len(key)},
		{"only the first key line", key[0], fixture.LogKeyBlock, 1},
		{"only W1 of the event phrase", `{"password":"` + fx.Parts[fixture.EventQuotedPhrase][0] + ` [REDACTED`, fixture.EventQuotedPhrase, 1},
		{"only a cut piece of W2 of the event phrase", `"password":"[REDACTED] ` + fx.Parts[fixture.EventQuotedPhrase][1][:12], fixture.EventQuotedPhrase, 2},
		{"only the tail of W4", "...ending " + fx.Parts[fixture.EventQuotedPhrase][3][4:], fixture.EventQuotedPhrase, 4},
		{"only W3 of the args phrase", "--password " + fx.Parts[fixture.ArgsPhrase][2], fixture.ArgsPhrase, 3},
		{"only W1 of the condition phrase", fx.Parts[fixture.ConditionQuotedPhrase][0], fixture.ConditionQuotedPhrase, 1},
		{"the spaced key", `{"api key":"` + fx.Canaries[fixture.LogSpacedKey] + `"}`, fixture.LogSpacedKey, 0},
	} {
		ls := leaksOf(f, c.text)
		if len(ls) != 1 || !hasLeak(ls, c.place, c.part) {
			t.Errorf("%s: leaks %+v, want exactly place %s part %d", c.name, ls, c.place, c.part)
		}
	}
}

func TestPartsAreFoundInEveryForm(t *testing.T) {
	fx := fixture.New("fx")
	f := fx.HarnessFixture("pod", "node")
	part := fx.Parts[fixture.LogKeyBlock][1]
	for form, text := range map[string]string{
		"raw":            part,
		"base64":         base64.StdEncoding.EncodeToString([]byte(part)),
		"base64-raw":     base64.RawStdEncoding.EncodeToString([]byte(part)),
		"base64-url-raw": base64.RawURLEncoding.EncodeToString([]byte(part)),
		"url-escaped":    url.QueryEscape(part),
	} {
		if !hasLeak(leaksOf(f, "x"+text+"y"), fixture.LogKeyBlock, 2) {
			t.Errorf("form %s of a key line was not found", form)
		}
	}
}

func TestRedactedTextIsNotALeak(t *testing.T) {
	fx := fixture.New("fx")
	f := fx.HarnessFixture("pod", "node")
	text := strings.Join([]string{
		`{"level":"info","msg":"login","api key":"[REDACTED:key-name]"}`,
		"[REDACTED:pem]", "[REDACTED:pem]", "[REDACTED:pem]",
		`message: {"password":"[REDACTED:key-name]","k":"v"}`,
		"args: --password [REDACTED:key-name]",
		"-----BEGIN RSA PRIVATE KEY-----", "-----END RSA PRIVATE KEY-----",
	}, "\n")
	if ls := leaksOf(f, text); len(ls) != 0 {
		t.Fatalf("false positives: %+v", ls)
	}
}

func TestPartsAreRandomLongAndMixed(t *testing.T) {
	a, b := fixture.New("a"), fixture.New("b")
	for place, parts := range a.Parts {
		for i, p := range parts {
			if p == b.Parts[place][i] {
				t.Errorf("%s part %d is not random", place, i+1)
			}
			if place == fixture.LogKeyBlock {
				if i < len(parts)-1 && len(p) != 64 {
					t.Errorf("key line %d has %d characters, want 64", i+1, len(p))
				}
				continue
			}
			if len(p) < 12 || strings.ToLower(p) == p || strings.ToUpper(p) == p || !strings.ContainsAny(p, "0123456789") {
				t.Errorf("%s part %d %q is short or not mixed", place, i+1, p)
			}
		}
	}
}

// The report names the place and the part.
func TestReportNamesThePart(t *testing.T) {
	fx := fixture.New("fx")
	rep := &harness.Report{Target: "x", Leaks: leaksOf(fx.HarnessFixture("pod", "node"), fx.Parts[fixture.EventQuotedPhrase][1])}
	if s := rep.String(); !strings.Contains(s, fmt.Sprintf("canary %q part 2 (raw form)", fixture.EventQuotedPhrase)) {
		t.Fatalf("report:\n%s", s)
	}
}

// The plan must make calls that would show each place.
func TestPlanHasTheShowCalls(t *testing.T) {
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	tools := []*mcp.Tool{
		{Name: "logs", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"pod": str, "workload": str, "namespace": str, "container": str, "previous": boolean, "full": boolean, "match": str,
		}}},
		{Name: "events", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"namespace": str, "full": boolean, "match": str,
		}}},
		{Name: "describe", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"kind": str, "name": str, "namespace": str, "full": boolean,
		}, "required": []any{"kind", "name"}}},
		{Name: "get", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"kind": str, "name": str, "namespace": str, "full": boolean,
		}, "required": []any{"kind", "name"}}},
		{Name: "list", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"kind": str, "namespace": str, "full": boolean, "all": boolean, "match": str,
		}, "required": []any{"kind"}}},
	}
	f := fixture.New("fx").HarnessFixture("web-1", "n1")
	calls := harness.PlannedCalls(tools, f)
	want := []struct {
		name string
		ok   func(tool string, a map[string]any) bool
	}{
		{"logs default", func(tl string, a map[string]any) bool {
			return tl == "logs" && a["pod"] == "web-1" && a["full"] == nil && a["previous"] == nil && a["match"] == nil
		}},
		{"logs full", func(tl string, a map[string]any) bool {
			return tl == "logs" && a["pod"] == "web-1" && a["full"] == true
		}},
		{"logs previous", func(tl string, a map[string]any) bool {
			return tl == "logs" && a["previous"] == true && a["pod"] == "web-1"
		}},
		{"logs previous full", func(tl string, a map[string]any) bool {
			return tl == "logs" && a["previous"] == true && a["full"] == true
		}},
		{"logs of the workload", func(tl string, a map[string]any) bool { return tl == "logs" && a["workload"] == "deployment/api" }},
		{"logs match", func(tl string, a map[string]any) bool {
			return tl == "logs" && a["match"] != nil && a["pod"] == "web-1"
		}},
		{"events", func(tl string, a map[string]any) bool { return tl == "events" && a["full"] == nil && a["match"] == nil }},
		{"events full", func(tl string, a map[string]any) bool { return tl == "events" && a["full"] == true }},
		{"events match", func(tl string, a map[string]any) bool { return tl == "events" && a["match"] != nil }},
		{"describe pod", func(tl string, a map[string]any) bool {
			return tl == "describe" && a["name"] == "web-1" && a["kind"] == "pods"
		}},
		{"describe pod full", func(tl string, a map[string]any) bool {
			return tl == "describe" && a["name"] == "web-1" && a["full"] == true
		}},
		{"describe workload", func(tl string, a map[string]any) bool {
			return tl == "describe" && a["name"] == "api" && a["kind"] == "deployments"
		}},
		{"get pod", func(tl string, a map[string]any) bool { return tl == "get" && a["name"] == "web-1" && a["full"] == nil }},
		{"get pod full", func(tl string, a map[string]any) bool {
			return tl == "get" && a["name"] == "web-1" && a["full"] == true
		}},
		{"get custom resource", func(tl string, a map[string]any) bool {
			return tl == "get" && a["kind"] == "widgets.example.com" && a["name"] == "w1"
		}},
		{"get custom resource full", func(tl string, a map[string]any) bool {
			return tl == "get" && a["kind"] == "widgets.example.com" && a["full"] == true
		}},
		{"list custom resource", func(tl string, a map[string]any) bool { return tl == "list" && a["kind"] == "widgets.example.com" }},
		{"list custom resource full", func(tl string, a map[string]any) bool {
			return tl == "list" && a["kind"] == "widgets.example.com" && a["full"] == true
		}},
		{"list custom resource all", func(tl string, a map[string]any) bool {
			return tl == "list" && a["kind"] == "widgets.example.com" && a["all"] == true
		}},
	}
	for _, w := range want {
		found := false
		for _, c := range calls {
			if !c.Hostile && w.ok(c.Tool, c.Args) {
				found = true
			}
		}
		if !found {
			t.Errorf("the plan has no call: %s", w.name)
		}
	}
	seen := map[string]bool{}
	for _, c := range calls {
		k := fmt.Sprint(c.Tool, c.Args, c.Hostile)
		if seen[k] {
			t.Errorf("duplicate call %s", k)
		}
		seen[k] = true
	}
}
