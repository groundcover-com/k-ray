package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Fixtures for the differences in describe: a Deployment with two revisions
// and pods of each, and a StatefulSet with many pods. The tests print the
// block they check (go test -v) so that its size can be read.

// tpl is the part of a pod template that the tests change.
type tpl struct {
	image       string
	args        []string
	env         map[string]string
	readiness   string // path; "" for no probe
	memLimit    string
	configMap   string // mounted ConfigMap, "" for none
	annotations map[string]string
}

func (x tpl) spec() corev1.PodTemplateSpec {
	c := corev1.Container{Name: "app", Image: x.image, Args: x.args}
	names := make([]string, 0, len(x.env))
	for k := range x.env {
		names = append(names, k)
	}
	sortStrings(names)
	for _, k := range names {
		c.Env = append(c.Env, corev1.EnvVar{Name: k, Value: x.env[k]})
	}
	if x.readiness != "" {
		c.ReadinessProbe = &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: x.readiness, Port: intstr.FromInt32(8080)}},
			InitialDelaySeconds: 5, PeriodSeconds: 10, TimeoutSeconds: 1, FailureThreshold: 3,
		}
	}
	if x.memLimit != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(x.memLimit)}
	}
	t := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}, Annotations: x.annotations},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{c}},
	}
	if x.configMap != "" {
		t.Spec.Volumes = []corev1.Volume{{Name: "cfg", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: x.configMap}}}}}
		t.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "cfg", MountPath: "/etc/cfg"}}
	}
	return t
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func revDeployment(t *testing.T, rev int, replicas int32, ready int32, updated int32, total int32, x tpl) unstructured.Unstructured {
	t.Helper()
	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: "web", UID: types.UID("uid-deployment-web"), Generation: 1,
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-72 * time.Hour)),
			Annotations:       map[string]string{revisionAnnotation: fmt.Sprint(rev)},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: x.spec(),
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: total, ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: updated},
	}
	return toObj(t, "apps/v1", "Deployment", &d)
}

func revRS(t *testing.T, rev int, hash string, replicas, ready int32, age time.Duration, x tpl, desired string) unstructured.Unstructured {
	t.Helper()
	tt := x.spec()
	tt.Labels["pod-template-hash"] = hash
	ann := map[string]string{revisionAnnotation: fmt.Sprint(rev)}
	if desired != "" {
		ann[desiredAnnotation] = desired
	}
	rs := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: "web-" + hash, UID: types.UID("uid-rs-" + hash),
			Labels:            map[string]string{"app": "web", "pod-template-hash": hash},
			Annotations:       ann,
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-age)),
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-deployment-web", Controller: boolPtr(true)}},
		},
		Spec:   appsv1.ReplicaSetSpec{Replicas: &replicas, Template: tt},
		Status: appsv1.ReplicaSetStatus{Replicas: replicas, ReadyReplicas: ready},
	}
	return toObj(t, "apps/v1", "ReplicaSet", &rs)
}

// podSpec is what a test says about one pod.
type podSpec struct {
	name, ownerKind, owner string
	labels                 map[string]string
	node, image, digest    string
	age                    time.Duration
	crash                  bool
	claim                  string
}

func (s podSpec) pod(t *testing.T) unstructured.Unstructured {
	t.Helper()
	if s.node == "" {
		s.node = "node-a"
	}
	if s.age == 0 {
		s.age = 3 * time.Hour
	}
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: s.name, UID: types.UID("uid-" + s.name), Labels: s.labels,
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-s.age)),
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: s.ownerKind, Name: s.owner, UID: types.UID("uid-" + s.owner), Controller: boolPtr(true)}},
		},
		Spec: corev1.PodSpec{NodeName: s.node, RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{Name: "app", Image: s.image}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, Image: s.image, ImageID: "registry.example/web@sha256:" + s.digest, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	if s.claim != "" {
		p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: s.claim}}}}
	}
	if s.crash {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
		p.Status.ContainerStatuses[0].Ready = false
		p.Status.ContainerStatuses[0].RestartCount = 5
		p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		p.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}
	}
	return toObj(t, "v1", "Pod", &p)
}

func hashLabels(hash string) map[string]string {
	return map[string]string{"app": "web", "pod-template-hash": hash}
}

// blockOf returns the lines of a block: the line that starts with first, the
// bullet lines after it, and a closing sentence that starts with one of tails.
func blockOf(out, first string, tails ...string) string {
	lines := strings.Split(out, "\n")
	var got []string
	in := false
	for _, l := range lines {
		if !in {
			if strings.HasPrefix(l, first) {
				in = true
				got = append(got, l)
			}
			continue
		}
		keep := strings.HasPrefix(l, "- ")
		for _, tl := range tails {
			if strings.HasPrefix(l, tl) {
				keep = true
			}
		}
		if !keep {
			break
		}
		got = append(got, l)
	}
	return strings.Join(got, "\n")
}

