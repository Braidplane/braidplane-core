package proofreceiptsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/proofidentity"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

const (
	CompilerVersion = "proof-preview-compiler-v3"
	PolicyVersion   = "proof-preview-policy-v1"
	cacheLifetime   = 24 * time.Hour
	retentionPeriod = 7 * 24 * time.Hour
)

type PipelineValidator interface {
	Validate(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinesvc.PipelineValidationResult, error)
}

type ContractValidator interface {
	ValidateRevision(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error)
}

type BindingResolver interface {
	ResolvePipelineDataSourceRefs(context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error)
}

type DeploymentReader interface {
	FindDeployment(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDeployment, error)
}

type SchemaReader interface {
	FindByDataSourceSlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*datasources.SchemaMetadata, error)
}

type EnvironmentSecretResolver interface {
	ResolveRuntimeSecrets(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error)
}

type Service interface {
	Compile(context.Context, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error)
	Get(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*ReceiptDetail, error)
	List(context.Context, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[*ReceiptDetail], error)
}

type ReceiptDetail struct {
	OperationalAdmissionEnabled *bool
	OperationalEvidenceStatus   model.ProofOperationalEvidenceStatus
	OperationalCurrentVerdict   model.ProofVerdict
	Receipt                     model.ProofReceipt
	LifecycleLinks              []LifecycleLink
}

type LifecycleLink struct {
	Kind                   model.ProofLifecycleLinkKind
	Identifier             string
	LinkedAt               time.Time
	RetainUntil            time.Time
	Status                 string
	PipelineSlug           *slug.Slug
	RunSlug                *slug.Slug
	CurrentVersionSpecHash string
}

type Compiler struct {
	txm         postgres.TransactionManager
	repo        proofreceipts.Repository
	validator   PipelineValidator
	contracts   ContractValidator
	bindings    BindingResolver
	schemas     SchemaReader
	deployments DeploymentReader
	secrets     EnvironmentSecretResolver
	clock       clock.Clock
}

func New(
	txm postgres.TransactionManager,
	repo proofreceipts.Repository,
	validator PipelineValidator,
	bindings BindingResolver,
	schemas SchemaReader,
	deployments DeploymentReader,
	secrets EnvironmentSecretResolver,
	contracts ContractValidator,
	clk clock.Clock,
) (*Compiler, error) {
	if txm == nil || repo == nil || validator == nil || bindings == nil || schemas == nil || deployments == nil || secrets == nil || contracts == nil || clk == nil {
		return nil, xerror.Unexpected().Detail("proof receipt dependencies are required")
	}
	return &Compiler{txm: txm, repo: repo, validator: validator, bindings: bindings, schemas: schemas, deployments: deployments, secrets: secrets, contracts: contracts, clock: clk}, nil
}

func (s *Compiler) Compile(ctx context.Context, orgSlug, candidateSlug slug.Slug) (*proofreceipts.Receipt, error) {
	latest, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofreceipts.Receipt, error) {
		return s.repo.FindLatestForCandidate(ctx, db, orgSlug, candidateSlug)
	})
	if err != nil && !xerror.Is(err, proofreceipts.ErrReceiptNotFound) {
		return nil, err
	}
	// A provider re-request has no live-observation authority. Retain the admitted
	// receipt; publication revalidates its freshness without downgrading it to static.
	if latest != nil && latest.Value.Candidate.CompilerVersion == LiveCompilerVersion {
		return latest, nil
	}
	return s.compile(ctx, orgSlug, candidateSlug, nil)
}

