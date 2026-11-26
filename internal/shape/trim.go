// Package shape turns cluster objects into compact text for a model: it
// trims noise, finds problems, renders tables and YAML, keeps each result in
// its size budget, and frames cluster content as data.
package shape

import (
	"fmt"
	"reflect"
	"strings"

	"sigs.k8s.io/yaml"
)

// LastApplied is always removed: it is a full copy of the object and a
// common place for secret values.
const LastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// Report says what Trim and CutLong left out of an object, so that the
// output can tell the reader that the object is not complete.
type Report struct {
	Dropped   int      // fields left out because they hold a default or repeat another field
	CutValues int      // long values that were cut or replaced by a marker
	Notes     []string // facts the reader needs because of what was left out
	Outline   []string // for a ConfigMap with large data: one line for each key
}

// Trim removes noise from an object in place. managedFields and
// last-applied-configuration go always. Without full, the fields that hold a
// Kubernetes default (defaultRows and the rules below), fields that repeat
// another field, bookkeeping fields and node image lists go too, and the
// status is made shorter without hiding any state: conditions that are
// healthy become one line, and the unhealthy ones stay in full.
//
// Trim runs before redaction. Long values are cut later, by CutLong, after
// redaction has seen each of them whole.
func Trim(obj map[string]any, full bool) Report {
	var rep Report
	if md, ok := obj["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
		if ann, ok := md["annotations"].(map[string]any); ok {
			delete(ann, LastApplied)
		}
	}
	if !full {
		t := &trimmer{rep: &rep}
		t.metadata(obj)
		t.kind(obj)
		t.status(obj)
		rep.Dropped += dropDefaults(obj)
	}
	prune(obj)
	return rep
}

type trimmer struct{ rep *Report }

// del removes a key and counts it.
func (t *trimmer) del(m map[string]any, key string) {
	if _, ok := m[key]; ok {
		delete(m, key)
		t.rep.Dropped++
	}
}

func (t *trimmer) metadata(obj map[string]any) {
	md, ok := obj["metadata"].(map[string]any)
	if !ok {
		return
	}
	for _, k := range []string{"resourceVersion", "uid", "selfLink", "generateName"} {
		t.del(md, k)
	}
	status, _ := obj["status"].(map[string]any)
	gen, hasGen := numOK(md["generation"])
	obs, hasObs := numOK(status["observedGeneration"])
	switch {
	case hasGen && hasObs && gen != obs:
		t.rep.Notes = append(t.rep.Notes, fmt.Sprintf("metadata.generation %d differs from status.observedGeneration %d: the controller has not acted on the last change yet.", int64(gen), int64(obs)))
	case hasGen && hasObs:
		t.del(md, "generation")
		t.del(status, "observedGeneration")
	default:
		t.del(md, "generation")
	}
	if ann, ok := md["annotations"].(map[string]any); ok {
		t.del(ann, "deployment.kubernetes.io/revision")
	}
	if l, ok := md["labels"].(map[string]any); ok {
		t.del(l, "pod-template-hash")
	}
	if refs, ok := md["ownerReferences"].([]any); ok {
		for i, r := range refs {
			m, _ := r.(map[string]any)
			kind, _ := m["kind"].(string)
			name, _ := m["name"].(string)
			if kind == "" || name == "" {
				continue
			}
			line := kind + "/" + name
			if m["controller"] == true {
				line += " (controller)"
			}
			refs[i] = line
		}
	}
}

// kind applies the rules that the table of defaults cannot say: a default
// that depends on another field, or on the value of the same field in a
// sibling.
func (t *trimmer) kind(obj map[string]any) {
	kind, _ := obj["kind"].(string)
	if root, ok := podSpecRoots[kind]; ok {
		if tpl := getMap(obj, strings.TrimSuffix(root, ".spec")); kind != "Pod" && tpl != nil {
			if md, ok := tpl["metadata"].(map[string]any); ok {
				delete(md, "creationTimestamp")
			}
		}
		t.podSpec(getMap(obj, root))
	}
	if root, ok := jobRoots[kind]; ok {
		t.jobSpec(getMap(obj, root))
	}
	switch kind {
	case "Service":
		t.service(getMap(obj, "spec"))
	case "Node":
		spec := getMap(obj, "spec")
		if cidrs, ok := spec["podCIDRs"].([]any); ok && len(cidrs) == 1 && cidrs[0] == spec["podCIDR"] {
			t.del(spec, "podCIDRs")
		}
		t.del(getMap(obj, "status"), "images")
	}
}

func (t *trimmer) jobSpec(spec map[string]any) {
	if spec == nil {
		return
	}
	// The selector that the API server makes for a Job names its controller-uid.
	if sel, ok := spec["selector"].(map[string]any); ok && len(sel) == 1 && spec["manualSelector"] != true {
		if ml, ok := sel["matchLabels"].(map[string]any); ok && len(ml) > 0 {
			auto := true
			for k := range ml {
				if !strings.Contains(k, "controller-uid") {
					auto = false
				}
			}
			if auto {
				t.del(spec, "selector")
			}
		}
	}
	if l := getMap(spec, "template.metadata.labels"); l != nil {
		for k := range l {
			if strings.Contains(k, "controller-uid") || k == "job-name" || k == "batch.kubernetes.io/job-name" || strings.HasPrefix(k, "batch.kubernetes.io/") {
				t.del(l, k)
			}
		}
	}
}

