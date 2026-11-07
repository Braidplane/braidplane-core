package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
)

func (s *server) ListProofReceipts(c fiber.Ctx, slugParam api.SlugParam, projectSlugParam api.ProjectSlugParam, environmentSlugParam api.EnvironmentSlugParam, params api.ListProofReceiptsParams) error {
	c.Set("Cache-Control", "no-store")
	if s.proofReceiptService == nil {
		return fiber.ErrNotFound
	}
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(slugParam, projectSlugParam, environmentSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		s.recordObservationAccessRejection(c, proofreceiptsvc.ObservationScope{Organization: orgSlug, Project: projectSlug, Environment: environmentSlug}, model.Read, model.ResourcePipelines)
		return err
	}
	response, err := s.proofReceiptService.List(c.Context(), orgSlug, projectSlug, environmentSlug, model.ListRequest{
		Limit: int(ptr.Value(params.Limit)), Cursor: ptr.Value(params.Cursor), Direction: string(ptr.Value(params.Direction)),
	})
	if err != nil {
		return mapServiceError(err)
	}
	receipts := make([]api.ProofReceipt, len(response.Items))
	scope := proofreceiptsvc.ObservationScope{Organization: orgSlug, Project: projectSlug, Environment: environmentSlug}
	for i, receipt := range response.Items {
		if err := proofreceiptsvc.AuditReceiptRead(c.Context(), s.eventsService, scope, s.eventActor(c), receipt.Receipt.ID); err != nil {
			return mapProofObservationError(err)
		}
		receipts[i] = toAPIProofReceipt(receipt)
	}
	return c.JSON(api.ProofReceiptListResponse{Receipts: receipts, HasMore: response.HasMore, NextCursor: response.NextCursor, PrevCursor: response.PrevCursor})
}

func (s *server) GetProofReceipt(c fiber.Ctx, slugParam api.SlugParam, projectSlugParam api.ProjectSlugParam, environmentSlugParam api.EnvironmentSlugParam, receiptSlugParam api.ProofReceiptSlugParam) error {
	c.Set("Cache-Control", "no-store")
	if s.proofReceiptService == nil {
		return fiber.ErrNotFound
	}
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(slugParam, projectSlugParam, environmentSlugParam)
	if err != nil {
		return err
	}
	receiptSlug, err := slug.New(receiptSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid proof receipt slug: %v", err))
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
		s.recordObservationAccessRejection(c, proofreceiptsvc.ObservationScope{Organization: orgSlug, Project: projectSlug, Environment: environmentSlug}, model.Read, model.ResourcePipelines)
		return err
	}
	receipt, err := s.proofReceiptService.Get(c.Context(), orgSlug, projectSlug, environmentSlug, receiptSlug)
	if err != nil {
		return mapServiceError(err)
	}
	if err := proofreceiptsvc.AuditReceiptRead(c.Context(), s.eventsService, proofreceiptsvc.ObservationScope{Organization: orgSlug, Project: projectSlug, Environment: environmentSlug}, s.eventActor(c), receiptSlug); err != nil {
		return mapProofObservationError(err)
	}
	return c.JSON(toAPIProofReceipt(receipt))
}

