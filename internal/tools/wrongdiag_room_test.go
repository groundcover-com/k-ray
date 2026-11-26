package tools

import (
	"testing"
)

// Item 5: the room check counts only the nodes that the pod can be placed on.

func roomNode(name, mem, taint, labels string) string {
	spec := `"spec":{}`
	if taint != "" {
		spec = `"spec":{"taints":[` + taint + `]}`
	}
	return `{"apiVersion":"v1","kind":"Node","metadata":{"name":"` + name + `","labels":{` + labels + `}},` + spec + `,"status":{"allocatable":{"cpu":"4","memory":"` + mem + `"}}}`
}

// roomPod is a pending pod that requests 2Gi. spec is more pod spec fields
// (with a trailing comma); msg is the scheduler message.
func roomPod(spec, msg string) string {
	return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","uid":"u-web-1","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{` + spec + `"containers":[{"name":"app","image":"x:1","resources":{"requests":{"cpu":"100m","memory":"2Gi"}}}]},
"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":` + jsonQuote(msg) + `}]}}`
}

// holder is a pod on a node that requests mem of it.
func holder(name, node, mem string) string {
	return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"` + name + `","namespace":"other","creationTimestamp":"2026-11-13T09:00:00Z"},
"spec":{"nodeName":"` + node + `","containers":[{"name":"c","image":"x:1","resources":{"requests":{"memory":"` + mem + `"}}}]},
"status":{"phase":"Running"}}`
}

const gpuTaint = `{"key":"dedicated","value":"gpu","effect":"NoSchedule"}`

// node-a has no room (3.5Gi of 4Gi requested); node-t1 and node-t2 are tainted
// and empty. By requests the pod fits on a tainted node, but cannot use it.
func TestRoomCheckDoesNotNameATaintedNode(t *testing.T) {
	objs := roomNode("node-a", "4Gi", "", "") + "," + roomNode("node-t1", "16Gi", gpuTaint, "") + "," + roomNode("node-t2", "16Gi", gpuTaint, "") +
		"," + holder("big", "node-a", "3584Mi") + "," +
		roomPod("", "0/3 nodes are available: 3 Insufficient memory, 3 node(s) had untolerated taint {dedicated: gpu}.")
	out := runCall(t, objs, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "likely cause:", "the most free on any node is 512Mi (node-a")
	mustNotContain(t, out, "node-t1", "node-t2", "has room for the pod")
}

// A toleration makes the tainted node usable again.
func TestRoomCheckCountsATaintedNodeThePodTolerates(t *testing.T) {
	objs := roomNode("node-a", "4Gi", "", "") + "," + roomNode("node-t1", "16Gi", gpuTaint, "") +
		"," + holder("big", "node-a", "3584Mi") + "," +
		roomPod(`"tolerations":[{"key":"dedicated","operator":"Equal","value":"gpu","effect":"NoSchedule"}],`, "0/2 nodes are available: 1 Insufficient memory.")
	out := runCall(t, objs, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "by requests, node node-t1 has room for the pod")
}

// nodeSelector and required node affinity (In, NotIn, Exists, DoesNotExist).
func TestRoomCheckHonoursNodeSelectorAndAffinity(t *testing.T) {
	nodes := roomNode("node-a", "4Gi", "", `"disk":"hdd","zone":"a"`) + "," + roomNode("node-b", "16Gi", "", `"disk":"ssd","zone":"b","spot":"true"`)
	pods := holder("big", "node-a", "3584Mi") + ","
	msg := "0/2 nodes are available: 2 Insufficient memory."
	cases := map[string]struct {
		spec string
		fits bool // whether node-b (the one with room) may be named
	}{
		"selector match":    {`"nodeSelector":{"disk":"ssd"},`, true},
		"selector mismatch": {`"nodeSelector":{"disk":"hdd"},`, false},
		"In":                {affinity(`{"key":"zone","operator":"In","values":["b","c"]}`), true},
		"In mismatch":       {affinity(`{"key":"zone","operator":"In","values":["a"]}`), false},
		"NotIn":             {affinity(`{"key":"zone","operator":"NotIn","values":["b"]}`), false},
		"Exists":            {affinity(`{"key":"spot","operator":"Exists"}`), true},
		"DoesNotExist":      {affinity(`{"key":"spot","operator":"DoesNotExist"}`), false},
	}
	for name, c := range cases {
		out := runCall(t, nodes+","+pods+roomPod(c.spec, msg), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
		if c.fits {
			mustContain(t, out, "by requests, node node-b has room for the pod")
		} else {
			mustNotContain(t, out, "node-b", "has room for the pod")
		}
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}
}

func affinity(expr string) string {
	return `"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[` + expr + `]}]}}},`
}

// A placement rule that is not evaluated (pod anti-affinity, topology spread,
// matchFields) means the "has room" sentence is not printed at all.
func TestSilentRoomSentenceWithAnUnevaluatedPlacementRule(t *testing.T) {
	nodes := roomNode("node-a", "4Gi", "", "") + "," + roomNode("node-b", "16Gi", "", "")
	msg := "0/2 nodes are available: 2 Insufficient memory."
	for name, spec := range map[string]string{
		"anti-affinity": `"affinity":{"podAntiAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[{"topologyKey":"kubernetes.io/hostname","labelSelector":{"matchLabels":{"app":"web"}}}]}},`,
		"pod affinity":  `"affinity":{"podAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[{"topologyKey":"kubernetes.io/hostname","labelSelector":{"matchLabels":{"app":"db"}}}]}},`,
		"spread":        `"topologySpreadConstraints":[{"maxSkew":1,"topologyKey":"zone","whenUnsatisfiable":"DoNotSchedule","labelSelector":{"matchLabels":{"app":"web"}}}],`,
		"matchFields":   `"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchFields":[{"key":"metadata.name","operator":"In","values":["node-a"]}]}]}}},`,
	} {
		out := runCall(t, nodes+","+roomPod(spec, msg), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
		mustNotContain(t, out, "has room", "another reason applies", "node-b")
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}
	// Without such a rule the sentence is there.
	out := runCall(t, nodes+","+roomPod("", msg), "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, out, "has room for the pod")
}
