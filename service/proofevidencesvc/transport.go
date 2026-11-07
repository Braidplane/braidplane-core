package proofevidencesvc

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type route struct{ method, url string }

// metadataTransport is owned by one observation. Durable dispatch must reserve
// cumulative budgets separately; these counters cannot authorize a retry.
type metadataTransport struct {
	mu             sync.Mutex
	grant          proofevidence.Grant
	routes         []route
	next           http.RoundTripper
	usage          proofevidence.Usage
	outcome        proofevidence.Outcome
	client         *http.Client
	nativeStarted  bool
	nativeDials    int
	maxConnections int
	pendingBytes   int64
}

func publicTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext: hostguard.NewDestinationPolicy(false).DialContext,
		// No proxy discovery, connection reuse/retries, or implicit compression.
		DisableKeepAlives: true, DisableCompression: true,
		MaxResponseHeaderBytes: 16 << 10, ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout: timeout,
	}
}

func (t *metadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer func(body io.ReadCloser) { _ = body.Close() }(req.Body)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outcome != proofevidence.OutcomeUnknown {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	if err := req.Context().Err(); err != nil {
		return nil, t.fail(contextOutcome(err))
	}
	if t.grant.Check() != nil || len(t.routes) == 0 || req.URL == nil ||
		req.Method != t.routes[0].method || req.URL.String() != t.routes[0].url ||
		req.Host != req.URL.Host || ((req.Method == http.MethodGet || req.Method == http.MethodHead) && req.Body != nil && req.Body != http.NoBody) {
		return nil, t.fail(proofevidence.OutcomeDenied)
	}
	limits := t.grant.Request().Limits
	if t.usage.Requests >= limits.MaxRequests {
		return nil, t.fail(proofevidence.OutcomeOverBudget)
	}
	if req.Body != nil {
		body, err := t.readBody(req.Body, limits.MaxBytes)
		if err != nil {
			return nil, err
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	t.routes = t.routes[1:]
	t.usage.Requests++
	t.usage.MaxConcurrency = 1
	response, err := t.next.RoundTrip(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, t.fail(contextOutcome(req.Context().Err()))
		}
		return nil, t.fail(proofevidence.OutcomeUnreachable)
	}
	defer func(body io.ReadCloser) { _ = body.Close() }(response.Body)
	if response.StatusCode != http.StatusOK && (req.Method != http.MethodHead || response.StatusCode != http.StatusNotFound) {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, t.fail(proofevidence.OutcomeDenied)
		case http.StatusTooManyRequests:
			return nil, t.fail(proofevidence.OutcomeRateLimited)
		case http.StatusPartialContent:
			return nil, t.fail(proofevidence.OutcomePartial)
		default:
			return nil, t.fail(proofevidence.OutcomeProviderError)
		}
	}
	body, err := t.readBody(response.Body, limits.MaxBytes)
	if err != nil {
		return nil, err
	}
	if req.Context().Err() != nil {
		return nil, t.fail(contextOutcome(req.Context().Err()))
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}

func (t *metadataTransport) readBody(body io.Reader, maximum int64) ([]byte, error) {
	// Match the provider clients' per-response ceiling even if policy is larger.
	remaining := min(maximum-t.usage.Bytes, 10<<20)
	if remaining <= 0 {
		err := t.fail(proofevidence.OutcomeOverBudget)
		return nil, err
	}
	// The extra byte detects overflow without admitting a truncated response.
	data, err := io.ReadAll(io.LimitReader(body, remaining+1))
	t.usage.Bytes += int64(len(data))
	if int64(len(data)) > remaining {
		err = t.fail(proofevidence.OutcomeOverBudget)
	} else if err != nil {
		err = t.fail(proofevidence.OutcomePartial)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (t *metadataTransport) fail(outcome proofevidence.Outcome) error {
	t.outcome = outcome
	return proofevidence.ErrAuthorizationDenied
}

func contextOutcome(err error) proofevidence.Outcome {
	if xerror.Is(err, context.DeadlineExceeded) {
		return proofevidence.OutcomeTimedOut
	}
	return proofevidence.OutcomeCanceled
}

func (t *metadataTransport) HTTPClient() *http.Client { return t.client }

func (t *metadataTransport) BeginNative(request proofevidence.MetadataRoute) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outcome != proofevidence.OutcomeUnknown || t.grant.Check() != nil || t.nativeStarted || len(t.routes) != 1 ||
		t.routes[0] != (route{request.Method, request.URL}) || request.Method != "metadata" ||
		t.grant.Request().Target.Scheme == "https" {
		return t.fail(proofevidence.OutcomeDenied)
	}
	if t.grant.Request().Limits.MaxRequests < 1 {
		return t.fail(proofevidence.OutcomeOverBudget)
	}
	t.nativeStarted = true
	t.routes = nil
	t.usage.Requests, t.usage.MaxConcurrency = 1, 1
	return nil
}

func (t *metadataTransport) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	t.mu.Lock()
	target := t.grant.Request().Target
	if t.outcome != proofevidence.OutcomeUnknown || !t.nativeStarted || t.nativeDials >= t.maxConnections || t.grant.Check() != nil || network != "tcp" ||
		address != net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))) {
		err := t.fail(proofevidence.OutcomeDenied)
		t.mu.Unlock()
		return nil, err
	}
	t.nativeDials++
	t.mu.Unlock()
	conn, err := hostguard.NewDestinationPolicy(false).DialContext(ctx, network, address)
	if err != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		return nil, t.fail(proofevidence.OutcomeUnreachable)
	}
	if err := conn.SetDeadline(t.grant.Deadline()); err != nil {
		_ = conn.Close()
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return &metadataConn{Conn: conn, transport: t}, nil
}