func (s *Compiler) compile(ctx context.Context, orgSlug, candidateSlug slug.Slug, live *liveSelection) (*proofreceipts.Receipt, error) {
	target, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofreceipts.CandidateTarget, error) {
		return s.repo.FindCandidateTarget(ctx, db, orgSlug, candidateSlug)
	})
	if err != nil {
		return nil, err
	}
	if live != nil && (target.OrganizationSlug != live.scope.Organization || target.ProjectSlug != live.scope.Project || target.EnvironmentSlug != live.scope.Environment) {
		return nil, proofreceipts.ErrReceiptNotFound
	}
	pipeline, err := pipelinevalidate.Load(target.PipelinePath, target.CanonicalSpec)
	if err != nil {
		return nil, xerror.Wrapf("load proof candidate pipeline: %w", err)
	}
	validation, err := s.validator.Validate(ctx, target.OrganizationSlug, target.ProjectSlug, target.EnvironmentSlug, "", pipeline)
	if err != nil {
		return nil, xerror.Wrapf("validate proof candidate pipeline: %w", err)
	}
	resolved, resolveErr := s.bindings.ResolvePipelineDataSourceRefs(ctx, target.OrganizationSlug, target.ProjectSlug, target.EnvironmentSlug, pipeline)
	if resolveErr != nil && validation.Valid {
		return nil, xerror.Wrapf("resolve proof candidate bindings: %w", resolveErr)
	}
	if resolved == nil {
		resolved = &datasourcerefssvc.ResolvedPipeline{}
	}

	contractInputs, err := s.contractInputs(ctx, target.OrganizationSlug, target.ProjectSlug, pipeline)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	secrets, err := s.secrets.ResolveRuntimeSecrets(ctx, target.OrganizationSlug, target.EnvironmentSlug)
	if err != nil {
		return nil, err
	}
	observations, deployment, err := s.readEvidenceState(ctx, target, pipeline.Metadata.Slug, resolved.Resolutions)
	if err != nil {
		return nil, err
	}
	bindingFingerprint, err := proofidentity.BindingFingerprint(resolved.Resolutions)
	if err != nil {
		return nil, err
	}
	capabilityFingerprint, err := proofidentity.CapabilityFingerprint(pipeline, resolved.Resolutions)
	if err != nil {
		return nil, err
	}
	compilerVersion, policyVersion := CompilerVersion, PolicyVersion
	if live != nil {
		compilerVersion, policyVersion = LiveCompilerVersion, LivePolicyVersion
	}
	identity := receiptCandidate(target, bindingFingerprint, capabilityFingerprint, compilerVersion, policyVersion)
	var liveClaims []model.ProofClaim
	if live != nil {
		liveClaims, err = live.compiler.evaluate(ctx, identity, target.CandidateSlug, pipeline, resolved.Resolutions, live.jobs, now)
		if err != nil {
			return nil, err
		}
	}
	evidenceDigest, err := digestJSON(struct {
		StaticCompilerVersion string
		Contracts             []contractInput
		SpecHash              string
		BindingFingerprint    string
		CapabilityFingerprint string
		Valid                 bool
		Diagnostics           []pipelinesvc.PipelineValidationDiagnostic
		Observations          []schemaObservation
		DeploymentSpecHash    string
	}{CompilerVersion, contractInputs, target.SpecHash, bindingFingerprint, capabilityFingerprint, validation.Valid, validation.Diagnostics, observations, deploymentSpecHash(deployment)})
	if err != nil {
		return nil, err
	}

	if live != nil {
		evidenceDigest, err = digestJSON(struct {
			Baseline string
			Claims   []model.ProofClaim
		}{evidenceDigest, liveClaims})
		if err != nil {
			return nil, err
		}
	}
	claims := compileClaims(pipeline, validation, resolved.Resolutions, observations, deployment, target.SpecHash, evidenceDigest, now)
	claims, err = appendODCSClaims(claims, contractInputs, resolved.Resolutions, observations, target.SpecHash, evidenceDigest, now, secretRedactor(secrets))
	if err != nil {
		return nil, err
	}
	claims = append(claims, liveClaims...)
	receiptKey, err := digestJSON(struct {
		CandidateKey          string
		BindingFingerprint    string
		CapabilityFingerprint string
		EvidenceDigest        string
		CompilerVersion       string
		PolicyVersion         string
	}{target.CandidateSlug.String() + ":" + target.HeadSHA + ":" + target.SpecHash, bindingFingerprint, capabilityFingerprint, evidenceDigest, compilerVersion, policyVersion})
	if err != nil {
		return nil, err
	}
	receiptSlug := slug.Slug("proof-" + strings.TrimPrefix(receiptKey, "sha256:")[:32])
	draft := model.ProofReceipt{
		SchemaVersion: model.ProofReceiptSchemaVersion,
		ID:            receiptSlug,
		Candidate:     identity,
		Claims:        claims, CreatedAt: now, RetainUntil: now.Add(retentionPeriod),
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*proofreceipts.Receipt, error) {
		// Retention audits reference the organization; do not hold source/organization
		// row locks while waiting for cleanup's tenant lock.
		if err := s.repo.LockTarget(ctx, tx, "proof-retention:"+orgSlug.String()); err != nil {
			return nil, err
		}
		if err := s.repo.LockSource(ctx, tx, target.OrganizationSlug, target.ProviderConnectionSlug); err != nil {
			return nil, err
		}
		lockKey := target.OrganizationSlug.String() + ":" + strconv.FormatInt(target.MappingID, 10) + ":" + target.ChangeRequestID
		if err := s.repo.LockTarget(ctx, tx, lockKey); err != nil {
			return nil, err
		}
		current, err := s.repo.FindCandidateTarget(ctx, tx, orgSlug, candidateSlug)
		if err != nil {
			return nil, err
		}
		if current.MappingID != target.MappingID {
			return nil, proofreceipts.ErrReceiptNotFound
		}
		if live != nil {
			refreshed, err := live.compiler.evaluateCurrent(ctx, identity, target.CandidateSlug, live.jobs)
			if err != nil {
				return nil, err
			}
			before, err := digestJSON(liveClaims)
			if err != nil {
				return nil, err
			}
			after, err := digestJSON(refreshed)
			if err != nil || before != after {
				return nil, proofevidence.ErrAuthorizationDenied
			}
		}
		if live != nil {
			if err := live.compiler.audit(ctx, live.actor, observationAudit{scope: live.scope, action: model.EventActionExecute, accepted: true, receiptSlug: receiptSlug}); err != nil {
				return nil, err
			}
		}
		expiresAt := latestExpiry(observations)
		for _, claim := range liveClaims {
			if claim.ExpiresAt != nil && (expiresAt == nil || claim.ExpiresAt.Before(*expiresAt)) {
				expiresAt = claim.ExpiresAt
			}
		}
		evidence, err := s.repo.CreateEvidence(ctx, tx, target, proofreceipts.EvidenceParams{
			Kind: model.ProofEvidenceReferenceKindValidation.String(), Digest: evidenceDigest,
			SourceIdentifier: target.CandidateSlug.String(), BindingFingerprint: bindingFingerprint,
			CapabilityFingerprint: capabilityFingerprint, ObservedAt: now, ExpiresAt: expiresAt,
		})
		if err != nil {
			return nil, err
		}
		latest, err := s.repo.FindLatest(ctx, tx, target, receiptKey)
		if xerror.Is(err, proofreceipts.ErrReceiptNotFound) {
			latest, err = nil, nil
		}
		if err != nil {
			return nil, err
		}
		if latest != nil {
			draft.SupersedesReceiptID = &latest.Slug
		}
		receipt, err := model.NewProofReceipt(draft, secretRedactor(secrets))
		if err != nil {
			return nil, err
		}
		stored, err := s.repo.Create(ctx, tx, target, proofreceipts.CreateReceiptParams{
			EvidenceID: evidence.ID, ReceiptKey: receiptKey, Receipt: receipt, Supersedes: latest,
		})
		if err != nil {
			return nil, err
		}
		if live != nil && live.compiler.gitChecksEnabled {
			if err := live.compiler.publication.EnqueueProofReceipt(ctx, tx, stored.Value); err != nil {
				return nil, xerror.Wrapf("enqueue live Proof publication: %w", err)
			}
		}
		return stored, nil
	})
}

