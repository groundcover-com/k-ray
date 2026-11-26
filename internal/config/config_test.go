package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.BudgetBytes != DefaultBudget || c.FullBudgetBytes != DefaultFullBudget || !strings.HasSuffix(c.AuditLog, "audit.jsonl") {
		t.Fatalf("defaults: %+v", c)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("explicit missing file must be an error")
	}
}

func TestLoadValues(t *testing.T) {
	c, err := Load(write(t, `
budgetBytes: 8192
fullBudgetBytes: 65536
auditLog: /tmp/a.jsonl
customResources:
  - {group: argoproj.io, version: v1alpha1, resource: rollouts, kind: Rollout, namespaced: true}
redaction:
  extraKeyNames: [license_key]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.BudgetBytes != 8192 || c.FullBudgetBytes != 65536 || c.AuditLog != "/tmp/a.jsonl" || len(c.CustomResources) != 1 || c.Redaction.ExtraKeyNames[0] != "license_key" {
		t.Fatalf("values: %+v", c)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":       "budget: 1\n",
		"small budget":      "budgetBytes: 10\n",
		"huge budget":       "budgetBytes: 99999999\n",
		"full below budget": "budgetBytes: 8192\nfullBudgetBytes: 4096\n",
		"core group":        "customResources: [{group: '', version: v1, resource: secrets, kind: Secret}]\n",
		"bad key name":      "redaction: {extraKeyNames: ['a b']}\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPathsUseXDG(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	if !strings.HasPrefix(InitKubeconfigPath(), cfg) || !strings.HasPrefix(DefaultAuditPath(), state) {
		t.Fatalf("paths: %s %s", InitKubeconfigPath(), DefaultAuditPath())
	}
}
