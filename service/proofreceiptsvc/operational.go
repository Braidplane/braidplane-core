package proofreceiptsvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/proofidentity"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
)

// OperationalCompiler admits structural checks and preserves historical receipts.
type OperationalCompiler struct {
	Service
	base   *Compiler
	runs   proofreceipts.OperationalRepository
	events eventrecorder.Recorder
}

type OperationalService interface {
	Service
	CompileOperational(context.Context, ObservationScope, slug.Slug, slug.Slug, OperationalSelection) (*proofreceipts.Receipt, error)
}

func NewOperationalCompiler(service Service, base *Compiler, runs proofreceipts.OperationalRepository, events eventrecorder.Recorder) (*OperationalCompiler, error) {
	if service == nil || base == nil || runs == nil || events == nil {
		return nil, xerror.Unexpected().Detail("operational Proof dependencies are required")
	}
	return &OperationalCompiler{Service: service, base: base, runs: runs, events: events}, nil
}

func (s *OperationalCompiler) CompileOperational(ctx context.Context, scope ObservationScope, actor, sourceReceipt slug.Slug, selection OperationalSelection) (out *proofreceipts.Receipt, retErr error) {
	defer func() {
		if retErr == nil {
			return
		}
		auditCtx, cancel := context.WithTimeout(postgres.WithoutTransaction(context.WithoutCancel(ctx)), 2*time.Second)
		defer cancel()
		if err := s.base.txm.RunInTx(auditCtx, func(ctx context.Context, _ postgres.DBTX) error {
			_, err := s.events.Record(ctx, scope.Organization, model.UserEventActor(actor), observationAudit{scope: scope, action: model.EventActionCreate, accepted: false, policyVersion: OperationalPolicyVersion})
			return err
		}); err != nil {
			retErr = ErrObservationAuditUnavailable
		}
	}()
	if selection.Candidate.OrganizationSlug != scope.Organization || selection.Candidate.ProjectSlug != scope.Project || selection.Candidate.EnvironmentSlug != scope.Environment {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	if err := validateOperationalSelection(selection); err != nil {
		return nil, err
	}
	return postgres.InTx(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*proofreceipts.Receipt, error) {
		if err := s.base.repo.LockTarget(ctx, db, "proof-retention:"+scope.Organization.String()); err != nil {
			return nil, err
		}
		source, err := s.base.repo.FindBySlug(ctx, db, scope.Organization, scope.Project, scope.Environment, sourceReceipt)
		if err != nil {
			return nil, err
		}
		if source.Value.Operational != nil || source.Value.Candidate != selection.Candidate || source.Value.Candidate.CompilerVersion != CompilerVersion {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		target, observation, expires, err := s.loadOperational(ctx, db, selection)
		if err != nil {
			return nil, err
		}
		now := s.base.clock.Now().UTC()
		verdict, digest, err := admitOperationalCheck(selection, observation, now)
		if err != nil {
			return nil, err
		}
		capture := &model.ProofOperationalCapture{SourceReceiptID: sourceReceipt, PolicyVersion: OperationalPolicyVersion, Selection: selection, RecordDigest: digest, ExpiresAt: expires}
		key, err := digestJSON(capture)
		if err != nil {
			return nil, err
		}
		receiptID, err := slug.WithRandomSuffix("proof", 24)
		if err != nil {
			return nil, err
		}
		draft := model.ProofReceipt{
			SchemaVersion: model.ProofReceiptSchemaVersion, ID: receiptID, Candidate: selection.Candidate, Operational: capture,
			CreatedAt: now, RetainUntil: now.Add(retentionPeriod), Claims: []model.ProofClaim{operationalClaim(selection, observation, verdict, digest)},
		}
		secrets, err := s.base.secrets.ResolveRuntimeSecrets(ctx, scope.Organization, scope.Environment)
		if err != nil {
			return nil, err
		}
		receipt, err := model.NewProofReceipt(draft, secretRedactor(secrets))
		if err != nil {
			return nil, err
		}
		// A source identifier is a retained run, not a second copy of its timeline.
		evidence, err := s.base.repo.CreateEvidence(ctx, db, target, proofreceipts.EvidenceParams{
			Kind: model.ProofEvidenceReferenceKindExecution.String(), Digest: key,
			SourceIdentifier: selection.Run.String(), BindingFingerprint: selection.Candidate.BindingFingerprint, CapabilityFingerprint: selection.Candidate.CapabilityFingerprint,
			ObservedAt: now, ExpiresAt: &expires,
		})
		if err != nil {
			return nil, err
		}
		stored, err := s.base.repo.Create(ctx, db, target, proofreceipts.CreateReceiptParams{EvidenceID: evidence.ID, ReceiptKey: "operational:" + key, Receipt: receipt})
		if err != nil {
			return nil, err
		}
		_, err = s.events.Record(ctx, scope.Organization, model.UserEventActor(actor), observationAudit{scope: scope, action: model.EventActionCreate, accepted: true, receiptSlug: stored.Value.ID, policyVersion: OperationalPolicyVersion})
		if err != nil {
			return nil, ErrObservationAuditUnavailable
		}
		return stored, nil
	})
}

func operationalClaim(selection OperationalSelection, observation *OperationalObservation, verdict model.ProofVerdict, digest string) model.ProofClaim {
	claim := model.ProofClaim{
		Identity:    model.ProofClaimIdentity{Family: model.ProofClaimFamilyFieldsTypes, PipelineVersionHash: selection.Candidate.SpecHash, SemanticPath: "/operational/checks/" + selection.CheckID},
		Description: "Structural conformance of the selected observed batch", RequiredEvidenceLevel: model.ProofEvidenceLevelOperational, Verdict: verdict,
		Evidence: []model.ProofEvidenceReference{}, Diagnostics: []model.ProofDiagnostic{{Code: "proof.operational_scope", Summary: "This check does not evaluate quality, freshness, whole-table completeness or delivery", Remediation: "Inspect the selected run and check scope independently", Retryable: false}},
	}
	if digest != "" {
		claim.Evidence = append(claim.Evidence, model.ProofEvidenceReference{Kind: model.ProofEvidenceReferenceKindExecution, Digest: digest})
	}
	if observation != nil && selection.CompletedAt != nil && observation.Evidence != nil && (verdict == model.ProofVerdictPassed || verdict == model.ProofVerdictFailed || verdict == model.ProofVerdictStale) {
		level := model.ProofEvidenceLevelOperational
		claim.ActualEvidenceLevel, claim.ObservedAt, claim.ExpiresAt = &level, selection.CompletedAt, &observation.ExpiresAt
	}
	return claim
}

func (s *OperationalCompiler) loadOperational(ctx context.Context, db postgres.DBTX, expected OperationalSelection) (*proofreceipts.CandidateTarget, *OperationalObservation, time.Time, error) {
	candidate := expected.Candidate
	target, err := s.base.repo.FindCandidateTarget(ctx, db, candidate.OrganizationSlug, expected.CandidateSlug)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	pipeline, err := pipelinevalidate.Load(target.PipelinePath, target.CanonicalSpec)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	resolved, err := s.base.bindings.ResolvePipelineDataSourceRefs(ctx, candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, pipeline)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if resolved == nil {
		return nil, nil, time.Time{}, proofevidence.ErrAuthorizationDenied
	}
	bindings, err := proofidentity.BindingFingerprint(resolved.Resolutions)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	capabilities, err := proofidentity.CapabilityFingerprint(pipeline, resolved.Resolutions)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if receiptCandidate(target, bindings, capabilities, candidate.CompilerVersion, candidate.PolicyVersion) != candidate {
		return nil, nil, time.Time{}, proofevidence.ErrAuthorizationDenied
	}
	run, err := s.runs.FindOperationalRun(ctx, db, candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, expected.Run)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	resolution, err := deploymentplan.Read(run.Resolution, run.ResolutionDigest)
	if err != nil {
		return nil, nil, time.Time{}, proofevidence.ErrAuthorizationDenied
	}
	for _, selected := range resolution.Targets {
		matched := false
		for _, binding := range resolved.Resolutions {
			if binding.PipelineSlug == run.Pipeline && binding.RefSlug == selected.Reference && binding.DataSourceSlug == selected.DataSource {
				matched = true
				break
			}
		}
		if !matched {
			return nil, nil, time.Time{}, proofevidence.ErrAuthorizationDenied
		}
	}
	observation, expires, err := operationalRunObservation(expected, run)
	return target, observation, expires, err
}

func operationalRunObservation(expected OperationalSelection, run *proofreceipts.OperationalRun) (*OperationalObservation, time.Time, error) {
	denied := func() (*OperationalObservation, time.Time, error) {
		return nil, time.Time{}, proofevidence.ErrAuthorizationDenied
	}
	if run == nil || run.EndedAt == nil || run.Pipeline != expected.Pipeline || run.Run != expected.Run || run.Attempt != expected.Attempt || run.VersionCommit != expected.VersionCommit || "sha256:"+run.SpecHash != expected.Candidate.SpecHash || run.ResolutionDigest != expected.DeploymentResolutionDigest {
		return denied()
	}
	if run.Status != "succeeded" && run.Status != "failed" && run.Status != "cancelled" {
		return denied()
	}
	resolution, err := deploymentplan.Read(run.Resolution, run.ResolutionDigest)
	if err != nil {
		return denied()
	}
	candidate := expected.Candidate
	if resolution.Organization != candidate.OrganizationSlug || resolution.Project != candidate.ProjectSlug || resolution.Environment != candidate.EnvironmentSlug || resolution.Pipeline.Metadata.Slug != run.Pipeline || resolution.CompiledDigest != expected.CompiledPlanDigest {
		return denied()
	}
	plan, err := contractplan.Read(resolution.CompiledPlan, resolution.CompiledDigest)
	if err != nil || plan.Source.Commit != candidate.HeadSHA || plan.Source.Path != candidate.PipelinePath || "sha256:"+plan.AuthoredSpecHash != candidate.SpecHash {
		return denied()
	}
	policy, err := retentionsvc.PolicyFromSettings(run.Settings)
	if err != nil {
		return nil, time.Time{}, err
	}
	expires := run.EndedAt.Add(time.Duration(policy.RunHistoryRetentionDays) * 24 * time.Hour)
	var timeline pipelinerun.RunTimeline
	if json.Unmarshal(run.Timeline, &timeline) != nil || timeline.RunSlug != run.Run.String() || timeline.PipelineSlug != run.Pipeline || timeline.ProjectSlug != candidate.ProjectSlug || timeline.EnvironmentSlug != candidate.EnvironmentSlug || timeline.DeploymentResolutionDigest != run.ResolutionDigest || timeline.Status != run.Status {
		return denied()
	}
	var found *pipelinerun.StructuralCheck
	for i := range timeline.StructuralChecks {
		check := &timeline.StructuralChecks[i]
		if check.ID == expected.CheckID {
			if found != nil {
				return denied()
			}
			found = check
		}
	}
	if found == nil {
		return nil, time.Time{}, proofreceipts.ErrReceiptNotFound
	}
	check := found
	selection := OperationalSelection{
		CandidateSlug: expected.CandidateSlug, Candidate: candidate, Pipeline: run.Pipeline, VersionCommit: run.VersionCommit,
		CompiledPlanDigest: check.CompiledPlanDigest, DeploymentResolutionDigest: check.DeploymentResolutionDigest, Run: run.Run, Attempt: check.AttemptNumber,
		CheckID: check.ID, Step: check.Step, Input: check.Input, Origin: check.Origin, Phase: check.Phase, Declaration: check.Declaration, ProjectionDigest: check.ProjectionDigest, Target: check.Target, Window: check.Window,
		Evaluator: check.Evaluator, EvaluatorPolicy: check.Policy, StartedAt: check.StartedAt, CompletedAt: check.CompletedAt, EvidenceDigest: check.EvidenceDigest,
	}
	// Stored terminal checks are authoritative, but cannot select another plan.
	if selection.CompiledPlanDigest != resolution.CompiledDigest || selection.DeploymentResolutionDigest != run.ResolutionDigest || selection.Attempt != run.Attempt {
		return denied()
	}
	return &OperationalObservation{Selection: selection, Status: check.Status, ReasonCode: check.ReasonCode, ExpiresAt: expires, Evidence: check.Evidence, ReusedFromRun: check.ReusedFromRun}, expires, nil
}

func (s *OperationalCompiler) Get(ctx context.Context, org, project, environment, receipt slug.Slug) (*ReceiptDetail, error) {
	detail, err := s.Service.Get(ctx, org, project, environment, receipt)
	if err != nil {
		return nil, err
	}
	if err := s.projectOperational(ctx, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *OperationalCompiler) List(ctx context.Context, org, project, environment slug.Slug, req model.ListRequest) (model.ListResponse[*ReceiptDetail], error) {
	response, err := s.Service.List(ctx, org, project, environment, req)
	if err != nil {
		return model.ListResponse[*ReceiptDetail]{}, err
	}
	for _, detail := range response.Items {
		if err := s.projectOperational(ctx, detail); err != nil {
			return model.ListResponse[*ReceiptDetail]{}, err
		}
	}
	return response, nil
}

func (s *OperationalCompiler) projectOperational(ctx context.Context, detail *ReceiptDetail) error {
	detail.OperationalAdmissionEnabled = new(true)
	capture := detail.Receipt.Operational
	if capture == nil {
		return nil
	}
	detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusAvailable, detail.Receipt.AggregateVerdict
	if capture.PolicyVersion != OperationalPolicyVersion {
		detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusChanged, model.ProofVerdictUnverified
		return nil
	}
	return s.base.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		candidate := capture.Selection.Candidate
		run, err := s.runs.FindOperationalRun(ctx, db, candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, capture.Selection.Run)
		var observation *OperationalObservation
		var expires time.Time
		if err == nil {
			observation, expires, err = operationalRunObservation(capture.Selection, run)
		}
		if xerror.Is(err, proofreceipts.ErrReceiptNotFound) || xerror.Is(err, proofreceipts.ErrCandidateNotFound) {
			detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusMissing, model.ProofVerdictUnverified
			return nil
		}
		if err != nil {
			if typed := new(xerror.Error); xerror.As(err, &typed) && (typed.Kind() == xerror.KindForbidden || typed.Kind() == xerror.KindNotFound || typed.Kind() == xerror.KindInvalidInput) {
				detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusChanged, model.ProofVerdictUnverified
				return nil
			}
			return err
		}
		if observation == nil {
			detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusMissing, model.ProofVerdictUnverified
			return nil
		}
		if expires.Before(capture.ExpiresAt) && !expires.After(s.base.clock.Now()) || !capture.ExpiresAt.After(s.base.clock.Now()) {
			detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusExpired, model.ProofVerdictStale
			return nil
		}
		observation.ExpiresAt = capture.ExpiresAt
		_, digest, err := admitOperationalCheck(capture.Selection, observation, s.base.clock.Now())
		if err != nil || digest != capture.RecordDigest {
			detail.OperationalEvidenceStatus, detail.OperationalCurrentVerdict = model.ProofOperationalEvidenceStatusChanged, model.ProofVerdictInconclusive
		}
		return nil
	})
}
