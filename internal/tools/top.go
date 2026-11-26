package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/shape"
)

// topListCap is how many pods top reads for the requests and limits.
const topListCap = 5000

type usageRow struct {
	ns, pod, container  string
	cpu, cpuReq, cpuLim int64 // millicores
	mem, memReq, memLim int64 // bytes
	cpuUtil, memUtil    float64
	cpuBasis, memBasis  string // "limit", "request" or ""
}

func (r *Runner) top(ctx context.Context, in TopInput) (output, error) {
	res := in.Resource
	if res == "" {
		res = "pods"
	}
	switch strings.ToLower(res) {
	case "pods", "pod", "po":
		res = "pods"
	case "nodes", "node", "no":
		res = "nodes"
	default:
		return output{}, badInput("resource must be pods or nodes")
	}
	sortBy := strings.ToLower(in.SortBy)
	if sortBy == "" {
		sortBy = "cpu"
	}
	if sortBy != "cpu" && sortBy != "memory" {
		return output{}, badInput("sortBy must be cpu or memory")
	}
	sel, err := selector(in.LabelSelector)
	if err != nil {
		return output{}, err
	}
	ns := ""
	if res == "pods" {
		pk, _ := r.kind("pods")
		if ns, err = r.namespaceFor(pk, in.Namespace, in.AllNamespaces); err != nil {
			return output{}, err
		}
	}
	out := output{namespace: ns}
	scope := "namespace " + ns
	if res == "nodes" {
		scope = "cluster"
	} else if ns == "" {
		scope = "all namespaces"
	}
	out.title = fmt.Sprintf("top %s · %s · context %s · sorted by %s utilization", res, scope, r.Context, sortBy)

	// The object list (for the requests and limits, or the node capacity)
	// does not depend on the usage, so both are read at the same time. When
	// the usage cannot be read, the list is not needed and is cancelled.
	type listRead struct {
		page kube.Page
		err  error
	}
	listF := start(ctx, func(c context.Context) listRead {
		if res == "nodes" {
			nk, _ := r.kind("nodes")
			p, err := r.Reader.List(c, nk, "", "", 5000)
			return listRead{p, err}
		}
		pk, _ := r.kind("pods")
		p, err := r.Reader.List(c, pk, ns, sel, topListCap)
		return listRead{p, err}
	})
	defer listF.drop()
	metrics, err := r.Reader.Metrics(ctx, kube.MetricsTarget(res), ns, sel)
	switch {
	case errors.Is(err, kube.ErrNoMetrics):
		out.summary = []string{"usage: not available"}
		out.notes = []string{"The metrics API (metrics.k8s.io) is not available in this cluster, so top cannot show CPU and memory usage. metrics-server provides this API. Usage is never read from the kubelet."}
		return out, nil
	case errors.Is(err, kube.ErrMetricsDown):
		out.summary = []string{"usage: not available"}
		out.notes = []string{"The metrics API (metrics.k8s.io) is registered but not answering, so top cannot show usage. metrics-server may be down; check its pods with list pods namespace=kube-system."}
		return out, nil
	case err != nil:
		return out, metricsFailure(err)
	}
	l := listF.wait()
	if res == "nodes" {
		return r.topNodes(out, metrics, l.page, l.err, sortBy)
	}
	return r.topPods(out, metrics, l.page, l.err, sortBy)
}

func (r *Runner) topPods(out output, metrics []unstructured.Unstructured, page kube.Page, err error, sortBy string) (output, error) {
	if err != nil {
		return out, err
	}
	specs := map[string]map[string]corev1.ResourceRequirements{}
	for _, u := range page.Items {
		var p corev1.Pod
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p) != nil {
			continue
		}
		m := map[string]corev1.ResourceRequirements{}
		for _, c := range p.Spec.Containers {
			m[c.Name] = c.Resources
		}
		// A native sidecar is in the metrics like any container.
		for _, c := range p.Spec.InitContainers {
			if isSidecar(&p, c.Name) {
				m[c.Name] = c.Resources
			}
		}
		specs[p.Namespace+"/"+p.Name] = m
	}
	var rows []usageRow
	for _, m := range metrics {
		cs, _, _ := unstructured.NestedSlice(m.Object, "containers")
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			name, _ := cm["name"].(string)
			usage, _ := cm["usage"].(map[string]any)
			use := containerUse{cpu: quantity(usage["cpu"], true), mem: quantity(usage["memory"], false)}
			rows = append(rows, newUsageRow(m.GetNamespace(), m.GetName(), name, use, specs[m.GetNamespace()+"/"+m.GetName()][name]))
		}
	}
	sortUsage(rows, sortBy)
	if page.More {
		out.notes = append(out.notes, fmt.Sprintf("the pod list was cut at %d; verdicts cover the pods read.", topListCap))
	}
	out.summary = []string{fmt.Sprintf("%s in %s with metrics", plural(len(rows), "container", "containers"), plural(len(metrics), "pod", "pods"))}
	if len(rows) == 0 {
		out.body = "no pod metrics found (new pods take about a minute to appear)"
		return out, nil
	}
	header := []string{"NAMESPACE", "POD", "CONTAINER", "CPU", "CPU REQ/LIM", "CPU%", "MEMORY", "MEM REQ/LIM", "MEM%", "VERDICT"}
	var cells [][]string
	warnings := map[string]int{}
	for _, u := range rows {
		fs := resourceFindings(u)
		for _, f := range fs {
			if f.warn {
				warnings[f.short]++
			}
		}
		cells = append(cells, []string{
			u.ns, u.pod, u.container,
			fmt.Sprintf("%dm", u.cpu), reqLim(u.cpuReq, u.cpuLim, true), pct(u.cpuUtil, u.cpuBasis),
			mib(u.mem), reqLim(u.memReq, u.memLim, false), pct(u.memUtil, u.memBasis), shorts(fs),
		})
	}
	if len(warnings) > 0 {
		names := make([]string, 0, len(warnings))
		for n := range warnings {
			names = append(names, n)
		}
		sort.Strings(names)
		for i, n := range names {
			names[i] = fmt.Sprintf("%d %s", warnings[n], n)
		}
		out.summary = append(out.summary, "verdicts: "+strings.Join(names, ", "))
	}
	r.fitTable(&out, header, cells, "containers")
	out.notes = append(out.notes,
		"CPU% and MEM% are usage divided by the limit; \"(req)\" means divided by the request because there is no limit; \"-\" means neither is set.",
		fmt.Sprintf("Verdicts: near OOM = memory at %d%% or more of its limit; CPU at limit = CPU at %d%% or more of its limit (throttling likely; the metrics API has no throttling counters); over-provisioned = the request is %d or more times the usage.", nearOOMPercent, cpuAtLimitPercent, overProvisioned))
	return out, nil
}