func (s *Compiler) Get(ctx context.Context, orgSlug, projectSlug, environmentSlug, receiptSlug slug.Slug) (*ReceiptDetail, error) {
	detail, err := s.readReceipt(ctx, orgSlug, projectSlug, environmentSlug, receiptSlug)
	if err != nil {
		return nil, err
	}
	markLiveClaimsStale(detail, model.ProofDiagnostic{Code: "proof.live_observation_unavailable", Summary: "live evidence cannot be revalidated while live observations are disabled", Remediation: "enable the authorized live observation path and refresh evidence", Retryable: true})
	return detail, nil
}

func (s *Compiler) List(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[*ReceiptDetail], error) {
	response, err := s.listReceipts(ctx, orgSlug, projectSlug, environmentSlug, req)
	if err != nil {
		return model.ListResponse[*ReceiptDetail]{}, err
	}
	for _, detail := range response.Items {
		markLiveClaimsStale(detail, model.ProofDiagnostic{Code: "proof.live_observation_unavailable", Summary: "live evidence cannot be revalidated while live observations are disabled", Remediation: "enable the authorized live observation path and refresh evidence", Retryable: true})
	}
	return response, nil
}

func (s *Compiler) listReceipts(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[*ReceiptDetail], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)
	details, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]*ReceiptDetail, error) {
		stored, err := s.repo.List(ctx, db, orgSlug, projectSlug, environmentSlug, queryReq)
		if err != nil {
			return nil, err
		}
		result := make([]*ReceiptDetail, 0, len(stored))
		// ponytail: at most 101 lifecycle lookups; batch if measured list latency requires it.
		for _, receipt := range stored {
			detail, err := s.projectReceipt(ctx, db, orgSlug, projectSlug, environmentSlug, receipt)
			if err != nil {
				return nil, err
			}
			result = append(result, detail)
		}
		return result, nil
	})
	if err != nil {
		return model.ListResponse[*ReceiptDetail]{}, err
	}
	details, hasMore := model.FinalizePage(details, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(details, normalized, hasMore, func(item *ReceiptDetail, _ string) (string, string) {
		return item.Receipt.CreatedAt.Format(time.RFC3339Nano), item.Receipt.ID.String()
	})
	responseHasMore := hasMore
	if normalized.Direction == model.ListDirectionPrev && normalized.Cursor != "" && len(details) > 0 {
		responseHasMore = true
		last := details[len(details)-1]
		nextCursor = model.EncodeCursor(last.Receipt.CreatedAt.Format(time.RFC3339Nano), last.Receipt.ID.String())
		if !hasMore {
			prevCursor = nil
		}
	}
	return model.ListResponse[*ReceiptDetail]{Items: details, HasMore: responseHasMore, NextCursor: nextCursor, PrevCursor: prevCursor}, nil
}

