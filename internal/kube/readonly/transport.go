package readonly

import (
	"context"
	"net/http"
	"sync/atomic"
)

type counterKey struct{}

// WithBlockCounter returns a context whose requests add 1 to c each time the
// transport blocks one. The tool layer uses it to put blocked_requests in the
// audit line of one call.
func WithBlockCounter(ctx context.Context, c *atomic.Int64) context.Context {
	return context.WithValue(ctx, counterKey{}, c)
}

// Wrapper returns a client-go transport wrapper that runs Check on every
// request. client-go applies WrapTransport inside its auth wrappers, so Check
// sees the final request, credentials included, just before the TLS
// transport sends it.
func Wrapper(t Target) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return &roundTripper{next: next, target: t}
	}
}

type roundTripper struct {
	next   http.RoundTripper
	target Target
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := Check(req, rt.target); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		if c, ok := req.Context().Value(counterKey{}).(*atomic.Int64); ok && c != nil {
			c.Add(1)
		}
		return nil, err
	}
	return rt.next.RoundTrip(req)
}
