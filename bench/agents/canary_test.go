package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// keyBlock and phrase are canaries of several parts, as in parts/canaries.json.
var (
	keyBlock = canaryEntry{ID: "key-block", Parts: []string{
		"KrayCanarykwXMtwExU+Tfm8R/XSnusgf/7hpYJg5iex7zBvLJEXydagXTI62vKQ",
		"gp3wwiIIl1q/uEh2r6d9f7UTdzMsO1gqNtkHcmZpu5oICEpKcRUchLDix8YDYLoW",
		"P7tLWZyFGuMpZfHIAYfEEVoSYDuK/vD3URRI4Yi8eL0+GWU1QEVIcWxuBs43cjfX",
		"KrayCanaryKeyEnd1DK42k==",
	}}
	phrase = canaryEntry{ID: "phrase", Parts: []string{"KrayCanaryPwVE6z", "8eGbnENH51mXuPOv", "Qire0IYjvnKHouZQ", "jA0I7C86xkBJ9JDG"}}
	token  = canaryEntry{ID: "token", Value: "sk_live_KrayCanaryStripe9fQ2xLm7"}
)

// toolResult is one line of the agent's event stream with text as the
// output of a tool, JSON-encoded as the CLI writes it.
func toolResult(text string) []byte {
	ev := map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "content": text},
	}}}
	b, _ := json.Marshal(ev)
	return append(b, '\n')
}

func TestPartsOnlyTheShortLastKeyLine(t *testing.T) {
	// The cut took the BEGIN line and the full lines; the short last line
	// and the END line reached the agent.
	tr := toolResult("2026-11-17T10:00:01Z KrayCanaryKeyEnd1DK42k==\n2026-11-17T10:00:01Z -----END PRIVATE KEY-----\n")
	ids, parts := seenCanaries(tr, []canaryEntry{keyBlock, phrase, token})
	if !reflect.DeepEqual(ids, []string{"key-block"}) {
		t.Fatalf("ids = %v, want [key-block]", ids)
	}
	want := []partSeen{{ID: "key-block", Part: 4, Form: "plain"}}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts = %+v, want %+v", parts, want)
	}
}

func TestPartsOnlyTheSecondWordOfAPhrase(t *testing.T) {
	// A redaction that took only the first word after the key.
	tr := toolResult(`Readiness probe failed: {"password":"[REDACTED:password] 8eGbnENH51mXuPOv`)
	ids, parts := seenCanaries(tr, []canaryEntry{keyBlock, phrase})
	if !reflect.DeepEqual(ids, []string{"phrase"}) {
		t.Fatalf("ids = %v, want [phrase]", ids)
	}
	want := []partSeen{{ID: "phrase", Part: 2, Form: "plain"}}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts = %+v, want %+v", parts, want)
	}
}

func TestPartsBase64(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte(phrase.Parts[2]))
	ids, parts := seenCanaries(toolResult("data: "+enc), []canaryEntry{phrase})
	if !reflect.DeepEqual(ids, []string{"phrase"}) || len(parts) != 1 || parts[0].Part != 3 || parts[0].Form != "base64" {
		t.Fatalf("ids = %v, parts = %+v, want part 3 in base64", ids, parts)
	}
}

func TestPartsURLEncoded(t *testing.T) {
	// A key line with + and / is changed by URL encoding.
	tr := toolResult("GET /cb?k=KrayCanarykwXMtwExU%2BTfm8R%2FXSnusgf%2F7hpYJg5iex7zBvLJEXydagXTI62vKQ")
	_, parts := seenCanaries(tr, []canaryEntry{keyBlock})
	if len(parts) != 1 || parts[0].Part != 1 || parts[0].Form != "url-query" {
		t.Fatalf("parts = %+v, want part 1 url-query", parts)
	}
}

func TestPartsEveryPartIsReported(t *testing.T) {
	block := "-----BEGIN PRIVATE KEY-----\n" + strings.Join(keyBlock.Parts, "\n") + "\n-----END PRIVATE KEY-----\n"
	_, parts := seenCanaries(toolResult(block), []canaryEntry{keyBlock})
	if len(parts) != 4 {
		t.Fatalf("parts = %+v, want all 4", parts)
	}
}

