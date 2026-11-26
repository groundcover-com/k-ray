package shape

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// inspectKindFunc fills the cells, state and problems of one kind. age is
// passed in because every kind shows it and it is the same for all of them.
type inspectKindFunc func(r *Row, u *unstructured.Unstructured, now time.Time, age string)

// inspectKinds is the table that Inspect dispatches on. A kind that is not
// here shows its age only.
var inspectKinds = map[string]inspectKindFunc{
	"Pod":                     inspectPod,
	"Deployment":              inspectDeployment,
	"StatefulSet":             inspectStatefulSet,
	"DaemonSet":               inspectDaemonSet,
	"ReplicaSet":              inspectReplicaSet,
	"Job":                     inspectJob,
	"CronJob":                 inspectCronJob,
	"HorizontalPodAutoscaler": inspectHPA,
	"PodDisruptionBudget":     inspectPDB,
	"Service":                 inspectService,
	"Ingress":                 inspectIngress,
	"PersistentVolumeClaim":   inspectPVC,
	"PersistentVolume":        inspectPV,
	"Node":                    inspectNode,
	"Namespace":               inspectNamespace,
	"ConfigMap":               inspectConfigMap,
	"Event":                   inspectEvent,
}

// inspectConvert converts into a typed object. When it fails, the kind adds
// nothing to the row; the conditions and signals that Inspect reads next still
// apply, because they need no typed object.
func inspectConvert(u *unstructured.Unstructured, into any) bool {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into) == nil
}

func inspectPod(r *Row, u *unstructured.Unstructured, now time.Time, _ string) {
	var p corev1.Pod
	if inspectConvert(u, &p) {
		podRow(r, &p, now)
	}
}

func inspectNode(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var n corev1.Node
	if inspectConvert(u, &n) {
		nodeRow(r, &n, age)
	}
}

func inspectDeployment(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var d appsv1.Deployment
	if !inspectConvert(u, &d) {
		return
	}
	want := replicas(d.Spec.Replicas)
	r.Cells = []string{fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, want), fmt.Sprint(d.Status.UpdatedReplicas), fmt.Sprint(d.Status.AvailableReplicas), age}
	r.State = Healthy
	if d.Status.AvailableReplicas < want {
		if deploymentRollingInBudget(&d, want) {
			r.AddProblem(Warning, inspectTextRollingOut(d.Status.AvailableReplicas, want, "available"))
		} else {
			r.AddProblem(Unhealthy, inspectTextTooFew(d.Status.AvailableReplicas, want, "replicas available"))
		}
	}
	for _, c := range d.Status.Conditions {
		switch {
		case c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse:
			r.AddProblem(Unhealthy, inspectTextNotProgressing(c.Reason, c.Message))
		case c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue:
			r.AddProblem(Unhealthy, inspectTextReplicaFailure(c.Reason, c.Message))
		}
	}
	if d.Spec.Paused {
		r.AddProblem(Warning, inspectTextRolloutPaused())
	}
}

func inspectStatefulSet(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var s appsv1.StatefulSet
	if !inspectConvert(u, &s) {
		return
	}
	want := replicas(s.Spec.Replicas)
	r.Cells = []string{fmt.Sprintf("%d/%d", s.Status.ReadyReplicas, want), age}
	r.State = Healthy
	if s.Status.ReadyReplicas < want {
		if statefulSetRollingInBudget(&s, want) {
			r.AddProblem(Warning, inspectTextRollingOut(s.Status.ReadyReplicas, want, "ready"))
		} else {
			r.AddProblem(Unhealthy, inspectTextTooFew(s.Status.ReadyReplicas, want, "replicas ready"))
		}
	}
}

func inspectDaemonSet(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var d appsv1.DaemonSet
	if !inspectConvert(u, &d) {
		return
	}
	st := d.Status
	r.Cells = []string{fmt.Sprint(st.DesiredNumberScheduled), fmt.Sprint(st.NumberReady), fmt.Sprint(st.NumberAvailable), age}
	r.State = Healthy
	if st.NumberUnavailable > 0 || st.NumberReady < st.DesiredNumberScheduled {
		if daemonSetRollingInBudget(&d) {
			r.AddProblem(Warning, inspectTextRollingOut(st.NumberReady, st.DesiredNumberScheduled, "ready"))
		} else {
			r.AddProblem(Unhealthy, inspectTextTooFew(st.NumberReady, st.DesiredNumberScheduled, "pods ready"))
		}
	}
	if st.NumberMisscheduled > 0 {
		r.AddProblem(Warning, inspectTextMisscheduled(st.NumberMisscheduled))
	}
}

