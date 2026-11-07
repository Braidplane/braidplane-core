package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"

	"github.com/stretchr/testify/require"
)

func TestHTTPRetryDelay(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
		fail  bool
	}{
		{"", time.Second, false},
		{"2", 2 * time.Second, false},
		{"0", time.Second, false},
		{"malformed", time.Second, false},
		{"-1", time.Second, false},
		{now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second, false},
		{now.Add(-time.Hour).Format(http.TimeFormat), time.Second, false},
		{"61", 0, true},
		{"9999999999999999999999999999", 0, true},
		{now.Add(time.Hour).Format(http.TimeFormat), 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			response := &http.Response{Header: http.Header{"Retry-After": []string{tc.value}}}
			delay, err := httpRetryDelay(response, time.Second, now)
			if tc.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, delay)
		})
	}
}

func TestHTTPRetryWaitBudgetAndCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "excessive", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			calls := atomic.Int32{}
			reached := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				select {
				case reached <- struct{}{}:
				default:
				}
				delay := "1"
				if mode == "excessive" {
					delay = "61"
				}
				if mode == "exhausted" {
					delay = "0"
				}
				w.Header().Set("Retry-After", delay)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 100*time.Millisecond)
			defer cancel()
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			if mode == "cancel" {
				go func() { <-reached; cancel() }()
			}
			start := time.Now()
			execution, err := connector.Execute(ctx, []byte(`{"method":"GET","path":"/"}`), nil, types.ExecutionLimits{HTTPRetryAttempts: 2})
			require.NoError(t, err)
			require.Less(t, time.Since(start), time.Second)
			if mode == "exhausted" {
				require.NoError(t, execution.Error())
				response, _ := execution.HTTP()
				require.Equal(t, 429, response.StatusCode)
				require.Equal(t, int32(2), calls.Load())
			} else {
				require.Error(t, execution.Error())
				require.Equal(t, int32(1), calls.Load())
			}
		})
	}
}
