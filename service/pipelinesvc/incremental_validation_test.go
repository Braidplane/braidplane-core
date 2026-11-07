package pipelinesvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestValidateReturnsStepScopedIncrementalDiagnostics(t *testing.T) {
	t.Parallel()

	service := newCRUDTestService(t, &fakeServicePipelineStore{})
	pipeline := &spec.Pipeline{
		Metadata: spec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
		Steps: []spec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &spec.QueryRef{
					DataSourceSlug: "acme-ftp",
					OperationType:  spec.OperationTypeFTP,
					OperationRef:   "read-users-csv",
					Incremental: &spec.IncrementalSourceContract{
						CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
						Field:          "updated_at",
					},
				},
			},
		},
	}

	result, err := service.Validate(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("data-pipelines"),
		noEnvironmentSlug,
		slug.Slug("owner"),
		pipeline,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Valid)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "error", result.Diagnostics[0].Severity)
	require.Equal(t, "PIPELINE_INVALID_INPUT", result.Diagnostics[0].Code)
	require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
	require.NotNil(t, result.Diagnostics[0].StepSlug)
	require.Equal(t, "fetch-users", *result.Diagnostics[0].StepSlug)
	require.Contains(t, result.Diagnostics[0].Message, `incremental mode is not supported for operation_type "ftp"`)
}

func TestValidateAcceptsIncrementalSourceContractsBeforeRuntimePrevalidation(t *testing.T) {
	t.Parallel()

	service := newCRUDTestService(t, &fakeServicePipelineStore{})
	pipeline := &spec.Pipeline{
		Metadata: spec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
		Steps: []spec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &spec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  spec.OperationTypeSQL,
					OperationRef:   "list-users-incremental",
					Incremental: &spec.IncrementalSourceContract{
						CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
						Field:          "updated_at",
						LookbackWindow: "15m",
						Ordering:       []string{"updated_at", "id"},
						DedupeKey:      "id",
					},
				},
			},
			{
				Kind: "sink",
				Slug: "load-users",
				Sink: &spec.SinkSpec{
					Input:            &spec.StepInput{FromStep: "fetch-users"},
					DataSourceSlug:   "acme-postgres",
					OperationType:    spec.OperationTypeSQL,
					OperationRef:     "upsert-users",
					IdempotencyClass: spec.SinkIdempotencyClassIdempotentWithKey,
					InputContract: &payload.Contract{
						Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{Name: "updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true},
						},
					},
				},
			},
		},
	}

	result, err := service.Validate(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("data-pipelines"),
		noEnvironmentSlug,
		slug.Slug("owner"),
		pipeline,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Valid)
	require.Empty(t, result.Diagnostics)
}

func TestValidateReturnsStepScopedLookbackWindowDiagnostics(t *testing.T) {
	t.Parallel()

	service := newCRUDTestService(t, &fakeServicePipelineStore{})
	pipeline := &spec.Pipeline{
		Metadata: spec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
		Steps: []spec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &spec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  spec.OperationTypeSQL,
					OperationRef:   "list-users-incremental",
					Incremental: &spec.IncrementalSourceContract{
						CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
						Field:          "updated_at",
						LookbackWindow: "eventually",
					},
				},
			},
		},
	}

	result, err := service.Validate(
		context.Background(),
		slug.Slug("acme"),
		slug.Slug("data-pipelines"),
		noEnvironmentSlug,
		slug.Slug("owner"),
		pipeline,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Valid)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
	require.NotNil(t, result.Diagnostics[0].StepSlug)
	require.Equal(t, "fetch-users", *result.Diagnostics[0].StepSlug)
	require.Contains(t, result.Diagnostics[0].Message, "lookback_window must be a positive duration")
}

func incrementalCheckpointKindPtr(
	value model.IncrementalCheckpointKind,
) *model.IncrementalCheckpointKind {
	return &value
}
