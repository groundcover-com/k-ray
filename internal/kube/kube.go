// Package kube reads the cluster for the tools. The tools see only the
// Reader interface: no client, no rest.Config, no URL. The implementation is
// built from a readonly.Cluster, so every request passes the read-only
// transport.
package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

// ObjectRef names the object that events are about.
type ObjectRef struct {
	Kind string // for example "Pod"
	Name string
	UID  string // when set, the only selector used
}

// LogOptions are the pod log options kray uses. There is no follow.
type LogOptions struct {
	Container    string
	Previous     bool
	SinceSeconds int64
	TailLines    int64
	LimitBytes   int64
}

// Page is one list result.
type Page struct {
	Items []unstructured.Unstructured
	// More is true when the cluster has more objects than were fetched.
	More bool
	// Remaining is the API server's estimate of objects not fetched, or -1.
	Remaining int64
}

// MetaPage is one metadata-only list result: the metadata of each object
// (name, namespace, labels, annotations, owner references), and no spec,
// status or data. It is a separate type so that it cannot be taken for the
// full objects.
type MetaPage struct {
	Items     []metav1.PartialObjectMetadata
	More      bool  // the cluster has more objects than were fetched
	Remaining int64 // the API server's estimate of objects not fetched, or -1
}

// MetricsTarget is "pods" or "nodes".
type MetricsTarget string

const (
	PodMetrics  MetricsTarget = "pods"
	NodeMetrics MetricsTarget = "nodes"
)

var (
	// ErrNoMetrics: the cluster does not serve metrics.k8s.io.
	ErrNoMetrics = errors.New("the metrics API (metrics.k8s.io) is not available in this cluster")
	// ErrMetricsDown: metrics.k8s.io is registered but does not answer.
	ErrMetricsDown = errors.New("the metrics API (metrics.k8s.io) is registered but not answering")
	// ErrMetricsFailed: metrics.k8s.io answered with an error. The text of the
	// error is not kept: an aggregated API writes it, and it is shown outside
	// the fence of a result.
	ErrMetricsFailed = errors.New("the metrics API (metrics.k8s.io) returned an error")
	// ErrUnauthorized: the API server rejected the credentials (HTTP 401).
	ErrUnauthorized = errors.New("the cluster rejected the credentials (HTTP 401); if they came from the init command, the token may have expired and a person must run the init command with --refresh")
)

// Reader is everything the tools may ask of the cluster.
type Reader interface {
	Get(ctx context.Context, k kinds.Kind, ns, name string) (*unstructured.Unstructured, error)
	List(ctx context.Context, k kinds.Kind, ns, labelSelector string, max int) (Page, error)
	// ListMeta reads the metadata of up to max objects of a kind, with the
	// same pages and caps as List. A search by name uses it, so that it does
	// not hold the bodies of a whole kind in memory.
	ListMeta(ctx context.Context, k kinds.Kind, ns string, max int) (MetaPage, error)
	Events(ctx context.Context, ns string, about *ObjectRef, max int) (Page, error)
	Logs(ctx context.Context, ns, pod string, o LogOptions) (string, error)
	Metrics(ctx context.Context, target MetricsTarget, ns, labelSelector string) ([]unstructured.Unstructured, error)
}

// Client implements Reader with client-go.
type Client struct {
	dyn  dynamic.Interface
	meta metadata.Interface
	cs   kubernetes.Interface
	disc discovery.DiscoveryInterface
}

// New builds the clients from the read-only cluster configuration.
func New(c *readonly.Cluster) (*Client, error) {
	cfg := c.RESTConfig()
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	// The metadata client sends GET with the Accept header of a metadata list
	// (application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1). It
	// is built from the same read-only config, so its requests pass the same
	// transport check.
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{dyn: dyn, meta: meta, cs: cs, disc: disc}, nil
}

const pageSize = 500

func (c *Client) resource(gvr schema.GroupVersionResource, namespaced bool, ns string) dynamic.ResourceInterface {
	r := c.dyn.Resource(gvr)
	if namespaced && ns != "" {
		return r.Namespace(ns)
	}
	return r
}

// Get reads one object.
func (c *Client) Get(ctx context.Context, k kinds.Kind, ns, name string) (*unstructured.Unstructured, error) {
	u, err := c.resource(k.GVR(), k.Namespaced(), ns).Get(ctx, name, metav1.GetOptions{})
	return u, mapErr(err)
}

// List reads up to max objects, page by page (at most 10 pages).
func (c *Client) List(ctx context.Context, k kinds.Kind, ns, sel string, max int) (Page, error) {
	return c.list(ctx, k.GVR(), k.Namespaced(), ns, metav1.ListOptions{LabelSelector: sel}, max)
}

func (c *Client) list(ctx context.Context, gvr schema.GroupVersionResource, namespaced bool, ns string, opts metav1.ListOptions, max int) (Page, error) {
	p := Page{Remaining: -1}
	if max <= 0 {
		return p, nil
	}
	r := c.resource(gvr, namespaced, ns)
	for pages := 0; pages < 10; pages++ {
		opts.Limit = int64(min(pageSize, max-len(p.Items)))
		l, err := r.List(ctx, opts)
		if err != nil {
			return p, mapErr(err)
		}
		p.Items = append(p.Items, l.Items...)
		cont := l.GetContinue()
		if cont == "" {
			return p, nil
		}
		if len(p.Items) >= max || pages == 9 {
			p.More = true
			if rem := l.GetRemainingItemCount(); rem != nil {
				p.Remaining = *rem
			}
			return p, nil
		}
		opts.Continue = cont
	}
	return p, nil
}

