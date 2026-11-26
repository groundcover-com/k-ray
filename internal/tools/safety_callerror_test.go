package tools

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
)

// failAll fails every read with one error.
type failAll struct{ err error }

func (f failAll) Get(context.Context, kinds.Kind, string, string) (*unstructured.Unstructured, error) {
	return nil, f.err
}

func (f failAll) List(context.Context, kinds.Kind, string, string, int) (kube.Page, error) {
	return kube.Page{}, f.err
}

func (f failAll) ListMeta(context.Context, kinds.Kind, string, int) (kube.MetaPage, error) {
	return kube.MetaPage{}, f.err
}

func (f failAll) Events(context.Context, string, *kube.ObjectRef, int) (kube.Page, error) {
	return kube.Page{}, f.err
}

func (f failAll) Logs(context.Context, string, string, kube.LogOptions) (string, error) {
	return "", f.err
}

func (f failAll) Metrics(context.Context, kube.MetricsTarget, string, string) ([]unstructured.Unstructured, error) {
	return nil, f.err
}

// statusErr is an API status error whose message is errMarker.
func statusErr(code int32, reason metav1.StatusReason) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: code, Reason: reason, Message: errMarker}}
}

// When a whole call fails, the result says a fixed phrase for the
// class of the error. The text of the error comes from the API server, the
// kubelet or the network, so it is never in the result.
func TestAWholeCallFailureNamesTheClassAndNotTheText(t *testing.T) {
	classes := []struct {
		name  string
		err   error
		class string // in the result of every tool but top
		top   string // in the result of top (the metrics API maps unknown errors)
	}{
		{"plain", errors.New(errMarker), "the read failed", "metrics api"},
		{"forbidden", forbidden("pods"), "forbidden (http 403)", "forbidden (http 403)"},
		{"not found", statusErr(404, metav1.StatusReasonNotFound), "not found", "metrics api"},
		{"unauthorized", apierrors.NewUnauthorized(errMarker), "rejected the credentials", "rejected the credentials"},
		{"server error", apierrors.NewInternalError(errors.New(errMarker)), "server error (http 500)", "server error (http 500)"},
		{"unavailable", apierrors.NewServiceUnavailable(errMarker), "service unavailable (http 503)", "service unavailable (http 503)"},
		{"too many", apierrors.NewTooManyRequests(errMarker, 1), "too many requests (http 429)", "too many requests (http 429)"},
		{"bad request", apierrors.NewBadRequest(errMarker), "bad request (http 400)", "bad request (http 400)"},
		{"server timeout", apierrors.NewTimeoutError(errMarker, 1), "did not answer in time", "did not answer in time"},
		{"expired", apierrors.NewResourceExpired(errMarker), "expired (http 410)", "expired (http 410)"},
		{"other status", statusErr(418, metav1.StatusReasonUnknown), "http 418", "http 418"},
		{"network", &url.Error{Op: "Get", URL: "https://" + errMarker + "/api", Err: errors.New(errMarker)}, "could not be reached", "could not be reached"},
	}
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"list", map[string]any{"kind": "pods", "namespace": "shop"}},
		{"get", map[string]any{"kind": "pods", "name": "web-1", "namespace": "shop"}},
		{"describe", map[string]any{"kind": "deployments", "name": "web", "namespace": "shop"}},
		{"describe", map[string]any{"name": "web"}},
		{"logs", map[string]any{"pod": "web-1", "namespace": "shop"}},
		{"events", map[string]any{"namespace": "shop"}},
		{"top", map[string]any{}},
	}
	for _, c := range classes {
		for _, k := range calls {
			for _, surface := range []string{"mcp", "cli"} {
				r, _ := newRunner(t, nil)
				r.Reader = failAll{c.err}
				r.Surface = surface
				res := call(t, r, k.tool, k.args)
				where := c.name + " / " + k.tool + " / " + surface
				if strings.Contains(res.Text, errMarker) {
					t.Errorf("%s: the error text is in the result:\n%s", where, res.Text)
				}
				want := c.class
				if k.tool == "top" {
					want = c.top
				}
				if !strings.Contains(strings.ToLower(res.Text), want) {
					t.Errorf("%s: the result does not name the class %q:\n%s", where, want, res.Text)
				}
				if !res.IsError {
					t.Errorf("%s: not an error:\n%s", where, res.Text)
				}
			}
		}
	}
}

// The detail of a fixed phrase is built from what the caller asked, never from
// the error.
func TestAWholeCallFailureSaysWhatWasAsked(t *testing.T) {
	r, _ := newRunner(t, nil)
	r.Reader = failAll{forbidden("pods")}
	res := call(t, r, "get", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, res.Text, `forbidden (HTTP 403)`, `asked for pods "web-1" in namespace shop`)

	r.Reader = failAll{statusErr(404, metav1.StatusReasonNotFound)}
	res = call(t, r, "get", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"})
	mustContain(t, res.Text, `pods "web-1" not found`)

	r.Reader = failAll{apierrors.NewBadRequest(errMarker)}
	res = call(t, r, "logs", map[string]any{"pod": "web-1", "namespace": "shop"})
	mustContain(t, res.Text, "bad request (HTTP 400)", "a container that has not started has no logs")
	mustNotContain(t, res.Text, errMarker)
}

// The messages of the tool itself stay as they are: they tell the caller what
// to do next.
func TestOwnMessagesStayWhole(t *testing.T) {
	r, _ := newRunner(t, &fakeReader{})
	res := call(t, r, "list", map[string]any{"kind": "secrets"})
	mustContain(t, res.Text, "never reads Secret objects")
	res = call(t, r, "list", map[string]any{"kind": "nothing-like-it"})
	mustContain(t, res.Text, "allowed kinds")
	res = call(t, r, "get", map[string]any{"kind": "pods"})
	if !res.IsError || strings.Contains(res.Text, "the read failed") {
		t.Fatalf("a validation error lost its text:\n%s", res.Text)
	}
	r.Reader = failAll{kube.ErrUnauthorized}
	res = call(t, r, "list", map[string]any{"kind": "pods"})
	mustContain(t, res.Text, kube.ErrUnauthorized.Error())
}

// The phrase of every class has no text of the error, in the short form of a
// failed related read and in the long form of a failed call.
func TestErrorPhrasesNeverQuoteTheError(t *testing.T) {
	for _, err := range []error{
		errors.New(errMarker), forbidden("pods"), statusErr(404, metav1.StatusReasonNotFound),
		apierrors.NewInternalError(errors.New(errMarker)), apierrors.NewServiceUnavailable(errMarker),
		apierrors.NewTooManyRequests(errMarker, 1), apierrors.NewBadRequest(errMarker),
		apierrors.NewTimeoutError(errMarker, 1), apierrors.NewResourceExpired(errMarker),
		statusErr(418, metav1.StatusReasonUnknown), &url.Error{Op: "Get", URL: errMarker, Err: errors.New(errMarker)},
	} {
		for _, s := range []string{
			failureReason(err), errText(err),
			callError(err, request{tool: "describe", kind: "deployments", name: "web", namespace: "shop"}),
		} {
			if strings.Contains(s, errMarker) || s == "" {
				t.Errorf("%T: %q", err, s)
			}
		}
	}
}
