package views

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestPipelineSpecProjectionPanelRendersStepsAndContracts(t *testing.T) {
	t.Parallel()

	pipeline := testPipelineWithSpecContracts(t)
	projection := PipelineSpecProjectionFromPipeline(pipeline)

	require.Equal(t, 3, projection.TotalSteps)
	require.Len(t, projection.SourceSteps, 1)
	require.Len(t, projection.TransformSteps, 1)
	require.Len(t, projection.SinkSteps, 1)
	require.Empty(t, projection.DecodeIssues)
	require.Equal(t, 1, projection.SourceSteps[0].SpecPosition)
	require.Equal(t, 2, projection.TransformSteps[0].SpecPosition)
	require.Equal(t, 3, projection.SinkSteps[0].SpecPosition)
	require.Empty(t, projection.TransformSteps[0].OperationLabel)

	var out bytes.Buffer
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &out))

	body := out.String()
	require.Contains(t, body, "Pipeline spec and contracts")
	require.Contains(t, body, "Source steps")
	require.Contains(t, body, "Transform steps")
	require.Contains(t, body, "Sink steps")
	require.Contains(t, body, "Fetch customers")
	require.Contains(t, body, "load-customers")
	require.Contains(t, body, "prod-db")
	require.Contains(t, body, "top-customers")
	require.Contains(t, body, "Normalize customers")
	require.Contains(t, body, "input &lt;- load-customers")
	require.Contains(t, body, "SELECT id, email, status FROM input")
	require.Contains(t, body, "Warehouse load")
	require.Contains(t, body, "payload &lt;- normalize-customers")
	require.Contains(t, body, "Output contract")
	require.Contains(t, body, "Input contract")
	require.Contains(t, body, "source-customers")
	require.Contains(t, body, "transform-input")
	require.Contains(t, body, "normalized-customers")
	require.Contains(t, body, "warehouse-input")
	require.Contains(t, body, "rows / 3 fields")
	require.NotContains(t, body, "Root shape")
	require.Contains(t, body, "format email")
	require.Contains(t, body, "enum active, pending")
	require.Contains(t, body, "unique")
	require.Contains(t, body, "min_length 1")
	require.Contains(t, body, ">01<")
	require.Contains(t, body, ">02<")
	require.Contains(t, body, ">03<")
	require.NotContains(t, body, "sql_v1")
}

func TestPipelineSpecProjectionRendersRunInputAsSourceStep(t *testing.T) {
	t.Parallel()

	representation := api.PipelineSchemaContractRepresentationJson
	var inputStep api.PipelineStep
	require.NoError(t, inputStep.FromPipelineInputStep(api.PipelineInputStep{
		Slug: "event",
		Input: api.PipelineInputSpec{OutputContract: &api.PipelineSchemaContract{
			Name: "event", Representation: &representation,
		}},
	}))

	pipeline := &api.Pipeline{Spec: api.PipelineSpec{
		Steps: []api.PipelineStep{inputStep},
	}}
	projection := PipelineSpecProjectionFromPipeline(pipeline)
	require.Empty(t, projection.DecodeIssues)
	require.Len(t, projection.SourceSteps, 1)
	require.Equal(t, "Input", projection.SourceSteps[0].KindLabel)
	require.Equal(t, "event", projection.SourceSteps[0].Slug)
	require.Equal(t, "event", projection.SourceSteps[0].ContractSlots[0].Contract.Name)

	var out bytes.Buffer
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &out))
	require.Contains(t, out.String(), ">Input<")
	require.Contains(t, out.String(), ">event<")
	require.Contains(t, out.String(), "Output contract")
}

func TestPipelineSpecContractHelpersRenderNilAndJSONShape(t *testing.T) {
	t.Parallel()

	require.Equal(t, "not declared", PipelineSpecContractSummary(nil))
	require.Empty(t, PipelineSpecContractName(nil))
	require.Equal(t, "-", PipelineSpecContractRepresentation(nil))
	require.Equal(t, "-", PipelineSpecContractRootShape(nil))
	require.Nil(t, PipelineSpecContractFields(nil))

	representation := api.PipelineSchemaContractRepresentationJson
	rootShape := api.PipelineSchemaContractRootShapeArray
	contract := &api.PipelineSchemaContract{
		Name:           "json-payload",
		Representation: &representation,
		RootShape:      &rootShape,
	}
	require.Equal(t, "json / root array / no fields", PipelineSpecContractSummary(contract))
	require.Equal(t, "json-payload", PipelineSpecContractName(contract))
	require.Equal(t, "json", PipelineSpecContractRepresentation(contract))
	require.Equal(t, "array", PipelineSpecContractRootShape(contract))
	require.True(t, PipelineSpecContractHasRootShape(contract))

	rowsRepresentation := api.PipelineSchemaContractRepresentationRows
	rowsContract := &api.PipelineSchemaContract{
		Representation: &rowsRepresentation,
		RootShape:      &rootShape,
	}
	require.False(t, PipelineSpecContractHasRootShape(rowsContract))
}

