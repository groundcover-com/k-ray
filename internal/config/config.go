// Package config loads the person's config file. The agent cannot change any
// of it: there is no tool argument for these values.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"sigs.k8s.io/yaml"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kinds"
)

const (
	DefaultBudget     = 16 << 10  // bytes of text per result (about 4k tokens)
	DefaultFullBudget = 256 << 10 // bytes when the agent asks for full=true
	MinBudget         = 2 << 10
	MaxBudget         = 1 << 20
)

// Config is the config file. Every field is optional.
type Config struct {
	// BudgetBytes caps each result. Default 16 KiB.
	BudgetBytes int `json:"budgetBytes,omitempty"`
	// FullBudgetBytes caps a result with full=true. Default 256 KiB.
	FullBudgetBytes int `json:"fullBudgetBytes,omitempty"`
	// AuditLog is the audit file path. Default: StateDir()/audit.jsonl.
	AuditLog string `json:"auditLog,omitempty"`
	// CustomResources are opt-in custom resources.
	CustomResources []kinds.CustomResource `json:"customResources,omitempty"`
	// Redaction adds sensitive key names to the built-in list.
	Redaction Redaction `json:"redaction,omitempty"`
}

// Redaction holds redaction settings.
type Redaction struct {
	ExtraKeyNames []string `json:"extraKeyNames,omitempty"`
}

var keyNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{2,64}$`)

// Default returns the defaults.
func Default() Config {
	return Config{BudgetBytes: DefaultBudget, FullBudgetBytes: DefaultFullBudget, AuditLog: DefaultAuditPath()}
}

// Load reads path, or the default path when path is empty. A missing file at
// the default path gives the defaults; a missing explicit file is an error.
func Load(path string) (Config, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	c := Default()
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		return c, nil
	case err != nil:
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var f Config
	if err := yaml.UnmarshalStrict(b, &f); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if f.BudgetBytes != 0 {
		c.BudgetBytes = f.BudgetBytes
	}
	if f.FullBudgetBytes != 0 {
		c.FullBudgetBytes = f.FullBudgetBytes
	}
	if f.AuditLog != "" {
		c.AuditLog = f.AuditLog
	}
	c.CustomResources = f.CustomResources
	c.Redaction = f.Redaction
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Validate checks the values.
func (c Config) Validate() error {
	if c.BudgetBytes < MinBudget || c.BudgetBytes > MaxBudget {
		return fmt.Errorf("budgetBytes must be between %d and %d", MinBudget, MaxBudget)
	}
	if c.FullBudgetBytes < c.BudgetBytes || c.FullBudgetBytes > MaxBudget {
		return fmt.Errorf("fullBudgetBytes must be between budgetBytes and %d", MaxBudget)
	}
	for _, cr := range c.CustomResources {
		if err := cr.Validate(); err != nil {
			return err
		}
	}
	for _, k := range c.Redaction.ExtraKeyNames {
		if !keyNameRE.MatchString(k) {
			return fmt.Errorf("redaction.extraKeyNames: %q is not a key name", k)
		}
	}
	return nil
}

// Dir is the config directory: $XDG_CONFIG_HOME/<name>, else ~/.config/<name>
// (on Windows, %AppData%\<name>).
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, brand.Name)
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, brand.Name)
		}
	}
	return filepath.Join(home(), ".config", brand.Name)
}

// StateDir is the state directory for the audit log: $XDG_STATE_HOME/<name>,
// else ~/.local/state/<name> (on Windows, %LocalAppData%\<name>).
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, brand.Name)
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, brand.Name)
		}
	}
	return filepath.Join(home(), ".local", "state", brand.Name)
}

// DefaultPath is the config file path.
func DefaultPath() string { return filepath.Join(Dir(), "config.yaml") }

// InitKubeconfigPath is where `init` writes the read-only kubeconfig.
func InitKubeconfigPath() string { return filepath.Join(Dir(), "kubeconfig") }

// DefaultAuditPath is the audit log path.
func DefaultAuditPath() string { return filepath.Join(StateDir(), "audit.jsonl") }

func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}