// ListMeta reads the metadata of up to max objects, page by page (at most 10
// pages), as List does.
func (c *Client) ListMeta(ctx context.Context, k kinds.Kind, ns string, max int) (MetaPage, error) {
	p := MetaPage{Remaining: -1}
	if max <= 0 {
		return p, nil
	}
	g := c.meta.Resource(k.GVR())
	var r metadata.ResourceInterface = g
	if k.Namespaced() && ns != "" {
		r = g.Namespace(ns)
	}
	opts := metav1.ListOptions{}
	for pages := 0; pages < 10; pages++ {
		opts.Limit = int64(min(pageSize, max-len(p.Items)))
		l, err := r.List(ctx, opts)
		if err != nil {
			return p, mapErr(err)
		}
		p.Items = append(p.Items, l.Items...)
		if l.Continue == "" {
			return p, nil
		}
		if len(p.Items) >= max || pages == 9 {
			p.More = true
			if rem := l.RemainingItemCount; rem != nil {
				p.Remaining = *rem
			}
			return p, nil
		}
		opts.Continue = l.Continue
	}
	return p, nil
}

var eventsGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// Events lists core/v1 events in ns ("" for all), optionally about one object.
func (c *Client) Events(ctx context.Context, ns string, about *ObjectRef, max int) (Page, error) {
	opts := metav1.ListOptions{}
	if about != nil {
		// Fixed order; values are escaped by the fields package.
		if about.UID != "" {
			opts.FieldSelector = fields.OneTermEqualSelector("involvedObject.uid", about.UID).String()
		} else {
			opts.FieldSelector = fields.AndSelectors(
				fields.OneTermEqualSelector("involvedObject.kind", about.Kind),
				fields.OneTermEqualSelector("involvedObject.name", about.Name),
			).String()
		}
	}
	return c.list(ctx, eventsGVR, true, ns, opts, max)
}

// Logs reads pod logs with timestamps, up to LimitBytes.
func (c *Client) Logs(ctx context.Context, ns, pod string, o LogOptions) (string, error) {
	opts := &corev1.PodLogOptions{Container: o.Container, Previous: o.Previous, Timestamps: true}
	if o.TailLines > 0 {
		opts.TailLines = &o.TailLines
	}
	if o.LimitBytes > 0 {
		opts.LimitBytes = &o.LimitBytes
	}
	if o.SinceSeconds > 0 {
		opts.SinceSeconds = &o.SinceSeconds
	}
	rc, err := c.cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return "", mapErr(err)
	}
	defer rc.Close()
	limit := o.LimitBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	b, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return string(b), err
	}
	return string(b), nil
}

const metricsGroup = "metrics.k8s.io"

// metricsVersion asks which version of metrics.k8s.io the cluster serves. It
// makes one request, GET /apis, in the plain format: the aggregated format of
// the discovery client lists every resource of every group, which is large
// and not needed here. The context ends the request.
func (c *Client) metricsVersion(ctx context.Context) (string, error) {
	var groups metav1.APIGroupList
	err := c.disc.RESTClient().Get().AbsPath("/apis").SetHeader("Accept", "application/json").Do(ctx).Into(&groups)
	if err != nil {
		return "", mapErr(err)
	}
	var versions []string
	for _, g := range groups.Groups {
		if g.Name != metricsGroup {
			continue
		}
		for _, v := range g.Versions {
			versions = append(versions, v.Version)
		}
	}
	for _, want := range []string{"v1", "v1beta1"} {
		for _, v := range versions {
			if v == want {
				return v, nil
			}
		}
	}
	return "", ErrNoMetrics
}

// Metrics lists pod or node usage from metrics.k8s.io (v1 when served,
// else v1beta1). It never falls back to the kubelet.
func (c *Client) Metrics(ctx context.Context, target MetricsTarget, ns, sel string) ([]unstructured.Unstructured, error) {
	v, err := c.metricsVersion(ctx)
	if err != nil {
		return nil, err
	}
	gvr := schema.GroupVersionResource{Group: metricsGroup, Version: v, Resource: string(target)}
	p, err := c.list(ctx, gvr, target == PodMetrics, ns, metav1.ListOptions{LabelSelector: sel}, 5000)
	if err != nil {
		var se *apierrors.StatusError
		switch {
		case apierrors.IsNotFound(err):
			return nil, ErrNoMetrics
		case apierrors.IsServiceUnavailable(err), errors.As(err, &se) && se.Status().Code == http.StatusServiceUnavailable:
			return nil, ErrMetricsDown
		case apierrors.IsUnauthorized(err), errors.Is(err, readonly.ErrBlocked), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, mapErr(err)
		}
		return nil, ErrMetricsFailed
	}
	return p.Items, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if apierrors.IsUnauthorized(err) {
		return ErrUnauthorized
	}
	if errors.Is(err, readonly.ErrBlocked) {
		return fmt.Errorf("%w", err)
	}
	return err
}
