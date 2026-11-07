package pipelineruntime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestPreValidateTransforms_NilPipeline(t *testing.T) {
	t.Parallel()

	runner := &Runner{dataSourceService: preValidationMockService{}}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), nil)
	require.ErrorIs(t, err, ErrNilPipeline)
}

func TestPreValidateTransforms_QueryOnlyWithoutContractSkipsSchemaLookup(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{}}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
	require.Nil(t, pipeline.Steps[0].Query.OutputContract)
}

func TestPreValidateTransforms_QueryOnlyFailsOnProvidedContractDrift(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
					}}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name: "users",
							Columns: []datasourcessvc.SchemaColumn{
								{Name: "id", Type: "integer"},
							},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, err, "query output_contract mismatch")
}

func TestPreValidateTransforms_TransformWithoutUpstreamQuerySchema(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					SQL:        "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{}}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.Error(t, err)
	require.ErrorContains(t, err, "must reference at least one upstream step output")
}

func TestPreValidateTransforms_CachedSchemaLoadFailure(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{findBySlugErr: fmt.Errorf("boom")},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.Error(t, err)
	require.ErrorContains(t, err, "load cached schema")
}

func TestPreValidateTransforms_ReturnsNotFoundWhenDataSourceLookupReturnsNil(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs:     map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}},
					SQL:        "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{}}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)
}

func TestPreValidateTransforms_ValidSchemaBackedTransform(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id, email AS email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
						{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						},
					}},
				},
			},
		},
	}

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "varchar"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("postgres-main"): schema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
}

func TestPreValidateTransforms_RowConnectorResolvedContractInference(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		dataSourceSlug    slug.Slug
		queryContract     *payload.Contract
		transformSQL      string
		transformContract *payload.Contract
		schema            datasourcessvc.SchemaResult
	}{
		{
			name:           "HubSpotAggregateAndCast",
			dataSourceSlug: s("hubspot-main"),
			queryContract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "email", Type: model.ContractFieldTypeValueString, Required: false},
			}},
			transformSQL: "SELECT CAST(COUNT(*) AS SIGNED) AS record_count FROM input",
			transformContract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "record_count", Type: model.ContractFieldTypeValueInteger, Required: true},
			}},
			schema: datasourcessvc.Schema[datasourcessvc.ObjectAPISchemaPayload]{
				SchemaTypeValue: datasourcessvc.SchemaTypeObjectAPI,
				Type:            "hubspot",
				Payload: datasourcessvc.ObjectAPISchemaPayload{Objects: []datasourcessvc.SchemaObjectAPIObject{
					{
						Name: "contacts",
						Fields: []datasourcessvc.SchemaObjectAPIField{
							{Name: "email", Type: "string"},
						},
					},
				}},
			},
		},
		{
			name:           "GitHubProjectionAliasAndFilter",
			dataSourceSlug: s("github-main"),
			queryContract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "account_id", Type: model.ContractFieldTypeValueString, Required: true},
				{Name: "active", Type: model.ContractFieldTypeValueBoolean, Required: true},
			}},
			transformSQL: "SELECT account_id AS id FROM input WHERE active = true",
			transformContract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "id", Type: model.ContractFieldTypeValueString, Required: true},
			}},
			schema: datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
				SchemaTypeValue: datasourcessvc.SchemaTypeFile,
				Type:            "github",
				Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
					{
						Name: "contacts.json",
						Fields: []datasourcessvc.SchemaField{
							{Name: "account_id", Type: "string"},
							{Name: "active", Type: "boolean"},
						},
					},
				}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pipeline := &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{Slug: s("row-inference"), Name: "Row inference"},
				Steps: []pipelinespec.Step{
					{
						Kind: "query",
						Slug: s("read-rows"),
						Query: &pipelinespec.QueryRef{
							DataSourceSlug: tc.dataSourceSlug,
							OperationType:  "read",
							OperationRef:   s("read-rows"),
							OutputContract: tc.queryContract,
						},
					},
					{
						Kind: "transform",
						Slug: s("transform-rows"),
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{
								"input": {FromStep: s("read-rows")},
							},
							SQL:            tc.transformSQL,
							OutputKind:     model.TransformOutputKindRows,
							OutputContract: tc.transformContract,
						},
					},
				},
			}

			runner := &Runner{dataSourceService: preValidationMockService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{tc.dataSourceSlug: tc.schema},
			}}

			err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
			require.NoError(t, err)
		})
	}
}

