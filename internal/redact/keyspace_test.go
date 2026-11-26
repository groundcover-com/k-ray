package redact

import (
	"strings"
	"testing"
)

// Item 5: a key name with spaces is still a key name.
func TestQuotedKeyWithSpacesIsSensitive(t *testing.T) {
	for _, in := range []string{
		`{"msg":"login","api key":"SECRETVALUE1234"}`,
		`{"msg":"login","API  Key":"SECRETVALUE1234","x":1}`,
		`{"client secret": "SECRETVALUE1234"}`,
		`{"db password":"SECRET VALUE 1234 and more"}`,
		`{'api key':'SECRETVALUE1234'}`,
		`login "api key": SECRETVALUE1234 done`,
	} {
		out := New(nil).Text(in)
		if strings.Contains(out, "SECRETVALUE") || strings.Contains(out, "SECRET VALUE") || strings.Contains(out, "and more") {
			t.Errorf("not redacted: %s -> %s", in, out)
		}
		if !strings.Contains(out, Mark(RuleKeyName)) {
			t.Errorf("no key-name mark: %s -> %s", in, out)
		}
	}
}

// A two-word sensitive name with no quotes, directly before = or :.
func TestUnquotedTwoWordKeyIsSensitive(t *testing.T) {
	names := []string{
		"api key", "access key", "secret key", "private key", "auth token", "access token", "client secret",
		"API Key", "Api  Key", "ACCESS\tKEY",
	}
	for _, name := range names {
		for _, form := range []string{
			name + "=SECRETVALUE1234",
			name + " = SECRETVALUE1234",
			name + ": SECRETVALUE1234",
			name + `="SECRET VALUE 1234 and more"`,
			name + `: "SECRET VALUE 1234 and more"`,
			name + `='SECRET VALUE 1234 and more'`,
			"level=info msg=login " + name + "=SECRETVALUE1234 user=bob",
		} {
			out := New(nil).Text(form)
			if strings.Contains(out, "SECRETVALUE") || strings.Contains(out, "SECRET VALUE") || strings.Contains(out, "and more") {
				t.Errorf("not redacted: %s -> %s", form, out)
			}
			if !strings.Contains(out, Mark(RuleKeyName)) {
				t.Errorf("no key-name mark: %s -> %s", form, out)
			}
			if !strings.Contains(out, name) {
				t.Errorf("the key name is lost: %s -> %s", form, out)
			}
		}
	}
	// The rest of the line stays.
	if out := New(nil).Text("api key=SECRETVALUE1234 user=bob"); !strings.HasSuffix(out, " user=bob") {
		t.Errorf("the rest of the line is lost: %s", out)
	}
	// One value counts once.
	r := New(nil)
	r.Text(`client secret: "SECRET VALUE"`)
	if n := r.Counts().Total(); n != 1 {
		t.Errorf("counted %d redactions, want 1", n)
	}
}

func TestKeyWithSpacesThatIsNotSecretIsKept(t *testing.T) {
	for _, in := range []string{
		`{"msg":"x","user name":"alice smith"}`,
		`{"monkey business":"1"}`,
		`monkey business=1`,
		`turkey count: 3`,
		`{"turkey count":"3"}`,
		`{"cache key":"a b c"}`,
		// Database and data-model terms are not secrets.
		`the key: value pairs`,
		`primary key: id`,
		`primary key=id`,
		`partition key=user_id`,
		`sort key: created_at`,
		`foreign key: account_id`,
		`cache key=user:42`,
		`public key: ssh-ed25519`,
		`key: value`,
		`snapi key=1`,
	} {
		r := New(nil)
		if out := r.Text(in); out != in {
			t.Errorf("changed: %s -> %s", in, out)
		}
	}
}