func TestPipelineSpecContractSlotCardRendersRootShapeOnlyForJSON(t *testing.T) {
	t.Parallel()

	rowsContract := testRowsContract("tabular-contract")
	var rowsOut bytes.Buffer
	require.NoError(t, PipelineSpecContractSlotCard(PipelineSpecContractSlot{Label: "Output contract", Contract: rowsContract}).Render(context.Background(), &rowsOut))
	require.NotContains(t, rowsOut.String(), "Root shape")

	representation := api.PipelineSchemaContractRepresentationJson
	rootShape := api.PipelineSchemaContractRootShapeObject
	jsonContract := api.PipelineSchemaContract{
		Name:           "json-contract",
		Representation: &representation,
		RootShape:      &rootShape,
	}
	var jsonOut bytes.Buffer
	require.NoError(t, PipelineSpecContractSlotCard(PipelineSpecContractSlot{Label: "Output contract", Contract: &jsonContract}).Render(context.Background(), &jsonOut))
	require.Contains(t, jsonOut.String(), "Root shape")
	require.Contains(t, jsonOut.String(), ">object<")
}

func TestPipelineSpecProjectionPanelRendersInlineOperationsAndSpecPositions(t *testing.T) {
	t.Parallel()

	var queryStep api.PipelineStep
	require.NoError(t, queryStep.FromPipelineQueryStep(api.PipelineQueryStep{
		Slug: "fetch-inline",
		Name: testAPINamePtr("Fetch inline"),
		Query: api.PipelineQueryRef{
			DataSourceRef: "prod-db",
			OperationType: api.PipelineQueryRefOperationTypeSql,
			OperationInline: testAPIOperationInlinePtr(map[string]interface{}{
				"sql":             "SELECT id FROM users",
				"timeout_seconds": float64(30),
			}),
		},
	}))

	var transformStep api.PipelineStep
	require.NoError(t, transformStep.FromPipelineTransformStep(api.PipelineTransformStep{
		Slug: "normalize-inline",
		Transform: api.PipelineTransformSpec{
			Inputs:     map[string]api.Slug{"input": "fetch-inline"},
			Sql:        "SELECT id FROM input",
			OutputKind: api.PipelineTransformSpecOutputKindRows,
		},
	}))

	var sinkStep api.PipelineStep
	require.NoError(t, sinkStep.FromPipelineSinkStep(api.PipelineSinkStep{
		Slug: "write-inline",
		Sink: api.PipelineSinkSpec{
			Input:         "normalize-inline",
			DataSourceRef: "warehouse-db",
			OperationType: api.PipelineSinkSpecOperationTypeSql,
			OperationInline: testAPIOperationInlinePtr(map[string]interface{}{
				"method": "POST",
				"path":   "/users",
			}),
		},
	}))

	pipeline := &api.Pipeline{
		Slug: "inline-sync",
		Name: "Inline Sync",
		Spec: api.PipelineSpec{
			Metadata: api.PipelineMetadata{Slug: "inline-sync", Name: "Inline Sync"},
			Steps:    []api.PipelineStep{queryStep, transformStep, sinkStep},
		},
	}
	projection := PipelineSpecProjectionFromPipeline(pipeline)

	require.Equal(t, 1, projection.SourceSteps[0].SpecPosition)
	require.Equal(t, 2, projection.TransformSteps[0].SpecPosition)
	require.Equal(t, 3, projection.SinkSteps[0].SpecPosition)
	require.Equal(t, "inline", projection.SourceSteps[0].OperationMode)
	require.Contains(t, projection.SourceSteps[0].OperationInline, "SELECT id FROM users")
	require.Contains(t, projection.SinkSteps[0].OperationInline, "/users")

	var out bytes.Buffer
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &out))

	body := out.String()
	require.Contains(t, body, ">01<")
	require.Contains(t, body, ">02<")
	require.Contains(t, body, ">03<")
	require.Contains(t, body, "SELECT id FROM users")
	require.Contains(t, body, "POST")
	require.Contains(t, body, "/users")
	require.NotContains(t, body, ">inline operation<")
}