func TestPreValidateTransforms_TypeMismatch(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						},
					}},
				},
			},
		},
	}

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{Name: "users", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}}},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("postgres-main"): schema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.Error(t, err)
	require.ErrorContains(t, err, "expects string")
}

func TestPreValidateTransforms_IntrospectedSourceTypeDoesNotBypassContractValidation(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT CAST(id AS CHAR) AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{Name: "users", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}}},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("postgres-main"): schema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, transformsql.ErrContractType)
	require.ErrorContains(t, err, "expects integer")
	require.ErrorContains(t, err, "resolves to string")
}

func TestPreValidateTransforms_UsesStepInputToResolveUpstreamSchema(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "query",
				Slug: s("fetch-orders"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-secondary"),
					OperationType:  "sql",
					OperationRef:   s("list-orders"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "users",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
						},
					}},
				},
				s("postgres-secondary"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "orders",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "varchar"}},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
}

func TestPreValidateTransforms_UsesUpstreamTransformContractForDownstreamTransform(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-user-id"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "user_id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: s("project-user-id-copy"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("project-user-id")}}, SQL: "SELECT user_id AS user_id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "user_id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "users",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
}

func TestPreValidateTransforms_AllowsForwardReferencedQueryProducer(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs:     map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}},
					SQL:        "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "users",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
	require.NotNil(t, pipeline.Steps[1].Query)
	require.NotNil(t, pipeline.Steps[1].Query.OutputContract)
}

func TestPreValidateTransforms_RejectsExplicitQueryOutputContractMismatchWithCachedSchema(
	t *testing.T,
) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
					// This explicit contract must match cached schema and should fail fast when
					// drifted.
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						},
					}},
				},
			},
		},
	}

	findBySlugCalled := false
	runner := &Runner{
		dataSourceService: preValidationMockService{
			findBySlugFn: func(orgSlug, dsSlug slug.Slug) {
				findBySlugCalled = true
			},
			// Cached schema reports id as integer.
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "users",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, err, "query output_contract mismatch")
	require.True(t, findBySlugCalled)
}

func TestPreValidateTransforms_DerivesQueryOutputContractFromSchema(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id, email AS email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
						{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						},
					}},
				},
			},
		},
	}

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "varchar"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("postgres-main"): schema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)

	require.NotNil(t, pipeline.Steps[0].Query)
	require.NotNil(t, pipeline.Steps[0].Query.OutputContract)
	require.Len(t, pipeline.Steps[0].Query.OutputContract.Fields, 2)

	fieldTypes := map[string]model.ContractFieldType{}
	for _, field := range pipeline.Steps[0].Query.OutputContract.Fields {
		fieldTypes[field.Name] = field.Type
	}
	require.Equal(t, model.ContractFieldTypeValueInteger, fieldTypes["id"])
	require.Equal(t, model.ContractFieldTypeValueString, fieldTypes["email"])
}

func TestPreValidateTransforms_DerivesTransformOutputContractFromSQL(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{
						"input": {FromStep: s("fetch-users")},
					},
					SQL:        "SELECT id AS user_id FROM input",
					OutputKind: model.TransformOutputKindRows,
				},
			},
			{
				Kind: "sink",
				Slug: s("load-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: s("project-users")},
					DataSourceSlug:   s("postgres-main"),
					OperationType:    "sql",
					OperationRef:     s("upsert-users"),
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "user_id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}},
				},
			},
		},
	}

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "varchar"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("postgres-main"): schema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)

	require.NotNil(t, pipeline.Steps[1].Transform)
	require.NotNil(t, pipeline.Steps[1].Transform.OutputContract)
	require.Equal(
		t,
		model.ContractRepresentationRows,
		pipeline.Steps[1].Transform.OutputContract.Representation,
	)
	require.Len(t, pipeline.Steps[1].Transform.OutputContract.Fields, 1)
	require.Equal(t, "user_id", pipeline.Steps[1].Transform.OutputContract.Fields[0].Name)
	require.Equal(
		t,
		model.ContractFieldTypeValueInteger,
		pipeline.Steps[1].Transform.OutputContract.Fields[0].Type,
	)
}