func (r *Runner) topNodes(out output, metrics []unstructured.Unstructured, page kube.Page, err error, sortBy string) (output, error) {
	if err != nil {
		return out, err
	}
	alloc := map[string]corev1.ResourceList{}
	for _, u := range page.Items {
		var n corev1.Node
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &n) == nil {
			alloc[n.Name] = n.Status.Allocatable
		}
	}
	var rows []usageRow
	for _, m := range metrics {
		usage, _, _ := unstructured.NestedMap(m.Object, "usage")
		row := usageRow{pod: m.GetName(), cpu: quantity(usage["cpu"], true), mem: quantity(usage["memory"], false)}
		if a, ok := alloc[m.GetName()]; ok {
			row.cpuLim, row.memLim = a.Cpu().MilliValue(), a.Memory().Value()
		}
		row.cpuUtil, row.cpuBasis = util(row.cpu, row.cpuLim, 0)
		row.memUtil, row.memBasis = util(row.mem, row.memLim, 0)
		rows = append(rows, row)
	}
	sortUsage(rows, sortBy)
	out.summary = []string{plural(len(rows), "node", "nodes") + " with metrics"}
	header := []string{"NODE", "CPU", "CPU ALLOCATABLE", "CPU%", "MEMORY", "MEM ALLOCATABLE", "MEM%"}
	var cells [][]string
	for _, u := range rows {
		cells = append(cells, []string{u.pod, fmt.Sprintf("%dm", u.cpu), fmt.Sprintf("%dm", u.cpuLim), pct(u.cpuUtil, u.cpuBasis), mib(u.mem), mib(u.memLim), pct(u.memUtil, u.memBasis)})
	}
	r.fitTable(&out, header, cells, "nodes")
	return out, nil
}

func (r *Runner) fitTable(out *output, header []string, cells [][]string, noun string) {
	shown := len(cells)
	body := shape.Table(header, cells)
	for len(body) > r.budget(false) && shown > 1 {
		shown = shown * 3 / 4
		body = shape.Table(header, cells[:shown])
	}
	out.body = body
	if shown < len(cells) {
		out.truncated = true
		out.notes = append(out.notes, fmt.Sprintf("showing the top %s of %s %s. Narrow with %s or %s.", shape.Thousands(shown), shape.Thousands(len(cells)), noun, r.argName("namespace"), r.selectorName()))
	}
}

func quantity(v any, cpu bool) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	if cpu {
		return q.MilliValue()
	}
	return q.Value()
}

func util(use, limit, request int64) (float64, string) {
	switch {
	case limit > 0:
		return float64(use) / float64(limit) * 100, "limit"
	case request > 0:
		return float64(use) / float64(request) * 100, "request"
	}
	return -1, ""
}

func sortUsage(rows []usageRow, by string) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if by == "memory" {
			if a.memUtil != b.memUtil {
				return a.memUtil > b.memUtil
			}
			return a.mem > b.mem
		}
		if a.cpuUtil != b.cpuUtil {
			return a.cpuUtil > b.cpuUtil
		}
		return a.cpu > b.cpu
	})
}

func pct(v float64, basis string) string {
	switch basis {
	case "limit":
		return fmt.Sprintf("%.0f%%", v)
	case "request":
		return fmt.Sprintf("%.0f%% (req)", v)
	}
	return "-"
}

func mib(b int64) string {
	if b <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dMi", b/(1<<20))
}

func reqLim(req, lim int64, cpu bool) string {
	f := func(v int64) string {
		if v <= 0 {
			return "-"
		}
		if cpu {
			return fmt.Sprintf("%dm", v)
		}
		return mib(v)
	}
	return f(req) + "/" + f(lim)
}

// metricsFailure keeps the errors that have a fixed text and maps any other
// error of the metrics API to one. An aggregated API writes its own error
// text, and an error is shown outside the fence of a result.
// An error of a known class is kept: the call shows a fixed phrase for the
// class, never the text (callError).
func metricsFailure(err error) error {
	switch classify(err) {
	case classUnknown, classNotFound:
		return kube.ErrMetricsFailed
	}
	return err
}
