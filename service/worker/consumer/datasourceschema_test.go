package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestDataSourceSchemaImportWorkerLogsQualifiedOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		result      *datasourcessvc.SchemaImportResult
		executeErr  error
		wantLevel   string
		wantOutcome string
		wantMessage string
		wantErr     bool
	}{
		{
			name: "Success",
			result: &datasourcessvc.SchemaImportResult{
				Status: model.DataSourceSchemaImportStatusReady,
			},
			wantLevel:   "INFO",
			wantOutcome: "succeeded",
			wantMessage: "data source schema import succeeded",
		},
		{
			name: "PersistedProviderFailure",
			result: &datasourcessvc.SchemaImportResult{
				Status: model.DataSourceSchemaImportStatusFailed,
				Diagnostic: &datasourcessvc.SchemaImportDiagnostic{
					Code:    "DATA_SOURCE_CONNECTION_FAILED",
					Message: "data source connection failed",
				},
			},
			wantLevel:   "WARN",
			wantOutcome: "failed_persisted",
			wantMessage: "data source schema import failed and diagnostic persisted",
		},
		{
			name:        "StaleAttempt",
			result:      &datasourcessvc.SchemaImportResult{Skipped: true},
			wantLevel:   "INFO",
			wantOutcome: "skipped",
			wantMessage: "data source schema import skipped",
		},
		{
			name:        "NilResult",
			wantLevel:   "ERROR",
			wantOutcome: "worker_failed",
			wantMessage: "data source schema import failed",
			wantErr:     true,
		},
		{
			name: "UnexpectedStatus",
			result: &datasourcessvc.SchemaImportResult{
				Status: model.DataSourceSchemaImportStatusImporting,
			},
			wantLevel:   "ERROR",
			wantOutcome: "worker_failed",
			wantMessage: "data source schema import failed",
			wantErr:     true,
		},
		{
			name:        "TrueWorkerFailure",
			executeErr:  xerror.Unexpected().Detail("database password=secret-marker"),
			wantLevel:   "ERROR",
			wantOutcome: "worker_failed",
			wantMessage: "data source schema import failed",
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
			worker := NewDataSourceSchemaImportWorker(
				&schemaImportExecutorStub{result: tc.result, err: tc.executeErr},
				logger,
			)
			ctx := reqctx.WithRequestID(context.Background(), "req-schema-import")
			err := worker.Work(ctx, &river.Job[jobs.DataSourceSchemaImportArgs]{
				Args: jobs.DataSourceSchemaImportArgs{
					OrganizationSlug: must.Slug("acme-corp"),
					DataSourceSlug:   must.Slug("hubspot-crm"),
					AttemptID:        "attempt-123",
					Actor:            model.UserEventActor(must.Slug("secret-actor-marker")),
				},
			})
			if tc.wantErr {
				require.Error(t, err)
				if tc.executeErr != nil {
					require.ErrorIs(t, err, tc.executeErr)
				}
			} else {
				require.NoError(t, err)
			}

			var entry map[string]any
			require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
			require.Equal(t, tc.wantLevel, entry["level"])
			require.Equal(t, tc.wantMessage, entry["msg"])
			require.Equal(t, "data_source.schema_import.completed", entry["braidplane.event"])
			require.Equal(t, tc.wantOutcome, entry["outcome"])
			require.Equal(t, "acme-corp", entry["organization.slug"])
			require.Equal(t, "hubspot-crm", entry["data_source.slug"])
			require.Equal(t, "attempt-123", entry["attempt_id"])
			require.Equal(t, "req-schema-import", entry["request_id"])
			require.NotContains(t, logBuffer.String(), "secret-marker")
			require.NotContains(t, logBuffer.String(), "secret-actor-marker")

			if tc.wantOutcome == "failed_persisted" {
				require.Equal(t, "provider_introspection", entry["error.category"])
				require.Equal(t, "DATA_SOURCE_CONNECTION_FAILED", entry["diagnostic.code"])
				require.Equal(t, "data source connection failed", entry["diagnostic.message"])
			}
		})
	}
}

type schemaImportExecutorStub struct {
	result *datasourcessvc.SchemaImportResult
	err    error
}

func (s *schemaImportExecutorStub) ExecuteQueuedSchemaImport(
	context.Context,
	jobs.DataSourceSchemaImportArgs,
) (*datasourcessvc.SchemaImportResult, error) {
	return s.result, s.err
}