func (s *Compiler) readReceipt(ctx context.Context, orgSlug, projectSlug, environmentSlug, receiptSlug slug.Slug) (*ReceiptDetail, error) {
	detail, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*ReceiptDetail, error) {
		stored, err := s.repo.FindBySlug(ctx, db, orgSlug, projectSlug, environmentSlug, receiptSlug)
		if err != nil {
			return nil, err
		}
		return s.projectReceipt(ctx, db, orgSlug, projectSlug, environmentSlug, stored)
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Compiler) projectReceipt(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, stored *proofreceipts.Receipt) (*ReceiptDetail, error) {
	receiptSlug := stored.Value.ID
	links, err := s.repo.ListLifecycleLinksByReceipt(ctx, db, orgSlug, projectSlug, environmentSlug, receiptSlug)
	if err != nil {
		return nil, err
	}
	receipt := stored.Value
	receipt.Claims = slices.Clone(receipt.Claims)
	expired := false
	for i := range receipt.Claims {
		claim := &receipt.Claims[i]
		if claim.ActualEvidenceLevel != nil && *claim.ActualEvidenceLevel == model.ProofEvidenceLevelLiveReadOnly && (claim.ExpiresAt == nil || !claim.ExpiresAt.After(s.clock.Now())) {
			claim.Verdict = model.ProofVerdictStale
			expired = true
			claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.live_observation_stale", Summary: "live observation has expired", Remediation: "authorize a new observation and compile a new receipt", Retryable: true}}
		}
	}
	if expired {
		receipt.AggregateVerdict = model.AggregateProofVerdict(receipt.Claims)
	}
	if stored.Superseded {
		receipt.AggregateVerdict = model.ProofVerdictSuperseded
	}
	projectedLinks := make([]LifecycleLink, 0, len(links))
	now := s.clock.Now()
	for i := range links {
		link := &links[i]
		projectedLinks = append(projectedLinks, LifecycleLink{
			Kind: link.Kind, Identifier: link.Identifier, LinkedAt: link.LinkedAt, RetainUntil: link.RetainUntil,
			Status:       pipelinesvc.ProofLifecycleLinkStatus(link, link.CurrentSpecHash, link.CurrentIdentifier, now),
			PipelineSlug: link.PipelineSlug, RunSlug: link.RunSlug, CurrentVersionSpecHash: link.CurrentSpecHash,
		})
	}
	return &ReceiptDetail{Receipt: receipt, LifecycleLinks: projectedLinks}, nil
}

type schemaObservation struct {
	DataSourceSlug slug.Slug
	Digest         string
	ObservedAt     time.Time
	ExpiresAt      time.Time
}

type evidenceState struct {
	Observations []schemaObservation
	Deployment   *pipelinesrepo.PipelineDeployment
}

