package pipelinebuilder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestBuilderSchemaReadinessPreservesIncrementalSourceContracts(t *testing.T) {
	t.Parallel()

	state, err := FromPipelineSpec("schema-ready.pipeline.json", []byte(`{
  "metadata": {"slug": "schema-ready", "name": "Schema Ready"},
  "steps": [{
    "kind": "query",
    "slug": "read-orders",
    "query": {
      "data_source_ref": "warehouse",
      "operation_type": "bigquery",
      "operation_inline": {
        "type": "bigquery",
        "operation": "query",
        "sql": "SELECT id, updated_at FROM analytics.orders WHERE updated_at > {{runtime.incremental.lower_bound.value}}"
      },
      "incremental": {
        "checkpoint_kind": "watermark",
        "field": "updated_at",
        "lookback_window": "15m",
        "ordering": ["updated_at", "id"],
        "dedupe_key": "id"
      },
      "output_contract": {
        "name": "orders",
        "representation": "rows",
        "fields": [
          {"name": "id", "type": "string", "required": true},
          {"name": "updated_at", "type": "datetime", "required": true}
        ]
      }
    }
  }]
}`))
	require.NoError(t, err)

	readOrders := requirePipelineStep(t, state.PipelineSpec.Steps, "read-orders")
	require.NotNil(t, readOrders.Query)
	require.NotNil(t, readOrders.Query.Incremental, "builder must preserve query.incremental through spec import")
	require.NotNil(t, readOrders.Query.Incremental.CheckpointKind)
	require.Equal(t, model.IncrementalCheckpointKindWatermark, *readOrders.Query.Incremental.CheckpointKind)
	require.Equal(t, "updated_at", readOrders.Query.Incremental.Field)
	require.Equal(t, "15m", readOrders.Query.Incremental.LookbackWindow)
	require.Equal(t, []string{"updated_at", "id"}, readOrders.Query.Incremental.Ordering)

	exported, err := SerializePipelineSpec(state, model.PipelineFormatJSON)
	require.NoError(t, err)
	require.Contains(t, string(exported), `"incremental"`)
	require.Contains(t, string(exported), `"checkpoint_kind": "watermark"`)
}

func TestBuilderSchemaReadinessPreservesFieldValidationsAndMultiSourceFanout(t *testing.T) {
	t.Parallel()

	unique := true
	minLength := 1
	state, err := FromPipeline(&pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("schema-ready"),
			Name: "Schema Ready",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: slug.Slug("read-orders"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: slug.Slug("warehouse"),
					OperationType:  "bigquery",
					OperationRef:   slug.Slug("orders-since-watermark"),
					OutputContract: validationContract("orders"),
				},
			},
			{
				Kind: "query",
				Slug: slug.Slug("read-accounts"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: slug.Slug("crm"),
					OperationType:  "hubspot",
					OperationRef:   slug.Slug("active-accounts"),
					OutputContract: &payload.Contract{
						Name:           "accounts",
						Representation: model.ContractRepresentationRows,
						Fields: []payload.ContractField{
							{
								Name:     "account_id",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
								Validations: &payload.ContractFieldValidations{
									MinLength: &minLength,
									Unique:    &unique,
								},
							},
						},
					},
				},
			},
			{
				Kind: "transform",
				Slug: slug.Slug("join-orders"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{
						"orders":   {FromStep: slug.Slug("read-orders")},
						"accounts": {FromStep: slug.Slug("read-accounts")},
					},
					SQL:            "SELECT orders.id, accounts.account_id FROM orders JOIN accounts ON orders.account_id = accounts.account_id",
					OutputKind:     model.TransformOutputKindRows,
					InputContract:  validationContract("joined_inputs"),
					OutputContract: validationContract("joined_orders"),
				},
			},
			{
				Kind: "sink",
				Slug: slug.Slug("write-warehouse"),
				Sink: &pipelinespec.SinkSpec{
					Input:            &pipelinespec.StepInput{FromStep: slug.Slug("join-orders")},
					InputContract:    validationContract("warehouse_input"),
					DataSourceSlug:   slug.Slug("warehouse"),
					OperationType:    "bigquery",
					OperationRef:     slug.Slug("insert-orders"),
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
				},
			},
			{
				Kind: "sink",
				Slug: slug.Slug("archive-orders"),
				Sink: &pipelinespec.SinkSpec{
					Input:            &pipelinespec.StepInput{FromStep: slug.Slug("join-orders")},
					InputContract:    validationContract("archive_input"),
					DataSourceSlug:   slug.Slug("object-store"),
					OperationType:    "s3",
					OperationInline:  map[string]any{"operation": "write", "key": "exports/orders.csv", "format": "csv"},
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
				},
			},
		},
	})
	require.NoError(t, err)

	joinOrders := requirePipelineStep(t, state.PipelineSpec.Steps, "join-orders")
	require.Equal(t, map[string]pipelinespec.StepInput{
		"accounts": {FromStep: slug.Slug("read-accounts")},
		"orders":   {FromStep: slug.Slug("read-orders")},
	}, joinOrders.Transform.Inputs)
	requireFieldValidation(t, joinOrders.Transform.OutputContract, "id")
	require.Equal(t, []Edge{
		{FromStep: slug.Slug("join-orders"), ToStep: slug.Slug("archive-orders")},
		{FromStep: slug.Slug("join-orders"), ToStep: slug.Slug("write-warehouse")},
		{FromStep: slug.Slug("read-accounts"), ToStep: slug.Slug("join-orders")},
		{FromStep: slug.Slug("read-orders"), ToStep: slug.Slug("join-orders")},
	}, state.Graph.Edges)

	rebuilt, err := ToPipeline(state)
	require.NoError(t, err)
	rebuiltJoin := requirePipelineStep(t, rebuilt.Steps, "join-orders")
	require.Equal(t, joinOrders.Transform.Inputs, rebuiltJoin.Transform.Inputs)
	requireFieldValidation(t, rebuiltJoin.Transform.OutputContract, "id")

	rebuiltWarehouse := requirePipelineStep(t, rebuilt.Steps, "write-warehouse")
	require.Equal(t, "bigquery", rebuiltWarehouse.Sink.OperationType)
	requireFieldValidation(t, rebuiltWarehouse.Sink.InputContract, "id")
	rebuiltArchive := requirePipelineStep(t, rebuilt.Steps, "archive-orders")
	require.Equal(t, "s3", rebuiltArchive.Sink.OperationType)
	require.Equal(t, "exports/orders.csv", rebuiltArchive.Sink.OperationInline["key"])
}