func TestPipelineSpecProjectionPanelRendersDecodeIssueDetails(t *testing.T) {
	t.Parallel()

	var malformedStep api.PipelineStep
	require.NoError(t, json.Unmarshal([]byte(`{"kind":"unsupported","slug":"bad-step"}`), &malformedStep))
	pipeline := &api.Pipeline{
		Slug: "malformed-sync",
		Name: "Malformed Sync",
		Spec: api.PipelineSpec{
			Metadata: api.PipelineMetadata{Slug: "malformed-sync", Name: "Malformed Sync"},
			Steps:    []api.PipelineStep{malformedStep},
		},
	}
	projection := PipelineSpecProjectionFromPipeline(pipeline)

	require.Equal(t, []string{"step 1 could not be decoded"}, projection.DecodeIssues)
	require.Equal(t, "2 spec steps could not be decoded for display.", PipelineSpecDecodeIssueSummary(2))

	var out bytes.Buffer
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &out))

	body := out.String()
	require.Contains(t, body, "1 spec step could not be decoded for display.")
	require.Contains(t, body, "step 1 could not be decoded")
}

func testPipelineWithSpecContracts(t *testing.T) *api.Pipeline {
	t.Helper()

	var queryStep api.PipelineStep
	require.NoError(t, queryStep.FromPipelineQueryStep(api.PipelineQueryStep{
		Slug: "load-customers",
		Name: testAPINamePtr("Fetch customers"),
		Query: api.PipelineQueryRef{
			DataSourceRef:  "prod-db",
			OperationType:  api.PipelineQueryRefOperationTypeSql,
			OperationRef:   testAPISlugPtr("top-customers"),
			OutputContract: testRowsContract("source-customers"),
		},
	}))

	var transformStep api.PipelineStep
	require.NoError(t, transformStep.FromPipelineTransformStep(api.PipelineTransformStep{
		Slug: "normalize-customers",
		Name: testAPINamePtr("Normalize customers"),
		Transform: api.PipelineTransformSpec{
			Inputs:         map[string]api.Slug{"input": "load-customers"},
			Sql:            "SELECT id, email, status FROM input",
			OutputKind:     api.PipelineTransformSpecOutputKindRows,
			InputContract:  testRowsContract("transform-input"),
			OutputContract: testRowsContract("normalized-customers"),
		},
	}))

	idempotencyClass := api.PipelineSinkSpecIdempotencyClassIdempotentWithKey
	var sinkStep api.PipelineStep
	require.NoError(t, sinkStep.FromPipelineSinkStep(api.PipelineSinkStep{
		Slug: "warehouse-load",
		Name: testAPINamePtr("Warehouse load"),
		Sink: api.PipelineSinkSpec{
			Input:            "normalize-customers",
			DataSourceRef:    "warehouse-db",
			OperationType:    api.PipelineSinkSpecOperationTypeSql,
			OperationRef:     testAPISlugPtr("upsert-customers"),
			IdempotencyClass: idempotencyClass,
			InputContract:    testRowsContract("warehouse-input"),
		},
	}))

	return &api.Pipeline{
		Slug: "customer-sync",
		Name: "Customer Sync",
		Spec: api.PipelineSpec{
			Metadata: api.PipelineMetadata{Slug: "customer-sync", Name: "Customer Sync"},
			Steps:    []api.PipelineStep{queryStep, transformStep, sinkStep},
		},
	}
}

