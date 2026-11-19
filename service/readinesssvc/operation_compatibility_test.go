package readinesssvc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func TestSavedOperationCompatibility(t *testing.T) {
	rows := &payload.Contract{Representation: model.ContractRepresentationRows}
	file := &payload.Contract{Representation: model.ContractRepresentationFile}
	fallback := "synthetic default"
	for _, tc := range []struct {
		name          string
		target        model.DataSourceType
		operationType string
		sink          bool
		readOnly      bool
		statement     string
		values        map[string]any
		parameters    []*queriessvc.QueryParameter
		declared      *payload.Contract
		saved         *payload.Contract
		upstream      *payload.Contract
		upstreamType  string
		want          string
	}{
		{name: "read source", target: model.DataSourceTypeS3, operationType: "s3", statement: `{"type":"s3","operation":"read"}`},
		{name: "read sink", target: model.DataSourceTypeS3, operationType: "s3", sink: true, statement: `{"type":"s3","operation":"read"}`, want: "missing_operation_capability"},
		{name: "write source", target: model.DataSourceTypeHubSpot, operationType: "hubspot", statement: `{"type":"hubspot","operation":"upsert"}`, want: "missing_operation_capability"},
		{name: "sink payload", target: model.DataSourceTypeGitlab, operationType: "git", sink: true, statement: `{"type":"git","operation":"write","content":"{{input}}"}`},
		{name: "legacy read defaults", target: model.DataSourceTypeBigQuery, operationType: "bigquery", statement: `{"sql":"SELECT 1"}`},
		{name: "incompatible connector", target: model.DataSourceTypePostgres, operationType: "sql", statement: `{"type":"mysql","sql":"SELECT 1"}`, want: "incompatible_operation_type"},
		{name: "read-only target", target: model.DataSourceTypePostgres, operationType: "sql", readOnly: true, statement: `{"type":"postgres","sql":"DELETE FROM synthetic"}`, want: "missing_operation_capability"},
		{name: "Snowflake sink reads input without inserting", target: model.DataSourceTypeSnowflake, operationType: "snowflake", sink: true, statement: `{"sql":"SELECT * FROM input"}`, want: "missing_operation_capability"},
		{name: "Snowflake insert from input", target: model.DataSourceTypeSnowflake, operationType: "snowflake", sink: true, statement: `{"sql":"INSERT INTO report SELECT * FROM input"}`},
		{name: "MongoDB upstream supplies array shape", target: model.DataSourceTypeS3, operationType: "s3", sink: true, statement: `{"operation":"write"}`, upstreamType: "mongodb", upstream: &payload.Contract{Representation: model.ContractRepresentationJSON}, saved: &payload.Contract{Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeArray}},
		{name: "read-only select", target: model.DataSourceTypePostgres, operationType: "sql", readOnly: true, statement: `{"sql":"SELECT 1"}`},
		{name: "opaque upstream output", target: model.DataSourceTypeS3, operationType: "s3", sink: true, statement: `{"operation":"write"}`, saved: file},
		{name: "workspace read in sink", target: model.DataSourceTypeGoogleWorkspace, operationType: "google_workspace", sink: true, statement: `{"type":"google_workspace","resource":"document","operation":"read","file_id":"synthetic-id"}`, want: "missing_operation_capability"},
		{name: "unknown internal sink parameter", target: model.DataSourceTypePostgres, operationType: "sql", sink: true, statement: `{"sql":"SELECT {{__braidplane_typo}}"}`, want: "missing_operation_parameter"},
		{name: "missing named parameter", target: model.DataSourceTypePostgres, operationType: "sql", statement: `{"type":"postgres","sql":"SELECT {{account}}"}`, want: "missing_operation_parameter"},
		{name: "available parameters", target: model.DataSourceTypePostgres, operationType: "sql", statement: `{"sql":"SELECT {{account}}, {{scope}}, {{runtime.run.slug}}"}`, values: map[string]any{"account": 1}, parameters: []*queriessvc.QueryParameter{{Name: "scope", DefaultValue: &fallback}}},
		{name: "sink runtime context in source", target: model.DataSourceTypePostgres, operationType: "sql", statement: `{"sql":"SELECT {{runtime.sink.idempotency_key}}"}`, want: "missing_operation_parameter"},
		{name: "matching output", target: model.DataSourceTypePostgres, operationType: "sql", statement: `{"sql":"SELECT 1"}`, saved: rows, declared: rows},
		{name: "incompatible output", target: model.DataSourceTypePostgres, operationType: "sql", statement: `{"sql":"SELECT 1"}`, saved: file, declared: rows, want: "incompatible_operation_contract"},
		{name: "incompatible sink input", target: model.DataSourceTypeS3, operationType: "s3", sink: true, statement: `{"operation":"write"}`, saved: file, declared: rows, want: "incompatible_operation_contract"},
		{name: "upstream fails saved input", target: model.DataSourceTypeS3, operationType: "s3", sink: true, statement: `{"operation":"write"}`, saved: file, upstream: rows, want: "incompatible_operation_contract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"statement": tc.statement})
			require.NoError(t, err)
			query := &queriessvc.Query{Data: raw, Parameters: tc.parameters}
			step := pipelinespec.Step{Slug: "operation"}
			if tc.sink {
				step.Kind = "sink"
				step.Sink = &pipelinespec.SinkSpec{OperationType: tc.operationType, Config: tc.values, InputContract: tc.declared, Input: &pipelinespec.StepInput{FromStep: "upstream"}}
				query.ExpectedInputContract = tc.saved
			} else {
				step.Kind = "query"
				step.Query = &pipelinespec.QueryRef{OperationType: tc.operationType, Parameters: tc.values, OutputContract: tc.declared}
				query.OutputContract = tc.saved
			}
			steps := map[slug.Slug]pipelinespec.Step{}
			if tc.upstream != nil {
				if tc.upstreamType == "" {
					tc.upstreamType = "sql"
				}
				steps["upstream"] = pipelinespec.Step{Slug: "upstream", Kind: "query", Query: &pipelinespec.QueryRef{OperationType: tc.upstreamType, OutputContract: tc.upstream}}
			}

			var codes []string
			checkOperation(step, model.DataSourceBinding{DataSourceType: tc.target, DataSourceReadOnly: tc.readOnly}, query, operationGraph{steps: steps}, func(code, action string) {
				codes = append(codes, code)
				require.NotEmpty(t, action)
			})
			if tc.want == "" {
				require.Empty(t, codes)
			} else {
				require.Equal(t, []string{tc.want}, codes)
			}
		})
	}
}

