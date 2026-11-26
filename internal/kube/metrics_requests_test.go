package kube

import (
	"context"
	"strings"
	"testing"
)

// Usage costs two requests: which version of the metrics API the cluster
// serves (the plain group list, not the aggregated discovery of every
// resource), then the list. The discovery request stops with the context.
func TestMetricsMakesTwoRequests(t *testing.T) {
	a := newAPI(t)
	a.metricsGroup = "v1beta1"
	c := a.client(t)
	if _, err := c.Metrics(context.Background(), PodMetrics, "shop", ""); err != nil {
		t.Fatal(err)
	}
	reqs := a.requests()
	if len(reqs) != 2 || !strings.HasPrefix(reqs[0], "GET /apis?") || !strings.HasPrefix(reqs[1], "GET /apis/metrics.k8s.io/v1beta1/namespaces/shop/pods") {
		t.Errorf("requests: %v", reqs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(a.requests())
	if _, err := c.Metrics(ctx, PodMetrics, "shop", ""); err == nil {
		t.Error("a cancelled context must stop the metrics read")
	}
	if n := len(a.requests()); n != before {
		t.Errorf("%d requests after the context ended", n-before)
	}
}