func logBlock(t *testing.T, name, block string) {
	t.Helper()
	t.Logf("%s: %d bytes\n%s", name, len(block)+1, block)
}

const revTail1, revTail2 = "the pods of revision", "only "

// A Deployment whose new revision changed the image, the probe path, the
// memory limit and the mounted ConfigMap, and whose new pods are not ready.
func TestRevisionDiffNewPodsFail(t *testing.T) {
	oldT := tpl{image: "registry.example/web:1", readiness: "/healthz", memLimit: "256Mi", configMap: "web-config-v1", env: map[string]string{"LOG_LEVEL": "info"}}
	newT := tpl{image: "registry.example/web:2", readiness: "/ready", memLimit: "128Mi", configMap: "web-config-v2", env: map[string]string{"LOG_LEVEL": "info"}}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		revDeployment(t, 2, 5, 3, 2, 5, newT),
		revRS(t, 1, "aaaa1", 3, 3, 48*time.Hour, oldT, "5"),
		revRS(t, 2, "bbbb2", 2, 0, 20*time.Minute, newT, "5"),
	)
	for i := 0; i < 3; i++ {
		f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-aaaa1-%d", i), ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), image: "registry.example/web:1", digest: "a1", age: 40 * time.Hour}.pod(t))
	}
	for i := 0; i < 2; i++ {
		f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-bbbb2-%d", i), ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2", age: 19 * time.Minute, crash: true}.pod(t))
	}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustContain(t, out,
		"changed in revision 2 (20m ago), against revision 1:",
		"- container app image: registry.example/web:1 -> registry.example/web:2",
		"- container app readiness probe: http /healthz:8080 (delay 5s, period 10s, timeout 1s, failures 3) -> http /ready:8080 (delay 5s, period 10s, timeout 1s, failures 3)",
		"- container app resources: limits memory 256Mi -> 128Mi",
		"- mount of volume cfg: configmap web-config-v1 -> configmap web-config-v2",
		"the pods of revision 2 are not ready; revision 1 has 3 ready pods",
		"the failing pods differ from the healthy ones in:",
	)
	// The image comes first, the mount last.
	rev := blockOf(out, "changed in revision", revTail1, revTail2)
	if strings.Index(rev, "image") > strings.Index(rev, "probe") || strings.Index(rev, "probe") > strings.Index(rev, "resources") || strings.Index(rev, "resources") > strings.Index(rev, "mount") {
		t.Errorf("the lines are not in the order of importance:\n%s", rev)
	}
	logBlock(t, "revision block", rev)
	logBlock(t, "sibling block", blockOf(out, "the failing pods differ"))
	noSecretReads(t, f)
}

// Only the restart annotation changed: a restart is a change, and it is the
// only one.
func TestRevisionDiffRestartOnly(t *testing.T) {
	oldT := tpl{image: "registry.example/web:1"}
	newT := tpl{image: "registry.example/web:1", annotations: map[string]string{restartedAtKey: "2026-11-13T11:50:00Z"}}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		revDeployment(t, 2, 2, 2, 2, 2, newT),
		revRS(t, 1, "aaaa1", 0, 0, 48*time.Hour, oldT, ""),
		revRS(t, 2, "bbbb2", 2, 2, 10*time.Minute, newT, ""),
		podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:1", digest: "a1", age: 9 * time.Minute}.pod(t),
		podSpec{name: "web-bbbb2-1", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:1", digest: "a1", age: 9 * time.Minute}.pod(t),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	rev := blockOf(out, "changed in revision", revTail1, revTail2)
	want := "changed in revision 2 (10m ago), against revision 1:\n- restarted at 2026-11-13T11:50:00Z, no other change"
	if rev != want {
		t.Errorf("got:\n%s\nwant:\n%s", rev, want)
	}
	logBlock(t, "revision block", rev)
}

// An environment value, an argument and an annotation changed. Each holds a
// secret-like value on BOTH sides; none appears. The value that is no secret
// is shown, old and new.
func TestRevisionDiffNeverShowsSecretValues(t *testing.T) {
	secrets := []string{"sk-live-OLDtoken0123456789abcdefABCDEF", "sk-live-NEWtoken9876543210fedcbaFEDCBA", "oldPassw0rdXYZ", "newPassw0rdXYZ", "hunter2old", "hunter2new", "oldAnnotationSecret77", "newAnnotationSecret88"}
	oldT := tpl{
		image:       "registry.example/web:1",
		args:        []string{"--port=8080", "--password=hunter2old"},
		env:         map[string]string{"API_TOKEN": secrets[0], "DB_URL": "postgres://app:oldPassw0rdXYZ@db:5432/shop", "LOG_LEVEL": "info"},
		annotations: map[string]string{"example.com/api-key": "oldAnnotationSecret77", "example.com/owner": "team-a"},
	}
	newT := tpl{
		image:       "registry.example/web:1",
		args:        []string{"--port=8080", "--password=hunter2new"},
		env:         map[string]string{"API_TOKEN": secrets[1], "DB_URL": "postgres://app:newPassw0rdXYZ@db:5432/shop", "LOG_LEVEL": "debug"},
		annotations: map[string]string{"example.com/api-key": "newAnnotationSecret88", "example.com/owner": "team-b"},
	}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs,
		revDeployment(t, 2, 1, 0, 1, 2, newT),
		revRS(t, 1, "aaaa1", 1, 1, 48*time.Hour, oldT, ""),
		revRS(t, 2, "bbbb2", 1, 0, 5*time.Minute, newT, ""),
		podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:1", digest: "a1", age: 4 * time.Minute, crash: true}.pod(t),
	)
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	mustNotContain(t, out, secrets...)
	mustContain(t, out,
		"- container app env LOG_LEVEL: info -> debug",
		"container app env API_TOKEN changed (value hidden by redaction)",
		"example.com/owner: team-a -> team-b",
	)
	logBlock(t, "revision block", blockOf(out, "changed in revision", revTail1, revTail2))
}