func TestSavedOutputCheckedAgainstInlineConsumers(t *testing.T) {
	rows := &payload.Contract{Representation: model.ContractRepresentationRows}
	read := pipelinespec.Step{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "api", OperationRef: "fetch", OperationType: "http"}}
	consume := pipelinespec.Step{Kind: "sink", Slug: "write", Sink: &pipelinespec.SinkSpec{DataSourceSlug: "out", OperationType: "sql", OperationInline: map[string]any{"sql": "select 1"}, Input: &pipelinespec.StepInput{FromStep: "read"}, InputContract: rows}}
	transform := pipelinespec.Step{Kind: "transform", Slug: "convert", Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"items": {FromStep: "read"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"items": {Contract: rows}}}}
	file := &payload.Contract{Representation: model.ContractRepresentationFile}
	fileRef := &pipelinespec.ODCSContractRef{Representation: model.ContractRepresentationFile}
	for _, tc := range []struct {
		name     string
		consumer pipelinespec.Step
		output   *payload.Contract
		ref      *pipelinespec.ODCSContractRef
	}{
		{"inline sink", consume, file, nil}, {"named transform input", transform, file, nil}, {"ODCS file facet", consume, nil, fileRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &queriessvc.Query{DataSourceSlug: "bound", Data: json.RawMessage(`{"statement":"{\"type\":\"http\"}"}`), OutputContract: tc.output, OutputContractRef: tc.ref}
			p := PipelineInput{Spec: &pipelinespec.Pipeline{Steps: []pipelinespec.Step{read, tc.consumer}}, Bindings: []model.DataSourceBinding{{DataSourceRefSlug: "api", EnvironmentSlug: "dev", DataSourceSlug: "bound", DataSourceType: model.DataSourceTypeHTTP, DataSourceActive: true}}, Operations: map[OperationKey]SavedOperation{{"bound", "fetch"}: {Query: source}}}
			report := Evaluate([]PipelineInput{p}, []EnvironmentInput{{Slug: "dev"}})
			var contractPaths []string
			for _, d := range report.Environments[0].Diagnostics {
				if d.Code == "incompatible_operation_contract" {
					contractPaths = append(contractPaths, d.FieldPath)
				}
			}
			require.Equal(t, []string{"/steps/0/query/operation_ref"}, contractPaths)
		})
	}
}

func TestSavedContractReferenceFacets(t *testing.T) {
	rows := &payload.Contract{Representation: model.ContractRepresentationRows}
	file := &pipelinespec.ODCSContractRef{Representation: model.ContractRepresentationFile}
	source := pipelinespec.Step{Query: &pipelinespec.QueryRef{OutputContract: rows}}
	require.False(t, operationContractsCompatible(source, &queriessvc.Query{OutputContractRef: file}, operationGraph{}))
	sink := pipelinespec.Step{Sink: &pipelinespec.SinkSpec{InputContractRef: file}}
	require.False(t, operationContractsCompatible(sink, &queriessvc.Query{ExpectedInputContract: rows}, operationGraph{}))
}

func TestDeclaredOutputCanStrengthenSavedOperationContract(t *testing.T) {
	rows := &payload.Contract{Representation: model.ContractRepresentationRows}
	withID := &payload.Contract{Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	source := pipelinespec.Step{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{OperationType: "sql", OperationRef: "read", OutputContract: withID}}
	consumer := pipelinespec.Step{Kind: "sink", Slug: "write", Sink: &pipelinespec.SinkSpec{InputContract: withID}}
	graph := operationGraph{consumers: map[slug.Slug][]pipelinespec.Step{source.Slug: {consumer}}}
	require.True(t, operationContractsCompatible(source, &queriessvc.Query{OutputContract: rows}, graph))
}
