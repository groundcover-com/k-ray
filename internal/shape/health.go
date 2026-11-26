package shape

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// State is the health of one object.
type State string

const (
	Healthy   State = "healthy"
	Unhealthy State = "unhealthy"
	Warning   State = "warning"
	NoHealth  State = "-" // kinds without a health notion (ConfigMap, Service, ...)
)

// rank orders states for "problems first".
func (s State) rank() int {
	switch s {
	case Unhealthy:
		return 0
	case Warning:
		return 1
	case Healthy:
		return 2
	}
	return 3
}

// Row is one list line.
type Row struct {
	Namespace string
	Name      string
	State     State
	Cells     []string // kind-specific columns, see Columns
	// Problems are the problem texts, whole (white space joined, not cut).
	// They are cluster text: a consumer prints them through Lines, which
	// redacts each text before it cuts it.
	Problems []string
	caps     []int // the length to cut each problem to; 0 means DefaultProblemMax
}

// DefaultProblemMax is the length that a problem is cut to when its writer
// did not give another.
const DefaultProblemMax = 300

// Lines returns the problems as they are shown: each one passed through
// redact first, then cut to its length at a word. A cut after redaction cannot
// leave part of a secret (a quoted value with spaces, for example, loses its
// closing quote when it is cut, and no pattern matches it any more). redact
// may be nil for text that is not shown.
func (r Row) Lines(redact func(string) string) []string {
	out := make([]string, len(r.Problems))
	for i, p := range r.Problems {
		if redact != nil {
			p = redact(p)
		}
		n := DefaultProblemMax
		if i < len(r.caps) && r.caps[i] > 0 {
			n = r.caps[i]
		}
		out[i] = CutWords(p, n)
	}
	return out
}

// Columns returns the kind-specific column names for list output.
func Columns(kind string) []string {
	switch kind {
	case "Pod":
		return []string{"READY", "STATUS", "RESTARTS", "AGE", "NODE"}
	case "Deployment":
		return []string{"READY", "UP-TO-DATE", "AVAILABLE", "AGE"}
	case "StatefulSet":
		return []string{"READY", "AGE"}
	case "DaemonSet":
		return []string{"DESIRED", "READY", "AVAILABLE", "AGE"}
	case "ReplicaSet":
		return []string{"DESIRED", "READY", "AGE"}
	case "Job":
		return []string{"COMPLETIONS", "STATUS", "AGE"}
	case "CronJob":
		return []string{"SCHEDULE", "SUSPEND", "LAST-SCHEDULE", "AGE"}
	case "HorizontalPodAutoscaler":
		return []string{"TARGET", "MIN", "MAX", "CURRENT", "AGE"}
	case "PodDisruptionBudget":
		return []string{"MIN-AVAILABLE", "MAX-UNAVAILABLE", "ALLOWED", "AGE"}
	case "Service":
		return []string{"TYPE", "CLUSTER-IP", "PORTS", "AGE"}
	case "Ingress":
		return []string{"CLASS", "HOSTS", "AGE"}
	case "PersistentVolumeClaim":
		return []string{"STATUS", "VOLUME", "CAPACITY", "CLASS", "AGE"}
	case "PersistentVolume":
		return []string{"CAPACITY", "STATUS", "CLAIM", "CLASS", "AGE"}
	case "Node":
		return []string{"STATUS", "ROLES", "AGE", "VERSION"}
	case "Namespace":
		return []string{"STATUS", "AGE"}
	case "ConfigMap":
		return []string{"KEYS", "AGE"}
	case "Event":
		return []string{"TYPE", "REASON", "OBJECT", "AGE"}
	}
	return []string{"AGE"}
}

// Inspect builds the list row for an object: its state, cells and problems.
func Inspect(u *unstructured.Unstructured, now time.Time) Row {
	r := Row{Namespace: u.GetNamespace(), Name: u.GetName(), State: NoHealth}
	age := Age(u.GetCreationTimestamp().Time, now)
	if f, ok := inspectKinds[u.GetKind()]; ok {
		f(&r, u, now, age)
	} else {
		r.Cells = []string{age}
	}
	inspectSignals(&r, u, now)
	return r
}

// inspectSignals adds what any kind says in its conditions and status: the
// same rule for a Deployment, a custom resource or a claim.
func inspectSignals(r *Row, u *unstructured.Unstructured, now time.Time) {
	if r.State == NoHealth && HasConditions(u) {
		r.State = Healthy
	}
	for _, s := range Signals(u, now) {
		r.Problems = append(r.Problems, s.Text)
		r.caps = append(r.caps, s.Max)
		if s.State.rank() < r.State.rank() {
			r.State = s.State
		}
	}
}

// AddProblem adds a whole problem text and raises the state if needed. The
// text is not a format, so a percent sign in cluster text stays as it is.
// Code outside this package uses it too, for example for a resource verdict
// from the metrics API.
func (r *Row) AddProblem(s State, text string) { r.problem(s, "%s", text) }

func (r *Row) problem(s State, format string, args ...any) {
	r.Problems = append(r.Problems, oneLine(fmt.Sprintf(format, args...)))
	r.caps = append(r.caps, 0)
	if s.rank() < r.State.rank() {
		r.State = s
	}
}

// A rolling update takes pods away on purpose. A workload whose missing pods
// are within the budget of its update is rolling out, which is a warning to
// look at, not a failure. The budget is the one that Kubernetes uses: a
// Deployment rounds a percentage of maxUnavailable down, a StatefulSet and a
// DaemonSet round it up, with at least 1.

