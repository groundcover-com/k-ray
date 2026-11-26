package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

// A read that fails is named in the answer, with a fixed reason. The text of
// the error comes from the cluster (an aggregated API writes its own), so it is
// never printed outside the fence: the reason is chosen by the class of the
// error. The answer never says "none" or "no problem" for what it could not read.

// errClass is the class of a failed read. The class is chosen by the type
// and the status code of the error, never by its text.
type errClass int

const (
	classUnknown errClass = iota
	classTimeout
	classCanceled
	classInternal
	classBlocked
	classUnauthorized
	classForbidden
	classNotFound
	classTooMany
	classUnavailable
	classServerError
	classBadRequest
	classExpired
	classHTTP
	classNetwork
)

func classify(err error) errClass {
	switch {
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return classTimeout
	case errors.Is(err, context.Canceled):
		return classCanceled
	case errors.Is(err, errInternal):
		return classInternal
	case errors.Is(err, readonly.ErrBlocked):
		return classBlocked
	case errors.Is(err, kube.ErrUnauthorized), apierrors.IsUnauthorized(err):
		return classUnauthorized
	case apierrors.IsForbidden(err):
		return classForbidden
	case apierrors.IsNotFound(err):
		return classNotFound
	case apierrors.IsTooManyRequests(err):
		return classTooMany
	case apierrors.IsServiceUnavailable(err):
		return classUnavailable
	case apierrors.IsInternalError(err):
		return classServerError
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err), apierrors.IsMethodNotSupported(err),
		apierrors.IsNotAcceptable(err), apierrors.IsUnsupportedMediaType(err):
		return classBadRequest
	case apierrors.IsGone(err), apierrors.IsResourceExpired(err):
		return classExpired
	}
	if statusCode(err) != 0 {
		return classHTTP
	}
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return classNetwork
	}
	return classUnknown
}

// statusCode is the HTTP status code of an API status error, or 0. A number
// cannot carry an instruction, so it is safe to show.
func statusCode(err error) int32 {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return st.Status().Code
	}
	return 0
}

// failureReason is the fixed reason for a failed list or events read.
func failureReason(err error) string {
	switch classify(err) {
	case classTimeout:
		return "timed out"
	case classCanceled:
		return "the call was stopped"
	case classInternal:
		return "internal error"
	case classBlocked:
		return "blocked as not read-only"
	case classUnauthorized:
		return "credentials rejected"
	case classForbidden:
		return "not allowed"
	case classNotFound:
		return "not found"
	case classTooMany:
		return "too many requests"
	case classUnavailable:
		return "service unavailable"
	case classServerError:
		return "server error"
	case classBadRequest:
		return "bad request"
	case classExpired:
		return "expired"
	case classHTTP:
		return fmt.Sprintf("HTTP %d", statusCode(err))
	case classNetwork:
		return "cluster not reached"
	}
	return "the read failed"
}

// request is what the caller asked for: the kind, name and namespace from the
// arguments of the call, never from the cluster or from an error.
type request struct{ tool, kind, name, namespace string }

// requestOf reads the request of a call from its raw arguments. It runs only
// after a tool failed, when the arguments decoded already.
func (r *Runner) requestOf(tool string, raw []byte) request {
	var a struct{ Kind, Name, Namespace, Pod, Workload, Resource string }
	_ = json.Unmarshal(raw, &a)
	q := request{tool: tool, name: a.Name, namespace: a.Namespace}
	kind := func(s string) string {
		if r.Kinds == nil || s == "" {
			return ""
		}
		if k, err := r.Kinds.Lookup(s); err == nil {
			return k.Name()
		}
		return ""
	}
	switch tool {
	case "list", "get", "describe":
		q.kind = kind(a.Kind)
	case "events":
		q.kind, q.name = "events", ""
	case "logs":
		q.kind, q.name = "pods", a.Pod
		if a.Workload != "" {
			k, n, _ := strings.Cut(a.Workload, "/")
			q.kind, q.name = kind(k), n
		}
	case "top":
		q.kind, q.name = "pods and their usage (metrics.k8s.io)", ""
		if strings.EqualFold(a.Resource, "nodes") {
			q.kind, q.namespace = "nodes and their usage (metrics.k8s.io)", ""
		}
	}
	return q
}