func (t *trimmer) service(spec map[string]any) {
	if spec == nil {
		return
	}
	if ips, ok := spec["clusterIPs"].([]any); ok && len(ips) == 1 && ips[0] == spec["clusterIP"] {
		t.del(spec, "clusterIPs")
	}
	// A single-stack service on an IPv4 cluster. An IPv6 family stays.
	if spec["ipFamilyPolicy"] == "SingleStack" {
		if fams, ok := spec["ipFamilies"].([]any); ok && len(fams) == 1 && fams[0] == "IPv4" {
			t.del(spec, "ipFamilies")
		}
	}
	// A port without a target port sends to its own number.
	ports, _ := spec["ports"].([]any)
	for _, p := range ports {
		if m, ok := p.(map[string]any); ok {
			if tp, ok := numOK(m["targetPort"]); ok {
				if port, ok := numOK(m["port"]); ok && tp == port {
					t.del(m, "targetPort")
				}
			}
		}
	}
}

// podSpec drops the defaults of a pod spec that need more than the value of
// one field.
func (t *trimmer) podSpec(ps map[string]any) {
	if ps == nil {
		return
	}
	if ps["serviceAccount"] == ps["serviceAccountName"] {
		t.del(ps, "serviceAccount")
	}
	if tols, ok := ps["tolerations"].([]any); ok {
		var kept []any
		for _, tol := range tols {
			m, _ := tol.(map[string]any)
			k, _ := m["key"].(string)
			if (k == "node.kubernetes.io/not-ready" || k == "node.kubernetes.io/unreachable") && m["effect"] == "NoExecute" && m["operator"] == "Exists" && num(m["tolerationSeconds"]) == 300 {
				t.rep.Dropped++
				continue
			}
			kept = append(kept, tol)
		}
		ps["tolerations"] = kept
	}
	// The projected service account token volume and its mounts.
	apiAccess := map[string]bool{}
	if vols, ok := ps["volumes"].([]any); ok {
		var kept []any
		for _, v := range vols {
			m, _ := v.(map[string]any)
			name, _ := m["name"].(string)
			if strings.HasPrefix(name, "kube-api-access-") {
				apiAccess[name] = true
				t.rep.Dropped++
				continue
			}
			kept = append(kept, v)
		}
		ps["volumes"] = kept
	}
	for _, key := range []string{"containers", "initContainers"} {
		cs, _ := ps[key].([]any)
		for _, c := range cs {
			m, _ := c.(map[string]any)
			if m == nil {
				continue
			}
			if img, _ := m["image"].(string); m["imagePullPolicy"] == defaultPullPolicy(img) {
				t.del(m, "imagePullPolicy")
			}
			if vms, ok := m["volumeMounts"].([]any); ok {
				var kept []any
				for _, vm := range vms {
					vmm, _ := vm.(map[string]any)
					if n, _ := vmm["name"].(string); apiAccess[n] {
						t.rep.Dropped++
						continue
					}
					kept = append(kept, vm)
				}
				m["volumeMounts"] = kept
			}
		}
	}
}

// status shortens the status without hiding state.
func (t *trimmer) status(obj map[string]any) {
	status, _ := obj["status"].(map[string]any)
	if status == nil {
		return
	}
	t.conditions(status)
	if obj["kind"] != "Pod" {
		return
	}
	spec, _ := obj["spec"].(map[string]any)
	if ip, ok := status["podIP"]; ok {
		if ips, ok := status["podIPs"].([]any); ok && repeatsIP(ips, ip) {
			t.del(status, "podIPs")
		}
	}
	if ip, ok := status["hostIP"]; ok {
		if ips, ok := status["hostIPs"].([]any); ok && repeatsIP(ips, ip) {
			t.del(status, "hostIPs")
		}
	}
	allDerived := true
	for _, key := range []string{"initContainerStatuses", "containerStatuses", "ephemeralContainerStatuses"} {
		list, _ := status[key].([]any)
		specKey := strings.Replace(key, "Statuses", "s", 1)
		for _, e := range list {
			m, _ := e.(map[string]any)
			if m == nil {
				continue
			}
			sc := specContainer(spec, specKey, m["name"])
			t.containerStatus(m, sc)
			if _, still := m["allocatedResources"]; still {
				allDerived = false
			}
		}
	}
	// The pod-level allocation is the sum of the containers' requests, which
	// the spec shows.
	if allDerived {
		t.del(status, "allocatedResources")
	}
}

func repeatsIP(ips []any, ip any) bool {
	if len(ips) != 1 {
		return false
	}
	m, _ := ips[0].(map[string]any)
	return len(m) == 1 && m["ip"] == ip
}

