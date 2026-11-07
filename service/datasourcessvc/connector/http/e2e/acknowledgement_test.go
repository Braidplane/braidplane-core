package http_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestAcknowledgementProviderAcceptanceAndRemoteReadback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the HTTP provider container")
	}
	config := getTestConfig()
	config.HealthPath = "/acknowledgement"
	config.TimeoutSeconds = 1
	credentials := httpconnector.Credentials{SecretHeaders: map[string]string{"X-Access-Key": "synthetic-access", "X-Secret-Key": "synthetic-secret", "X-App-Key": "synthetic-app"}}
	conn, err := httpconnector.New(transportsecurity.WithPlaintext(t.Context()), config, credentials)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.TestConnection(t.Context()))
	for _, tc := range []struct {
		mode     string
		want     error
		accepted int
	}{
		{"success", nil, 1},
		{"reject", xerror.HTTPAcknowledgementRejected(), 0},
		{"record-error", xerror.HTTPAcknowledgementRejected(), 1},
		{"missing", xerror.HTTPAcknowledgementUncertain(), 1},
		{"malformed", xerror.HTTPAcknowledgementUncertain(), 1},
		{"timeout", xerror.HTTPAcknowledgementUncertain(), 1},
		{"echo", xerror.HTTPAcknowledgementUncertain(), 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			identity := fmt.Sprintf("ack-%s-%d", tc.mode, time.Now().UnixNano())
			statement := map[string]any{"method": "POST", "path": "/acknowledgement", "query_params": map[string]any{"mode": tc.mode}, "body": map[string]any{"name": "Synthetic acceptance", "status": identity}, "acknowledgement": map[string]any{"status_codes": []int{200, 201}, "success": []map[string]any{{"path": "status.code", "equals": "OK"}}, "empty_fields": []string{"errors"}}}
			data, err := json.Marshal(statement)
			require.NoError(t, err)
			execution, err := conn.Execute(t.Context(), data, nil, types.ExecutionLimits{HTTPRetryAttempts: 3})
			require.NoError(t, err)
			if tc.want == nil {
				require.NoError(t, execution.Error())
			} else {
				require.ErrorIs(t, execution.Error(), tc.want)
				response, _ := execution.HTTP()
				require.Nil(t, response)
				require.NotContains(t, execution.Error().Error(), "synthetic-secret")
			}
			// Read provider state independently of the connector's acknowledgement result.
			records, err := executeHTTP(t.Context(), conn, map[string]any{"method": "GET", "path": "/acknowledgement", "query_params": map[string]any{"status": identity}}, nil)
			require.NoError(t, err)
			require.Len(t, records.(map[string]any)["records"], tc.accepted)
		})
	}
	for _, contract := range []string{
		`null`,
		`{"status_codes":[200],"success":[{"path":"status..code","equals":"OK"}]}`,
		`{"status_codes":[200],"success":[{"path":"status.code"}]}`,
		`{"status_codes":[200],"success":[{"path":"status.code","equals":"OK"}],"unsupported":true}`,
	} {
		_, err := executeHTTPRaw(t.Context(), conn, []byte(`{"method":"POST","path":"/acknowledgement","body":{"name":"invalid-contract","status":"invalid-contract"},"acknowledgement":`+contract+`}`), nil)
		require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
	}
	records, err := executeHTTP(t.Context(), conn, map[string]any{"method": "GET", "path": "/acknowledgement", "query_params": map[string]any{"status": "invalid-contract"}}, nil)
	require.NoError(t, err)
	require.Empty(t, records.(map[string]any)["records"])
	// Existing single-key credentials are still constructible; provider rejection is separate.
	wrong, err := httpconnector.New(transportsecurity.WithPlaintext(t.Context()), config, httpconnector.Credentials{APIKey: "synthetic-access", APIKeyHeader: "X-Api-Key"})
	require.NoError(t, err)
	defer func() { require.NoError(t, wrong.Close()) }()
	require.ErrorIs(t, wrong.TestConnection(t.Context()), xerror.InvalidCredentials())
	require.NoError(t, conn.TestConnection(t.Context()))
}
