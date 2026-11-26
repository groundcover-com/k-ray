package setup

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

const (
	minTokenLife = 10 * time.Minute // the API server minimum
	maxTokenLife = 24 * time.Hour
)

// InitOptions are the init flags.
type InitOptions struct {
	Kubeconfig string // the person's kubeconfig
	Context    string // the person's context
	DryRun     bool
	Yes        bool
	Refresh    bool
	TokenStdin bool
	Duration   time.Duration
	Kinds      *kinds.Set
	Target     string // where to write the read-only kubeconfig
	Out, Err   io.Writer
	In         io.Reader
}

// Init creates the read-only identity and writes a kubeconfig with a
// short-lived token for it.
func Init(ctx context.Context, o InitOptions) error {
	if o.Duration < minTokenLife || o.Duration > maxTokenLife {
		return fmt.Errorf("--duration must be between %s and %s", minTokenLife, maxTokenLife)
	}
	if o.DryRun && (o.Refresh || o.TokenStdin) {
		return errors.New("--dry-run cannot be combined with --refresh or --token-stdin")
	}
	if o.Kinds == nil {
		o.Kinds = kinds.Default()
	}
	// A dry run needs no cluster access: a security team can review and
	// apply the manifests with its own tools.
	if o.DryRun {
		fmt.Fprintf(o.Out, "These objects create an identity that can only get and list the allowed kinds:\n\n%s\n", ManifestYAML(o.Kinds))
		fmt.Fprintf(o.Out, "Dry run: nothing was applied.\nTo apply them yourself: save the manifests above, run `kubectl apply -f <file>`, then\n  kubectl create token %s -n %s --duration %s | %s init --token-stdin\n",
			ServiceAccount(), Namespace(), o.Duration, brand.Name)
		return nil
	}
	// The person's kubeconfig, never the read-only one.
	src, err := readonly.LoadForSetup(readonly.Options{Kubeconfig: o.Kubeconfig, Context: o.Context})
	if err != nil {
		return err
	}
	cfg := src.RESTConfig()
	cfg.UserAgent = brand.UserAgent() + " (init)"
	cfg.ContentType = "application/json"

	if o.TokenStdin {
		tok, err := readToken(o.In)
		if err != nil {
			return err
		}
		exp, _ := jwtExpiry(tok)
		return writeKubeconfig(o, src, cfg, tok, exp)
	}

	if !o.Refresh {
		fmt.Fprintf(o.Out, "These objects create an identity that can only get and list the allowed kinds:\n\n%s\n", ManifestYAML(o.Kinds))
		if !o.Yes {
			fmt.Fprintf(o.Out, "Apply these objects to context %q (%s) with your current credentials? [y/N] ", src.Context, cfg.Host)
			line, _ := bufio.NewReader(o.In).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
				return errors.New("nothing applied")
			}
		}
		if err := apply(ctx, cfg, o.Kinds); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "Applied namespace %s, service account %s, cluster role and binding %s.\n", Namespace(), ServiceAccount(), RoleName())
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	secs := int64(o.Duration.Seconds())
	tr, err := cs.CoreV1().ServiceAccounts(Namespace()).CreateToken(ctx, ServiceAccount(),
		&authv1.TokenRequest{Spec: authv1.TokenRequestSpec{ExpirationSeconds: &secs}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("request a token for %s/%s: %w", Namespace(), ServiceAccount(), err)
	}
	exp := tr.Status.ExpirationTimestamp.Time
	if !exp.IsZero() && time.Until(exp) < o.Duration-time.Minute {
		fmt.Fprintf(o.Out, "Note: the API server shortened the token to %s (its maximum token lifetime).\n", time.Until(exp).Round(time.Minute))
	}
	return writeKubeconfig(o, src, cfg, tr.Status.Token, exp)
}

func readToken(in io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(in, 64<<10))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") {
		return "", errors.New("stdin must hold one token (for example from `kubectl create token`)")
	}
	return tok, nil
}