func TestPreValidateTransforms_DerivesTransformOutputContractFromCTE(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("postgres-main"),
					OperationType:  "sql",
					OperationRef:   s("list-users"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-active-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{
						"input": {FromStep: s("fetch-users")},
					},
					SQL: "WITH active AS (SELECT id, email FROM input WHERE status = 'active') " +
						"SELECT email AS active_email FROM active",
					OutputKind: model.TransformOutputKindRows,
				},
			},
			{
				Kind: "sink",
				Slug: s("load-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: s("project-active-users")},
					DataSourceSlug:   s("postgres-main"),
					OperationType:    "sql",
					OperationRef:     s("upsert-users"),
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "active_email",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
					}}},
				},
			},
		},
	}

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
			{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "varchar"},
					{Name: "status", Type: "varchar"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("postgres-main"): schema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)

	require.NotNil(t, pipeline.Steps[1].Transform)
	require.NotNil(t, pipeline.Steps[1].Transform.OutputContract)
	require.Len(t, pipeline.Steps[1].Transform.OutputContract.Fields, 1)
	require.Equal(t, "active_email", pipeline.Steps[1].Transform.OutputContract.Fields[0].Name)
	require.Equal(
		t,
		model.ContractFieldTypeValueString,
		pipeline.Steps[1].Transform.OutputContract.Fields[0].Type,
	)
}

func TestPreValidateTransforms_DerivesFTPQueryOutputContractFromSchema(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users-csv"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("ftp-main"),
					OperationType:  "sql",
					OperationRef:   s("read-users-csv"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-csv")}}, SQL: "SELECT id AS id, email AS email FROM input",
					OutputKind: model.TransformOutputKindRows,
				},
			},
		},
	}

	ftpSchema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "ftp",
		Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
			{
				Name: "users.csv",
				Fields: []datasourcessvc.SchemaField{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "string"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("ftp-main"): ftpSchema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)

	require.NotNil(t, pipeline.Steps[0].Query)
	require.NotNil(t, pipeline.Steps[0].Query.OutputContract)
	require.Len(t, pipeline.Steps[0].Query.OutputContract.Fields, 2)

	fieldTypes := map[string]model.ContractFieldType{}
	for _, field := range pipeline.Steps[0].Query.OutputContract.Fields {
		fieldTypes[field.Name] = field.Type
	}
	require.Equal(t, model.ContractFieldTypeValueInteger, fieldTypes["id"])
	require.Equal(t, model.ContractFieldTypeValueString, fieldTypes["email"])
}

func TestPreValidateTransforms_FTPIntrospectionFailureIncludesStepContext(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users-csv"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("ftp-main"),
					OperationType:  "sql",
					OperationRef:   s("read-users-csv"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-csv")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
					}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			findBySlugErr: fmt.Errorf("ftp schema unavailable"),
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.Error(t, err)
	require.ErrorContains(t, err, "step[0] \"fetch-users-csv\" load cached schema")
	require.ErrorContains(t, err, "datasource \"ftp-main\"")
	require.ErrorContains(t, err, "ftp schema unavailable")
}