// A healthy rollout that is old prints nothing: the output is as long as it
// is for a Deployment that has only one revision.
func TestRevisionDiffHealthyOldRolloutPrintsNothing(t *testing.T) {
	build := func(withOld bool) *fakeReader {
		oldT := tpl{image: "registry.example/web:1", readiness: "/healthz"}
		newT := tpl{image: "registry.example/web:2", readiness: "/ready"}
		f := &fakeReader{logs: map[string]string{}}
		f.objs = append(f.objs, revDeployment(t, 2, 2, 2, 2, 2, newT))
		if withOld {
			f.objs = append(f.objs, revRS(t, 1, "aaaa1", 0, 0, 72*time.Hour, oldT, ""))
		}
		f.objs = append(f.objs,
			revRS(t, 2, "bbbb2", 2, 2, 5*time.Hour, newT, ""),
			podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2"}.pod(t),
			podSpec{name: "web-bbbb2-1", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2"}.pod(t),
		)
		return f
	}
	args := map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"}
	r1, _ := newRunner(t, build(true))
	with := describeText(t, r1, args)
	r2, _ := newRunner(t, build(false))
	without := describeText(t, r2, args)
	mustNotContain(t, with, "changed in revision", "differ from the healthy", "no difference in")
	// The old ReplicaSet is read either way; only its list line may differ.
	if len(with) != len(without) {
		t.Errorf("a healthy, old rollout grew the output: %d bytes against %d\n%s", len(with), len(without), with)
	}
}

// A young rollout prints the difference even when all is well; an old one
// that is not complete does too. The first revision has nothing to compare.
func TestRevisionDiffWhenYoungOrIncomplete(t *testing.T) {
	oldT := tpl{image: "registry.example/web:1"}
	newT := tpl{image: "registry.example/web:2"}
	build := func(age time.Duration, updated int32) *fakeReader {
		f := &fakeReader{logs: map[string]string{}}
		f.objs = append(f.objs,
			revDeployment(t, 2, 2, 2, updated, 2, newT),
			revRS(t, 1, "aaaa1", 0, 0, 72*time.Hour, oldT, ""),
			revRS(t, 2, "bbbb2", 2, 2, age, newT, ""),
			podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2"}.pod(t),
		)
		return f
	}
	args := map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"}
	r, _ := newRunner(t, build(10*time.Minute, 2))
	mustContain(t, describeText(t, r, args), "changed in revision 2 (10m ago), against revision 1:", "- container app image: registry.example/web:1 -> registry.example/web:2")
	r, _ = newRunner(t, build(5*time.Hour, 1))
	mustContain(t, describeText(t, r, args), "changed in revision 2 (5h ago), against revision 1:")
	r, _ = newRunner(t, build(5*time.Hour, 2))
	mustNotContain(t, describeText(t, r, args), "changed in revision")

	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, revDeployment(t, 1, 2, 1, 2, 2, newT), revRS(t, 1, "aaaa1", 2, 1, 10*time.Minute, newT, ""),
		podSpec{name: "web-aaaa1-0", ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), image: "registry.example/web:2", digest: "b2", crash: true}.pod(t))
	r, _ = newRunner(t, f)
	mustNotContain(t, describeText(t, r, args), "changed in revision")
}