func TestBuilderSchemaReadinessResolvesCurrentSinkOperationBackends(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name          string
		operationType string
		want          model.SinkBackend
	}{
		{name: "SQL", operationType: "sql", want: model.SinkBackendDB},
		{name: "BigQuery", operationType: "bigquery", want: model.SinkBackendDB},
		{name: "HTTP", operationType: "http", want: model.SinkBackendHTTP},
		{name: "MongoDB", operationType: "mongodb", want: model.SinkBackendMongo},
		{name: "FTP", operationType: "ftp", want: model.SinkBackendFTP},
		{name: "Git", operationType: "git", want: model.SinkBackendGit},
		{name: "S3", operationType: "s3", want: model.SinkBackendS3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ResolveSinkBackend(tt.operationType))
		})
	}
}

func TestBuilderSchemaReadinessResolvesDescriptorSinkBackends(t *testing.T) {
	t.Parallel()

	for _, capability := range pipelinespec.AllConnectorCapabilities() {
		if !capability.SupportsPipelineSink || capability.SinkBackend == model.SinkBackendUnknown {
			continue
		}

		t.Run(capability.DataSourceType.String(), func(t *testing.T) {
			t.Parallel()

			require.Equal(
				t,
				capability.SinkBackend,
				ResolveSinkBackend(capability.QueryOperationType),
			)
		})
	}
}

func validationContract(name string) *payload.Contract {
	unique := true
	minLength := 1

	return &payload.Contract{
		Name:           name,
		Representation: model.ContractRepresentationRows,
		Fields: []payload.ContractField{
			{
				Name:     "id",
				Type:     model.ContractFieldTypeValueString,
				Required: true,
				Validations: &payload.ContractFieldValidations{
					Format:    "uuid",
					MinLength: &minLength,
					Unique:    &unique,
				},
			},
			{
				Name:     "status",
				Type:     model.ContractFieldTypeValueString,
				Required: true,
				Validations: &payload.ContractFieldValidations{
					Enum: []any{"active", "pending"},
				},
			},
			{
				Name: "total",
				Type: model.ContractFieldTypeValueNumber,
				Validations: &payload.ContractFieldValidations{
					Min: 0,
					Max: 1000000,
				},
			},
		},
	}
}

func requirePipelineStep(t *testing.T, steps []pipelinespec.Step, stepSlug string) pipelinespec.Step {
	t.Helper()

	for _, step := range steps {
		if step.Slug == slug.Slug(stepSlug) {
			return step
		}
	}
	require.FailNowf(t, "missing pipeline step", "step %q not found", stepSlug)
	return pipelinespec.Step{}
}

func requireFieldValidation(t *testing.T, contract *payload.Contract, fieldName string) {
	t.Helper()

	require.NotNil(t, contract)
	for _, field := range contract.Fields {
		if field.Name == fieldName {
			require.NotNil(t, field.Validations, "field %q must preserve validations", fieldName)
			require.True(t, payload.ContractHasFieldValidations(contract))
			return
		}
	}
	require.FailNowf(t, "missing contract field", "field %q not found", fieldName)
}

func TestBuilderClonePreservesDecimalDeclarationAndBounds(t *testing.T) {
	precision, scale := 38, 2
	original := &payload.Contract{Fields: []payload.ContractField{{Name: "price", Type: model.ContractFieldTypeValueDecimal, Precision: &precision, Scale: &scale, Validations: &payload.ContractFieldValidations{Min: json.Number("9007199254740993.10")}}}}
	cloned := cloneSchemaContract(original)
	require.Equal(t, json.Number("9007199254740993.10"), cloned.Fields[0].Validations.Min)
	*cloned.Fields[0].Precision = 20
	*cloned.Fields[0].Scale = 3
	require.Equal(t, 38, *original.Fields[0].Precision)
	require.Equal(t, 2, *original.Fields[0].Scale)
}