func TestPreValidateTransforms_ValidatesTransformContractFromDerivedFTPSchema(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users-csv"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("ftp-main"),
					OperationType:  "sql",
					OperationRef:   s("read-users-csv"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-csv")}}, SQL: "SELECT id AS id, email AS email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
		},
	}

	ftpSchema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "ftp",
		Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
			{
				Name: "users.csv",
				Fields: []datasourcessvc.SchemaField{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "string"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("ftp-main"): ftpSchema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
}

func TestPreValidateTransforms_RejectsTransformContractTypeMismatchFromDerivedFTPSchema(
	t *testing.T,
) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users-csv"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("ftp-main"),
					OperationType:  "sql",
					OperationRef:   s("read-users-csv"),
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-csv")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
		},
	}

	ftpSchema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "ftp",
		Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
			{
				Name: "users.csv",
				Fields: []datasourcessvc.SchemaField{
					{Name: "id", Type: "integer"},
				},
			},
		}},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("ftp-main"): ftpSchema},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, transformsql.ErrContractType)
	require.ErrorContains(t, err, "expects string")
	require.ErrorContains(t, err, "resolves to integer")
}

func TestPreValidateTransforms_UsesExplicitFTPQueryOutputContractAndStillLoadsCachedSchema(
	t *testing.T,
) {
	t.Parallel()

	findBySlugCalls := 0
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users-csv"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("ftp-main"),
					OperationType:  "sql",
					OperationRef:   s("read-users-csv"),
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-csv")}}, SQL: "SELECT id AS id, email AS email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			findBySlugFn: func(_, _ slug.Slug) {
				findBySlugCalls++
			},
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("ftp-main"): datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeFile,
					Type:            "ftp",
					Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
						{
							Name: "users.csv",
							Fields: []datasourcessvc.SchemaField{
								{Name: "id", Type: "integer"},
								{Name: "email", Type: "string"},
							},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.NoError(t, err)
	require.Equal(t, 1, findBySlugCalls)
}

func TestPreValidateTransforms_SharedInputContractTypesHTTPProjection(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query", Slug: s("fetch-records"),
				Query: &pipelinespec.QueryRef{DataSourceSlug: s("http-main"), OperationType: "http", OperationRef: s("list-records")},
			},
			{
				Kind: "transform", Slug: s("first-record"),
				Transform: &pipelinespec.TransformSpec{
					Inputs:         map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-records")}},
					SQL:            "SELECT MIN(id) AS first_id FROM input",
					OutputKind:     model.TransformOutputKindRows,
					InputContract:  pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}),
					OutputContract: pipelineContract(payload.ContractField{Name: "first_id", Type: model.ContractFieldTypeValueNumber, Required: true}),
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
		s("http-main"): datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeHTTP, Type: "http"},
	}}}

	require.NoError(t, runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline))
}

func TestPreValidateTransforms_SharedInputContractKeepsNamedFieldsLocal(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{Kind: "query", Slug: s("fetch-left"), Query: &pipelinespec.QueryRef{DataSourceSlug: s("http-main"), OperationType: "http", OperationRef: s("list-left")}},
			{Kind: "query", Slug: s("fetch-right"), Query: &pipelinespec.QueryRef{DataSourceSlug: s("http-main"), OperationType: "http", OperationRef: s("list-right")}},
			{
				Kind: "transform", Slug: s("project-left"),
				Transform: &pipelinespec.TransformSpec{
					Inputs:     map[string]pipelinespec.StepInput{"orders": {FromStep: s("fetch-left")}, "customers": {FromStep: s("fetch-right")}},
					SQL:        "SELECT orders.id AS id FROM orders",
					OutputKind: model.TransformOutputKindRows,
					InputContract: pipelineContract(
						payload.ContractField{Name: "orders.id", Type: model.ContractFieldTypeValueInteger, Required: true},
						payload.ContractField{Name: "customers.id", Type: model.ContractFieldTypeValueString, Required: true},
					),
					OutputContract: pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}),
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
		s("http-main"): datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeHTTP, Type: "http"},
	}}}

	require.NoError(t, runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline))
}

