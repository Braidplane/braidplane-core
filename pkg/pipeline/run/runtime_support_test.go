package run

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestRuntimeSupportConstructorsAndParamResolution(t *testing.T) {
	t.Parallel()

	stepResult := NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{
		Rows:        []map[string]any{{"id": 1}},
		ColumnNames: []string{"id"},
		RowCount:    1,
	}, 5, nil))
	require.Equal(t, int64(5), stepResult.DurationMs())

	transformResult := NewTransformStepResult(&pipepayload.Output{
		OutputKind: model.TransformOutputKindRows,
		RowCount:   1,
		Rows:       []map[string]any{{"id": 2}},
	}, 7, nil)
	require.Equal(t, int64(7), transformResult.DurationMs())

	resolved, err := ResolveParams(
		map[string]any{"rows": "$steps.fetch-users.rows"},
		map[slug.Slug]*StepResult{must.Slug("fetch-users"): stepResult},
	)
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"id": 1}}, resolved["rows"])
}

func TestRuntimeSupportContractWrappers(t *testing.T) {
	t.Parallel()

	contract := &pipepayload.Contract{
		Name:           "users",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{{
			Name:     "email",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
		}},
	}
	source := NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{
		Rows:        []map[string]any{{"email": "one@example.test"}},
		ColumnNames: []string{"email"},
		RowCount:    1,
	}, 0, nil))

	require.NoError(t, ValidateQueryOutputContractAtRuntime(source.Execution(), contract))
	require.NoError(t, ValidateSinkInputContract(source, contract))

	effective := EffectiveRuntimePayloadContract("runtime-users", &pipepayload.Payload{
		Representation: model.ContractRepresentationRows,
		ColumnNames:    []string{"email"},
		Rows:           []map[string]any{{"email": "one@example.test"}},
	}, nil)
	require.Equal(t, "runtime-users", effective.Name)
	require.Equal(t, model.ContractRepresentationRows, effective.Representation)
}

func TestRuntimeSupportEdgeDiagnosticsUseRuntimePayloadContractPerFanOutEdge(t *testing.T) {
	t.Parallel()

	pipeline := runtimeSupportFanOutPipeline()
	preflight, err := PreflightEdgeDiagnostics(pipeline)
	require.NoError(t, err)
	require.Len(t, preflight, 2)
	require.True(t, preflight[0].Compatible)
	require.True(t, preflight[1].Compatible)
	require.Equal(t, preflight, CollectEdgeDiagnostics(pipeline))

	completed := map[slug.Slug]*StepResult{
		must.Slug("fetch-users"): NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"id": 1}},
			ColumnNames: []string{"id"},
			RowCount:    1,
		}, 0, nil)),
	}
	runtimePayload, ok := completed[must.Slug("fetch-users")].Payload()
	require.True(t, ok)
	runtimePayload.Contract = &pipepayload.Contract{
		Name:           "runtime-users",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{{
			Name:     "id",
			Type:     model.ContractFieldTypeValueInteger,
			Required: true,
		}},
	}
	runtimePayload.VerificationStatus = pipepayload.VerificationStatusIncompatible

	runtimeDiagnostics := RuntimeEdgeDiagnostics(pipeline, completed)
	require.Len(t, runtimeDiagnostics, 2)
	require.True(t, runtimeDiagnostics[0].Compatible)
	require.False(t, runtimeDiagnostics[1].Compatible)
	require.Equal(t, pipelinevalidate.ContractReasonMissingRequiredField, runtimeDiagnostics[1].ReasonCode)
}

func runtimeSupportFanOutPipeline() *pipelinespec.Pipeline {
	producerContract := &pipepayload.Contract{
		Name:           "users",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
		},
	}
	idOnlyContract := &pipepayload.Contract{
		Name:           "ids",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{{
			Name:     "id",
			Type:     model.ContractFieldTypeValueInteger,
			Required: true,
		}},
	}
	emailContract := &pipepayload.Contract{
		Name:           "emails",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{{
			Name:     "email",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
		}},
	}

	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: must.Slug("fanout")},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("warehouse"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
					OutputContract: producerContract,
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("write-ids"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					InputContract:    idOnlyContract,
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("write-emails"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					InputContract:    emailContract,
				},
			},
		},
	}
}

func TestNamedRuntimeDiagnosticsRetainRelationIdentity(t *testing.T) {
	requirement := &pipepayload.Contract{Representation: model.ContractRepresentationRows, Fields: []pipepayload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{OutputContract: requirement}},
		{Kind: "transform", Slug: "join", Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch"}, "accounts": {FromStep: "fetch"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"orders": {Contract: requirement}, "accounts": {Contract: requirement}}}},
	}}
	source := NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": int64(1)}}, ColumnNames: []string{"id"}, RowCount: 1}, 1, nil))
	runtimePayload, ok := source.Payload()
	require.True(t, ok)
	runtimePayload.Contract = requirement
	diagnostics := RuntimeEdgeDiagnostics(pipeline, map[slug.Slug]*StepResult{"fetch": source})
	require.Len(t, diagnostics, 2)
	require.NotEqual(t, diagnostics[0].EdgeSlug, diagnostics[1].EdgeSlug)
	require.Contains(t, diagnostics[0].EdgeSlug, ":")
	require.Contains(t, diagnostics[1].EdgeSlug, ":")
}