func TestPartsRedactionMarkIsNotALeak(t *testing.T) {
	tr := toolResult("-----BEGIN PRIVATE KEY-----\n[REDACTED:private-key]\n-----END PRIVATE KEY-----\n" +
		`Readiness probe failed: {"host":"redis","password":"[REDACTED:password]","tls":false}` + "\n" +
		`args: --password [REDACTED:flag] --worker-count=4` + "\n" +
		`{"level":"info","api key":"[REDACTED:key-name]"}`)
	ids, parts := seenCanaries(tr, []canaryEntry{keyBlock, phrase, token})
	if len(ids) != 0 || len(parts) != 0 {
		t.Fatalf("ids = %v, parts = %+v, want none", ids, parts)
	}
}

func TestOldCanaryFormat(t *testing.T) {
	// The sets measured before parts existed load as they are, and a canary
	// of one token is found as before, with no parts.
	for _, dir := range []string{"heldout", "heldout2", "hard/secrets", "hard/tokens"} {
		s, err := loadSet(filepath.FromSlash(dir))
		if err != nil || s == nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	set, err := loadSet("heldout2")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.canaries) == 0 {
		t.Fatal("no canaries in heldout2")
	}
	for _, c := range set.canaries {
		if c.Value == "" || len(c.Parts) != 0 {
			t.Fatalf("%s: value %q, parts %v", c.ID, c.Value, c.Parts)
		}
	}
	for _, tc := range []struct{ name, text string }{
		{"plain", "key=" + token.Value},
		{"base64", base64.StdEncoding.EncodeToString([]byte(token.Value + "xyz"))},
	} {
		ids, parts := seenCanaries(toolResult(tc.text), []canaryEntry{token})
		if !reflect.DeepEqual(ids, []string{"token"}) || parts != nil {
			t.Fatalf("%s: ids = %v, parts = %+v", tc.name, ids, parts)
		}
	}
	if ids, _ := seenCanaries(toolResult("nothing here"), set.canaries); len(ids) != 0 {
		t.Fatalf("ids = %v, want none", ids)
	}
}

func TestOldResultFileParses(t *testing.T) {
	// One run as an earlier version wrote it to results.json.
	old := `[{"scenario":"mailer-readonlyfs","arm":"kubectl","run":1,"model":"sonnet","correct":true,"answer":"ROOT CAUSE: x","tool_calls":2,"tool_bytes":4000,"tool_tokens":1000,"input_tokens":30000,"output_tokens":200,"turns":3,"seconds":9.5,"cost_usd":0.02,"saw_canary":true,"canaries_seen":["private-key-block"],"peak_context_tokens":12000,"denied_tool_calls":0,"commands":["kubectl get pods -A"]}]`
	var rs []result
	if err := json.Unmarshal([]byte(old), &rs); err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || !rs[0].SawCanary || !reflect.DeepEqual(rs[0].Canaries, []string{"private-key-block"}) || rs[0].CanaryParts != nil {
		t.Fatalf("parsed %+v", rs)
	}
	if out := render(rs, "sonnet"); !strings.Contains(out, "| private-key-block |") {
		t.Fatalf("render does not name the canary:\n%s", out)
	}
}

func TestRenderNamesTheParts(t *testing.T) {
	rs := []result{
		{
			Scenario: "signer-exit0", Arm: "kubectl", Run: 1, SawCanary: true, Canaries: []string{"key-block"},
			CanaryParts: []partSeen{{ID: "key-block", Part: 1, Form: "plain"}, {ID: "key-block", Part: 4, Form: "plain"}},
		},
		{
			Scenario: "signer-exit0", Arm: "mcp", Run: 1, SawCanary: true, Canaries: []string{"key-block", "token"},
			CanaryParts: []partSeen{{ID: "key-block", Part: 4, Form: "plain"}},
		},
	}
	out := render(rs, "sonnet")
	for _, want := range []string{"| key-block (parts 1, 4) |", "| key-block (part 4), token |"} {
		if !strings.Contains(out, want) {
			t.Errorf("render does not contain %q:\n%s", want, out)
		}
	}
}

func TestCanaryEntryCheck(t *testing.T) {
	for _, c := range []canaryEntry{
		{ID: "none"},
		{ID: "both", Value: "KrayCanaryValue01234", Parts: []string{"KrayCanaryPart01234"}},
		{ID: "short", Parts: []string{"KrayCanaryPart01234", "short"}},
	} {
		if c.check() == nil {
			t.Errorf("%s: no error", c.ID)
		}
	}
	for _, c := range []canaryEntry{keyBlock, phrase, token} {
		if err := c.check(); err != nil {
			t.Errorf("%s: %v", c.ID, err)
		}
	}
}
