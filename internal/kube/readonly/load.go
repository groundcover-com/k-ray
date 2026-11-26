package readonly

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/groundcover-com/kray/internal/brand"
)

// Options selects the kubeconfig and context. The person sets them when the
// process starts; no tool argument can change them.
type Options struct {
	// Kubeconfig is an explicit kubeconfig path. When empty, the init
	// kubeconfig is used if it exists, else the default loading rules
	// (KUBECONFIG, ~/.kube/config).
	Kubeconfig string
	// Context is the context name. When empty, the current context is used.
	Context string
	// InitKubeconfig is the path that `init` writes. Empty disables it.
	InitKubeconfig string
	// StdinUnavailable stops exec credential plugins from reading stdin. The
	// MCP server sets it, because stdin carries the protocol.
	StdinUnavailable bool
}

// Source says where the kubeconfig came from.
type Source string

const (
	SourceInit       Source = "init"
	SourceKubeconfig Source = "kubeconfig"
)

// Cluster is a loaded, read-only cluster configuration. Its rest.Config
// always has the read-only transport; there is no way to get it without.
type Cluster struct {
	cfg *rest.Config

	Context    string
	Namespace  string
	Server     string
	Source     Source
	Identity   string // set when Source is SourceInit
	ExecPlugin string // command of the exec credential plugin, if any
	Target     Target
}

// RESTConfig returns a copy of the read-only rest.Config.
func (c *Cluster) RESTConfig() *rest.Config { return rest.CopyConfig(c.cfg) }

// Load reads the kubeconfig and returns a Cluster whose every request passes
// Check. It is the only rest.Config builder for the server.
func Load(o Options) (*Cluster, error) {
	src, err := locate(o)
	if err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(src.config)

	u, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("API server URL: %w", err)
	}
	t, err := TargetFor(u)
	if err != nil {
		return nil, err
	}
	cfg.Wrap(Wrapper(t))
	cfg.UserAgent = brand.UserAgent()
	cfg.QPS = 10
	cfg.Burst = 20
	cfg.WarningHandler = rest.NoWarnings{}
	if cfg.ExecProvider != nil && o.StdinUnavailable {
		ep := *cfg.ExecProvider
		ep.StdinUnavailable = true
		ep.StdinUnavailableMessage = "stdin carries the MCP protocol"
		cfg.ExecProvider = &ep
	}

	return &Cluster{
		cfg:        cfg,
		Context:    src.Context,
		Namespace:  src.Namespace,
		Server:     u.String(),
		Source:     src.Source,
		Identity:   src.Identity,
		ExecPlugin: src.ExecPlugin,
		Target:     t,
	}, nil
}

// SetupSource is the raw configuration for `init` and `doctor`. Its
// rest.Config has no read-only transport. A guard test allows
// LoadForSetup only in internal/setup.
type SetupSource struct {
	config *rest.Config
	Raw    clientcmdapi.Config

	Context    string
	Namespace  string
	Source     Source
	Identity   string
	ExecPlugin string
}

// RESTConfig returns a copy of the raw rest.Config.
func (s *SetupSource) RESTConfig() *rest.Config { return rest.CopyConfig(s.config) }

// LoadForSetup returns the raw configuration that Load would wrap.
func LoadForSetup(o Options) (*SetupSource, error) { return locate(o) }

func locate(o Options) (*SetupSource, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	source := SourceKubeconfig
	switch {
	case o.Kubeconfig != "":
		rules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: o.Kubeconfig}
	case o.InitKubeconfig != "" && fileExists(o.InitKubeconfig):
		rules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: o.InitKubeconfig}
		source = SourceInit
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: o.Context}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	ctxName := o.Context
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}
	kctx, ok := raw.Contexts[ctxName]
	if !ok || kctx == nil {
		if ctxName == "" {
			return nil, errors.New("kubeconfig has no current context")
		}
		return nil, fmt.Errorf("kubeconfig has no context %q", ctxName)
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig context %q: %w", ctxName, err)
	}
	ns, _, err := cc.Namespace()
	if err != nil || ns == "" {
		ns = "default"
	}
	s := &SetupSource{
		config:    cfg,
		Raw:       raw,
		Context:   ctxName,
		Namespace: ns,
		Source:    source,
	}
	if ai := raw.AuthInfos[kctx.AuthInfo]; ai != nil && ai.Exec != nil {
		s.ExecPlugin = ai.Exec.Command
	}
	if source == SourceInit {
		s.Identity, _, _ = strings.Cut(kctx.AuthInfo, "@")
	}
	return s, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
