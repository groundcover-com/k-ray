package shape

// This file keeps the trimming as it was before the structure-aware compaction,
// for the size comparison in TestCompactSizes only. Nothing else may use it.

import "strings"

// legacyTrim is the old Trim, unchanged.
func legacyTrim(obj map[string]any, full bool) {
	if md, ok := obj["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
		if ann, ok := md["annotations"].(map[string]any); ok {
			delete(ann, LastApplied)
		}
		if !full {
			for _, k := range []string{"generation", "resourceVersion", "uid", "selfLink"} {
				delete(md, k)
			}
			if ann, ok := md["annotations"].(map[string]any); ok {
				delete(ann, "deployment.kubernetes.io/revision")
			}
		}
	}
	if !full {
		legacyTrimDefaults(obj)
	}
	prune(obj)
}

func legacyTrimDefaults(obj map[string]any) {
	kind, _ := obj["kind"].(string)
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)

	switch kind {
	case "Pod":
		legacyTrimPodSpec(spec, true)
	case "Deployment":
		legacyDelIfEq(spec, "progressDeadlineSeconds", 600)
		legacyDelIfEq(spec, "revisionHistoryLimit", 10)
		if st, ok := spec["strategy"].(map[string]any); ok && st["type"] == "RollingUpdate" {
			ru, _ := st["rollingUpdate"].(map[string]any)
			if ru == nil || (ru["maxSurge"] == "25%" && ru["maxUnavailable"] == "25%") {
				delete(spec, "strategy")
			}
		}
		legacyTrimTemplate(spec)
	case "StatefulSet":
		legacyDelIfEq(spec, "podManagementPolicy", "OrderedReady")
		legacyDelIfEq(spec, "revisionHistoryLimit", 10)
		if us, ok := spec["updateStrategy"].(map[string]any); ok && us["type"] == "RollingUpdate" {
			ru, _ := us["rollingUpdate"].(map[string]any)
			if ru == nil || num(ru["partition"]) == 0 {
				delete(spec, "updateStrategy")
			}
		}
		if p, ok := spec["persistentVolumeClaimRetentionPolicy"].(map[string]any); ok && p["whenDeleted"] == "Retain" && p["whenScaled"] == "Retain" {
			delete(spec, "persistentVolumeClaimRetentionPolicy")
		}
		legacyTrimTemplate(spec)
	case "DaemonSet":
		legacyDelIfEq(spec, "revisionHistoryLimit", 10)
		if us, ok := spec["updateStrategy"].(map[string]any); ok && us["type"] == "RollingUpdate" {
			ru, _ := us["rollingUpdate"].(map[string]any)
			if ru == nil || (num(ru["maxSurge"]) == 0 && num(ru["maxUnavailable"]) == 1) {
				delete(spec, "updateStrategy")
			}
		}
		legacyTrimTemplate(spec)
	case "ReplicaSet":
		legacyTrimTemplate(spec)
	case "Job":
		legacyTrimJob(spec)
	case "CronJob":
		legacyDelIfEq(spec, "concurrencyPolicy", "Allow")
		legacyDelIfEq(spec, "successfulJobsHistoryLimit", 3)
		legacyDelIfEq(spec, "failedJobsHistoryLimit", 1)
		legacyDelIfEq(spec, "suspend", false)
		if jt, ok := spec["jobTemplate"].(map[string]any); ok {
			if js, ok := jt["spec"].(map[string]any); ok {
				legacyTrimJob(js)
			}
		}
	case "Service":
		legacyDelIfEq(spec, "sessionAffinity", "None")
		legacyDelIfEq(spec, "internalTrafficPolicy", "Cluster")
		if spec["ipFamilyPolicy"] == "SingleStack" {
			delete(spec, "ipFamilyPolicy")
			delete(spec, "ipFamilies")
		}
		if ips, ok := spec["clusterIPs"].([]any); ok && len(ips) == 1 && ips[0] == spec["clusterIP"] {
			delete(spec, "clusterIPs")
		}
		legacyTrimPorts(spec["ports"])
	case "Node":
		delete(status, "images")
		if conds, ok := status["conditions"].([]any); ok {
			for _, c := range conds {
				if m, ok := c.(map[string]any); ok {
					delete(m, "lastHeartbeatTime")
				}
			}
		}
	}
	if conds, ok := status["conditions"].([]any); ok {
		for _, c := range conds {
			if m, ok := c.(map[string]any); ok {
				delete(m, "lastProbeTime")
				delete(m, "lastUpdateTime")
			}
		}
	}
}