var applyOrder = []schema.GroupVersionResource{
	{Version: "v1", Resource: "namespaces"},
	{Version: "v1", Resource: "serviceaccounts"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
}

func apply(ctx context.Context, cfg *rest.Config, ks *kinds.Set) error {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	for i, obj := range Objects(ks) {
		u := &unstructured.Unstructured{Object: obj}
		r := dyn.Resource(applyOrder[i])
		var ri dynamic.ResourceInterface = r
		if u.GetNamespace() != "" {
			ri = r.Namespace(u.GetNamespace())
		}
		if _, err := ri.Apply(ctx, u.GetName(), u, metav1.ApplyOptions{FieldManager: brand.Name + "-init", Force: true}); err != nil {
			return fmt.Errorf("apply %s %s: %w", u.GetKind(), u.GetName(), err)
		}
	}
	return nil
}

// writeKubeconfig adds (or replaces) one context in the read-only
// kubeconfig: the cluster's server and CA, the token, no exec plugin.
func writeKubeconfig(o InitOptions, src *readonly.SetupSource, cfg *rest.Config, token string, exp time.Time) error {
	if o.Target == "" {
		return errors.New("no target path for the kubeconfig")
	}
	kc := clientcmdapi.NewConfig()
	if existing, err := clientcmd.LoadFromFile(o.Target); err == nil {
		kc = existing
	}
	name := brand.Name + "@" + src.Context
	user := Identity() + "@" + src.Context

	cluster := clientcmdapi.NewCluster()
	cluster.Server = cfg.Host
	if cfg.APIPath != "" && cfg.APIPath != "/api" {
		cluster.Server = strings.TrimRight(cfg.Host, "/")
	}
	cluster.CertificateAuthorityData = cfg.CAData
	if len(cluster.CertificateAuthorityData) == 0 && cfg.CAFile != "" {
		b, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return fmt.Errorf("read the cluster CA: %w", err)
		}
		cluster.CertificateAuthorityData = b
	}
	cluster.InsecureSkipTLSVerify = cfg.Insecure
	cluster.TLSServerName = cfg.ServerName
	if kctx := src.Raw.Contexts[src.Context]; kctx != nil {
		if c := src.Raw.Clusters[kctx.Cluster]; c != nil {
			cluster.ProxyURL = c.ProxyURL
		}
	}

	auth := clientcmdapi.NewAuthInfo()
	auth.Token = token

	kctx := clientcmdapi.NewContext()
	kctx.Cluster, kctx.AuthInfo, kctx.Namespace = name, user, src.Namespace

	kc.Clusters[name] = cluster
	kc.AuthInfos[user] = auth
	kc.Contexts[name] = kctx
	kc.CurrentContext = name

	if err := os.MkdirAll(filepath.Dir(o.Target), 0o700); err != nil {
		return err
	}
	if err := clientcmd.WriteToFile(*kc, o.Target); err != nil {
		return err
	}
	if err := os.Chmod(o.Target, 0o600); err != nil {
		return err
	}
	when := "unknown"
	if !exp.IsZero() {
		when = fmt.Sprintf("%s (in %s)", exp.UTC().Format(time.RFC3339), time.Until(exp).Round(time.Minute))
	}
	fmt.Fprintf(o.Out, "Wrote %s\n  context:  %s\n  identity: %s\n  expires:  %s\n%s serve now uses this identity. Refresh with `%s init --refresh`.\n",
		o.Target, name, Identity(), when, brand.Name, brand.Name)
	return nil
}

// jwtExpiry reads the exp claim of a service account token without
// verifying it. It is for display only.
func jwtExpiry(tok string) (time.Time, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(b, &c); err != nil || c.Exp == 0 {
		return time.Time{}, errors.New("no exp claim")
	}
	return time.Unix(c.Exp, 0), nil
}
