package http

import (
	"bytes"
	"encoding/json"
	"io"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/gofiber/fiber/v3"
)

func parseODCSContractPath(orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam) (slug.Slug, slug.Slug, slug.Slug, error) {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return "", "", "", err
	}
	identity, err := slug.New(contractParam)
	if err != nil {
		return "", "", "", apierror.ValidationFailed("Invalid contract slug")
	}
	return org, project, identity, nil
}

// Strict envelopes keep duplicate keys, unknown metadata, and trailing values out of catalog writes.
func bindODCSBody(c fiber.Ctx, target any) error {
	if len(c.Body()) > odcs.MaxDocumentBytes+4096 {
		return apierror.ValidationFailed("Catalog request exceeds size limit")
	}
	if err := odcs.CheckRequestJSON(c.Body()); err != nil {
		return mapServiceError(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return apierror.InvalidRequestBody()
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return apierror.InvalidRequestBody()
	}
	return nil
}

func (s *server) ListODCSContracts(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, params api.ListODCSContractsParams) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	page, err := s.odcsContractsService.ListContracts(c.Context(), org, project, model.ListRequest{Limit: int(ptr.Value(params.Limit)), Cursor: ptr.Value(params.Cursor), Order: string(ptr.Value(params.Order)), Direction: string(ptr.Value(params.Direction))})
	if err != nil {
		return mapServiceError(err)
	}
	items := make([]api.ODCSContract, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, odcsContractToHTTP(item))
	}
	return c.JSON(api.ODCSContractListResponse{Contracts: items, HasMore: page.HasMore, NextCursor: page.NextCursor, PrevCursor: page.PrevCursor})
}

func (s *server) CreateODCSContract(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Create, model.ResourceProjects); err != nil {
		return err
	}
	var input api.NewODCSContract
	if err := bindODCSBody(c, &input); err != nil {
		return err
	}
	var identity slug.Slug
	if input.Slug != nil {
		identity, err = slug.New(*input.Slug)
	}
	if err != nil {
		return apierror.ValidationFailed("Invalid contract slug")
	}
	item, err := s.odcsContractsService.CreateContract(c.Context(), org, project, model.NewODCSContract{Slug: identity, Name: input.Name, ODCSID: input.OdcsId})
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(fiber.StatusCreated).JSON(odcsContractToHTTP(*item))
}

func (s *server) GetODCSContract(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam) error {
	org, project, identity, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	item, err := s.odcsContractsService.GetContract(c.Context(), org, project, identity)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(odcsContractToHTTP(*item))
}

func (s *server) UpdateODCSContract(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam) error {
	org, project, identity, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceProjects); err != nil {
		return err
	}
	var input api.UpdateODCSContract
	if err := bindODCSBody(c, &input); err != nil {
		return err
	}
	item, err := s.odcsContractsService.UpdateContract(c.Context(), org, project, identity, model.UpdateODCSContract{Name: input.Name, Status: string(input.Status)})
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(odcsContractToHTTP(*item))
}

func (s *server) ListODCSRevisions(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam, params api.ListODCSRevisionsParams) error {
	org, project, identity, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	page, err := s.odcsContractsService.ListRevisions(c.Context(), org, project, identity, model.ListRequest{Limit: int(ptr.Value(params.Limit)), Cursor: ptr.Value(params.Cursor), Order: string(ptr.Value(params.Order)), Direction: string(ptr.Value(params.Direction))})
	if err != nil {
		return mapServiceError(err)
	}
	items := make([]api.ODCSRevisionMetadata, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, odcsRevisionMetadataToHTTP(item))
	}
	return c.JSON(api.ODCSRevisionListResponse{Revisions: items, HasMore: page.HasMore, NextCursor: page.NextCursor, PrevCursor: page.PrevCursor})
}

func (s *server) CreateODCSRevision(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam) error {
	org, project, identity, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Create, model.ResourceProjects); err != nil {
		return err
	}
	var input api.NewODCSRevision
	if err := bindODCSBody(c, &input); err != nil {
		return err
	}
	var revisionSlug slug.Slug
	if input.Slug != nil {
		revisionSlug, err = slug.New(*input.Slug)
	}
	if err != nil {
		return apierror.ValidationFailed("Invalid revision slug")
	}
	item, err := s.odcsContractsService.CreateRevision(c.Context(), org, project, identity, revisionSlug, input.Document)
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(fiber.StatusCreated).JSON(odcsRevisionToHTTP(*item))
}

func (s *server) GetODCSRevision(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam, revisionParam api.RevisionSlugParam) error {
	org, project, identity, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	revisionSlug, err := slug.New(revisionParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid revision slug")
	}
	item, err := s.odcsContractsService.GetRevision(c.Context(), org, project, identity, revisionSlug)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(odcsRevisionToHTTP(*item))
}