func (s *Compiler) readEvidenceState(ctx context.Context, target *proofreceipts.CandidateTarget, pipelineSlug slug.Slug, resolutions []model.ResolvedPipelineDataSourceRef) ([]schemaObservation, *pipelinesrepo.PipelineDeployment, error) {
	state, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (evidenceState, error) {
		seen := map[slug.Slug]struct{}{}
		observations := make([]schemaObservation, 0, len(resolutions))
		for _, resolution := range resolutions {
			if _, ok := seen[resolution.DataSourceSlug]; ok {
				continue
			}
			seen[resolution.DataSourceSlug] = struct{}{}
			metadata, err := s.schemas.FindByDataSourceSlug(ctx, db, target.OrganizationSlug, resolution.DataSourceSlug)
			if err != nil {
				if xerror.Is(err, datasources.ErrSchemaMetadataNotFound) {
					continue
				}
				return evidenceState{}, err
			}
			digest, err := digestJSON(metadata.SchemaData)
			if err != nil {
				return evidenceState{}, err
			}
			observations = append(observations, schemaObservation{
				DataSourceSlug: resolution.DataSourceSlug, Digest: digest,
				ObservedAt: metadata.UpdatedAt.UTC(), ExpiresAt: metadata.UpdatedAt.UTC().Add(cacheLifetime),
			})
		}
		sort.Slice(observations, func(i, j int) bool { return observations[i].DataSourceSlug < observations[j].DataSourceSlug })
		deployment, err := s.deployments.FindDeployment(ctx, db, target.OrganizationSlug, target.ProjectSlug, target.EnvironmentSlug, pipelineSlug)
		if err != nil && !xerror.Is(err, pipelinesrepo.ErrPipelineDeploymentNotFound) {
			return evidenceState{}, err
		}
		return evidenceState{Observations: observations, Deployment: deployment}, nil
	})
	return state.Observations, state.Deployment, err
}

func digestJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func deploymentSpecHash(deployment *pipelinesrepo.PipelineDeployment) string {
	if deployment == nil {
		return ""
	}
	return deployment.CurrentVersionSpecHash
}

func latestExpiry(observations []schemaObservation) *time.Time {
	if len(observations) == 0 {
		return nil
	}
	earliest := observations[0].ExpiresAt
	for _, observation := range observations[1:] {
		if observation.ExpiresAt.Before(earliest) {
			earliest = observation.ExpiresAt
		}
	}
	return &earliest
}

