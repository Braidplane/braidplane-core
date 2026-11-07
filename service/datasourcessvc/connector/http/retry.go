package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// A provider delay cannot consume an unbounded worker slot, even without a caller deadline.
const maxHTTPRetryDelay = 60 * time.Second

func httpRetryDelay(response *http.Response, backoff time.Duration, now time.Time) (time.Duration, error) {
	delay := backoff
	if response != nil {
		value := strings.TrimSpace(response.Header.Get("Retry-After"))
		if value != "" {
			if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
				if seconds > uint64(maxHTTPRetryDelay/time.Second) {
					return 0, excessiveHTTPRetryDelay()
				}
				delay = max(delay, time.Duration(seconds)*time.Second)
			} else if date, err := http.ParseTime(value); err == nil {
				delay = max(delay, date.Sub(now))
			} else if len(value) > 0 && strings.Trim(value, "0123456789") == "" {
				return 0, excessiveHTTPRetryDelay()
			}
		}
	}
	if delay > maxHTTPRetryDelay {
		return 0, excessiveHTTPRetryDelay()
	}
	return max(delay, 0), nil
}

func excessiveHTTPRetryDelay() error {
	return xerror.Unavailable(xerror.DataSource).WithPublicDetail("HTTP retry delay exceeds the 60 second wait budget; retry stopped")
}
