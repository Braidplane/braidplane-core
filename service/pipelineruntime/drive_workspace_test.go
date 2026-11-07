package pipelineruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type workspaceInlineSinkExecutor struct {
	fakeSinkQueryExecutor
	inline func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
}

func (e *workspaceInlineSinkExecutor) ExecuteInline(ctx context.Context, org, source slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
	return e.inline(ctx, org, source, operation, values)
}

func TestWorkspaceDriveCreateBindsObjectRowsAndBlocksUnsafeRedelivery(t *testing.T) {
	var calls int
	var captured map[string]any
	executor := &workspaceInlineSinkExecutor{inline: func(_ context.Context, _, _ slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
		calls++
		captured = values
		require.Equal(t, "create", operation["operation"])
		return result.NewWorkspaceFileResult(&result.FileResult{ResourceID: "new-stable-id", Name: "crm.json", MIMEType: "application/json", Operation: "create", Content: []byte("provider response")}, 4, nil), nil
	}}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassUnsafe)
	input.SinkBackend = model.SinkBackendHTTP
	input.SinkOperationType = catalog.OperationTypeGoogleWorkspace
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"type": "google_workspace", "resource": "file", "operation": "create", "folder_id": "folder", "name": "crm.json", "format": "json", "content": "{{input}}"}
	input.SourceResult = pipelinerun.NewStepResult(result.NewObjectRowsResult("hubspot", &result.ObjectRowsResult{ObjectType: "contacts", Rows: []map[string]any{{"id": "1"}, {"id": "2"}}, ColumnNames: []string{"id"}, RowCount: 2}, 0, nil))

	runtime := NewSinkRuntime(executor, ledger)
	require.NoError(t, runtime.Execute(context.Background(), input))
	require.Equal(t, []any{map[string]any{"id": "1"}, map[string]any{"id": "2"}}, captured[sinkbinding.Input])
	require.Equal(t, "unsafe", captured[sinkbinding.InternalIdempotencyClass])
	file, ok := input.SinkResult.File()
	require.True(t, ok)
	require.Equal(t, "new-stable-id", file.ResourceID)
	require.Equal(t, "crm.json", file.Name)
	require.Equal(t, "application/json", file.MIMEType)
	require.Empty(t, file.Content)

	ledger.effect.Claimed = false
	require.ErrorIs(t, runtime.Execute(context.Background(), input), errSinkReplayBlocked)
	require.Equal(t, 1, calls, "unsafe create must not be delivered twice")
}

func TestWorkspaceFileSnapshotContainsOnlyWriteMetadata(t *testing.T) {
	written := 2
	output := &pipelinerun.StepOutputSnapshot{}
	applyFileOutputSnapshot(output, &result.FileResult{ResourceID: "new-stable-id", Name: "crm.json", MIMEType: "application/json", Operation: "update", TargetPath: "new-stable-id", Content: []byte("private"), WrittenRows: &written})
	require.Equal(t, "update", output.OperationType)
	require.NotNil(t, output.Details)
	require.Equal(t, pipelinerun.StepOutputDetailKindFileWrite, output.Details.Kind)
	require.Equal(t, "new-stable-id", *output.Details.TargetPath)
}