func TestPreValidateTransforms_NamedInputContractOverridesSharedContract(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{Kind: "query", Slug: s("fetch-records"), Query: &pipelinespec.QueryRef{DataSourceSlug: s("http-main"), OperationType: "http", OperationRef: s("list-records")}},
			{
				Kind: "transform", Slug: s("project-record"),
				Transform: &pipelinespec.TransformSpec{
					Inputs:        map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-records")}},
					SQL:           "SELECT id FROM input",
					OutputKind:    model.TransformOutputKindRows,
					InputContract: pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}),
					InputContracts: map[string]*pipelinespec.InputContractAttachment{
						"input": {Contract: pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true})},
					},
					OutputContract: pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}),
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
		s("http-main"): datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeHTTP, Type: "http"},
	}}}

	require.NoError(t, runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline))
}

func TestPreValidateTransforms_SharedInputContractRejectsKnownProviderContradiction(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{Kind: "query", Slug: s("fetch-records"), Query: &pipelinespec.QueryRef{DataSourceSlug: s("postgres-main"), OperationType: "sql", OperationRef: s("list-records")}},
			{
				Kind: "transform", Slug: s("project-record"),
				Transform: &pipelinespec.TransformSpec{
					Inputs:        map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-records")}},
					SQL:           "SELECT id FROM input",
					OutputKind:    model.TransformOutputKindRows,
					InputContract: pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}),
				},
			},
		},
	}

	runner := &Runner{dataSourceService: preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
		s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
				Name: "records", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "varchar"}},
			}}},
		},
	}}}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, pipelinevalidate.ErrContractMismatch)
	require.ErrorContains(t, err, "contradicts known input shape")
}

func TestPreValidateTransforms_ExplicitFTPContractTypeMismatchStillFails(
	t *testing.T,
) {
	t.Parallel()

	findBySlugCalls := 0
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: s("fetch-users-csv"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: s("ftp-main"),
					OperationType:  "sql",
					OperationRef:   s("read-users-csv"),
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: s("project-users"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-csv")}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
		},
	}

	runner := &Runner{
		dataSourceService: preValidationMockService{
			findBySlugFn: func(_, _ slug.Slug) {
				findBySlugCalls++
			},
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("ftp-main"): datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeFile,
					Type:            "ftp",
					Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
						{
							Name:   "users.csv",
							Fields: []datasourcessvc.SchemaField{{Name: "id", Type: "integer"}},
						},
					}},
				},
			},
		},
	}

	err := runner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
	require.ErrorIs(t, err, transformsql.ErrContractType)
	require.ErrorContains(t, err, "expects string")
	require.ErrorContains(t, err, "resolves to integer")
	require.Equal(t, 1, findBySlugCalls)
}

func TestPreValidateTransforms_StepInputResolutionErrors(t *testing.T) {
	t.Parallel()

	baseRunner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "users",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
						},
					}},
				},
			},
		},
	}

	t.Run("MissingStepInputFromStep", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: s("fetch-users"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: s("postgres-main"),
						OperationType:  "sql",
						OperationRef:   s("list-users"),
					},
				},
				{
					Kind: "transform",
					Slug: s("normalize-users"),
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("some-other-step")}}, SQL: "SELECT id AS id FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		}

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.ErrorContains(t, err, `must reference step "some-other-step"`)
	})

	t.Run("ExplicitFromStepSelectsIntendedUpstream", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: s("fetch-users-a"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: s("postgres-main"),
						OperationType:  "sql",
						OperationRef:   s("list-users"),
					},
				},
				{
					Kind: "query",
					Slug: s("fetch-users-b"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: s("postgres-main"),
						OperationType:  "sql",
						OperationRef:   s("list-users"),
					},
				},
				{
					Kind: "transform",
					Slug: s("normalize-users"),
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users-a")}}, SQL: "SELECT id AS id FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		}

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.NoError(t, err)
	})
}

func s(v string) slug.Slug {
	return slug.FromString(v)
}

type preValidationMockService struct {
	datasourcessvc.Service
	schemas       map[slug.Slug]datasourcessvc.SchemaResult
	findBySlugErr error
	findBySlugFn  func(orgSlug, dsSlug slug.Slug)
}

