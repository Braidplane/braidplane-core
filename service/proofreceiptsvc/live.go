package proofreceiptsvc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

const (
	LiveCompilerVersion = "proof-live-metadata-compiler-v2"
	LivePolicyVersion   = "proof-live-metadata-policy-v2"
)

// LiveCompiler is a separately admitted path. The ordinary compiler never
// dispatches observations or silently upgrades cached evidence.
type LiveCompiler struct {
	coordinator      *proofevidencesvc.Coordinator
	events           eventrecorder.Recorder
	base             *Compiler
	ledger           *proofevidencesvc.Ledger
	sources          datasources.Repository
	operations       queries.Repository
	publication      ReceiptPublicationProducer
	gitChecksEnabled bool
}

type ReceiptPublicationProducer interface {
	EnqueueProofReceipt(context.Context, postgres.DBTX, model.ProofReceipt) error
}

type liveSelection struct {
	actor    slug.Slug
	compiler *LiveCompiler
	jobs     []slug.Slug
	scope    ObservationScope
}

func NewLiveCompiler(base *Compiler, ledger *proofevidencesvc.Ledger, sources datasources.Repository, operations queries.Repository, coordinator *proofevidencesvc.Coordinator, events eventrecorder.Recorder, publication ReceiptPublicationProducer, gitChecksEnabled bool) (*LiveCompiler, error) {
	if base == nil || ledger == nil || sources == nil || operations == nil || coordinator == nil || events == nil || publication == nil {
		return nil, xerror.Unexpected().Detail("proof live compiler dependencies are required")
	}
	return &LiveCompiler{base: base, ledger: ledger, sources: sources, operations: operations, coordinator: coordinator, events: events, publication: publication, gitChecksEnabled: gitChecksEnabled}, nil
}