func legacyTrimJob(spec map[string]any) {
	legacyDelIfEq(spec, "backoffLimit", 6)
	legacyDelIfEq(spec, "completionMode", "NonIndexed")
	legacyDelIfEq(spec, "suspend", false)
	legacyDelIfEq(spec, "podReplacementPolicy", "TerminatingOrFailed")
	legacyDelIfEq(spec, "manualSelector", false)
	if sel, ok := spec["selector"].(map[string]any); ok {
		if ml, ok := sel["matchLabels"].(map[string]any); ok && len(sel) == 1 {
			auto := true
			for k := range ml {
				if !strings.Contains(k, "controller-uid") {
					auto = false
				}
			}
			if auto {
				delete(spec, "selector")
			}
		}
	}
	if tpl, ok := spec["template"].(map[string]any); ok {
		if md, ok := tpl["metadata"].(map[string]any); ok {
			if l, ok := md["labels"].(map[string]any); ok {
				for k := range l {
					if strings.Contains(k, "controller-uid") || k == "job-name" || strings.HasPrefix(k, "batch.kubernetes.io/") {
						delete(l, k)
					}
				}
			}
		}
	}
	legacyTrimTemplate(spec)
}

func legacyTrimTemplate(spec map[string]any) {
	tpl, ok := spec["template"].(map[string]any)
	if !ok {
		return
	}
	if md, ok := tpl["metadata"].(map[string]any); ok {
		delete(md, "creationTimestamp")
	}
	ps, _ := tpl["spec"].(map[string]any)
	legacyTrimPodSpec(ps, false)
}

func legacyTrimPodSpec(ps map[string]any, isPod bool) {
	if ps == nil {
		return
	}
	legacyDelIfEq(ps, "dnsPolicy", "ClusterFirst")
	legacyDelIfEq(ps, "restartPolicy", "Always")
	legacyDelIfEq(ps, "schedulerName", "default-scheduler")
	legacyDelIfEq(ps, "terminationGracePeriodSeconds", 30)
	legacyDelIfEq(ps, "enableServiceLinks", true)
	legacyDelIfEq(ps, "preemptionPolicy", "PreemptLowerPriority")
	legacyDelIfEq(ps, "priority", 0)
	if ps["serviceAccount"] == ps["serviceAccountName"] {
		delete(ps, "serviceAccount")
	}
	legacyDelIfEq(ps, "serviceAccountName", "default")
	if tols, ok := ps["tolerations"].([]any); ok {
		var kept []any
		for _, t := range tols {
			m, _ := t.(map[string]any)
			k, _ := m["key"].(string)
			if (k == "node.kubernetes.io/not-ready" || k == "node.kubernetes.io/unreachable") && m["effect"] == "NoExecute" && num(m["tolerationSeconds"]) == 300 {
				continue
			}
			kept = append(kept, t)
		}
		ps["tolerations"] = kept
	}
	apiAccess := map[string]bool{}
	if vols, ok := ps["volumes"].([]any); ok {
		var kept []any
		for _, v := range vols {
			m, _ := v.(map[string]any)
			name, _ := m["name"].(string)
			if strings.HasPrefix(name, "kube-api-access-") {
				apiAccess[name] = true
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
			legacyDelIfEq(m, "terminationMessagePath", "/dev/termination-log")
			legacyDelIfEq(m, "terminationMessagePolicy", "File")
			if img, _ := m["image"].(string); m["imagePullPolicy"] == legacyDefaultPullPolicy(img) {
				delete(m, "imagePullPolicy")
			}
			for _, p := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
				if pr, ok := m[p].(map[string]any); ok {
					legacyDelIfEq(pr, "timeoutSeconds", 1)
					legacyDelIfEq(pr, "periodSeconds", 10)
					legacyDelIfEq(pr, "successThreshold", 1)
					legacyDelIfEq(pr, "failureThreshold", 3)
					if hg, ok := pr["httpGet"].(map[string]any); ok {
						legacyDelIfEq(hg, "scheme", "HTTP")
					}
				}
			}
			legacyTrimPorts(m["ports"])
			if vms, ok := m["volumeMounts"].([]any); ok {
				var kept []any
				for _, vm := range vms {
					vmm, _ := vm.(map[string]any)
					if n, _ := vmm["name"].(string); apiAccess[n] {
						continue
					}
					kept = append(kept, vm)
				}
				m["volumeMounts"] = kept
			}
		}
	}
	_ = isPod
}

func legacyTrimPorts(v any) {
	ports, _ := v.([]any)
	for _, p := range ports {
		if m, ok := p.(map[string]any); ok {
			legacyDelIfEq(m, "protocol", "TCP")
		}
	}
}

func legacyDefaultPullPolicy(image string) string {
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

func legacyDelIfEq(m map[string]any, key string, want any) {
	if m == nil {
		return
	}
	v, ok := m[key]
	if !ok {
		return
	}
	switch w := want.(type) {
	case int:
		if f, ok := numOK(v); ok && f == float64(w) {
			delete(m, key)
		}
	default:
		if v == want {
			delete(m, key)
		}
	}
}
