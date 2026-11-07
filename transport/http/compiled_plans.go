package http

import (
	"encoding/json"
	"fmt"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	climodel "github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) CompilePipelineVersion(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, pipelineParam api.PipelineSlugParam, version api.PipelineVersionIdParam, params api.CompilePipelineVersionParams) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	org, project, err := parseOrgProjectSlugs(orgParam, projectParam)
	if err != nil {
		return err
	}
	pipeline, err := slug.New(pipelineParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}
	if err := s.canOrg(c, org, model.Update, model.ResourcePipelines); err != nil {
		return err
	}
	var environment slug.Slug
	if params.EnvironmentSlug != nil {
		environment, err = slug.New(*params.EnvironmentSlug)
		if err != nil {
			return apierror.ValidationFailed("Invalid environment slug")
		}
	}
	var request api.PipelineCompilationRequest
	if len(c.Body()) != 0 {
		if err := bindODCSBody(c, &request); err != nil {
			return err
		}
	}
	var envelope map[string]json.RawMessage
	if len(c.Body()) != 0 {
		if err := json.Unmarshal(c.Body(), &envelope); err != nil || string(envelope["resource_scopes"]) == "null" {
			return apierror.InvalidRequestBody()
		}
	}
	var projection *odcs.ProjectionRequest
	if request.Projection != nil {
		projection, err = compilationProjection(request.Projection)
		if err != nil {
			return err
		}
	}
	scopes, err := compilationResourceScopes(ptr.Value(request.ResourceScopes))
	if err != nil {
		return err
	}
	stored, err := s.compiledPlansService.CompileVersion(c.Context(), org, project, pipeline, version, environment, projection, scopes)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(toAPICompiledPlan(stored))
}

func compilationResourceScopes(selections []api.PipelineResourceScopeSelection) ([]contractplansvc.OperationScope, error) {
	if len(selections) > 128 {
		return nil, apierror.ValidationFailed("Select at most 128 resource scopes")
	}
	scopes := make([]contractplansvc.OperationScope, 0, len(selections))
	for _, selected := range selections {
		step, err := slug.New(selected.Step)
		if err != nil || (selected.Role != "query" && selected.Role != "sink") {
			return nil, apierror.ValidationFailed("Invalid resource scope step or role")
		}
		value := selected.Scope
		if value.S3.Prefix == nil || value.S3.DynamicKey == nil || value.S3.ForcePathStyle == nil {
			return nil, apierror.ValidationFailed("Select explicit resource scope prefix and selector flags")
		}
		scope := resourcescope.Scope{PolicyID: string(value.PolicyId), DataSource: slug.Slug(value.DataSource), Operation: slug.Slug(value.Operation), Contract: slug.Slug(value.Contract), Revision: slug.Slug(value.Revision), ObjectID: value.ObjectId, Representation: climodel.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: value.S3.Endpoint, Region: value.S3.Region, Bucket: value.S3.Bucket, Prefix: *value.S3.Prefix, DynamicKey: *value.S3.DynamicKey, ForcePathStyle: *value.S3.ForcePathStyle}}
		for _, identity := range []*slug.Slug{&scope.DataSource, &scope.Operation, &scope.Contract, &scope.Revision} {
			parsed, err := slug.New(identity.String())
			if err != nil {
				return nil, apierror.ValidationFailed("Invalid resource scope identity")
			}
			*identity = parsed
		}
		if value.Representation != "rows" || scope.Validate() != nil {
			return nil, apierror.ValidationFailed("Invalid resource scope selection")
		}
		scopes = append(scopes, contractplansvc.OperationScope{Step: step, Role: string(selected.Role), Scope: scope})
	}
	return scopes, nil
}