type metadataConn struct {
	net.Conn
	transport *metadataTransport
}

func (c *metadataConn) deadline(requested time.Time) time.Time {
	admitted := c.transport.grant.Deadline()
	if requested.IsZero() || requested.After(admitted) {
		return admitted
	}
	return requested
}

func (c *metadataConn) SetDeadline(deadline time.Time) error {
	if err := c.Conn.SetDeadline(c.deadline(deadline)); err != nil {
		return xerror.Wrapf("set metadata socket deadline: %w", err)
	}
	return nil
}

func (c *metadataConn) SetReadDeadline(deadline time.Time) error {
	if err := c.Conn.SetReadDeadline(c.deadline(deadline)); err != nil {
		return xerror.Wrapf("set metadata socket read deadline: %w", err)
	}
	return nil
}

func (c *metadataConn) SetWriteDeadline(deadline time.Time) error {
	if err := c.Conn.SetWriteDeadline(c.deadline(deadline)); err != nil {
		return xerror.Wrapf("set metadata socket write deadline: %w", err)
	}
	return nil
}

func (c *metadataConn) Read(buffer []byte) (int, error) {
	t := c.transport
	t.mu.Lock()
	remaining := t.grant.Request().Limits.MaxBytes - t.usage.Bytes - t.pendingBytes
	if t.outcome != proofevidence.OutcomeUnknown || remaining <= 0 || t.grant.Check() != nil {
		err := t.fail(proofevidence.OutcomeOverBudget)
		t.mu.Unlock()
		return 0, err
	}
	reserved := min(int64(len(buffer)), remaining+1)
	t.pendingBytes += reserved
	t.mu.Unlock()
	n, err := c.Conn.Read(buffer[:reserved])
	return c.charge(n, err, reserved)
}

func (c *metadataConn) Write(buffer []byte) (int, error) {
	t := c.transport
	t.mu.Lock()
	if t.outcome != proofevidence.OutcomeUnknown || int64(len(buffer)) > t.grant.Request().Limits.MaxBytes-t.usage.Bytes-t.pendingBytes || t.grant.Check() != nil {
		err := t.fail(proofevidence.OutcomeOverBudget)
		t.mu.Unlock()
		return 0, err
	}
	reserved := int64(len(buffer))
	t.pendingBytes += reserved
	t.mu.Unlock()
	n, err := c.Conn.Write(buffer)
	return c.charge(n, err, reserved)
}

func (c *metadataConn) charge(n int, err error, reserved int64) (int, error) {
	t := c.transport
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingBytes -= reserved
	t.usage.Bytes += int64(n)
	if t.usage.Bytes > t.grant.Request().Limits.MaxBytes {
		err = t.fail(proofevidence.OutcomeOverBudget)
	}
	return n, err
}
