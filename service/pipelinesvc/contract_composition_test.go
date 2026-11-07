package pipelinesvc

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/stretchr/testify/require"
)

func TestEnabledCompositionReturnsStructuredValidationFailures(t *testing.T) {
	pipeline := testODCSQueryPipeline()
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "audit", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id FROM orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"unknown": {Contract: &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}}}}})
	svc := &service{}
	result, err := svc.Validate(t.Context(), "acme", "etl", "dev", "alice", pipeline)
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Len(t, result.Diagnostics, 1)
	require.Contains(t, result.Diagnostics[0].Message, "input_contracts.unknown")
}

func TestCompositionVersionRequiresResolvedDeployment(t *testing.T) {
	t.Parallel()
	svc := &service{}
	pipeline := testPipelineSpecWithSlug("orders")
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "audit", Transform: &pipelinespec.TransformSpec{InputContracts: map[string]*pipelinespec.InputContractAttachment{"input": {Contract: &payload.Contract{}}}}})
	version := &pipelinesrepo.PipelineVersion{SpecJSONB: mustJSON(t, pipeline)}
	_, err := svc.executeResolvedVersion(t.Context(), "acme", "etl", "dev", version, nil, nil, model.EventActor{}, testRunSource(model.EventActor{}), nil, false, nil, pipelinerun.IncrementalRunOptions{}, nil)
	require.ErrorIs(t, err, deploymentplan.ErrInvalid)
}