func compilationProjection(selection *api.ODCSProjectionSelection) (*odcs.ProjectionRequest, error) {
	request := &odcs.ProjectionRequest{}
	for _, selected := range selection.Inputs {
		representation, err := climodel.ContractRepresentationString(string(selected.Mapping.Representation))
		if err != nil {
			return nil, apierror.ValidationFailed("Unsupported projection representation")
		}
		root := climodel.ContractRootShapeUnknown
		if selected.Mapping.RootShape != nil {
			root, err = climodel.ContractRootShapeString(string(*selected.Mapping.RootShape))
			if err != nil {
				return nil, apierror.ValidationFailed("Unsupported projection root shape")
			}
		}
		input := odcs.ProjectionInput{
			Contract: selected.ContractSlug, Revision: selected.RevisionSlug, ObjectID: selected.SchemaObjectId, Digest: selected.Digest,
			Mapping: odcs.ProjectionMapping{ID: selected.Mapping.Id, Representation: representation, RootShape: root, Types: map[payload.StructuralKind]bool{}, Physical: map[string]odcs.PhysicalTypeMapping{}}, Variables: map[string]odcs.ResolutionValue{},
		}
		for _, value := range selected.Mapping.Types {
			kind, err := payload.StructuralKindString(string(value))
			if err != nil || kind == payload.StructuralKindUnknown {
				return nil, apierror.ValidationFailed("Unsupported projection type")
			}
			input.Mapping.Types[kind] = true
		}
		for name, value := range ptr.Value(selected.Mapping.Physical) {
			kind, err := payload.StructuralKindString(string(value.Kind))
			if err != nil || kind == payload.StructuralKindUnknown {
				return nil, apierror.ValidationFailed("Unsupported projection physical type")
			}
			input.Mapping.Physical[name] = odcs.PhysicalTypeMapping{LogicalType: value.LogicalType, Kind: kind, Bits: ptr.Value(value.Bits), Precision: ptr.Value(value.Precision), Scale: ptr.Value(value.Scale)}
		}
		for name, value := range ptr.Value(selected.Variables) {
			input.Variables[name] = odcs.ResolutionValue{Value: value.Value, Revision: value.Revision}
		}
		request.Inputs = append(request.Inputs, input)
	}
	for _, relationship := range ptr.Value(selection.Relationships) {
		request.Relationships = append(request.Relationships, odcs.RelationshipSelection{Input: relationship.Input, Target: relationship.Target, Path: relationship.Path})
	}
	return request, nil
}

func (s *server) GetPipelineCompiledPlan(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, pipelineParam api.PipelineSlugParam, digest string) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	org, project, err := parseOrgProjectSlugs(orgParam, projectParam)
	if err != nil {
		return err
	}
	pipeline, err := slug.New(pipelineParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	stored, err := s.compiledPlansService.Get(c.Context(), org, project, pipeline, digest)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(toAPICompiledPlan(stored))
}

func toAPICompiledPlan(summary *contractplansvc.Summary) api.PipelineCompiledPlan {
	result := api.PipelineCompiledPlan{Digest: summary.Digest, SpecHash: summary.SpecHash, PipelineSlug: summary.PipelineSlug.String(), AdmissionEnabled: summary.AdmissionEnabled, Status: api.PipelineCompiledPlanStatus(summary.Status), Pins: []api.PipelineContractPinInspection{}, Operations: []api.PipelineOperationInspection{}, SourceCommit: optionalCheckString(summary.Source.Commit), SourcePath: optionalCheckString(summary.Source.Path), CompilerId: optionalCheckString(summary.CompilerID), StructuralPolicyId: optionalCheckString(summary.StructuralPolicyID)}
	for _, pin := range summary.Pins {
		result.Pins = append(result.Pins, api.PipelineContractPinInspection{Step: pin.Step.String(), Port: pin.Port, Origin: api.PipelineContractPinInspectionOrigin(pin.Origin), ContractSlug: pin.Contract.String(), RevisionSlug: pin.Revision.String(), SchemaObjectId: pin.Object, Digest: pin.Digest, CompilerId: pin.Compiler, Representation: pin.Representation, RootShape: pin.RootShape})
	}
	for _, operation := range summary.Operations {
		result.Operations = append(result.Operations, api.PipelineOperationInspection{Step: operation.Step.String(), Role: operation.Role, OperationSlug: operation.Operation.String(), Revision: operation.Revision, Digest: operation.Digest})
		result.Operations[len(result.Operations)-1].ResourceScope = toAPIResourceScopeIdentity(operation.ResourceScope)
	}
	if len(summary.Projections) > 0 {
		projections := make([]api.PipelineProjectionInspection, 0, len(summary.Projections))
		for _, projection := range summary.Projections {
			projections = append(projections, api.PipelineProjectionInspection{Input: projection.Input, ContractSlug: projection.Contract, RevisionSlug: projection.Revision, SchemaObjectId: projection.Object, Digest: projection.Digest, ResolvedDigest: projection.ResolvedDigest, MappingId: projection.MappingID, Representation: projection.Representation, RootShape: projection.RootShape})
		}
		result.Projections = &projections
	}
	return result
}

func toAPIResourceScopeIdentity(scope *resourcescope.Identity) *api.PipelineResourceScopeInspection {
	if scope == nil {
		return nil
	}
	return &api.PipelineResourceScopeInspection{PolicyId: scope.PolicyID, DataSource: scope.DataSource, Operation: scope.Operation, Contract: scope.Contract, Revision: scope.Revision, ObjectId: scope.ObjectID, Representation: scope.Representation}
}
