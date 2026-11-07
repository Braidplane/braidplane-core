package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type dataSourceSchemaImportExecutor interface {
	ExecuteQueuedSchemaImport(ctx context.Context, args jobs.DataSourceSchemaImportArgs) (*datasourcessvc.SchemaImportResult, error)
}

// NewDataSourceSchemaImportWorker creates the worker that imports one data-source schema.
func NewDataSourceSchemaImportWorker(
	service dataSourceSchemaImportExecutor,
	logger *slog.Logger,
) river.Worker[jobs.DataSourceSchemaImportArgs] {
	if service == nil {
		panic("nil dataSourceSchemaImportExecutor passed to NewDataSourceSchemaImportWorker")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &dataSourceSchemaImportWorker{
		service: service,
		logger:  logger,
	}
}

type dataSourceSchemaImportWorker struct {
	river.WorkerDefaults[jobs.DataSourceSchemaImportArgs]
	service dataSourceSchemaImportExecutor
	logger  *slog.Logger
}

// Work executes one queued schema import and logs only safe data-source identifiers.
func (w *dataSourceSchemaImportWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.DataSourceSchemaImportArgs],
) error {
	attrs := []slog.Attr{
		slog.String("braidplane.event", "data_source.schema_import.completed"),
		slog.String("organization.slug", job.Args.OrganizationSlug.String()),
		slog.String("data_source.slug", job.Args.DataSourceSlug.String()),
		slog.String("attempt_id", job.Args.AttemptID),
	}
	attrs = append(attrs, logging.ContextAttrs(ctx)...)

	result, err := w.service.ExecuteQueuedSchemaImport(ctx, job.Args)
	if err != nil {
		attrs = append(attrs,
			slog.String("outcome", "worker_failed"),
			slog.String("error.category", "schema_import_execution"),
		)
		w.logger.LogAttrs(ctx, slog.LevelError, "data source schema import failed", attrs...)
		return err
	}

	if result == nil {
		err := xerror.Unexpected().Detail("schema import executor returned no result")
		attrs = append(attrs,
			slog.String("outcome", "worker_failed"),
			slog.String("error.category", "invalid_execution_result"),
		)
		w.logger.LogAttrs(ctx, slog.LevelError, "data source schema import failed", attrs...)
		return err
	}

	if result.Skipped {
		queue.MarkWorkerSkipped(ctx)
		attrs = append(attrs, slog.String("outcome", "skipped"))
		w.logger.LogAttrs(ctx, slog.LevelInfo, "data source schema import skipped", attrs...)
		return nil
	}

	switch result.Status {
	case model.DataSourceSchemaImportStatusFailed:
		diagnosticCode := "DATA_SOURCE_SCHEMA_IMPORT_FAILED"
		if result.Diagnostic != nil && result.Diagnostic.Code != "" {
			diagnosticCode = result.Diagnostic.Code
		}
		queue.MarkWorkerPersistedFailure(ctx, diagnosticCode, "provider_introspection")
		attrs = append(attrs,
			slog.String("outcome", "failed_persisted"),
			slog.String("error.category", "provider_introspection"),
		)
		if result.Diagnostic != nil {
			attrs = append(attrs,
				slog.String("diagnostic.code", result.Diagnostic.Code),
				slog.String("diagnostic.message", result.Diagnostic.Message),
			)
		} else {
			attrs = append(attrs,
				slog.String("diagnostic.code", "DATA_SOURCE_SCHEMA_IMPORT_FAILED"),
				slog.String("diagnostic.message", "Schema import failed."),
			)
		}
		w.logger.LogAttrs(ctx, slog.LevelWarn, "data source schema import failed and diagnostic persisted", attrs...)
	case model.DataSourceSchemaImportStatusReady:
		attrs = append(attrs, slog.String("outcome", "succeeded"))
		w.logger.LogAttrs(ctx, slog.LevelInfo, "data source schema import succeeded", attrs...)
	default:
		err := xerror.Unexpected().Detail("schema import executor returned unexpected status")
		attrs = append(attrs,
			slog.String("outcome", "worker_failed"),
			slog.String("error.category", "invalid_execution_result"),
		)
		w.logger.LogAttrs(ctx, slog.LevelError, "data source schema import failed", attrs...)
		return err
	}
	return nil
}
