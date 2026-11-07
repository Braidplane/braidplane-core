package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestLogMethodCompletionIncludesOperationalFields(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := reqctx.WithRequestID(context.Background(), "req-123")

	LogMethodCompletion(ctx, logger, MethodCompletion{
		Layer:       "service",
		Package:     "service/projectssvc",
		Interface:   "Service",
		Method:      "CreateProject",
		StartedAt:   time.Now().Add(-25 * time.Millisecond),
		CompletedAt: time.Now(),
		Err:         fmt.Errorf("create project: %w", xerror.InvalidInput(xerror.Project).Detail("invalid project")),
	})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(t, "service.method.completed", entry["braidplane.event"])
	require.Equal(t, "service", entry["braidplane.layer"])
	require.Equal(t, "service/projectssvc.Service.CreateProject", entry["braidplane.method"])
	require.Equal(t, errorValue, entry["outcome"])
	require.Equal(t, "req-123", entry["request_id"])
	require.Contains(t, entry, "duration_ms")
	require.Equal(t, "PROJECT_INVALID_INPUT", entry["error.type"])
	require.Equal(t, "invalid_input", entry["error.kind"])
	require.Equal(t, "create project: project invalid input: invalid project", entry["error.message"])

	raw := buf.String()
	require.NotContains(t, raw, "orgSlug")
	require.NotContains(t, raw, "projectSlug")
	require.Contains(t, raw, "invalid project")
}

func TestLogMethodCompletionLogsSuccessAtDebug(t *testing.T) {
	t.Parallel()

	var infoBuf bytes.Buffer
	infoLogger := slog.New(
		slog.NewJSONHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)
	LogMethodCompletion(context.Background(), infoLogger, MethodCompletion{
		Layer:       "repository",
		Package:     "postgres/projects",
		Interface:   "Repository",
		Method:      "Create",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
	})
	require.Empty(t, infoBuf.String())

	var debugBuf bytes.Buffer
	debugLogger := slog.New(
		slog.NewJSONHandler(&debugBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	LogMethodCompletion(context.Background(), debugLogger, MethodCompletion{
		Layer:       "repository",
		Package:     "postgres/projects",
		Interface:   "Repository",
		Method:      "Create",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
	})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(debugBuf.Bytes(), &entry))
	require.Equal(t, "repository.method.completed", entry["braidplane.event"])
	require.Equal(t, "success", entry["outcome"])
}

func TestLogErrorIncludesSafeDiagnosticAndCorrelation(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogError(
		reqctx.WithRequestID(context.Background(), "req-123"),
		"dispatch failed",
		xerror.Unexpected().Detail("sink rejected password=secret-value Bearer bearer-value https://user:pass@example.test/path/token?access_token=query-value"),
		"bearer-value",
	)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(t, "dispatch failed", entry["msg"])
	require.Equal(t, "req-123", entry["request_id"])
	require.Equal(t, xerror.Unexpected().Code(), entry["error.type"])
	require.Equal(t, "sink rejected password=[REDACTED] Bearer [REDACTED] [REDACTED_URL]", entry["error.message"])

	raw := buf.String()
	for _, value := range []string{"secret-value", "bearer-value", "user:pass", "query-value"} {
		require.NotContains(t, raw, value)
	}
}

func TestLogErrorSkipsNil(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogError(context.Background(), "ignored", nil)
	require.Empty(t, buf.String())
}