func testRowsContract(name string) *api.PipelineSchemaContract {
	representation := api.PipelineSchemaContractRepresentationRows
	format := api.PipelineSchemaContractFieldValidationsFormatEmail
	minLength := 1
	unique := true
	enumValues := []api.PipelineSchemaContractValidationLiteral{
		testContractLiteral("active"),
		testContractLiteral("pending"),
	}
	fields := []api.PipelineSchemaContractField{
		{
			Name:     "id",
			Type:     api.PipelineSchemaContractFieldTypeString,
			Required: true,
			Validations: &api.PipelineSchemaContractFieldValidations{
				MinLength: &minLength,
				Unique:    &unique,
			},
		},
		{
			Name:     "email",
			Type:     api.PipelineSchemaContractFieldTypeString,
			Required: true,
			Validations: &api.PipelineSchemaContractFieldValidations{
				Format: &format,
			},
		},
		{
			Name:     "status",
			Type:     api.PipelineSchemaContractFieldTypeString,
			Required: false,
			Validations: &api.PipelineSchemaContractFieldValidations{
				Enum: &enumValues,
			},
		},
	}
	return &api.PipelineSchemaContract{
		Name:           name,
		Representation: &representation,
		Fields:         &fields,
	}
}

func testContractLiteral(value string) api.PipelineSchemaContractValidationLiteral {
	var literal api.PipelineSchemaContractValidationLiteral
	_ = literal.FromPipelineSchemaContractValidationLiteral0(value)
	return literal
}

func testAPINamePtr(value string) *api.Name {
	name := value
	return &name
}

func testAPISlugPtr(value string) *api.Slug {
	slug := value
	return &slug
}

func testAPIOperationInlinePtr(value map[string]interface{}) *map[string]interface{} {
	return &value
}

func TestPipelineOverviewBoundsLargeWorkflows(t *testing.T) {
	pipeline := testPipelineWithSpecContracts(t)
	source := pipeline.Spec.Steps[0]
	pipeline.Spec.Steps = []api.PipelineStep{source, source, source, source, source}
	var overview, workflow bytes.Buffer
	require.NoError(t, PipelineWorkflowOverview(pipeline).Render(context.Background(), &overview))
	require.Equal(t, 3, strings.Count(overview.String(), "Fetch customers"))
	require.Contains(t, overview.String(), "2 more in Workflow")
	require.NotContains(t, overview.String(), "top-customers")
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &workflow))
	require.Equal(t, 5, strings.Count(workflow.String(), "data-pipeline-step"))
	require.Contains(t, workflow.String(), "top-customers")
}

func TestPipelineSpecProjectionDisplaysReferencedEventAgreement(t *testing.T) {
	var input api.PipelineStep
	require.NoError(t, json.Unmarshal([]byte(`{"kind":"input","slug":"event","input":{"output_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","representation":"json","root_shape":"object"}}}`), &input))
	pipeline := &api.Pipeline{Spec: api.PipelineSpec{Steps: []api.PipelineStep{input}}}
	projection := PipelineSpecProjectionFromPipeline(pipeline)
	require.Empty(t, projection.DecodeIssues)
	require.Equal(t, "orders", projection.SourceSteps[0].ContractSlots[0].Ref.ContractSlug)
	var out bytes.Buffer
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &out))
	require.Contains(t, out.String(), "Exact ODCS reference")
	require.Contains(t, out.String(), "orders · v1 · row")
	require.NotContains(t, out.String(), "not declared")
}

func TestPipelineSpecProjectionDisplaysIndependentNamedRequirements(t *testing.T) {
	var step api.PipelineStep
	require.NoError(t, json.Unmarshal([]byte(`{"kind":"transform","slug":"merge","transform":{"sql":"SELECT * FROM orders","output_kind":"rows","inputs":{"orders":"fetch","accounts":"accounts"},"input_contracts":{"orders":{"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row"}},"accounts":{"contract_ref":{"contract_slug":"accounts","revision_slug":"v2","schema_object_id":"account"}}}}}`), &step))
	pipeline := &api.Pipeline{Spec: api.PipelineSpec{Steps: []api.PipelineStep{step}}}
	projection := PipelineSpecProjectionFromPipeline(pipeline)
	require.Empty(t, projection.DecodeIssues)
	require.Len(t, projection.TransformSteps[0].ContractSlots, 3)
	require.Equal(t, "Output contract", projection.TransformSteps[0].ContractSlots[0].Label)
	require.Equal(t, "Input requirement: accounts", projection.TransformSteps[0].ContractSlots[1].Label)
	require.Equal(t, "Input requirement: orders", projection.TransformSteps[0].ContractSlots[2].Label)
	var out bytes.Buffer
	require.NoError(t, PipelineSpecProjectionPanel(pipeline, false).Render(context.Background(), &out))
	require.Contains(t, out.String(), "accounts · v2 · account")
	require.Contains(t, out.String(), "orders · v1 · row")
}
