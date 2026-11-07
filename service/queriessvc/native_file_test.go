package queriessvc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type nativeFileExecutorFunc func(context.Context, queriessvc.ConnectorExecutionRequest) (*result.ExecutionResult, error)

func (f nativeFileExecutorFunc) ExecuteConnector(ctx context.Context, r queriessvc.ConnectorExecutionRequest) (*result.ExecutionResult, error) {
	return f(ctx, r)
}

func TestNativeFileProfileSelection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, org, format string
		savedTimeout      int
		native            bool
	}{
		{"native scoped file", "fixture", "file", 0, true},
		{"saved timeout stays smaller", "fixture", "file", 1, true},
		{"other tenant", "other", "file", 0, false},
		{"structured file", "fixture", "csv", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			statement, err := json.Marshal(map[string]any{"type": "file", "operation": "read", "path": "fixture", "format": tc.format})
			require.NoError(t, err)
			saved, err := json.Marshal(map[string]any{"statement": string(statement), "timeout_seconds": tc.savedTimeout})
			require.NoError(t, err)
			repo, sources := safetyLimitQueryFixtures(t, saved)
			find := sources.FindBySlugFn
			sources.FindBySlugFn = func(ctx context.Context, org, ds slug.Slug) (*datasourcessvc.DataSource, error) {
				source, err := find(ctx, org, ds)
				source.Type = model.DataSourceTypeFTP
				return source, err
			}
			limits := testExecutionLimits()
			limits.QueryTimeout = time.Second
			limits.ConnectorTimeout = time.Second
			limits.NativeFiles = filestream.Profile{Organization: "fixture", MaxBytes: 2 << 30, Timeout: time.Minute, Transfers: 1, Connectors: 2}
			start := time.Now().Add(-20 * time.Second)
			called := false
			executor := nativeFileExecutorFunc(func(ctx context.Context, request queriessvc.ConnectorExecutionRequest) (*result.ExecutionResult, error) {
				called = true
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.Equal(t, limits.MaxResponseBytes, request.Limits.MaxResponseBytes)
				if tc.native {
					require.Equal(t, limits.NativeFiles.MaxBytes, request.Limits.MaxFileBytes)
					if tc.savedTimeout > 0 {
						require.LessOrEqual(t, time.Until(deadline), time.Second)
						require.Equal(t, time.Second, request.Limits.ConnectorTimeout)
					} else {
						require.Equal(t, start.Add(time.Minute), deadline)
						require.Equal(t, time.Minute, request.Limits.ConnectorTimeout)
					}
				} else {
					require.Zero(t, request.Limits.MaxFileBytes)
					require.LessOrEqual(t, time.Until(deadline), time.Second)
				}
				return result.NewFTPResult(&result.FileResult{Operation: "read", TargetPath: "fixture"}, 0, nil), nil
			})
			svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, repo, sources, executor, newFakeCredentialService(), &testutil.FakeClock{}, limits, eventtest.NewProducer(&fakeQueryEventRecorder{}))
			require.NoError(t, err)
			ctx, finishFiles := filestream.BeginAttempt(t.Context(), start)
			defer finishFiles()
			_, err = svc.Execute(ctx, slug.Slug(tc.org), "postgres-main", "limited-query", model.SystemEventActor(), nil, queriessvc.WithoutExecutionRecording())
			require.NoError(t, err)
			require.True(t, called)
		})
	}
}