func (m preValidationMockService) FindBySlug(
	_ context.Context,
	orgSlug slug.Slug,
	dsSlug slug.Slug,
) (*datasourcessvc.DataSource, error) {
	if m.findBySlugFn != nil {
		m.findBySlugFn(orgSlug, dsSlug)
	}
	if m.findBySlugErr != nil {
		return nil, m.findBySlugErr
	}
	if m.schemas == nil {
		return nil, nil //nolint:nilnil // Test double returns missing schema without service error.
	}
	schema, ok := m.schemas[dsSlug]
	if !ok {
		return nil, nil //nolint:nilnil // Test double returns missing schema without service error.
	}

	dataSourceType, err := model.DataSourceTypeString(schema.VendorType())
	if err != nil {
		return nil, err
	}

	return &datasourcessvc.DataSource{Type: dataSourceType, Schema: schema}, nil
}

func TestRunner_Run_UsesRequestOrgSlugForTransformPrevalidation(t *testing.T) {
	t.Parallel()

	observedOrgSlug := slug.Slug("")
	runner := &Runner{dataSourceService: preValidationMockService{
		findBySlugFn: func(orgSlug, _ slug.Slug) {
			observedOrgSlug = orgSlug
		},
		schemas: map[slug.Slug]datasourcessvc.SchemaResult{
			s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
				SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
				Type:            "postgres",
				Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
					{
						Name:    "users",
						Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
					},
				}},
			},
		},
	}}

	err := runner.PreValidateTransforms(
		context.Background(),
		s("org-from-request"),
		&pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: s("org-from-pipeline")},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: s("fetch-users"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: s("postgres-main"),
						OperationType:  "sql",
						OperationRef:   s("list-users"),
					},
				},
				{
					Kind: "transform",
					Slug: s("normalize-users"),
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: s("fetch-users")}}, SQL: "SELECT id AS id FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, s("org-from-request"), observedOrgSlug)
}