func inspectReplicaSet(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var s appsv1.ReplicaSet
	if !inspectConvert(u, &s) {
		return
	}
	want := replicas(s.Spec.Replicas)
	r.Cells = []string{fmt.Sprint(want), fmt.Sprint(s.Status.ReadyReplicas), age}
	r.State = Healthy
	if want > 0 && s.Status.ReadyReplicas < want {
		r.AddProblem(Unhealthy, inspectTextTooFew(s.Status.ReadyReplicas, want, "replicas ready"))
	}
	for _, c := range s.Status.Conditions {
		if c.Type == appsv1.ReplicaSetReplicaFailure && c.Status == corev1.ConditionTrue {
			r.AddProblem(Unhealthy, inspectTextReplicaFailure(c.Reason, c.Message))
		}
	}
}

func inspectJob(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var j batchv1.Job
	if !inspectConvert(u, &j) {
		return
	}
	want := int32(1)
	if j.Spec.Completions != nil {
		want = *j.Spec.Completions
	}
	r.State = Healthy
	status := inspectJobStatus(r, &j)
	if status == "Running" && j.Status.Failed > 0 {
		r.AddProblem(Warning, inspectTextFailedPodsSoFar(j.Status.Failed))
	}
	r.Cells = []string{fmt.Sprintf("%d/%d", j.Status.Succeeded, want), status, age}
}

// inspectJobStatus returns the STATUS cell from the true conditions, and adds
// a problem for each failure. The last true condition sets the cell.
func inspectJobStatus(r *Row, j *batchv1.Job) string {
	status := "Running"
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			status = "Complete"
		case batchv1.JobFailed:
			status = "Failed"
			r.AddProblem(Unhealthy, inspectTextJobFailed(c.Reason, c.Message))
		case batchv1.JobSuspended:
			status = "Suspended"
		}
	}
	return status
}

func inspectCronJob(r *Row, u *unstructured.Unstructured, now time.Time, age string) {
	var c batchv1.CronJob
	if !inspectConvert(u, &c) {
		return
	}
	susp := c.Spec.Suspend != nil && *c.Spec.Suspend
	last := "-"
	if c.Status.LastScheduleTime != nil {
		last = Age(c.Status.LastScheduleTime.Time, now) + " ago"
	}
	r.Cells = []string{c.Spec.Schedule, fmt.Sprint(susp), last, age}
	r.State = Healthy
	if susp {
		r.AddProblem(Warning, inspectTextSuspended())
	}
}

func inspectHPA(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var h autoscalingv2.HorizontalPodAutoscaler
	if !inspectConvert(u, &h) {
		return
	}
	minR := int32(1)
	if h.Spec.MinReplicas != nil {
		minR = *h.Spec.MinReplicas
	}
	r.Cells = []string{h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name, fmt.Sprint(minR), fmt.Sprint(h.Spec.MaxReplicas), fmt.Sprint(h.Status.CurrentReplicas), age}
	r.State = Healthy
	// A range of one (min equal to max) cannot scale: being at the
	// maximum is the setting, not a limit that was reached.
	if h.Status.CurrentReplicas >= h.Spec.MaxReplicas && h.Spec.MaxReplicas > 0 && minR != h.Spec.MaxReplicas {
		r.AddProblem(Warning, inspectTextAtMaxReplicas(h.Spec.MaxReplicas))
	}
	// ScalingActive and the other conditions come from Signals.
}

func inspectPDB(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var p policyv1.PodDisruptionBudget
	if !inspectConvert(u, &p) {
		return
	}
	minA, maxU := "-", "-"
	if p.Spec.MinAvailable != nil {
		minA = p.Spec.MinAvailable.String()
	}
	if p.Spec.MaxUnavailable != nil {
		maxU = p.Spec.MaxUnavailable.String()
	}
	r.Cells = []string{minA, maxU, fmt.Sprint(p.Status.DisruptionsAllowed), age}
	r.State = Healthy
	if p.Status.DisruptionsAllowed == 0 && p.Status.ExpectedPods > 0 && !budgetForbidsByItself(&p) {
		r.AddProblem(Warning, inspectTextNoDisruptions(p.Status.CurrentHealthy, p.Status.ExpectedPods))
	}
}