func (s *LiveCompiler) CompileObserved(ctx context.Context, scope ObservationScope, actor, candidate slug.Slug, jobs []slug.Slug) (out *proofreceipts.Receipt, retErr error) {
	defer func() { recordObservationControl(model.EventActionExecute, retErr) }()
	if len(jobs) == 0 || len(jobs) > 2 {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	jobs = slices.Clone(jobs)
	slices.Sort(jobs)
	if len(jobs) == 2 && jobs[0] == jobs[1] {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return s.base.compile(ctx, scope.Organization, candidate, &liveSelection{compiler: s, jobs: jobs, scope: scope, actor: actor})
}

func (s *LiveCompiler) evaluate(ctx context.Context, candidate model.ProofCandidateIdentity, candidateSlug slug.Slug, pipeline *pipelinespec.Pipeline, resolutions []model.ResolvedPipelineDataSourceRef, jobs []slug.Slug, now time.Time) ([]model.ProofClaim, error) {
	claims := make([]model.ProofClaim, 0, len(jobs))
	seen := make(map[model.ProofClaimIdentity]bool)
	for _, job := range jobs {
		evidence, err := s.ledger.ReadForReceipt(ctx, ObservationScope{Organization: candidate.OrganizationSlug, Project: candidate.ProjectSlug, Environment: candidate.EnvironmentSlug}, job)
		if err != nil {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		request := evidence.Request
		if request.Candidate != candidate || request.CandidateSlug != candidateSlug || seen[request.Claim] {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		seen[request.Claim] = true
		if evidence.Observation.Result.Outcome != proofevidence.OutcomeComplete {
			if !failedObservationMatchesStep(pipeline, resolutions, request) {
				return nil, proofevidence.ErrAuthorizationDenied
			}
			claim, err := observationClaim(evidence, now)
			if err != nil {
				return nil, err
			}
			claims = append(claims, claim)
			continue
		}
		source, err := s.selectedSource(ctx, candidate, pipeline, resolutions, request.Claim.SemanticPath, evidence.Target)
		if err != nil || !credentialMatches(source, request, now) || !boundTargetMatches(source, evidence.Target) {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		claim, err := observationClaim(evidence, now)
		if err != nil {
			return nil, err
		}
		if source.Credential.AccessTokenExpiresAt != nil && source.Credential.AccessTokenExpiresAt.Before(*claim.ExpiresAt) {
			claim.ExpiresAt = source.Credential.AccessTokenExpiresAt
		}
		claims = append(claims, claim)
	}
	slices.SortFunc(claims, func(a, b model.ProofClaim) int {
		return strings.Compare(a.Identity.SemanticPath, b.Identity.SemanticPath)
	})
	return claims, nil
}

func (s *LiveCompiler) selectedSource(ctx context.Context, candidate model.ProofCandidateIdentity, pipeline *pipelinespec.Pipeline, resolutions []model.ResolvedPipelineDataSourceRef, claimPath string, target proofevidence.MetadataPlan) (*datasources.DataSource, error) {
	for _, step := range pipeline.Steps {
		ref, operationType, operationRef, inline, path := selectedStep(step, target.ConnectorType)
		if path != claimPath || !observationOperationTypeMatches(target.ConnectorType, operationType) {
			continue
		}
		for _, resolution := range resolutions {
			if resolution.RefSlug != ref || resolution.DataSourceType != target.ConnectorType {
				continue
			}
			source, err := postgres.InRead(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*datasources.DataSource, error) {
				return s.sources.FindBySlug(ctx, db, candidate.OrganizationSlug, resolution.DataSourceSlug)
			})
			if err != nil || source == nil || source.Type != resolution.DataSourceType {
				return nil, proofevidence.ErrAuthorizationDenied
			}
			raw, err := json.Marshal(inline)
			if err != nil {
				return nil, proofevidence.ErrAuthorizationDenied
			}
			if operationRef != "" {
				if len(inline) != 0 {
					return nil, proofevidence.ErrAuthorizationDenied
				}
				operation, err := postgres.InRead(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*queries.Query, error) {
					saved, _, err := s.operations.FindBySlug(ctx, db, candidate.OrganizationSlug, source.Slug, operationRef)
					return saved, err
				})
				if err != nil || operation == nil || operation.DeletedAt != nil || operation.DataSourceSlug != source.Slug {
					return nil, proofevidence.ErrAuthorizationDenied
				}
				raw, err = queriessvc.ExtractQueryStatement(operation.Data)
				if err != nil {
					return nil, proofevidence.ErrAuthorizationDenied
				}
			}
			if !targetMatchesRole(source, raw, target, step.Query != nil) {
				return nil, proofevidence.ErrAuthorizationDenied
			}
			return source, nil
		}
	}
	return nil, proofevidence.ErrAuthorizationDenied
}

func credentialMatches(source *datasources.DataSource, request proofevidence.Request, now time.Time) bool {
	if source == nil || source.Credential == nil || !source.IsActive || source.Type != request.ConnectorType || source.Slug != request.DataSourceSlug || request.CredentialDataSourceSlug != source.Slug {
		return false
	}
	credential := source.Credential
	return credential.Status == "active" && credential.Slug.String()+"/"+credential.UpdatedAt.UTC().Format(time.RFC3339Nano) == request.CredentialVersion &&
		(credential.AccessTokenExpiresAt == nil || credential.AccessTokenExpiresAt.After(now))
}

func selectedStep(step pipelinespec.Step, kind model.DataSourceType) (slug.Slug, string, slug.Slug, map[string]any, string) {
	descriptor, ok := catalog.DescriptorFor(kind)
	if !ok {
		return "", "", "", nil, ""
	}
	base := "/steps/" + pointerToken(step.Slug.String())
	if step.Query != nil && descriptor.ProofMetadata.SourceClaimSuffix != "" {
		return step.Query.DataSourceSlug, step.Query.OperationType, step.Query.OperationRef, step.Query.OperationInline, base + descriptor.ProofMetadata.SourceClaimSuffix
	}
	if step.Sink != nil && descriptor.ProofMetadata.SinkClaimSuffix != "" {
		return step.Sink.DataSourceSlug, step.Sink.OperationType, step.Sink.OperationRef, step.Sink.OperationInline, base + descriptor.ProofMetadata.SinkClaimSuffix
	}
	return "", "", "", nil, ""
}

func observationOperationTypeMatches(kind model.DataSourceType, operation string) bool {
	descriptor, ok := catalog.DescriptorFor(kind)
	return ok && descriptor.QueryOperationType == operation
}

// Failed attempts bind to the original canonical step and data-source mapping,
// without interpreting current provider state as evidence of an earlier failure.
func failedObservationMatchesStep(pipeline *pipelinespec.Pipeline, resolutions []model.ResolvedPipelineDataSourceRef, request proofevidence.Request) bool {
	for _, step := range pipeline.Steps {
		ref, connector, _, _, path := selectedStep(step, request.ConnectorType)
		if path != request.Claim.SemanticPath || !observationOperationTypeMatches(request.ConnectorType, connector) {
			continue
		}
		for _, resolution := range resolutions {
			if resolution.RefSlug == ref && resolution.DataSourceSlug == request.DataSourceSlug && resolution.DataSourceType == request.ConnectorType {
				return true
			}
		}
	}
	return false
}

func observationClaim(evidence *proofevidencesvc.ReceiptObservation, now time.Time) (model.ProofClaim, error) {
	request, observation := evidence.Request, evidence.Observation
	result := observation.Result
	requestDigest, err := request.Digest()
	if err != nil || result.RequestDigest != requestDigest || request.EvidenceLevel != model.ProofEvidenceLevelLiveReadOnly || request.Claim.Family != model.ProofClaimFamilyAccessCapability || !request.MetadataRetainUntil.After(now) {
		return model.ProofClaim{}, proofevidence.ErrAuthorizationDenied
	}
	level := model.ProofEvidenceLevelLiveReadOnly
	claim := model.ProofClaim{Identity: request.Claim, RequiredEvidenceLevel: level, Evidence: []model.ProofEvidenceReference{{Kind: model.ProofEvidenceReferenceKindIntrospection, Digest: requestDigest}}}
	claim.Description = "selected resource metadata is reachable with the bound data-source credential"
	if result.Outcome != proofevidence.OutcomeComplete {
		if !result.Outcome.IsAOutcome() || observation.Snapshot != nil {
			return model.ProofClaim{}, proofevidence.ErrAuthorizationDenied
		}
		claim.Verdict = model.ProofVerdictInconclusive
		summary := "the authorized observation did not produce usable live evidence"
		retryable := false
		switch result.Outcome {
		case proofevidence.OutcomeUnknown, proofevidence.OutcomeComplete:
			return model.ProofClaim{}, proofevidence.ErrAuthorizationDenied
		case proofevidence.OutcomeUnsupported:
			claim.Verdict = model.ProofVerdictUnverified
			summary = "the selected observation is unsupported"
		case proofevidence.OutcomeDenied:
			summary = "access needed for the observation was denied"
		case proofevidence.OutcomeUnreachable:
			summary = "the observation endpoint could not be reached"
			retryable = true
		case proofevidence.OutcomeRateLimited:
			summary = "the provider rate limit prevented observation"
			retryable = true
		case proofevidence.OutcomeTimedOut:
			summary = "the observation did not finish before its deadline"
			retryable = true
		case proofevidence.OutcomeOverBudget:
			summary = "the observation exceeded an admitted limit"
		case proofevidence.OutcomeCanceled:
			summary = "the observation was canceled"
		case proofevidence.OutcomeWorkerLost:
			summary = "the worker did not complete the admitted observation"
			retryable = true
		case proofevidence.OutcomePartial:
			summary = "the observation returned incomplete metadata"
			retryable = true
		case proofevidence.OutcomeProviderError:
			summary = "the provider could not complete the observation"
			retryable = true
		case proofevidence.OutcomeAttestationFailed:
			summary = "the runner's required attestation could not be verified"
		case proofevidence.OutcomeCleanupUncertain:
			summary = "the observation's cleanup could not be verified"
		}
		claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.observation." + result.Outcome.String(), Summary: summary, Remediation: "resolve the reported condition, then explicitly authorize a new observation and compile a new receipt", Retryable: retryable}}
		return claim, nil
	}
	observer, err := connector.NewMetadataObserver(evidence.Target)
	if err != nil {
		return model.ProofClaim{}, err
	}
	claim.Description = observer.Description()
	if result.EvidenceLevel != level || !result.ExpiresAt.After(now) || evidence.DispatchedAt.IsZero() || !result.ExpiresAt.After(evidence.DispatchedAt) || observation.Snapshot == nil {
		return model.ProofClaim{}, proofevidence.ErrAuthorizationDenied
	}
	claim.ActualEvidenceLevel, claim.ObservedAt, claim.ExpiresAt = &level, &evidence.DispatchedAt, &result.ExpiresAt
	claim.Verdict, claim.Diagnostics, err = observer.AssessMetadata(observation.Snapshot.Metadata)
	if err != nil {
		return model.ProofClaim{}, err
	}
	return claim, nil
}

// Compile preserves admitted live receipts for provider re-requests. Live dispatch
// and receipt upgrades require explicit authorization and CompileObserved.
func (s *LiveCompiler) Compile(ctx context.Context, org, candidate slug.Slug) (*proofreceipts.Receipt, error) {
	return s.base.Compile(ctx, org, candidate)
}

func (s *LiveCompiler) Get(ctx context.Context, org, project, environment, receiptSlug slug.Slug) (*ReceiptDetail, error) {
	detail, err := s.base.readReceipt(ctx, org, project, environment, receiptSlug)
	if err != nil {
		return nil, err
	}
	s.revalidateReceipt(ctx, org, project, environment, detail)
	return detail, nil
}

func (s *LiveCompiler) List(ctx context.Context, org, project, environment slug.Slug, req model.ListRequest) (model.ListResponse[*ReceiptDetail], error) {
	response, err := s.base.listReceipts(ctx, org, project, environment, req)
	if err != nil {
		return model.ListResponse[*ReceiptDetail]{}, err
	}
	for _, detail := range response.Items {
		s.revalidateReceipt(ctx, org, project, environment, detail)
	}
	return response, nil
}

func (s *LiveCompiler) revalidateReceipt(ctx context.Context, org, project, environment slug.Slug, detail *ReceiptDetail) {
	jobs := make([]slug.Slug, 0, 2)
	var candidateSlug slug.Slug
	valid := true
	for _, claim := range detail.Receipt.Claims {
		if claim.ActualEvidenceLevel == nil || *claim.ActualEvidenceLevel != model.ProofEvidenceLevelLiveReadOnly {
			continue
		}
		if len(claim.Evidence) != 1 || claim.Evidence[0].Kind != model.ProofEvidenceReferenceKindIntrospection {
			valid = false
			break
		}
		evidence, err := s.ledger.ReadForReceiptDigest(ctx, ObservationScope{Organization: org, Project: project, Environment: environment}, claim.Evidence[0].Digest)
		if err != nil || evidence.Observation.Result.Outcome != proofevidence.OutcomeComplete || evidence.Request.Candidate != detail.Receipt.Candidate || evidence.Request.Claim != claim.Identity {
			valid = false
			break
		}
		if candidateSlug != "" && candidateSlug != evidence.Request.CandidateSlug {
			valid = false
			break
		}
		candidateSlug = evidence.Request.CandidateSlug
		jobs = append(jobs, evidence.Request.JobID)
	}
	if valid && len(jobs) == 0 {
		return
	}
	if valid {
		_, err := s.evaluateCurrent(ctx, detail.Receipt.Candidate, candidateSlug, jobs)
		valid = err == nil
	}
	if !valid {
		markLiveClaimsStale(detail, model.ProofDiagnostic{Code: "proof.live_observation_changed", Summary: "live evidence is expired or no longer matches current authorization and target state", Remediation: "authorize fresh evidence and compile a new receipt", Retryable: true})
	}
}

func (s *LiveCompiler) evaluateCurrent(ctx context.Context, identity model.ProofCandidateIdentity, candidateSlug slug.Slug, jobs []slug.Slug) ([]model.ProofClaim, error) {
	scope := ObservationScope{Organization: identity.OrganizationSlug, Project: identity.ProjectSlug, Environment: identity.EnvironmentSlug}
	current, pipeline, resolutions, err := s.observationCandidate(ctx, scope, candidateSlug)
	if err != nil || current != identity {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return s.evaluate(ctx, identity, candidateSlug, pipeline, resolutions, jobs, s.base.clock.Now())
}

func markLiveClaimsStale(detail *ReceiptDetail, diagnostic model.ProofDiagnostic) {
	changed := false
	for i := range detail.Receipt.Claims {
		claim := &detail.Receipt.Claims[i]
		if claim.ActualEvidenceLevel != nil && *claim.ActualEvidenceLevel == model.ProofEvidenceLevelLiveReadOnly {
			claim.Verdict = model.ProofVerdictStale
			claim.Diagnostics = []model.ProofDiagnostic{diagnostic}
			changed = true
		}
	}
	if changed && detail.Receipt.AggregateVerdict != model.ProofVerdictSuperseded {
		detail.Receipt.AggregateVerdict = model.AggregateProofVerdict(detail.Receipt.Claims)
	}
}

func targetMatchesRole(source *datasources.DataSource, operation json.RawMessage, target proofevidence.MetadataPlan, sourceRole bool) bool {
	observer, err := connector.NewMetadataObserver(target)
	return err == nil && source.Type == target.ConnectorType && observer.MatchesResource(source.Config, operation, sourceRole)
}

func boundTargetMatches(source *datasources.DataSource, target proofevidence.MetadataPlan) bool {
	if source == nil || source.Credential == nil {
		return false
	}
	current, err := connector.BindMetadataPlan(target, source.Config, source.Credential.Kind)
	if err != nil {
		return false
	}
	want, err := target.Fingerprint()
	if err != nil {
		return false
	}
	got, err := current.Fingerprint()
	return err == nil && got == want
}