// asked is `; asked for KIND "NAME" in namespace NS`, or "".
func (q request) asked() string {
	var parts []string
	if q.kind != "" {
		parts = append(parts, q.kind)
	}
	if q.name != "" {
		parts = append(parts, strconv.Quote(clip(q.name)))
	}
	if q.namespace != "" {
		parts = append(parts, "in namespace "+clip(q.namespace))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; asked for " + strings.Join(parts, " ")
}

// notFound is `KIND "NAME" not found`, from the request: the same words as
// the error of the API, which does not name the namespace either.
func (q request) notFound() string {
	if q.name == "" {
		return "not found (HTTP 404)" + q.asked() + "; the cluster may not serve this kind"
	}
	kind := q.kind
	if kind == "" {
		kind = "object"
	}
	return kind + " " + strconv.Quote(clip(q.name)) + " not found"
}

// errText is the text of an error when the request is not known.
func errText(err error) string { return callError(err, request{}) }

// callError is the text of an error that ends a call, or one block of it. The
// messages of this server stay whole, because they say what to do next:
// argument errors, the allowed kinds, not-found with the closest names, and the
// fixed errors of the kube package. An error from the API server, the kubelet
// or the network becomes a fixed phrase for its class, with what the caller
// asked: the cluster writes its text, and the text is shown outside the fence.
func callError(err error, q request) string {
	var in errInput
	if errors.As(err, &in) {
		return in.msg
	}
	for _, own := range []error{errInternal, kube.ErrUnauthorized, kube.ErrNoMetrics, kube.ErrMetricsDown, kube.ErrMetricsFailed} {
		if errors.Is(err, own) {
			return own.Error()
		}
	}
	var nf notFoundError
	if errors.As(err, &nf) {
		// The text after the error of the API is this server's own: that all
		// namespaces were read, and the closest names.
		rest := ""
		if nf.err != nil && strings.HasPrefix(nf.msg, nf.err.Error()) {
			rest = strings.TrimPrefix(nf.msg, nf.err.Error())
		}
		return q.notFound() + rest
	}
	switch classify(err) {
	case classTimeout:
		return "the cluster did not answer in time (timed out)" + q.asked() + "; call again, or narrow the call"
	case classCanceled:
		return "the call was stopped"
	case classBlocked:
		var be *readonly.BlockedError
		if errors.As(err, &be) {
			// The method, path and reason are written by the read-only
			// transport, from the request that this server made.
			return "the read-only transport blocked a request: " + be.Error()
		}
		return "the read-only transport blocked a request"
	case classUnauthorized:
		return kube.ErrUnauthorized.Error()
	case classForbidden:
		return "forbidden (HTTP 403): the credentials are not allowed a read that this call needs" + q.asked() + "; a person can grant get and list in RBAC"
	case classNotFound:
		return q.notFound()
	case classTooMany:
		return "too many requests (HTTP 429): the cluster is busy" + q.asked() + "; call again later"
	case classUnavailable:
		return "service unavailable (HTTP 503): the API that serves this read is not answering" + q.asked() + "; call again later"
	case classServerError:
		return "server error (HTTP 500) in the cluster" + q.asked() + "; call again later"
	case classBadRequest:
		if q.tool == "logs" {
			return fmt.Sprintf("bad request (HTTP %d): the cluster refused the log read%s; a container that has not started has no logs: call describe for the pod", statusCode(err), q.asked())
		}
		return fmt.Sprintf("bad request (HTTP %d): the cluster refused the request%s", statusCode(err), q.asked())
	case classExpired:
		return "expired (HTTP 410): the list changed while it was read" + q.asked() + "; call again"
	case classHTTP:
		return fmt.Sprintf("the cluster returned an error (HTTP %d)%s", statusCode(err), q.asked())
	case classNetwork:
		return "the cluster could not be reached (network or TLS error)" + q.asked() + "; a person must check the connection to the cluster"
	}
	// The plain not-found error of this server has no type. The text only
	// selects the phrase; the phrase is built from the request.
	if q.name != "" && strings.HasSuffix(err.Error(), strconv.Quote(q.name)+" not found") {
		return q.notFound()
	}
	return "the read failed" + q.asked() + "; the text of the error is not shown, because the cluster writes it"
}

// metricsReason is the fixed reason when the usage could not be read.
func metricsReason(err error) string {
	switch {
	case errors.Is(err, kube.ErrNoMetrics), errors.Is(err, kube.ErrMetricsDown):
		return "no metrics API"
	case apierrors.IsForbidden(err):
		return "not allowed"
	}
	return "the read failed"
}

// readFailures collects the failed reads of the relations of one object. The
// relations run at the same time, so it is safe for concurrent use.
type readFailures struct {
	mu    sync.Mutex
	items []failedRead
}

type failedRead struct{ resource, reason string }

type failuresKey struct{}

func withFailures(ctx context.Context, f *readFailures) context.Context {
	return context.WithValue(ctx, failuresKey{}, f)
}

// noteFailure records a failed read when the context collects them.
func noteFailure(ctx context.Context, resource string, err error) {
	f, _ := ctx.Value(failuresKey{}).(*readFailures)
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.items {
		if it.resource == resource {
			return
		}
	}
	f.items = append(f.items, failedRead{resource, failureReason(err)})
}

// line is "kind, kind (reason); kind (reason)", in a fixed order, or "".
func (f *readFailures) line() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.items) == 0 {
		return ""
	}
	var reasons []string
	by := map[string][]string{}
	for _, it := range f.items {
		if _, ok := by[it.reason]; !ok {
			reasons = append(reasons, it.reason)
		}
		by[it.reason] = append(by[it.reason], it.resource)
	}
	// Sorted, so that the order does not depend on which read ended first.
	sort.Strings(reasons)
	var parts []string
	for _, rs := range reasons {
		names := by[rs]
		sort.Strings(names)
		parts = append(parts, strings.Join(names, ", ")+" ("+rs+")")
	}
	return "related, not read: " + strings.Join(parts, "; ")
}