func inspectService(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var s corev1.Service
	if !inspectConvert(u, &s) {
		return
	}
	var ports []string
	for _, p := range s.Spec.Ports {
		ports = append(ports, fmt.Sprintf("%d/%s", p.Port, p.Protocol))
	}
	r.Cells = []string{string(s.Spec.Type), dash(s.Spec.ClusterIP), dash(strings.Join(ports, ",")), age}
}

func inspectIngress(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	class, _, _ := unstructured.NestedString(u.Object, "spec", "ingressClassName")
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	var hosts []string
	for _, rl := range rules {
		if m, ok := rl.(map[string]any); ok {
			if h, ok := m["host"].(string); ok {
				hosts = append(hosts, h)
			}
		}
	}
	r.Cells = []string{dash(class), dash(strings.Join(hosts, ",")), age}
}

func inspectPVC(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var c corev1.PersistentVolumeClaim
	if !inspectConvert(u, &c) {
		return
	}
	capacity := "-"
	if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
		capacity = q.String()
	}
	class := "-"
	if c.Spec.StorageClassName != nil {
		class = *c.Spec.StorageClassName
	}
	r.Cells = []string{string(c.Status.Phase), dash(c.Spec.VolumeName), capacity, class, age}
	r.State = Healthy
	if c.Status.Phase != corev1.ClaimBound {
		r.AddProblem(Unhealthy, inspectTextClaimNotBound(c.Status.Phase))
	}
}

func inspectPV(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	var v corev1.PersistentVolume
	if !inspectConvert(u, &v) {
		return
	}
	capacity := "-"
	if q, ok := v.Spec.Capacity[corev1.ResourceStorage]; ok {
		capacity = q.String()
	}
	claim := "-"
	if v.Spec.ClaimRef != nil {
		claim = v.Spec.ClaimRef.Namespace + "/" + v.Spec.ClaimRef.Name
	}
	r.Cells = []string{capacity, string(v.Status.Phase), claim, dash(v.Spec.StorageClassName), age}
	r.State = Healthy
	if v.Status.Phase == corev1.VolumeFailed {
		r.AddProblem(Unhealthy, inspectTextVolumeFailed(v.Status.Message))
	}
}

func inspectNamespace(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	r.Cells = []string{dash(phase), age}
	r.State = Healthy
	if phase == "Terminating" {
		r.AddProblem(Warning, inspectTextNamespaceTerminating())
	}
}

func inspectConfigMap(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	data, _, _ := unstructured.NestedMap(u.Object, "data")
	bin, _, _ := unstructured.NestedMap(u.Object, "binaryData")
	r.Cells = []string{fmt.Sprint(len(data) + len(bin)), age}
}

func inspectEvent(r *Row, u *unstructured.Unstructured, _ time.Time, age string) {
	typ, _, _ := unstructured.NestedString(u.Object, "type")
	reason, _, _ := unstructured.NestedString(u.Object, "reason")
	k, _, _ := unstructured.NestedString(u.Object, "involvedObject", "kind")
	n, _, _ := unstructured.NestedString(u.Object, "involvedObject", "name")
	r.Cells = []string{typ, reason, strings.ToLower(k) + "/" + n, age}
	if typ == "Warning" {
		r.State = Warning
	}
}

func nodeRow(r *Row, n *corev1.Node, age string) {
	status := "Unknown"
	r.State = Healthy
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			if c.Status == corev1.ConditionTrue {
				status = "Ready"
			} else {
				status = "NotReady"
				r.AddProblem(Unhealthy, inspectTextNodeNotReady(c.Reason, c.Message))
			}
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
			if c.Status == corev1.ConditionTrue {
				r.AddProblem(Unhealthy, inspectTextNodePressure(c.Type, c.Message))
			}
		}
	}
	if n.Spec.Unschedulable {
		status += ",SchedulingDisabled"
		r.AddProblem(Warning, inspectTextCordoned())
	}
	r.Cells = []string{status, dash(strings.Join(inspectNodeRoles(n.Labels), ",")), age, dash(n.Status.NodeInfo.KubeletVersion)}
}

// inspectNodeRoles returns the roles in the node-role labels, sorted so the
// cell is the same on every call.
func inspectNodeRoles(labels map[string]string) []string {
	var roles []string
	for k := range labels {
		if role, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && role != "" {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return roles
}
