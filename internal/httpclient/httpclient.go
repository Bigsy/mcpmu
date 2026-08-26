// Package httpclient is the one place mcpmu builds outbound HTTP clients and
// reads response bodies. Both the MCP transport and the OAuth flows go through
// it so every upstream request gets the same redirect policy and every body
// read is bounded.
package httpclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	// MaxRedirects is how many hops a client follows before giving up.
	MaxRedirects = 3

	// DefaultConnectTimeout bounds dialing, the TLS handshake and the wait for
	// response headers. It is deliberately not a whole-request deadline: SSE
	// bodies stay open indefinitely.
	DefaultConnectTimeout = 30 * time.Second
)

// ErrBodyTooLarge is returned by ReadBody when a response body exceeds the
// caller's limit.
var ErrBodyTooLarge = errors.New("response body too large")

// ErrCrossOriginRedirect is the reason a client refuses to follow a redirect
// to a different scheme or host.
var ErrCrossOriginRedirect = errors.New("refusing cross-origin redirect")

// New returns a client derived from base (http.DefaultClient when nil) that
// refuses cross-origin redirects and never applies http.Client.Timeout.
//
// Go replays custom headers — Authorization, Mcp-Session-Id, CF-Access-* — on
// every hop it follows, so a same-origin-only policy is what keeps an upstream
// from bouncing our credentials to a third party. Per-request deadlines come
// from the request context; connection-level timeouts from the transport.
func New(base *http.Client) *http.Client {
	c := &http.Client{}
	if base == nil {
		base = http.DefaultClient
	}
	*c = *base
	c.Timeout = 0
	c.CheckRedirect = checkRedirect

	if c.Transport == nil {
		c.Transport = defaultTransport()
		return c
	}
	if t, ok := c.Transport.(*http.Transport); ok {
		tt := t.Clone()
		if tt.ResponseHeaderTimeout == 0 {
			tt.ResponseHeaderTimeout = DefaultConnectTimeout
		}
		if tt.TLSHandshakeTimeout == 0 {
			tt.TLSHandshakeTimeout = DefaultConnectTimeout
		}
		if tt.DialContext == nil {
			tt.DialContext = (&net.Dialer{
				Timeout:   DefaultConnectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext
		}
		c.Transport = tt
	}
	return c
}

// checkRedirect allows at most MaxRedirects hops, all to the origin
// (scheme+host) of the request that started the chain.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("%w: %s://%s -> %s://%s",
			ErrCrossOriginRedirect, first.Scheme, first.Host, req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// ReadBody reads resp.Body up to limit bytes and closes it. A body larger than
// limit yields ErrBodyTooLarge (wrapped), never a silently truncated slice.
func ReadBody(resp *http.Response, limit int64) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrBodyTooLarge, limit)
	}
	return data, nil
}

func defaultTransport() *http.Transport {
	// Start from Go's defaults and add a header timeout so requests that never
	// respond don't hang indefinitely, without imposing a hard deadline for
	// long-lived response bodies like SSE.
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t := dt.Clone()
		t.ResponseHeaderTimeout = DefaultConnectTimeout
		if t.TLSHandshakeTimeout == 0 {
			t.TLSHandshakeTimeout = DefaultConnectTimeout
		}
		return t
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   DefaultConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   DefaultConnectTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: DefaultConnectTimeout,
	}
}