func odcsContractToHTTP(item model.ODCSContract) api.ODCSContract {
	return api.ODCSContract{Slug: item.Slug.String(), Name: item.Name, OdcsId: item.ODCSID, Status: api.ODCSContractStatus(item.Status), CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func odcsRevisionMetadataToHTTP(item model.ODCSRevision) api.ODCSRevisionMetadata {
	objects := make([]api.ODCSSchemaObject, 0, len(item.SchemaObjects))
	for _, object := range item.SchemaObjects {
		objects = append(objects, api.ODCSSchemaObject{Id: object.ID, Name: object.Name})
	}
	return api.ODCSRevisionMetadata{Slug: item.Slug.String(), Digest: item.Digest, OdcsVersion: api.ODCSRevisionMetadataOdcsVersion(item.ODCSVersion), DocumentVersion: item.DocumentVersion, DeclaredStatus: item.DeclaredStatus, Status: api.ODCSRevisionMetadataStatus(item.Status), SchemaObjects: objects, CreatedAt: item.CreatedAt}
}

func odcsRevisionToHTTP(item model.ODCSRevision) api.ODCSRevision {
	metadata := odcsRevisionMetadataToHTTP(item)
	return api.ODCSRevision{Slug: metadata.Slug, Digest: metadata.Digest, OdcsVersion: api.ODCSRevisionOdcsVersion(metadata.OdcsVersion), DocumentVersion: metadata.DocumentVersion, DeclaredStatus: metadata.DeclaredStatus, Status: api.ODCSRevisionStatus(metadata.Status), SchemaObjects: metadata.SchemaObjects, CreatedAt: metadata.CreatedAt, Document: item.Document}
}

func (s *server) PreviewODCSRevisionImpact(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam) error {
	org, project, contract, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	var input api.ODCSRevisionImpactRequest
	if err := bindODCSBody(c, &input); err != nil {
		return err
	}
	from, err := slug.New(input.FromRevisionSlug)
	if err != nil {
		return apierror.ValidationFailed("Invalid source revision slug")
	}
	to, err := slug.New(input.ToRevisionSlug)
	if err != nil {
		return apierror.ValidationFailed("Invalid target revision slug")
	}
	if (input.FromPlan == nil) != (input.ToPlan == nil) {
		return apierror.ValidationFailed("Select both retained compiled plans")
	}
	var fromPlan, toPlan *odcscontractsvc.ImpactPlanSelection
	if input.FromPlan != nil {
		c.Set(fiber.HeaderCacheControl, "no-store")
		if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
			return err
		}
		fromPipeline, err := slug.New(input.FromPlan.PipelineSlug)
		if err != nil {
			return apierror.ValidationFailed("Invalid source pipeline slug")
		}
		toPipeline, err := slug.New(input.ToPlan.PipelineSlug)
		if err != nil {
			return apierror.ValidationFailed("Invalid target pipeline slug")
		}
		fromPlan = &odcscontractsvc.ImpactPlanSelection{Pipeline: fromPipeline, Digest: input.FromPlan.Digest, ProjectionInput: input.FromPlan.ProjectionInput}
		toPlan = &odcscontractsvc.ImpactPlanSelection{Pipeline: toPipeline, Digest: input.ToPlan.Digest, ProjectionInput: input.ToPlan.ProjectionInput}
	}
	result, err := s.odcsContractsService.PreviewRevisionImpact(c.Context(), org, project, contract, odcscontractsvc.RevisionImpactRequest{From: from, To: to, FromPlan: fromPlan, ToPlan: toPlan})
	if err != nil {
		return mapServiceError(err)
	}
	response := api.ODCSRevisionImpact{ProjectSlug: project.String(), ContractSlug: contract.String(), FromRevisionSlug: from.String(), ToRevisionSlug: to.String(), FromDigest: result.FromDigest, ToDigest: result.ToDigest, ExternalConsumers: api.ODCSRevisionImpactExternalConsumers("unknown"), Objects: []api.ODCSObjectImpact{}, Consumers: []api.ODCSConsumerImpact{}}
	if result.FromPlan != nil && result.ToPlan != nil {
		response.FromPlan = &api.ODCSImpactPlanSelection{PipelineSlug: result.FromPlan.Pipeline.String(), Digest: result.FromPlan.Digest, ProjectionInput: result.FromPlan.ProjectionInput}
		response.ToPlan = &api.ODCSImpactPlanSelection{PipelineSlug: result.ToPlan.Pipeline.String(), Digest: result.ToPlan.Digest, ProjectionInput: result.ToPlan.ProjectionInput}
	}
	for _, object := range result.Objects {
		item := api.ODCSObjectImpact{SchemaObjectId: object.SchemaObjectID, Comparison: api.ODCSObjectImpactComparison(object.Comparison), Changes: []api.ODCSImpactChange{}, Diagnostics: odcsImpactDiagnosticsToHTTP(object.Diagnostics)}
		if object.Scope != "" {
			item.ComparisonScope = new(api.ODCSObjectImpactComparisonScope(object.Scope))
		}
		for _, change := range object.Changes {
			value := api.ODCSImpactChange{Kind: change.Kind, Path: change.Path, Before: change.Before, After: change.After, Severity: api.DriftImpactReportSeverity(change.Severity.String()), ReasonCode: change.ReasonCode}
			if change.BeforePropertyID != "" {
				value.BeforePropertyId = &change.BeforePropertyID
			}
			if change.AfterPropertyID != "" {
				value.AfterPropertyId = &change.AfterPropertyID
			}
			item.Changes = append(item.Changes, value)
		}
		response.Objects = append(response.Objects, item)
	}
	for _, consumer := range result.Consumers {
		item := api.ODCSConsumerImpact{ProjectSlug: consumer.ProjectSlug.String(), PipelineSlug: consumer.PipelineSlug.String(), StepSlug: consumer.StepSlug.String(), Port: consumer.Port, Origin: api.ODCSConsumerImpactOrigin(consumer.Origin), SpecHash: consumer.SpecHash, RevisionSlug: consumer.RevisionSlug.String(), SchemaObjectId: consumer.SchemaObjectID, Comparison: api.ODCSConsumerImpactComparison(consumer.Comparison), Diagnostics: odcsImpactDiagnosticsToHTTP(consumer.Diagnostics)}
		if consumer.Environment != "" {
			item.EnvironmentSlug = &consumer.Environment
		}
		if consumer.Impact != nil {
			item.Impact = &api.DriftImpactReportPreview{Severity: api.DriftImpactReportSeverity(consumer.Impact.Severity.String()), Summary: consumer.Impact.Summary, Changes: toAPIDriftImpactChanges(consumer.Impact.Changes), Impacts: toAPIDriftImpactImpacts(consumer.Impact.Impacts)}
		}
		response.Consumers = append(response.Consumers, item)
	}
	return c.JSON(response)
}

func odcsImpactDiagnosticsToHTTP(items []odcscontractsvc.ImpactDiagnostic) []api.ODCSImpactDiagnostic {
	result := make([]api.ODCSImpactDiagnostic, 0, len(items))
	for _, item := range items {
		value := api.ODCSImpactDiagnostic{RevisionSlug: item.RevisionSlug.String(), Code: item.Code, Path: item.Path, Message: item.Message}
		if item.PropertyID != "" {
			value.PropertyId = &item.PropertyID
		}
		result = append(result, value)
	}
	return result
}

func (s *server) InspectODCSRevision(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, contractParam api.ContractSlugParam, revisionParam api.RevisionSlugParam, params api.InspectODCSRevisionParams) error {
	org, project, contract, err := parseODCSContractPath(orgParam, projectParam, contractParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	revision, err := slug.New(revisionParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid revision slug")
	}
	result, err := s.odcsContractsService.InspectRevision(c.Context(), org, project, contract, revision, ptr.Value(params.SchemaObjectId))
	if err != nil {
		return mapServiceError(err)
	}
	projection := result.Projection
	response := api.ODCSRevisionInspection{
		Revision: odcsRevisionToHTTP(*result.Revision), CompilerId: projection.CompilerID,
		SchemaObjectId: projection.SchemaObjectID, DocumentValid: projection.DocumentValid,
		CoverageComplete: projection.CoverageComplete, ProjectionAvailable: projection.Shape != nil,
		Shape:    pipelineSchemaContractToAPI(projection.Shape),
		Coverage: []api.ODCSDeclarationCoverage{}, Diagnostics: []api.ODCSInspectionDiagnostic{},
	}
	for _, entry := range projection.Coverage {
		item := api.ODCSDeclarationCoverage{Path: entry.Path, State: api.ODCSDeclarationCoverageState(entry.State), Reason: entry.Reason, Applicability: api.ODCSDeclarationCoverageApplicability(entry.Applicability)}
		if entry.RuntimeScope != "" {
			item.RuntimeScope = &entry.RuntimeScope
		}
		response.Coverage = append(response.Coverage, item)
	}
	for _, diagnostic := range projection.Diagnostics {
		response.Diagnostics = append(response.Diagnostics, api.ODCSInspectionDiagnostic{Code: diagnostic.Code, Path: diagnostic.Path, Message: diagnostic.Message})
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(response)
}
