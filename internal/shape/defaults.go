package shape

import (
	"strings"
)

// Default is one value that the API server fills in when a field is left
// out. A field that holds exactly this value says nothing: the reader gets
// the same answer from its absence. A field that holds any other value is
// always kept.
//
// To extend the table, add a row to defaultRows (or to one of the pod-spec
// lists), and the test TestDefaultRows checks it: the field is dropped at
// the default and kept at another value. Add a row only for a value that is
// the default on every supported Kubernetes version. When unsure, keep it.
type Default struct {
	Kind string // object kind, for example "Deployment"
	Path string // from the object root; "[]" steps into every list element
	Want any    // the default value
	// Unless names sibling fields: when the parent holds one of them, the
	// default is not the default any more (it depends on them), so the
	// field stays.
	Unless []string
}

// podSpecRoots is where a pod spec sits in each kind.
var podSpecRoots = map[string]string{
	"Pod":         "spec",
	"Deployment":  "spec.template.spec",
	"StatefulSet": "spec.template.spec",
	"DaemonSet":   "spec.template.spec",
	"ReplicaSet":  "spec.template.spec",
	"Job":         "spec.template.spec",
	"CronJob":     "spec.jobTemplate.spec.template.spec",
}

// podSpecDefaults are paths from the root of a pod spec.
var podSpecDefaults = []Default{
	{Path: "dnsPolicy", Want: "ClusterFirst"},
	{Path: "restartPolicy", Want: "Always"},
	{Path: "schedulerName", Want: "default-scheduler"},
	{Path: "terminationGracePeriodSeconds", Want: 30},
	{Path: "enableServiceLinks", Want: true},
	{Path: "preemptionPolicy", Want: "PreemptLowerPriority"},
	{Path: "priority", Want: 0},
	{Path: "serviceAccountName", Want: "default"},
	{Path: "volumes[].configMap.defaultMode", Want: 420},
	{Path: "volumes[].secret.defaultMode", Want: 420},
	{Path: "volumes[].projected.defaultMode", Want: 420},
	{Path: "volumes[].downwardAPI.defaultMode", Want: 420},
	{Path: "volumes[].downwardAPI.items[].fieldRef.apiVersion", Want: "v1"},
	{Path: "volumes[].hostPath.type", Want: ""},
}

// containerDefaults are paths from the root of a container. They apply to
// containers and initContainers.
var containerDefaults = []Default{
	{Path: "terminationMessagePath", Want: "/dev/termination-log"},
	{Path: "terminationMessagePolicy", Want: "File"},
	{Path: "ports[].protocol", Want: "TCP"},
	{Path: "env[].valueFrom.fieldRef.apiVersion", Want: "v1"},
}

// probeDefaults are paths from the root of a probe.
var probeDefaults = []Default{
	{Path: "timeoutSeconds", Want: 1},
	{Path: "periodSeconds", Want: 10},
	{Path: "successThreshold", Want: 1},
	{Path: "failureThreshold", Want: 3},
	{Path: "initialDelaySeconds", Want: 0},
	{Path: "httpGet.scheme", Want: "HTTP"},
}

var probeNames = []string{"livenessProbe", "readinessProbe", "startupProbe"}

// jobSpecDefaults are paths from the root of a Job spec.
var jobSpecDefaults = []Default{
	{Path: "backoffLimit", Want: 6, Unless: []string{"backoffLimitPerIndex"}},
	{Path: "completionMode", Want: "NonIndexed"},
	{Path: "suspend", Want: false},
	{Path: "podReplacementPolicy", Want: "TerminatingOrFailed", Unless: []string{"podFailurePolicy"}},
	{Path: "manualSelector", Want: false},
	{Path: "parallelism", Want: 1},
}

