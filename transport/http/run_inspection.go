package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func toAPIRunComposition(value *pipelinesvc.RunCompositionInspection) *api.PipelineRunCompositionInspection {
	if value == nil {
		return nil
	}
	result := &api.PipelineRunCompositionInspection{
		AdmissionEnabled: value.AdmissionEnabled,
		Status:           api.PipelineRunCompositionInspectionStatus(value.Status), TemplateDigest: optionalCheckString(value.TemplateDigest), CompiledPlanDigest: optionalCheckString(value.CompiledPlanDigest), DeploymentResolutionDigest: optionalCheckString(value.ResolutionDigest), SourceCommit: optionalCheckString(value.SourceCommit),
		Pins: []api.PipelineContractPinInspection{}, Operations: []api.PipelineOperationInspection{}, Targets: []api.PipelineTargetInspection{},
	}
	for _, pin := range value.Pins {
		result.Pins = append(result.Pins, api.PipelineContractPinInspection{Step: pin.Step.String(), Port: pin.Port, Origin: api.PipelineContractPinInspectionOrigin(pin.Origin), ContractSlug: pin.Contract.String(), RevisionSlug: pin.Revision.String(), SchemaObjectId: pin.Object, Digest: pin.Digest, CompilerId: pin.Compiler, Representation: pin.Representation, RootShape: pin.RootShape})
	}
	for _, operation := range value.Operations {
		result.Operations = append(result.Operations, api.PipelineOperationInspection{Step: operation.Step.String(), Role: operation.Role, OperationSlug: operation.Operation.String(), Revision: operation.Revision, Digest: operation.Digest, ResourceScope: toAPIResourceScopeIdentity(operation.ResourceScope)})
	}
	for _, target := range value.Targets {
		result.Targets = append(result.Targets, api.PipelineTargetInspection{Step: target.Step.String(), Role: target.Role, Reference: target.Reference.String(), DataSourceSlug: target.DataSource.String(), BindingRevision: target.BindingRevision})
	}
	return result
}
