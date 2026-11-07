package pipelinesvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

func TestValidateSavedNativeWorkspaceReplay(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		for _, operation := range []string{"create", "update"} {
			for _, policy := range []pipelinespec.SinkIdempotencyClass{pipelinespec.SinkIdempotencyClassUnsafe, pipelinespec.SinkIdempotencyClassNaturallyIdempotent, pipelinespec.SinkIdempotencyClassDeduplicatedByLedger} {
				t.Run(resource+"/"+operation+"/"+policy.String(), func(t *testing.T) {
					pipeline := testPipelineSpec()
					contract := &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
					pipeline.Steps[0].Query.OutputContract = contract
					pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: "write-native", Sink: &pipelinespec.SinkSpec{
						Input: &pipelinespec.StepInput{FromStep: "fetch-users"}, InputContract: contract,
						DataSourceSlug: "workspace-ref", OperationType: pipelinespec.OperationTypeGoogleWorkspace, OperationRef: "write-native", IdempotencyClass: policy,
					}})
					resolved := *pipeline
					resolved.Steps = append([]pipelinespec.Step(nil), pipeline.Steps...)
					sink := *pipeline.Steps[1].Sink
					sink.DataSourceSlug = "workspace-dev"
					resolved.Steps[1].Sink = &sink
					resolver := &fakeDataSourceRefResolver{resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{"dev": &resolved}}
					svc := newValidationTestServiceWithDataSourceRefResolver(t, &fakeDataSourceService{schemas: testCachedSchemas()}, resolver)
					statement := map[string]any{"type": "google_workspace", "resource": resource, "operation": operation}
					if operation == "create" {
						statement["folder_id"], statement["name"] = "folder", "Synthetic"
					} else {
						statement["file_id"], statement["match_text"], statement["content"] = "native", "marker", "{{input}}"
					}
					calls := 0
					svc.(*service).queryExecutor = &fakeQueryExecutor{findBySlugFn: func(_ context.Context, org, source, reference slug.Slug) (*queriessvc.Query, error) {
						calls++
						require.Equal(t, slug.Slug("acme"), org)
						require.Equal(t, slug.Slug("workspace-dev"), source)
						require.Equal(t, slug.Slug("write-native"), reference)
						raw, err := json.Marshal(statement)
						require.NoError(t, err)
						data, err := json.Marshal(map[string]string{"statement": string(raw)})
						require.NoError(t, err)
						return &queriessvc.Query{Data: data}, nil
					}}
					result, err := svc.Validate(t.Context(), "acme", defaultProjectSlug, "dev", "test-user", pipeline)
					require.NoError(t, err)
					require.Equal(t, 1, calls)
					if policy == pipelinespec.SinkIdempotencyClassUnsafe {
						require.Truef(t, result.Valid, "%+v", result.Diagnostics)
					} else {
						require.False(t, result.Valid)
						require.Len(t, result.Diagnostics, 1)
						require.Contains(t, result.Diagnostics[0].Message, "unsafe replay policy")
					}
					require.Equal(t, slug.Slug("workspace-ref"), pipeline.Steps[1].Sink.DataSourceSlug)
					require.Equal(t, slug.Slug("write-native"), pipeline.Steps[1].Sink.OperationRef)
					require.Nil(t, pipeline.Steps[1].Sink.OperationInline)
				})
			}
		}
	}
}

func TestSavedWorkspaceQueryRoleAndLookupFailure(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		t.Run(resource, func(t *testing.T) {
			pipeline := testPipelineSpec()
			pipeline.Steps[0].Query = &pipelinespec.QueryRef{DataSourceSlug: "workspace", OperationType: pipelinespec.OperationTypeGoogleWorkspace, OperationRef: "create-native"}
			svc := newValidationTestService(t, nil)
			svc.(*service).queryExecutor = &fakeQueryExecutor{findBySlugFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
				raw, err := json.Marshal(map[string]string{"statement": `{"type":"google_workspace","resource":"` + resource + `","operation":"create","folder_id":"folder","name":"Synthetic"}`})
				require.NoError(t, err)
				return &queriessvc.Query{Data: raw}, nil
			}}
			result, err := svc.Validate(t.Context(), "acme", defaultProjectSlug, "dev", "test-user", pipeline)
			require.NoError(t, err)
			require.False(t, result.Valid)
			require.Contains(t, result.Diagnostics[0].Message, "Workspace source operations require")
			svc.(*service).queryExecutor = &fakeQueryExecutor{findBySlugFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
				return nil, queriessvc.ErrQueryNotFound
			}}
			result, err = svc.Validate(t.Context(), "acme", defaultProjectSlug, "dev", "test-user", pipeline)
			require.NoError(t, err)
			require.False(t, result.Valid)
			require.Equal(t, "fetch-users", *result.Diagnostics[0].StepSlug)
			require.Contains(t, result.Diagnostics[0].Message, "resolve saved Workspace operation")
			svc.(*service).queryExecutor = &fakeQueryExecutor{findBySlugFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
				return nil, context.Canceled
			}}
			_, err = svc.Validate(t.Context(), "acme", defaultProjectSlug, "dev", "test-user", pipeline)
			require.ErrorIs(t, err, context.Canceled)
			result, err = svc.Validate(t.Context(), "acme", defaultProjectSlug, noEnvironmentSlug, "test-user", pipeline)
			require.NoError(t, err)
			require.False(t, result.Valid)
			require.Contains(t, result.Diagnostics[0].Message, "an environment is required")
		})
	}
}