func secretRedactor(secrets []model.RuntimeSecretValue) func(string) string {
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret.Value != "" {
			values = append(values, secret.Value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return func(value string) string {
		for _, secret := range values {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
		return value
	}
}

func compileClaims(pipeline *pipelinespec.Pipeline, validation *pipelinesvc.PipelineValidationResult, resolutions []model.ResolvedPipelineDataSourceRef, observations []schemaObservation, deployment *pipelinesrepo.PipelineDeployment, specHash, evidenceDigest string, now time.Time) []model.ProofClaim {
	staticLevel := model.ProofEvidenceLevelStatic
	ref := model.ProofEvidenceReference{Kind: model.ProofEvidenceReferenceKindValidation, Digest: evidenceDigest}
	resolvedRefs := make(map[slug.Slug]model.ResolvedPipelineDataSourceRef, len(resolutions))
	for _, resolution := range resolutions {
		resolvedRefs[resolution.RefSlug] = resolution
	}
	observed := make(map[slug.Slug]schemaObservation, len(observations))
	for _, observation := range observations {
		observed[observation.DataSourceSlug] = observation
	}
	claims := make([]model.ProofClaim, 0, len(pipeline.Steps)*4)
	for stepIndex, step := range pipeline.Steps {
		diagnostics := clauseDiagnostics(validation.Diagnostics, stepIndex, step.Slug)
		base := "/steps/" + pointerToken(step.Slug.String())
		if step.Input != nil {
			claims = appendContractClaims(claims, base+"/input/output_contract", step.Input.OutputContract, false, slug.Slug(""), validation.Valid, observed, specHash, ref, now)
		}
		if step.Query != nil {
			_, bound := resolvedRefs[step.Query.DataSourceSlug]
			claims = append(claims,
				staticClaim(model.ProofClaimFamilySourceIdentity, base+"/source", "source binding resolves for the exact target", validation.Valid && bound, validation.Valid, diagnostics, specHash, ref, now, staticLevel),
				staticClaim(model.ProofClaimFamilyAccessCapability, base+"/source/capability", "source connector supports the declared operation", validation.Valid && bound, validation.Valid, diagnostics, specHash, ref, now, staticLevel),
			)
			dataSourceSlug := slug.Slug("")
			if resolution, ok := resolvedRefs[step.Query.DataSourceSlug]; ok {
				dataSourceSlug = resolution.DataSourceSlug
			}
			claims = appendContractClaims(claims, base+"/query/output_contract", step.Query.OutputContract, true, dataSourceSlug, validation.Valid, observed, specHash, ref, now)
			if step.Query.Incremental != nil {
				claims = append(claims, staticClaim(model.ProofClaimFamilyCursorCheckpoint, base+"/query/incremental", "incremental checkpoint declaration is valid", validation.Valid && bound, validation.Valid, diagnostics, specHash, ref, now, staticLevel))
			}
		}
		if step.Transform != nil {
			claims = appendContractClaims(claims, base+"/transform/input_contract", step.Transform.InputContract, false, "", validation.Valid, observed, specHash, ref, now)
			claims = appendContractClaims(claims, base+"/transform/output_contract", step.Transform.OutputContract, false, "", validation.Valid, observed, specHash, ref, now)
		}
		if step.Sink != nil {
			resolution, bound := resolvedRefs[step.Sink.DataSourceSlug]
			claims = append(claims,
				staticClaim(model.ProofClaimFamilyDestinationIdentity, base+"/destination", "destination binding resolves for the exact target", validation.Valid && bound, validation.Valid, diagnostics, specHash, ref, now, staticLevel),
				staticClaim(model.ProofClaimFamilyAccessCapability, base+"/destination/capability", "destination connector supports the declared operation", validation.Valid && bound, validation.Valid, diagnostics, specHash, ref, now, staticLevel),
			)
			replaySafe := bound && pipelinespec.SupportsSinkIdempotencyClass(resolution.DataSourceType, step.Sink.IdempotencyClass) && step.Sink.IdempotencyClass.IsReplaySafe()
			claims = append(claims,
				staticClaim(model.ProofClaimFamilyIdempotency, base+"/sink/idempotency", "sink idempotency declaration is supported by the bound connector", validation.Valid && replaySafe, validation.Valid, diagnostics, specHash, ref, now, staticLevel),
				staticClaim(model.ProofClaimFamilyReplay, base+"/sink/replay", "sink declaration permits repeat-safe replay", validation.Valid && replaySafe, validation.Valid, diagnostics, specHash, ref, now, staticLevel),
			)
			claims = appendContractClaims(claims, base+"/sink/input_contract", step.Sink.InputContract, false, "", validation.Valid, observed, specHash, ref, now)
		}
	}
	if !validation.Valid {
		claim := staticClaim(model.ProofClaimFamilyDriftImpact, "/validation", "exact candidate passes canonical pipeline validation", false, true, nil, specHash, ref, now, staticLevel)
		claim.Verdict = model.ProofVerdictFailed
		if selectedProjectionUnavailable(validation.Diagnostics) {
			claim.Verdict = model.ProofVerdictUnverified
		}
		claim.Diagnostics = validationDiagnostics(validation.Diagnostics)
		claims = append(claims, claim)
	}
	claims = append(claims, deploymentClaim(deployment, validation, specHash, ref, now))
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Identity.Family == claims[j].Identity.Family {
			return claims[i].Identity.SemanticPath < claims[j].Identity.SemanticPath
		}
		return claims[i].Identity.Family < claims[j].Identity.Family
	})
	return claims
}

func appendContractClaims(claims []model.ProofClaim, base string, contract *pipelinepayload.Contract, cached bool, dataSourceSlug slug.Slug, valid bool, observations map[slug.Slug]schemaObservation, specHash string, ref model.ProofEvidenceReference, now time.Time) []model.ProofClaim {
	if contract == nil {
		return claims
	}
	for _, field := range contract.Fields {
		path := base + "/fields/" + pointerToken(field.Name)
		if !cached {
			claims = append(claims, staticClaim(model.ProofClaimFamilyFieldsTypes, path, "declared field and type are statically valid", valid, valid, nil, specHash, ref, now, model.ProofEvidenceLevelStatic))
			continue
		}
		claim := model.ProofClaim{
			Identity:              model.ProofClaimIdentity{Family: model.ProofClaimFamilyFieldsTypes, PipelineVersionHash: specHash, SemanticPath: path},
			Description:           "declared source field and type match timestamped cached observation",
			RequiredEvidenceLevel: model.ProofEvidenceLevelCachedObserved,
			Evidence:              []model.ProofEvidenceReference{ref},
		}
		observation, ok := observations[dataSourceSlug]
		if !ok {
			level := model.ProofEvidenceLevelStatic
			claim.ActualEvidenceLevel, claim.ObservedAt, claim.Verdict = &level, &now, model.ProofVerdictUnverified
			claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.cached_observation_missing", Summary: "no matching cached schema observation is available", Remediation: "refresh the bound data source schema, then recompile Proof Preview"}}
		} else {
			level := model.ProofEvidenceLevelCachedObserved
			claim.ActualEvidenceLevel, claim.ObservedAt, claim.ExpiresAt = &level, &observation.ObservedAt, &observation.ExpiresAt
			switch {
			case !observation.ExpiresAt.After(now):
				claim.Verdict = model.ProofVerdictStale
				claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.cached_observation_stale", Summary: "the matching cached schema observation has expired", Remediation: "refresh the bound data source schema, then recompile Proof Preview", Retryable: true}}
			default:
				claim.Verdict = model.ProofVerdictInconclusive
				claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.cached_observation_unlinked", Summary: "cached schema exists but cannot be linked to this operation output", Remediation: "refresh operation-level schema evidence, then recompile Proof Preview"}}
			}
		}
		claims = append(claims, claim)
	}
	return claims
}

func staticClaim(family model.ProofClaimFamily, path, description string, passed, evaluated bool, diagnostics []pipelinesvc.PipelineValidationDiagnostic, specHash string, ref model.ProofEvidenceReference, now time.Time, level model.ProofEvidenceLevel) model.ProofClaim {
	verdict := model.ProofVerdictPassed
	var proofDiagnostics []model.ProofDiagnostic
	if !passed {
		verdict = model.ProofVerdictUnverified
		proofDiagnostics = []model.ProofDiagnostic{{Code: "proof.static_evaluation_unverified", Summary: "validation did not prove this clause", Remediation: "inspect validation evidence and recompile Proof Preview when the required checks are available"}}
		if evaluated {
			verdict = model.ProofVerdictFailed
			proofDiagnostics = validationDiagnostics(nil)
		}
		for _, diagnostic := range diagnostics {
			bindingFailure := diagnostic.Code == "DATA_SOURCE_BINDING_NOT_FOUND" || diagnostic.Code == "DATA_SOURCE_REF_NOT_FOUND" || diagnostic.Code == "DATA_SOURCE_BINDING_UNAVAILABLE"
			capabilityFailure := diagnostic.Code == "DATA_SOURCE_REF_INCOMPATIBLE"
			identity := family == model.ProofClaimFamilySourceIdentity || family == model.ProofClaimFamilyDestinationIdentity
			if (identity && bindingFailure) || (family == model.ProofClaimFamilyAccessCapability && capabilityFailure) {
				verdict = model.ProofVerdictFailed
				proofDiagnostics = []model.ProofDiagnostic{{Code: diagnostic.Code, Summary: "the target binding does not satisfy this clause", Remediation: "correct the target binding and recompile Proof Preview"}}
			}
		}
	}
	return model.ProofClaim{
		Identity:    model.ProofClaimIdentity{Family: family, PipelineVersionHash: specHash, SemanticPath: path},
		Description: description, RequiredEvidenceLevel: level, ActualEvidenceLevel: &level,
		ObservedAt: &now, Verdict: verdict, Evidence: []model.ProofEvidenceReference{ref}, Diagnostics: proofDiagnostics,
	}
}

// A component location is insufficient to disprove every promise on that step.
// Only typed binding findings identify an independently evaluated clause.
func clauseDiagnostics(diagnostics []pipelinesvc.PipelineValidationDiagnostic, index int, stepSlug slug.Slug) []pipelinesvc.PipelineValidationDiagnostic {
	var out []pipelinesvc.PipelineValidationDiagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.StepSlug == nil || *diagnostic.StepSlug != stepSlug.String() {
			continue
		}
		prefix := "steps[" + strconv.Itoa(index) + "]"
		if diagnostic.Field == prefix+".query" || diagnostic.Field == prefix+".sink" {
			out = append(out, diagnostic)
		}
	}
	return out
}

