// Package googleclient owns transport policies shared by Google resource clients.
package googleclient

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// DefaultMaxResponseBytes is the common budget before resource-specific narrowing.
const DefaultMaxResponseBytes = 10 * 1024 * 1024

// NewHTTPClient uses the caller's transport without discovering credentials,
// retrying requests, or following redirects. Resources own host restrictions and deadlines.
func NewHTTPClient(timeout time.Duration, transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ReadResponse checks the byte budget before provider decoding. Read errors are
// left intact for resource retry classification and must be mapped before exposure.
func ReadResponse(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, xerror.ExecutionResponseSizeExceeded()
	}
	readLimit := limit
	if limit < math.MaxInt64 {
		readLimit++
	}
	result, err := io.ReadAll(io.LimitReader(body, readLimit))
	if err != nil {
		return nil, xerror.Wrapf("read provider response: %w", err)
	}
	if int64(len(result)) > limit {
		return nil, xerror.ExecutionResponseSizeExceeded()
	}
	return result, nil
}

// StatusError deliberately excludes provider bodies and headers from diagnostics.
func StatusError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return xerror.InvalidCredentials()
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource)
	case http.StatusNotFound:
		return xerror.NotFound(xerror.DataSource)
	default:
		if status >= 500 || status == http.StatusTooManyRequests {
			return xerror.Unavailable(xerror.DataSource)
		}
		return xerror.InvalidInput(xerror.DataSource)
	}
}

// TransportError keeps cancellation and timeouts typed without retaining URLs or credentials.
func TransportError(ctx context.Context, err error) error {
	var transport *url.Error
	if ctx.Err() != nil || xerror.As(err, &transport) && transport.Timeout() {
		return xerror.Timeout(xerror.DataSource)
	}
	return xerror.Unavailable(xerror.DataSource)
}
