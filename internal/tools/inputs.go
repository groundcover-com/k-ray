package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/shape"
)

// Inputs. The json tags are the MCP argument names; the jsonschema tags are
// their descriptions. There is no argument for a context, kubeconfig,
// server, user or credential.
//
// The arguments that most tools share (kind, namespace, allNamespaces,
// labelSelector, full) have no description of their own: the server
// instructions say what they mean once, so that the text is not sent six times.

type ListInput struct {
	Kind          string `json:"kind"`
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"allNamespaces,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	Match         string `json:"match,omitempty" jsonschema:"words; a comma means any of them"`
	Limit         int    `json:"limit,omitempty" jsonschema:"default 200"`
	All           bool   `json:"all,omitempty" jsonschema:"also rows for healthy objects"`
	Full          bool   `json:"full,omitempty"`
}

type GetInput struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Full      bool   `json:"full,omitempty" jsonschema:"keep defaults"`
	JSONPath  string `json:"jsonpath,omitempty" jsonschema:"template such as {.spec.template.spec.containers[0].env}"`
}

type DescribeInput struct {
	Kind          string `json:"kind,omitempty"`
	Name          string `json:"name,omitempty" jsonschema:"alone, it is found in any namespace"`
	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty" jsonschema:"describes up to 3 matches"`
	AllNamespaces bool   `json:"allNamespaces,omitempty"`
}

type LogsInput struct {
	Pod           string `json:"pod,omitempty" jsonschema:"give one of pod, workload, labelSelector"`
	Workload      string `json:"workload,omitempty" jsonschema:"kind/name, such as deployment/web"`
	LabelSelector string `json:"labelSelector,omitempty"`
	AllNamespaces bool   `json:"allNamespaces,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Container     string `json:"container,omitempty"`
	Previous      bool   `json:"previous,omitempty" jsonschema:"the crashed instance"`
	Since         string `json:"since,omitempty" jsonschema:"such as 10m or 2h, at most 7d"`
	Tail          int    `json:"tail,omitempty" jsonschema:"lines per pod, default 2000"`
	Match         string `json:"match,omitempty" jsonschema:"words; a comma means any of them"`
	Full          bool   `json:"full,omitempty" jsonschema:"raw lines"`
}

type EventsInput struct {
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"allNamespaces,omitempty"`
	Kind          string `json:"kind,omitempty" jsonschema:"with name"`
	Name          string `json:"name,omitempty" jsonschema:"with kind"`
	Reason        string `json:"reason,omitempty" jsonschema:"such as BackOff"`
	Since         string `json:"since,omitempty" jsonschema:"such as 30m, at most 7d"`
	WarningsOnly  bool   `json:"warningsOnly,omitempty"`
	All           bool   `json:"all,omitempty" jsonschema:"also Normal events"`
	Match         string `json:"match,omitempty" jsonschema:"words; a comma means any of them"`
	Limit         int    `json:"limit,omitempty" jsonschema:"default 50"`
}

type TopInput struct {
	Resource      string `json:"resource,omitempty" jsonschema:"default pods"`
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"allNamespaces,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	SortBy        string `json:"sortBy,omitempty" jsonschema:"default cpu"`
}

const (
	maxSelector = 1024
	maxSince    = 7 * 24 * time.Hour
)

func (r *Runner) kind(s string) (kinds.Kind, error) {
	if strings.TrimSpace(s) == "" {
		return kinds.Kind{}, badInput("kind is required; allowed kinds: %s", strings.Join(r.Kinds.Names(), ", "))
	}
	k, err := r.Kinds.Lookup(s)
	if err != nil {
		return kinds.Kind{}, badInput("%v", err)
	}
	return k, nil
}

var reasonRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,62}$`)

func eventReason(s string) error {
	if s != "" && !reasonRE.MatchString(s) {
		return badInput("reason %q is not an event reason (a word such as Failed or BackOff)", clip(s))
	}
	return nil
}

func objectName(field, s string) error {
	if s == "" {
		return badInput("%s is required", field)
	}
	if errs := validation.IsDNS1123Subdomain(s); len(errs) > 0 {
		return badInput("%s %q is not a valid Kubernetes object name", field, clip(s))
	}
	return nil
}

func labelName(field, s string) error {
	if errs := validation.IsDNS1123Label(s); len(errs) > 0 {
		return badInput("%s %q is not a valid name", field, clip(s))
	}
	return nil
}

// namespaceFor returns "" for all namespaces or cluster-scoped kinds.
func (r *Runner) namespaceFor(k kinds.Kind, ns string, all bool) (string, error) {
	if !k.IsZero() && !k.Namespaced() {
		return "", nil
	}
	if all {
		return "", nil
	}
	if ns == "" {
		ns = r.Namespace
	}
	if ns == "" {
		ns = "default"
	}
	if err := labelName("namespace", ns); err != nil {
		return "", err
	}
	return ns, nil
}

func selector(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if len(s) > maxSelector {
		return "", badInput("labelSelector is longer than %d characters", maxSelector)
	}
	sel, err := labels.Parse(s)
	if err != nil {
		return "", badInput("labelSelector %q does not parse: %v", clip(s), err)
	}
	return sel.String(), nil
}

// duration parses Go durations and a day suffix ("1d"). Zero means none.
func duration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, badInput("%s %q is not a duration (use 10m, 2h or 1d)", field, clip(s))
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, badInput("%s %q is not a duration (use 10m, 2h or 1d)", field, clip(s))
		}
	}
	if d <= 0 || d > maxSince {
		return 0, badInput("%s must be between 1s and 7d", field)
	}
	return d, nil
}

func bounded(field string, v, def, lo, hi int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < lo || v > hi {
		return 0, badInput("%s must be between %d and %d", field, lo, hi)
	}
	return v, nil
}

func clip(s string) string {
	return shape.CutWords(s, 80)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