func safeValidationLocation(field string) string {
	if !strings.HasPrefix(field, "steps[") {
		return "spec"
	}
	end := strings.IndexByte(field, ']')
	if end < 6 {
		return "spec"
	}
	index, err := strconv.Atoi(field[6:end])
	if err != nil || index < 0 {
		return "spec"
	}
	location := "steps[" + strconv.Itoa(index) + "]"
	switch field[end+1:] {
	case ".query", ".sink", ".transform", ".input":
		location += field[end+1:]
	}
	return location
}

func validationDiagnostics(diagnostics []pipelinesvc.PipelineValidationDiagnostic) []model.ProofDiagnostic {
	if len(diagnostics) == 0 {
		return []model.ProofDiagnostic{{Code: "proof.static_evaluation_failed", Summary: "the declared promise could not be proven from the exact target state", Remediation: "update the pipeline specification or target binding, then recompile Proof Preview"}}
	}
	out := make([]model.ProofDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if selectedProjectionUnavailable([]pipelinesvc.PipelineValidationDiagnostic{diagnostic}) {
			out = append(out, selectedProjectionDiagnostic())
			continue
		}
		out = append(out, model.ProofDiagnostic{Code: "proof.validation_failed", Summary: "canonical pipeline validation failed at " + safeValidationLocation(diagnostic.Field), Remediation: "update the pipeline specification or target binding, then recompile Proof Preview"})
	}
	return out
}