// kindDefaults are the rows that belong to one kind only.
var kindDefaults = []Default{
	{Kind: "Deployment", Path: "spec.revisionHistoryLimit", Want: 10},
	{Kind: "Deployment", Path: "spec.progressDeadlineSeconds", Want: 600},
	{Kind: "Deployment", Path: "spec.paused", Want: false},
	{Kind: "Deployment", Path: "spec.strategy.type", Want: "RollingUpdate"},
	{Kind: "Deployment", Path: "spec.strategy.rollingUpdate.maxSurge", Want: "25%"},
	{Kind: "Deployment", Path: "spec.strategy.rollingUpdate.maxUnavailable", Want: "25%"},

	{Kind: "StatefulSet", Path: "spec.podManagementPolicy", Want: "OrderedReady"},
	{Kind: "StatefulSet", Path: "spec.revisionHistoryLimit", Want: 10},
	{Kind: "StatefulSet", Path: "spec.updateStrategy.type", Want: "RollingUpdate"},
	{Kind: "StatefulSet", Path: "spec.updateStrategy.rollingUpdate.partition", Want: 0},
	{Kind: "StatefulSet", Path: "spec.persistentVolumeClaimRetentionPolicy.whenDeleted", Want: "Retain"},
	{Kind: "StatefulSet", Path: "spec.persistentVolumeClaimRetentionPolicy.whenScaled", Want: "Retain"},

	{Kind: "DaemonSet", Path: "spec.revisionHistoryLimit", Want: 10},
	{Kind: "DaemonSet", Path: "spec.updateStrategy.type", Want: "RollingUpdate"},
	{Kind: "DaemonSet", Path: "spec.updateStrategy.rollingUpdate.maxUnavailable", Want: 1},
	{Kind: "DaemonSet", Path: "spec.updateStrategy.rollingUpdate.maxSurge", Want: 0},

	{Kind: "CronJob", Path: "spec.concurrencyPolicy", Want: "Allow"},
	{Kind: "CronJob", Path: "spec.successfulJobsHistoryLimit", Want: 3},
	{Kind: "CronJob", Path: "spec.failedJobsHistoryLimit", Want: 1},
	{Kind: "CronJob", Path: "spec.suspend", Want: false},

	{Kind: "Service", Path: "spec.sessionAffinity", Want: "None"},
	{Kind: "Service", Path: "spec.internalTrafficPolicy", Want: "Cluster"},
	{Kind: "Service", Path: "spec.externalTrafficPolicy", Want: "Cluster"},
	{Kind: "Service", Path: "spec.allocateLoadBalancerNodePorts", Want: true},
	{Kind: "Service", Path: "spec.ports[].protocol", Want: "TCP"},
	// ipFamilyPolicy goes after the rule for ipFamilies (see trimService).
	{Kind: "Service", Path: "spec.ipFamilyPolicy", Want: "SingleStack"},

	{Kind: "HorizontalPodAutoscaler", Path: "spec.minReplicas", Want: 1},

	{Kind: "PersistentVolumeClaim", Path: "spec.volumeMode", Want: "Filesystem"},

	{Kind: "Node", Path: "spec.unschedulable", Want: false},
	{Kind: "Node", Path: "status.daemonEndpoints.kubeletEndpoint.Port", Want: 10250},

	{Kind: "ConfigMap", Path: "immutable", Want: false},
}

// jobRoots is where a Job spec sits.
var jobRoots = map[string]string{
	"Job":     "spec",
	"CronJob": "spec.jobTemplate.spec",
}

// defaultRows is the whole table, one row per kind and path.
var defaultRows = buildDefaults()

// DefaultRows returns a copy of the table of defaults that the trimming
// drops, one row per kind and path.
func DefaultRows() []Default { return append([]Default(nil), defaultRows...) }

func buildDefaults() []Default {
	var rows []Default
	kinds := []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"}
	for _, kind := range kinds {
		root := podSpecRoots[kind]
		for _, d := range podSpecDefaults {
			rows = append(rows, Default{Kind: kind, Path: root + "." + d.Path, Want: d.Want, Unless: d.Unless})
		}
		for _, list := range []string{"containers[]", "initContainers[]"} {
			for _, d := range containerDefaults {
				rows = append(rows, Default{Kind: kind, Path: root + "." + list + "." + d.Path, Want: d.Want})
			}
			for _, p := range probeNames {
				for _, d := range probeDefaults {
					rows = append(rows, Default{Kind: kind, Path: root + "." + list + "." + p + "." + d.Path, Want: d.Want})
				}
			}
		}
	}
	for _, kind := range []string{"Job", "CronJob"} {
		for _, d := range jobSpecDefaults {
			rows = append(rows, Default{Kind: kind, Path: jobRoots[kind] + "." + d.Path, Want: d.Want, Unless: d.Unless})
		}
	}
	return append(rows, kindDefaults...)
}

// dropDefaults removes every field of the object that holds its default and
// returns how many it removed.
func dropDefaults(obj map[string]any) int {
	kind, _ := obj["kind"].(string)
	n := 0
	for _, d := range defaultRows {
		if d.Kind != kind {
			continue
		}
		eachParent(obj, strings.Split(d.Path, "."), func(m map[string]any, key string) {
			v, ok := m[key]
			if !ok || !sameValue(v, d.Want) {
				return
			}
			for _, u := range d.Unless {
				if _, has := m[u]; has {
					return
				}
			}
			delete(m, key)
			n++
		})
	}
	return n
}

// eachParent calls fn for each map that holds the last segment of the path.
func eachParent(node any, segs []string, fn func(m map[string]any, key string)) {
	m, ok := node.(map[string]any)
	if !ok || len(segs) == 0 {
		return
	}
	if len(segs) == 1 {
		fn(m, segs[0])
		return
	}
	key := strings.TrimSuffix(segs[0], "[]")
	child := m[key]
	if strings.HasSuffix(segs[0], "[]") {
		list, _ := child.([]any)
		for _, e := range list {
			eachParent(e, segs[1:], fn)
		}
		return
	}
	eachParent(child, segs[1:], fn)
}

// sameValue compares a value of the object with a default. A number matches
// a number of the same value; a string never matches a number.
func sameValue(got, want any) bool {
	if w, ok := want.(int); ok {
		f, ok := numOK(got)
		return ok && f == float64(w)
	}
	return got == want
}