// The difference adds no read: the ReplicaSets are listed once, and the
// number of reads does not depend on whether the revision block prints.
func TestRevisionDiffAddsNoRead(t *testing.T) {
	oldT := tpl{image: "registry.example/web:1"}
	newT := tpl{image: "registry.example/web:2"}
	build := func(rev int) *fakeReader {
		f := &fakeReader{logs: map[string]string{}}
		f.objs = append(f.objs,
			revDeployment(t, rev, 2, 1, 1, 2, newT),
			revRS(t, 1, "aaaa1", 1, 1, 48*time.Hour, oldT, ""),
			revRS(t, 2, "bbbb2", 1, 0, 5*time.Minute, newT, ""),
			podSpec{name: "web-aaaa1-0", ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), image: "registry.example/web:1", digest: "a1"}.pod(t),
			podSpec{name: "web-bbbb2-0", ownerKind: "ReplicaSet", owner: "web-bbbb2", labels: hashLabels("bbbb2"), image: "registry.example/web:2", digest: "b2", crash: true}.pod(t),
		)
		return f
	}
	args := map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"}
	f := build(2)
	r, _ := newRunner(t, f)
	out := describeText(t, r, args)
	mustContain(t, out, "changed in revision 2")
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "list replicasets") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the ReplicaSets were listed %d times: %v", n, f.calls)
	}
	// The same cluster with no revision annotations: no block, same reads.
	g := build(0)
	for i := range g.objs {
		if g.objs[i].GetKind() == "ReplicaSet" {
			g.objs[i].SetAnnotations(nil)
		}
	}
	r, _ = newRunner(t, g)
	out2 := describeText(t, r, args)
	mustNotContain(t, out2, "changed in revision")
	if fmtCounts(f.counts()) != fmtCounts(g.counts()) {
		t.Errorf("reads differ: %s against %s", fmtCounts(f.counts()), fmtCounts(g.counts()))
	}
}

// The failing pods of a Deployment are all on one node.
func TestSiblingDiffAllOnOneNode(t *testing.T) {
	x := tpl{image: "registry.example/web:1"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, revDeployment(t, 1, 6, 3, 6, 6, x), revRS(t, 1, "aaaa1", 6, 3, 5*time.Hour, x, ""))
	nodes := []string{"node-b", "node-b", "node-b", "node-a", "node-c", "node-a"}
	for i, n := range nodes {
		f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-aaaa1-%d", i), ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), node: n, image: "registry.example/web:1", digest: "a1", crash: i < 3}.pod(t))
	}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	sib := blockOf(out, "the failing pods differ")
	want := "the failing pods differ from the healthy ones in:\n- node: node-b in all 3 failing pods, in none of the 3 healthy pods"
	if sib != want {
		t.Errorf("got:\n%s\nwant:\n%s", sib, want)
	}
	logBlock(t, "sibling block", sib)
}

// Nothing separates the failing pods from the healthy ones: that is said.
func TestSiblingDiffNothingSeparatesThem(t *testing.T) {
	x := tpl{image: "registry.example/web:1"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, revDeployment(t, 1, 4, 2, 4, 4, x), revRS(t, 1, "aaaa1", 4, 2, 5*time.Hour, x, ""))
	for i := 0; i < 4; i++ {
		f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-aaaa1-%d", i), ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), image: "registry.example/web:1", digest: "a1", crash: i < 2}.pod(t))
	}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	sib := blockOf(out, "no difference in")
	want := "no difference in node, revision, image, image ID, age, restart policy or QoS class between the 2 failing and the 2 healthy pods"
	if sib != want {
		t.Errorf("got:\n%s\nwant:\n%s", sib, want)
	}
	mustNotContain(t, out, "differ from the healthy")
	logBlock(t, "sibling block", sib)
}

// With no healthy pod, or no failing pod, there is nothing to compare.
func TestSiblingDiffNeedsBothGroups(t *testing.T) {
	x := tpl{image: "registry.example/web:1"}
	for _, crashAll := range []bool{true, false} {
		f := &fakeReader{logs: map[string]string{}}
		f.objs = append(f.objs, revDeployment(t, 1, 3, 0, 3, 3, x), revRS(t, 1, "aaaa1", 3, 0, 5*time.Hour, x, ""))
		for i := 0; i < 3; i++ {
			f.objs = append(f.objs, podSpec{name: fmt.Sprintf("web-aaaa1-%d", i), ownerKind: "ReplicaSet", owner: "web-aaaa1", labels: hashLabels("aaaa1"), image: "registry.example/web:1", digest: "a1", crash: crashAll}.pod(t))
		}
		r, _ := newRunner(t, f)
		out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
		mustNotContain(t, out, "differ from the healthy", "no difference in")
	}
}