// Flat conversion cannot disprove a contract that requires a selected rich projection.
func selectedProjectionUnavailable(diagnostics []pipelinesvc.PipelineValidationDiagnostic) bool {
	if len(diagnostics) == 0 {
		return false
	}
	for _, diagnostic := range diagnostics {
		switch diagnostic.Code {
		case "ODCS_TYPE_UNSUPPORTED", "ODCS_MAPPING_UNSUPPORTED":
		default:
			return false
		}
	}
	return true
}

func selectedProjectionDiagnostic() model.ProofDiagnostic {
	return model.ProofDiagnostic{Code: "proof.selected_projection_unavailable", Summary: "static Proof has not evaluated the exact selected contract projection", Remediation: "inspect the exact revision and selected projection; keep these static clauses unverified until matching static evidence is available; inspect operational receipts separately"}
}

func deploymentClaim(deployment *pipelinesrepo.PipelineDeployment, validation *pipelinesvc.PipelineValidationResult, specHash string, ref model.ProofEvidenceReference, now time.Time) model.ProofClaim {
	claim := model.ProofClaim{
		Identity:              model.ProofClaimIdentity{Family: model.ProofClaimFamilyDriftImpact, PipelineVersionHash: specHash, SemanticPath: "/deployment"},
		Description:           "candidate version is compared with the exact target deployment",
		RequiredEvidenceLevel: model.ProofEvidenceLevelStatic, Evidence: []model.ProofEvidenceReference{ref},
	}
	level := model.ProofEvidenceLevelStatic
	claim.ActualEvidenceLevel, claim.ObservedAt = &level, &now
	switch {
	case deployment == nil:
		claim.Verdict = model.ProofVerdictUnverified
		claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.deployment_missing", Summary: "the target has no deployed pipeline version to compare", Remediation: "deploy a pipeline version to this target, then recompile Proof Preview"}}
	case deployment.CurrentVersionSpecHash == specHash:
		claim.Verdict = model.ProofVerdictPassed
	case validation.ImpactReport == nil:
		claim.Verdict = model.ProofVerdictUnverified
		claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.drift_impact_missing", Summary: "the candidate differs from the deployed version without a matching drift-impact report", Remediation: "validate the exact candidate against the target, then recompile Proof Preview"}}
	case validation.Valid:
		claim.Verdict = model.ProofVerdictPassed
	default:
		claim.Verdict = model.ProofVerdictFailed
		claim.Diagnostics = validationDiagnostics(validation.Diagnostics)
	}
	return claim
}

func pointerToken(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}

func receiptCandidate(target *proofreceipts.CandidateTarget, bindingFingerprint, capabilityFingerprint, compilerVersion, policyVersion string) model.ProofCandidateIdentity {
	return model.ProofCandidateIdentity{
		OrganizationSlug: target.OrganizationSlug, ProjectSlug: target.ProjectSlug, EnvironmentSlug: target.EnvironmentSlug,
		ProviderConnectionSlug: target.ProviderConnectionSlug, RepositoryID: target.RepositoryID, ChangeRequestID: target.ChangeRequestID,
		HeadSHA: target.HeadSHA, TargetRef: target.TargetRef, PipelinePath: target.PipelinePath, SpecHash: target.SpecHash,
		BindingFingerprint: bindingFingerprint, CapabilityFingerprint: capabilityFingerprint, CompilerVersion: compilerVersion, PolicyVersion: policyVersion,
	}
}