func TestPreValidateTransforms_NamedInputSQLContracts(t *testing.T) {
	t.Parallel()

	baseRunner := &Runner{
		dataSourceService: preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				s("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name: "orders",
							Columns: []datasourcessvc.SchemaColumn{
								{Name: "id", Type: "integer"},
								{Name: "customer_id", Type: "integer"},
							},
						},
						{
							Name: "customers",
							Columns: []datasourcessvc.SchemaColumn{
								{Name: "id", Type: "integer"},
								{Name: "email", Type: "varchar"},
							},
						},
					}},
				},
			},
		},
	}

	t.Run("TwoQueryInputsQualifiedColumnsPass", func(t *testing.T) {
		t.Parallel()

		pipeline := namedInputJoinPipeline(
			"SELECT orders.id AS order_id, customers.email AS customer_email FROM orders JOIN customers ON orders.customer_id = customers.id",
			pipelineContract(
				payload.ContractField{Name: "order_id", Type: model.ContractFieldTypeValueInteger, Required: true},
				payload.ContractField{Name: "customer_email", Type: model.ContractFieldTypeValueString, Required: true},
			),
		)

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.NoError(t, err)
	})

	t.Run("SourceAndPriorTransformInputsPass", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
			Steps: []pipelinespec.Step{
				namedInputQueryStep("fetch-orders", pipelineContract(
					payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
					payload.ContractField{Name: "customer_id", Type: model.ContractFieldTypeValueInteger, Required: true},
				)),
				namedInputQueryStep("fetch-customers", pipelineContract(
					payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
					payload.ContractField{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
				)),
				{
					Kind: "transform",
					Slug: s("project-customers"),
					Transform: &pipelinespec.TransformSpec{
						Inputs:     map[string]pipelinespec.StepInput{"customers": {FromStep: s("fetch-customers")}},
						SQL:        "SELECT id AS customer_id, email AS email FROM customers",
						OutputKind: model.TransformOutputKindRows,
						OutputContract: pipelineContract(
							payload.ContractField{Name: "customer_id", Type: model.ContractFieldTypeValueInteger, Required: true},
							payload.ContractField{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
						),
					},
				},
				{
					Kind: "transform",
					Slug: s("join-orders"),
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"orders":    {FromStep: s("fetch-orders")},
							"customers": {FromStep: s("project-customers")},
						},
						SQL:        "SELECT orders.id AS order_id, customers.email AS customer_email FROM orders JOIN customers ON orders.customer_id = customers.customer_id",
						OutputKind: model.TransformOutputKindRows,
						OutputContract: pipelineContract(
							payload.ContractField{Name: "order_id", Type: model.ContractFieldTypeValueInteger, Required: true},
							payload.ContractField{Name: "customer_email", Type: model.ContractFieldTypeValueString, Required: true},
						),
					},
				},
			},
		}

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.NoError(t, err)
	})

	t.Run("UnknownInputRelationFailsClosed", func(t *testing.T) {
		t.Parallel()

		pipeline := namedInputJoinPipeline(
			"SELECT invoices.id AS invoice_id FROM invoices",
			pipelineContract(payload.ContractField{Name: "invoice_id", Type: model.ContractFieldTypeValueInteger, Required: true}),
		)

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.Error(t, err)
		require.ErrorContains(t, err, `step[2] "join-orders"`)
		require.ErrorContains(t, err, `unknown transform input "invoices"`)
	})

	t.Run("UnqualifiedUniqueColumnIsTypeChecked", func(t *testing.T) {
		t.Parallel()

		pipeline := namedInputJoinPipeline(
			"SELECT email AS customer_email FROM orders JOIN customers ON orders.customer_id = customers.id",
			pipelineContract(payload.ContractField{Name: "customer_email", Type: model.ContractFieldTypeValueInteger, Required: true}),
		)

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.ErrorIs(t, err, transformsql.ErrContractType)
		require.ErrorContains(t, err, `step[2] "join-orders"`)
		require.ErrorContains(t, err, `expects integer`)
		require.ErrorContains(t, err, `resolves to string`)
	})

	t.Run("UnqualifiedAmbiguousColumnFailsClosed", func(t *testing.T) {
		t.Parallel()

		pipeline := namedInputJoinPipeline(
			"SELECT id AS id FROM orders JOIN customers ON orders.customer_id = customers.id",
			pipelineContract(payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}),
		)

		err := baseRunner.PreValidateTransforms(context.Background(), s("org-a"), pipeline)
		require.Error(t, err)
		require.ErrorContains(t, err, `step[2] "join-orders"`)
		require.ErrorContains(t, err, `ambiguous column reference "id"`)
	})
}

func namedInputJoinPipeline(sqlText string, outputContract *payload.Contract) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: s("org-a"), Name: "Org A"},
		Steps: []pipelinespec.Step{
			namedInputQueryStep("fetch-orders", pipelineContract(
				payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
				payload.ContractField{Name: "customer_id", Type: model.ContractFieldTypeValueInteger, Required: true},
			)),
			namedInputQueryStep("fetch-customers", pipelineContract(
				payload.ContractField{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
				payload.ContractField{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
			)),
			{
				Kind: "transform",
				Slug: s("join-orders"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{
						"orders":    {FromStep: s("fetch-orders")},
						"customers": {FromStep: s("fetch-customers")},
					},
					SQL:            sqlText,
					OutputKind:     model.TransformOutputKindRows,
					OutputContract: outputContract,
				},
			},
		},
	}
}

func namedInputQueryStep(stepSlug string, outputContract *payload.Contract) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "query",
		Slug: s(stepSlug),
		Query: &pipelinespec.QueryRef{
			DataSourceSlug: s("postgres-main"),
			OperationType:  "sql",
			OperationRef:   s("list-" + stepSlug),
			OutputContract: outputContract,
		},
	}
}

func pipelineContract(fields ...payload.ContractField) *payload.Contract {
	return &payload.Contract{
		Representation: model.ContractRepresentationRows,
		Fields:         fields,
	}
}