// budget resolves a maxUnavailable value against total.
func budget(v *intstr.IntOrString, def intstr.IntOrString, total int, roundUp bool) int {
	if v == nil {
		v = &def
	}
	n, err := intstr.GetScaledValueFromIntOrPercent(v, total, roundUp)
	if err != nil || n < 0 {
		return 0
	}
	if roundUp && n == 0 && v.Type == intstr.String {
		return 1
	}
	return n
}

// deploymentRollingInBudget: Progressing is True and the rollout is not
// finished (not NewReplicaSetAvailable) nor past its deadline, the strategy is
// a rolling update, and the available replicas are at least the replicas less
// maxUnavailable (25 percent by default, rounded down).
func deploymentRollingInBudget(d *appsv1.Deployment, want int32) bool {
	if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return false
	}
	progressing := false
	for _, c := range d.Status.Conditions {
		if c.Type != appsv1.DeploymentProgressing {
			continue
		}
		if c.Reason == "ProgressDeadlineExceeded" || c.Status != corev1.ConditionTrue {
			return false
		}
		progressing = c.Reason != "NewReplicaSetAvailable"
	}
	if !progressing {
		return false
	}
	var mu *intstr.IntOrString
	if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
		mu = ru.MaxUnavailable
	}
	n := budget(mu, intstr.FromString("25%"), int(want), false)
	return int(d.Status.AvailableReplicas) >= int(want)-n
}

// statefulSetRollingInBudget: an update is in progress (fewer updated pods than
// the partition leaves to update) and the missing pods are within
// maxUnavailable (1 by default).
func statefulSetRollingInBudget(s *appsv1.StatefulSet, want int32) bool {
	if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
		return false
	}
	target := int(want)
	var mu *intstr.IntOrString
	if ru := s.Spec.UpdateStrategy.RollingUpdate; ru != nil {
		mu = ru.MaxUnavailable
		if ru.Partition != nil {
			target -= int(*ru.Partition)
		}
	}
	// An update is in progress when the revision to update to differs from the
	// current one, or some pods are updated and not all that should be.
	updating := s.Status.UpdatedReplicas > 0
	if s.Status.CurrentRevision != "" && s.Status.UpdateRevision != "" {
		updating = s.Status.CurrentRevision != s.Status.UpdateRevision
	}
	if !updating || int(s.Status.UpdatedReplicas) >= target {
		return false // no update in progress
	}
	return int(s.Status.ReadyReplicas) >= int(want)-budget(mu, intstr.FromInt32(1), int(want), true)
}

// daemonSetRollingInBudget: an update is in progress (updated pods fewer than
// desired) and the missing pods are within maxUnavailable (1 by default).
func daemonSetRollingInBudget(d *appsv1.DaemonSet) bool {
	if d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
		return false
	}
	desired := int(d.Status.DesiredNumberScheduled)
	if int(d.Status.UpdatedNumberScheduled) >= desired {
		return false
	}
	var mu *intstr.IntOrString
	if ru := d.Spec.UpdateStrategy.RollingUpdate; ru != nil {
		mu = ru.MaxUnavailable
	}
	return int(d.Status.NumberReady) >= desired-budget(mu, intstr.FromInt32(1), desired, true)
}

// budgetForbidsByItself reports whether a budget that allows no disruption
// does so by its own setting: maxUnavailable is 0, or minAvailable is as many
// as the pods it expects. That is what the owner wrote, not a state of the
// pods. A budget that would allow a disruption if every pod were healthy is
// not this: a pod that is not healthy is the reason.
func budgetForbidsByItself(p *policyv1.PodDisruptionBudget) bool {
	expected := int(p.Status.ExpectedPods)
	if v := p.Spec.MaxUnavailable; v != nil {
		if n, err := intstr.GetScaledValueFromIntOrPercent(v, expected, true); err == nil && n == 0 {
			return true
		}
	}
	if v := p.Spec.MinAvailable; v != nil {
		if n, err := intstr.GetScaledValueFromIntOrPercent(v, expected, true); err == nil && n >= expected {
			return true
		}
	}
	return false
}

// BudgetForbidsByItself is budgetForbidsByItself for an object read from the
// cluster: a PodDisruptionBudget that allows no disruption, by its own setting.
func BudgetForbidsByItself(u *unstructured.Unstructured) bool {
	if u.GetKind() != "PodDisruptionBudget" {
		return false
	}
	var p policyv1.PodDisruptionBudget
	if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p) != nil {
		return false
	}
	return p.Status.DisruptionsAllowed == 0 && p.Status.ExpectedPods > 0 && budgetForbidsByItself(&p)
}

func replicas(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// oneLine joins white space. It does not cut: a cut comes after redaction
// (see Row.Lines).
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// MaxProblems is how many problem lines a result shows for one object.
const MaxProblems = 10

// ProblemLines renders at most MaxProblems lines and says how many more.
func ProblemLines(problems []string) []string {
	if len(problems) <= MaxProblems {
		return problems
	}
	out := append([]string(nil), problems[:MaxProblems]...)
	return append(out, fmt.Sprintf("(%d more problems)", len(problems)-MaxProblems))
}

// Age formats a duration like kubectl: 37s, 5m, 2h, 3d.
func Age(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// SortRows puts problems first, then by namespace and name.
func SortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rows[i].State.rank(), rows[j].State.rank(); a != b {
			return a < b
		}
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
}
