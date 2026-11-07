package googleclient

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
)

func TestLogErrorKeepsProviderDiagnosticWithoutPayloads(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	err := &googleapi.Error{Code: 400, Message: "invalid request token=fixture-token", Body: "private response body", Details: []any{"private detail"}, Errors: []googleapi.ErrorItem{{Message: "private item"}}}
	LogError(context.Background(), err, "fixture-token")
	require.Contains(t, logs.String(), "googleapi: Error 400: invalid request")
	for _, secret := range []string{"fixture-token", "private response body", "private detail", "private item"} {
		require.NotContains(t, logs.String(), secret)
	}
	require.Equal(t, "private response body", err.Body)
}
