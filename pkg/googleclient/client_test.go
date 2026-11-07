package googleclient

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientRejectsRedirectsAndDoesNotAddCredentials(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/start", r.URL.Path)
				assert.Empty(t, r.Header.Get("Authorization"))
				w.Header().Set("Location", "/destination")
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := NewHTTPClient(time.Second, http.DefaultTransport)
			defer client.CloseIdleConnections()
			require.Equal(t, time.Second, client.Timeout)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/start", nil)
			require.NoError(t, err)
			response, err := client.Do(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, status, response.StatusCode)
			require.Equal(t, 1, calls)
		})
	}
}

func TestReadResponseEnforcesBudgetBeforeDecoding(t *testing.T) {
	for _, limit := range []int64{-1, 0, 3, 4, 5, math.MaxInt64} {
		body, err := ReadResponse(strings.NewReader("data"), limit)
		if limit < 4 {
			require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
			require.Nil(t, body)
		} else {
			require.NoError(t, err)
			require.Equal(t, "data", string(body))
		}
	}
	_, err := ReadResponse(failingReader{}, 4)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSafeErrorsPreserveIdentity(t *testing.T) {
	for status, want := range map[int]error{
		400: xerror.InvalidInput(xerror.DataSource),
		401: xerror.InvalidCredentials(),
		403: xerror.Forbidden(xerror.DataSource),
		404: xerror.NotFound(xerror.DataSource),
		429: xerror.Unavailable(xerror.DataSource),
		503: xerror.Unavailable(xerror.DataSource),
	} {
		require.ErrorIs(t, StatusError(status), want)
	}
	secret := xerror.Unexpected().Detail("token=private-runtime-token")
	err := TransportError(t.Context(), &url.Error{URL: "https://provider/?token=private-runtime-token", Err: secret})
	require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
	require.NotContains(t, err.Error(), "private-runtime-token")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, TransportError(ctx, secret), xerror.Timeout(xerror.DataSource))
	require.ErrorIs(t, TransportError(t.Context(), &url.Error{Err: context.DeadlineExceeded}), xerror.Timeout(xerror.DataSource))
}
