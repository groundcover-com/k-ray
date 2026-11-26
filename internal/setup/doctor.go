package setup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

// DoctorOptions select the same kubeconfig as serve.
type DoctorOptions struct {
	Kubeconfig     string
	Context        string
	InitKubeconfig string
	Out            io.Writer
}

// reviewPaths are the only POST targets doctor may use. These review APIs
// create nothing; they answer a question about the caller.
var reviewPaths = map[string]bool{
	"/apis/authentication.k8s.io/v1/selfsubjectreviews":      true,
	"/apis/authorization.k8s.io/v1/selfsubjectaccessreviews": true,
	"/apis/authorization.k8s.io/v1/selfsubjectrulesreviews":  true,
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// reviewWrapper allows GET under the read-only rules and POST to the three
// review endpoints on the configured host. Everything else is blocked.
func reviewWrapper(t readonly.Target) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return rtFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost && strings.EqualFold(req.URL.Scheme, t.Scheme) && strings.EqualFold(req.URL.Host, t.Host) &&
				req.URL.RawPath == "" && reviewPaths[strings.TrimPrefix(req.URL.Path, t.Prefix)] && onlyTimeout(req.URL.RawQuery) &&
				req.Header.Get("Upgrade") == "" {
				return next.RoundTrip(req)
			}
			if err := readonly.Check(req, t); err != nil {
				if req.Body != nil {
					_ = req.Body.Close()
				}
				return nil, err
			}
			return next.RoundTrip(req)
		})
	}
}

func onlyTimeout(raw string) bool {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	for k := range q {
		if k != "timeout" {
			return false
		}
	}
	return true
}

type check struct {
	verb, group, resource, sub string
}

func (c check) String() string {
	r := c.resource
	if c.group != "" {
		r += "." + c.group
	}
	if c.sub != "" {
		r += "/" + c.sub
	}
	return c.verb + " " + r
}

// dangerous permissions: any of these means the identity can do more than
// read the allowed kinds.
var dangerous = []check{
	{"*", "*", "*", ""},
	{"get", "", "secrets", ""},
	{"list", "", "secrets", ""},
	{"watch", "", "secrets", ""},
	{"create", "", "pods", ""},
	{"delete", "", "pods", ""},
	{"update", "", "configmaps", ""},
	{"patch", "apps", "deployments", ""},
	{"delete", "apps", "deployments", ""},
	{"create", "", "pods", "exec"},
	{"get", "", "pods", "exec"},
	{"create", "", "pods", "attach"},
	{"create", "", "pods", "portforward"},
	{"get", "", "pods", "proxy"},
	{"get", "", "nodes", "proxy"},
	{"get", "", "services", "proxy"},
	{"create", "", "serviceaccounts", "token"},
	{"impersonate", "", "users", ""},
	{"impersonate", "", "serviceaccounts", ""},
	{"impersonate", "", "groups", ""},
	{"escalate", "rbac.authorization.k8s.io", "clusterroles", ""},
	{"bind", "rbac.authorization.k8s.io", "clusterroles", ""},
	{"create", "rbac.authorization.k8s.io", "clusterrolebindings", ""},
}

// Doctor reports the identity that serve uses and warns when it can do more
// than read. It returns true when a dangerous permission was found.
func Doctor(ctx context.Context, o DoctorOptions) (bool, error) {
	src, err := readonly.LoadForSetup(readonly.Options{Kubeconfig: o.Kubeconfig, Context: o.Context, InitKubeconfig: o.InitKubeconfig})
	if err != nil {
		return false, err
	}
	cfg := src.RESTConfig()
	u, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return false, err
	}
	t, err := readonly.TargetFor(u)
	if err != nil {
		return false, err
	}
	cfg.Wrap(reviewWrapper(t))
	cfg.UserAgent = brand.UserAgent() + " (doctor)"
	cfg.ContentType = "application/json"
	cfg.QPS, cfg.Burst = 50, 100
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false, err
	}
	out := o.Out
	fmt.Fprintf(out, "context:   %s (%s)\nserver:    %s\n", src.Context, sourceText(src.Source), u.String())

	ssr, err := cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("SelfSubjectReview: %w", err)
	}
	fmt.Fprintf(out, "identity:  %s\ngroups:    %s\n", ssr.Status.UserInfo.Username, strings.Join(ssr.Status.UserInfo.Groups, ", "))
	if src.ExecPlugin != "" {
		fmt.Fprintf(out, "credential plugin: %s (runs from your kubeconfig)\n", src.ExecPlugin)
	}
	if src.Source == readonly.SourceInit {
		if exp, err := jwtExpiry(cfg.BearerToken); err == nil {
			left := time.Until(exp).Round(time.Minute)
			if left <= 0 {
				fmt.Fprintf(out, "token:     EXPIRED at %s; run `%s init --refresh`\n", exp.UTC().Format(time.RFC3339), brand.Name)
			} else {
				fmt.Fprintf(out, "token:     expires %s (in %s)\n", exp.UTC().Format(time.RFC3339), left)
			}
		}
	}

	namespaces := []string{"", "default", "kube-system"}
	if src.Namespace != "" && src.Namespace != "default" && src.Namespace != "kube-system" {
		namespaces = append(namespaces, src.Namespace)
	}
	var found []string
	for _, c := range dangerous {
		for _, ns := range namespaces {
			review := &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authzv1.ResourceAttributes{
				Namespace: ns, Verb: c.verb, Group: c.group, Resource: c.resource, Subresource: c.sub,
			}}}
			res, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
			if err != nil {
				return false, fmt.Errorf("SelfSubjectAccessReview: %w", err)
			}
			if res.Status.Allowed {
				where := "in all namespaces"
				if ns != "" {
					where = "in namespace " + ns
				}
				found = append(found, c.String()+" "+where)
				break
			}
		}
	}

	ns := src.Namespace
	if ns == "" {
		ns = "default"
	}
	if rr, err := cs.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, &authzv1.SelfSubjectRulesReview{Spec: authzv1.SelfSubjectRulesReviewSpec{Namespace: ns}}, metav1.CreateOptions{}); err == nil {
		note := ""
		if rr.Status.Incomplete {
			note = " (incomplete: an authorizer cannot list its rules)"
		}
		fmt.Fprintf(out, "rules:     %d resource rules in namespace %s%s\n", len(rr.Status.ResourceRules), ns, note)
	}

	if groups, err := cs.Discovery().ServerGroups(); err == nil {
		metrics := "not available (top will say so)"
		for _, g := range groups.Groups {
			if g.Name == "metrics.k8s.io" {
				metrics = "available"
			}
		}
		fmt.Fprintf(out, "metrics:   %s\n", metrics)
	}

	if len(found) == 0 {
		fmt.Fprintln(out, "result:    OK: no permission beyond reading was found in the checks")
		return false, nil
	}
	fmt.Fprintf(out, "result:    WARNING: this identity can do more than read:\n")
	for _, f := range found {
		fmt.Fprintf(out, "  - can %s\n", f)
	}
	fmt.Fprintf(out, "The tools stay read-only either way, but a narrower identity limits the damage of a bug or a leaked token. Run `%s init`.\n", brand.Name)
	return true, nil
}

func sourceText(s readonly.Source) string {
	if s == readonly.SourceInit {
		return "read-only kubeconfig from init"
	}
	return "your kubeconfig"
}
