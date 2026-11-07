package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

const acknowledgementFixture = `{"status_codes":[200,201],"success":[{"path":"status.code","equals":"OK"}],"empty_fields":["errors"]}`

func TestHTTPAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"success", `{"status":{"code":"OK"},"items":{"0":{"record":{"id":1}}}}`, 200, nil},
		{"empty errors", `{"status":{"code":"OK"},"items":[{"errors":[]},{"errors":{}},{"errors":null}]}`, 201, nil},
		{"application rejection", `{"status":{"code":"ERROR"}}`, 200, xerror.HTTPAcknowledgementRejected()},
		{"http rejection", `{"status":{"code":"OK"}}`, 400, xerror.HTTPAcknowledgementRejected()},
		{"record conflict", `{"status":{"code":"OK"},"invoices":{"0":{"invoice":{"errors":{"error":{"message":"sensitive-data"}}}}}}`, 200, xerror.HTTPAcknowledgementRejected()},
		{"array record error", `{"status":{"code":"OK"},"items":[{"errors":[{"message":"sensitive-data"}]}]}`, 200, xerror.HTTPAcknowledgementRejected()},
		{"scalar errors", `{"status":{"code":"OK"},"errors":"sensitive-data"}`, 200, xerror.HTTPAcknowledgementRejected()},
		{"conflicting duplicate", `{"status":{"code":"ERROR","code":"OK"}}`, 200, xerror.HTTPAcknowledgementUncertain()},
		{"missing", `{}`, 200, xerror.HTTPAcknowledgementUncertain()},
		{"null", `{"status":{"code":null}}`, 200, xerror.HTTPAcknowledgementUncertain()},
		{"wrong type", `{"status":{"code":{}}}`, 200, xerror.HTTPAcknowledgementUncertain()},
		{"malformed", `{"status":`, 200, xerror.HTTPAcknowledgementUncertain()},
		{"trailing JSON", `{"status":{"code":"OK"}} {}`, 200, xerror.HTTPAcknowledgementUncertain()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execution, err := c.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/records","acknowledgement":`+acknowledgementFixture+`}`), nil)
			require.NoError(t, err)
			if tc.want == nil {
				require.NoError(t, execution.Error())
			} else {
				require.ErrorIs(t, execution.Error(), tc.want)
				response, _ := execution.HTTP()
				require.Nil(t, response)
				require.NotContains(t, execution.Error().Error(), "sensitive-data")
			}
			// Existing authored operations retain their status-and-body contract.
			legacy, err := c.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/records"}`), nil)
			require.NoError(t, err)
			require.NoError(t, legacy.Error())
		})
	}
}

func TestInvalidAcknowledgementNeverDispatches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	for _, raw := range []string{`null`, `{}`, `{"status_codes":[200],"success":[]}`, `{"status_codes":[500],"success":[{"path":"ok","equals":true}]}`, `{"status_codes":[200,200],"success":[{"path":"ok","equals":true}]}`, `{"status_codes":[200],"success":[{"path":"ok"}]}`, `{"status_codes":[200],"success":[{"path":"ok","equals":null}]}`, `{"status_codes":[200],"success":[{"path":"items.*.ok","equals":true}]}`, `{"status_codes":[200],"success":[{"path":"ok","equals":true,"unknown":1}]}`} {
		execution, err := c.Execute(transportsecurity.WithPlaintext(t.Context()), []byte(`{"method":"POST","path":"/records","acknowledgement":`+raw+`}`), nil)
		require.NoError(t, err)
		require.ErrorIs(t, execution.Error(), xerror.InvalidInput(xerror.Query))
	}
	require.Zero(t, calls)
}

func TestAcknowledgementTimeoutAfterRemoteAcceptance(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(accepted); <-release }))
	defer server.Close()
	c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), time.Second)
	defer cancel()
	execution, err := c.Execute(ctx, json.RawMessage(`{"method":"POST","path":"/records","acknowledgement":`+acknowledgementFixture+`}`), nil, types.ExecutionLimits{HTTPRetryAttempts: 3})
	require.NoError(t, err)
	close(release)
	select {
	case <-accepted:
	default:
		t.Fatal("provider did not accept the request before the timeout")
	}
	require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementUncertain())
	response, _ := execution.HTTP()
	require.Nil(t, response)
}