func toAPIProofReceipt(detail *proofreceiptsvc.ReceiptDetail) api.ProofReceipt {
	if detail == nil {
		return api.ProofReceipt{Claims: []api.ProofClaim{}, LifecycleLinks: []api.ProofLifecycleLink{}}
	}
	receipt := &detail.Receipt
	claims := make([]api.ProofClaim, len(receipt.Claims))
	for i, claim := range receipt.Claims {
		evidence := make([]api.ProofEvidenceReference, len(claim.Evidence))
		for j, reference := range claim.Evidence {
			evidence[j] = api.ProofEvidenceReference{Kind: api.ProofEvidenceReferenceKind(reference.Kind.String()), Digest: reference.Digest}
		}
		diagnostics := make([]api.ProofDiagnostic, len(claim.Diagnostics))
		for j, diagnostic := range claim.Diagnostics {
			diagnostics[j] = api.ProofDiagnostic{Code: diagnostic.Code, Summary: diagnostic.Summary, Remediation: diagnostic.Remediation, Retryable: diagnostic.Retryable}
		}
		var actual *api.ProofEvidenceLevel
		if claim.ActualEvidenceLevel != nil {
			value := api.ProofEvidenceLevel(claim.ActualEvidenceLevel.String())
			actual = &value
		}
		claims[i] = api.ProofClaim{
			Identity:    api.ProofClaimIdentity{Family: api.ProofClaimFamily(claim.Identity.Family.String()), PipelineVersionHash: claim.Identity.PipelineVersionHash, SemanticPath: claim.Identity.SemanticPath},
			Description: claim.Description, RequiredEvidenceLevel: api.ProofEvidenceLevel(claim.RequiredEvidenceLevel.String()), ActualEvidenceLevel: actual,
			ObservedAt: claim.ObservedAt, ExpiresAt: claim.ExpiresAt, Verdict: api.ProofVerdict(claim.Verdict.String()), Evidence: evidence, Diagnostics: diagnostics,
		}
	}
	var supersedes *api.Slug
	if receipt.SupersedesReceiptID != nil {
		value := api.Slug(*receipt.SupersedesReceiptID)
		supersedes = &value
	}
	links := make([]api.ProofLifecycleLink, len(detail.LifecycleLinks))
	for i, link := range detail.LifecycleLinks {
		var pipelineSlug, runSlug *api.Slug
		if link.PipelineSlug != nil {
			value := api.Slug(*link.PipelineSlug)
			pipelineSlug = &value
		}
		if link.RunSlug != nil {
			value := api.Slug(*link.RunSlug)
			runSlug = &value
		}
		var currentSpecHash *string
		if link.CurrentVersionSpecHash != "" {
			currentSpecHash = &link.CurrentVersionSpecHash
		}
		links[i] = api.ProofLifecycleLink{
			Kind: api.ProofLifecycleLinkKind(link.Kind.String()), ReceiptId: api.Slug(receipt.ID), Identifier: link.Identifier,
			LinkedAt: link.LinkedAt, RetainUntil: link.RetainUntil, Status: api.ProofLifecycleLinkStatus(link.Status),
			PipelineSlug: pipelineSlug, RunSlug: runSlug, CurrentVersionSpecHash: currentSpecHash,
		}
	}
	var operational *api.ProofOperationalCapture
	var availability *api.ProofReceiptOperationalEvidenceStatus
	var currentVerdict *api.ProofVerdict
	if capture := receipt.Operational; capture != nil {
		operational = &api.ProofOperationalCapture{SourceReceiptId: capture.SourceReceiptID.String(), PolicyVersion: capture.PolicyVersion, Selection: toAPIOperationalSelection(capture.Selection), RecordDigest: capture.RecordDigest, ExpiresAt: capture.ExpiresAt}
		status := api.ProofReceiptOperationalEvidenceStatus(detail.OperationalEvidenceStatus.String())
		verdict := api.ProofVerdict(detail.OperationalCurrentVerdict.String())
		availability, currentVerdict = &status, &verdict
	}

	return api.ProofReceipt{
		OperationalAdmissionEnabled: detail.OperationalAdmissionEnabled,
		SchemaVersion:               api.ProofReceiptSchemaVersion(receipt.SchemaVersion), Id: api.Slug(receipt.ID),
		Operational: operational, OperationalEvidenceStatus: availability, OperationalCurrentVerdict: currentVerdict,
		Candidate: toAPIProofCandidateIdentity(receipt.Candidate),
		Claims:    claims, AggregateVerdict: api.ProofVerdict(receipt.AggregateVerdict.String()), SupersedesReceiptId: supersedes,
		CreatedAt: receipt.CreatedAt, RetainUntil: receipt.RetainUntil, LifecycleLinks: links,
	}
}

func toAPIProofCandidateIdentity(candidate model.ProofCandidateIdentity) api.ProofCandidateIdentity {
	return api.ProofCandidateIdentity{
		OrganizationSlug: api.Slug(candidate.OrganizationSlug), ProjectSlug: api.Slug(candidate.ProjectSlug),
		EnvironmentSlug: api.Slug(candidate.EnvironmentSlug), ProviderConnectionSlug: api.Slug(candidate.ProviderConnectionSlug),
		RepositoryId: candidate.RepositoryID, ChangeRequestId: candidate.ChangeRequestID, HeadSha: candidate.HeadSHA,
		TargetRef: candidate.TargetRef, PipelinePath: candidate.PipelinePath, SpecHash: candidate.SpecHash,
		BindingFingerprint: candidate.BindingFingerprint, CapabilityFingerprint: candidate.CapabilityFingerprint,
		CompilerVersion: candidate.CompilerVersion, PolicyVersion: candidate.PolicyVersion,
	}
}