func specContainer(spec map[string]any, key string, name any) map[string]any {
	list, _ := spec[key].([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok && m["name"] == name {
			return m
		}
	}
	return nil
}

func (t *trimmer) containerStatus(m, spec map[string]any) {
	t.del(m, "containerID")
	for _, sk := range []string{"state", "lastState"} {
		if st, ok := m[sk].(map[string]any); ok {
			if term, ok := st["terminated"].(map[string]any); ok {
				t.del(term, "containerID")
			}
		}
	}
	if id, ok := m["imageID"].(string); ok {
		if d := shortDigest(id); d != "" {
			delete(m, "imageID")
			m["imageDigest"] = d
		}
	}
	if st, ok := m["state"].(map[string]any); ok && m["started"] == true {
		if _, running := st["running"]; running {
			t.del(m, "started")
		}
	}
	if spec != nil {
		if res, ok := m["resources"]; ok && reflect.DeepEqual(res, spec["resources"]) {
			t.del(m, "resources")
		}
		if al, ok := m["allocatedResources"]; ok {
			req, _ := spec["resources"].(map[string]any)
			if reflect.DeepEqual(al, req["requests"]) {
				t.del(m, "allocatedResources")
			}
		}
		// Mounts that the spec lists too, and the service account mount.
		if vms, ok := m["volumeMounts"].([]any); ok {
			inSpec := map[string]bool{}
			specMounts, _ := spec["volumeMounts"].([]any)
			for _, sv := range specMounts {
				if sm, ok := sv.(map[string]any); ok {
					inSpec[fmt.Sprint(sm["name"], "\x00", sm["mountPath"])] = true
				}
			}
			repeat := true
			for _, v := range vms {
				vm, _ := v.(map[string]any)
				name, _ := vm["name"].(string)
				if !inSpec[fmt.Sprint(vm["name"], "\x00", vm["mountPath"])] && !strings.HasPrefix(name, "kube-api-access-") {
					repeat = false
				}
			}
			if repeat {
				t.del(m, "volumeMounts")
			}
		}
	}
}

// shortDigest returns "sha256:" and 12 hex digits of an image ID.
func shortDigest(id string) string {
	i := strings.Index(id, "sha256:")
	if i < 0 {
		return ""
	}
	h := id[i+len("sha256:"):]
	n := 0
	for n < len(h) && n < 12 && isHex(h[n]) {
		n++
	}
	if n < 12 {
		return ""
	}
	return "sha256:" + h[:n]
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}

// conditions puts the healthy conditions on one line and keeps each
// condition that is not healthy in full.
func (t *trimmer) conditions(status map[string]any) {
	conds, ok := status["conditions"].([]any)
	if !ok {
		return
	}
	var bad []any
	var healthy []string
	for _, c := range conds {
		m, _ := c.(map[string]any)
		typ, _ := m["type"].(string)
		st, _ := m["status"].(string)
		if typ == "" || st == "" {
			return // not the usual shape: leave it all
		}
		reason, _ := m["reason"].(string)
		if ConditionHealthy(typ, st, reason) {
			healthy = append(healthy, typ)
			continue
		}
		for _, k := range []string{"lastProbeTime", "lastUpdateTime", "lastHeartbeatTime"} {
			t.del(m, k)
		}
		bad = append(bad, c)
	}
	if len(healthy) == 0 {
		status["conditions"] = bad
		return
	}
	t.rep.Dropped += len(healthy)
	if len(bad) == 0 {
		delete(status, "conditions")
	} else {
		status["conditions"] = bad
	}
	status["conditions ok"] = strings.Join(healthy, ", ")
}

// getMap follows a dotted path of map keys.
func getMap(obj map[string]any, path string) map[string]any {
	cur := obj
	for _, seg := range strings.Split(path, ".") {
		if cur == nil {
			return nil
		}
		cur, _ = cur[seg].(map[string]any)
	}
	return cur
}

func defaultPullPolicy(image string) string {
	ref := image
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return "IfNotPresent"
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i < 0 || ref[i+1:] == "latest" {
		return "Always"
	}
	return "IfNotPresent"
}

func num(v any) float64 {
	f, _ := numOK(v)
	return f
}

func numOK(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// keepEmpty are keys whose empty value means something: an emptyDir volume,
// and a selector that selects everything.
var keepEmpty = map[string]bool{"emptyDir": true, "podSelector": true, "namespaceSelector": true, "selector": true}

// prune removes nil values, empty maps and empty lists, recursively, except
// the empty maps of keepEmpty.
func prune(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		for k, c := range x {
			if prune(c) {
				if m, isMap := c.(map[string]any); isMap && keepEmpty[k] && len(m) == 0 {
					continue
				}
				delete(x, k)
			}
		}
		return len(x) == 0
	case []any:
		for _, e := range x {
			prune(e)
		}
		return len(x) == 0
	}
	return false
}

// YAML renders an object as YAML.
func YAML(obj map[string]any) string {
	b, err := yaml.Marshal(obj)
	if err != nil {
		return "(object could not be rendered)"
	}
	return strings.TrimRight(string(b), "\n")
}
