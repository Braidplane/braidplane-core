package proofreceiptsvc

import (
	"context"
	"reflect"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/proofidentity"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
)

// ObservationScope selects a tenant's project environment without a persistence capability.
type ObservationScope = proofobservations.Scope

// ObservationJob exposes only the public status of retained evidence.
type ObservationJob struct {
	Slug                  slug.Slug
	State                 string
	RequestDigest         string
	Deadline              time.Time
	CancellationRequested bool
	Outcome               *proofevidence.Outcome
}

func observationJob(job *proofobservations.Job, err error) (*ObservationJob, error) {
	if err != nil {
		return nil, err
	}
	out := &ObservationJob{Slug: job.Slug, State: job.State.String(), RequestDigest: job.RequestDigest, Deadline: job.Deadline, CancellationRequested: job.CancellationRequested}
	if job.State == proofobservations.StateFinished {
		if !job.Result.Outcome.IsAOutcome() || job.Result.Outcome == proofevidence.OutcomeUnknown {
			return nil, xerror.Unexpected().Detail("invalid Proof observation outcome")
		}
		outcome := job.Result.Outcome
		out.Outcome = &outcome
	}
	return out, nil
}

// ObservationPolicySettings are per-connector cumulative bounds in a rolling hour.
// Fixed metadata routes, zero retries/cost, and one concurrent job are not editable.
type ObservationPolicySettings struct {
	Enabled                            bool
	TimeoutSeconds, EvidenceTTLSeconds int
	MaxRequests, MaxRows, MaxBytes     int64
}

type ObservationPolicy struct {
	Version  string
	Settings ObservationPolicySettings
}

type ObservationPreview struct {
	IntentToken   string `json:"-"`
	RequestDigest string
	Request       proofevidence.Request      `json:"-"`
	Target        proofevidence.MetadataPlan `json:"-"`
	ExpiresAt     time.Time
}

func (s *LiveCompiler) SetObservationPolicy(ctx context.Context, scope ObservationScope, actor slug.Slug, settings ObservationPolicySettings) (out *ObservationPolicy, retErr error) {
	defer func() { recordObservationControl(model.EventActionUpdate, retErr) }()
	if settings.TimeoutSeconds < 1 || settings.TimeoutSeconds > 30 || settings.EvidenceTTLSeconds < 60 || settings.EvidenceTTLSeconds > 3600 || settings.MaxRequests < 2 || settings.MaxRequests > 120 || settings.MaxRows < 1 || settings.MaxRows > 1000000 || settings.MaxBytes < 1024 || settings.MaxBytes > 64<<20 {
		return nil, xerror.ValidationFailed().Detail("invalid Proof observation limits")
	}
	version, err := slug.WithRandomSuffix("policy", 24)
	if err != nil {
		return nil, err
	}
	policy := observationPolicy(settings, version.String())
	return postgres.InTx(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*ObservationPolicy, error) {
		if err := s.ledger.SetPolicy(ctx, scope, policy, proofevidencesvc.ManagedExecutorIdentity); err != nil {
			return nil, err
		}
		if err := s.audit(ctx, actor, observationAudit{scope: scope, action: model.EventActionUpdate, accepted: true, policyVersion: policy.Version, enabled: &settings.Enabled}); err != nil {
			return nil, err
		}
		return &ObservationPolicy{Version: policy.Version, Settings: settings}, nil
	})
}

func observationPolicy(settings ObservationPolicySettings, version string) proofevidence.Policy {
	limits := proofevidence.Limits{Timeout: time.Duration(settings.TimeoutSeconds) * time.Second, MaxRows: settings.MaxRows, MaxBytes: settings.MaxBytes, MaxRequests: settings.MaxRequests, MaxConcurrency: 1, RateWindow: time.Hour}
	policy := proofevidence.Policy{Enabled: settings.Enabled, Version: version, MaxAuthorizationTTL: time.Minute, MaxEvidenceRetention: time.Duration(settings.EvidenceTTLSeconds) * time.Second, MaxMetadataRetention: 24 * time.Hour}
	policy.Operations = connector.MetadataPolicies(limits)
	return policy
}

func (s *LiveCompiler) GetObservationPolicy(ctx context.Context, scope ObservationScope, actor slug.Slug) (out *ObservationPolicy, retErr error) {
	defer func() { recordObservationControl(model.EventActionRead, retErr) }()
	policy, err := s.ledger.Policy(ctx, scope)
	if err != nil {
		return nil, err
	}
	view, err := s.policyView(policy)
	if err != nil {
		return nil, err
	}
	if err := s.audit(ctx, actor, observationAudit{scope: scope, action: model.EventActionRead, accepted: true}); err != nil {
		return nil, err
	}
	return view, nil
}

func (s *LiveCompiler) policyView(policy *proofobservations.Policy) (*ObservationPolicy, error) {
	if len(policy.Value.Operations) == 0 || policy.WorkerIdentity != proofevidencesvc.ManagedExecutorIdentity {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	limits := policy.Value.Operations[0].Limits
	view := &ObservationPolicy{Version: policy.Value.Version, Settings: ObservationPolicySettings{Enabled: policy.Value.Enabled, TimeoutSeconds: int(limits.Timeout / time.Second), EvidenceTTLSeconds: int(policy.Value.MaxEvidenceRetention / time.Second), MaxRequests: limits.MaxRequests, MaxRows: limits.MaxRows, MaxBytes: limits.MaxBytes}}
	expected := observationPolicy(view.Settings, view.Version)
	if !reflect.DeepEqual(policy.Value, expected) {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return view, nil
}

// PrepareObservation resolves a reviewable intent without reserving a job,
// decrypting a credential, or contacting a provider. HTTP owns user permissions.
func (s *LiveCompiler) PrepareObservation(ctx context.Context, scope ObservationScope, actor, candidateSlug, stepSlug slug.Slug, target proofevidence.MetadataPlan) (out *ObservationPreview, retErr error) {
	defer func() { recordObservationControl(model.EventActionCreate, retErr) }()
	preview, err := postgres.InTx(ctx, s.base.txm, func(ctx context.Context, _ postgres.DBTX) (*ObservationPreview, error) {
		policy, err := s.ledger.Policy(ctx, scope)
		if err != nil {
			return nil, err
		}
		if _, err := s.policyView(policy); err != nil {
			return nil, err
		}
		identity, pipeline, resolutions, err := s.observationCandidate(ctx, scope, candidateSlug)
		if err != nil {
			return nil, err
		}
		var claimPath string
		for _, step := range pipeline.Steps {
			if step.Slug == stepSlug {
				_, _, _, _, claimPath = selectedStep(step, target.ConnectorType)
			}
		}
		if claimPath == "" {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		source, err := s.selectedSource(ctx, identity, pipeline, resolutions, claimPath, target)
		if err != nil || source.Credential == nil {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		target, err = connector.BindMetadataPlan(target, source.Config, source.Credential.Kind)
		if err != nil {
			return nil, err
		}
		return s.sealObservationPreview(policy, scope, actor, candidateSlug, identity, claimPath, source, target)
	})
	if err != nil {
		return nil, s.auditRejectedControl(ctx, scope, actor, model.EventActionCreate, err)
	}
	if err := s.audit(ctx, actor, observationAudit{scope: scope, action: model.EventActionCreate, accepted: true}); err != nil {
		return nil, err
	}
	return preview, nil
}

// Construct the reviewed grant without storage, credential decryption, or provider access.
func (s *LiveCompiler) sealObservationPreview(policy *proofobservations.Policy, scope ObservationScope, actor, candidateSlug slug.Slug, identity model.ProofCandidateIdentity, claimPath string, source *datasources.DataSource, target proofevidence.MetadataPlan) (*ObservationPreview, error) {
	now := time.Now().UTC()
	job, err := slug.WithRandomSuffix("observation", 24)
	if err != nil {
		return nil, err
	}
	fingerprint, err := target.Fingerprint()
	if err != nil {
		return nil, err
	}
	operation := target.Operation
	var limits proofevidence.Limits
	for _, allowed := range policy.Value.Operations {
		if allowed.Operation == operation && allowed.ConnectorType == target.ConnectorType {
			limits = allowed.Limits
		}
	}
	limits.Timeout = min(limits.Timeout, 30*time.Second)
	limits.MaxRows = min(limits.MaxRows, 1000)
	limits.MaxBytes = min(limits.MaxBytes, 1<<20)
	limits.MaxRequests, limits.MaxConcurrency, limits.MaxRetries, limits.MaxCostUnits = int64(len(target.Routes)), 1, 0, 0
	request := proofevidence.Request{JobID: job, OrganizationSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, CandidateSlug: candidateSlug, Candidate: identity, Claim: model.ProofClaimIdentity{Family: model.ProofClaimFamilyAccessCapability, PipelineVersionHash: identity.SpecHash, SemanticPath: claimPath}, DataSourceSlug: source.Slug, CredentialDataSourceSlug: source.Slug, CredentialVersion: source.Credential.Slug.String() + "/" + source.Credential.UpdatedAt.UTC().Format(time.RFC3339Nano), TargetFingerprint: fingerprint, ConnectorType: source.Type, Operation: operation, EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly, Target: target.Target, Egress: target.Egress, Limits: limits, Worker: proofevidence.Worker{Identity: proofevidencesvc.ManagedExecutorIdentity, Trust: proofevidence.RunnerTrustManaged}, PolicyVersion: policy.Value.Version, EvidenceRetainUntil: now.Add(min(policy.Value.MaxEvidenceRetention, time.Hour)), MetadataRetainUntil: now.Add(min(policy.Value.MaxMetadataRetention, 24*time.Hour))}
	if !credentialMatches(source, request, now.Add(limits.Timeout)) {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	digest, err := request.Digest()
	if err != nil {
		return nil, err
	}
	expires := now.Add(min(policy.Value.MaxAuthorizationTTL, time.Minute))
	authorization := proofevidence.Authorization{ID: job, UserSlug: actor, Capability: proofevidence.Capability, RequestDigest: digest, PolicyVersion: request.PolicyVersion, AuthorizedAt: now, ExpiresAt: expires}
	if _, err := policy.Value.Authorize(request, authorization); err != nil {
		return nil, err
	}
	token, err := s.ledger.SealIntent(actor, request, target, expires)
	if err != nil {
		return nil, err
	}
	return &ObservationPreview{IntentToken: token, RequestDigest: digest, Request: request, Target: target, ExpiresAt: expires}, nil
}

func (s *LiveCompiler) AuthorizeObservation(ctx context.Context, scope ObservationScope, actor slug.Slug, token, digest string) (out *ObservationJob, retErr error) {
	defer func() { recordObservationControl(model.EventActionAccept, retErr) }()
	intent, err := s.ledger.OpenIntent(scope, actor, token, digest)
	if err != nil {
		return nil, s.auditRejectedControl(ctx, scope, actor, model.EventActionAccept, err)
	}
	job, err := postgres.InTx(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*proofobservations.Job, error) {
		request := intent.Request
		identity, pipeline, resolutions, err := s.observationCandidate(ctx, scope, request.CandidateSlug)
		if err != nil || identity != request.Candidate {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		source, err := s.selectedSource(ctx, identity, pipeline, resolutions, request.Claim.SemanticPath, intent.Target)
		now := time.Now().UTC()
		if err != nil || !credentialMatches(source, request, now.Add(request.Limits.Timeout)) || !boundTargetMatches(source, intent.Target) {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		authorization := proofevidence.Authorization{ID: request.JobID, UserSlug: actor, Capability: proofevidence.Capability, RequestDigest: digest, PolicyVersion: request.PolicyVersion, AuthorizedAt: now, ExpiresAt: intent.ExpiresAt}
		observer, err := connector.NewMetadataObserver(intent.Target)
		if err != nil {
			return nil, err
		}
		job, err := s.ledger.Reserve(ctx, request, authorization, observer)
		if err != nil {
			return nil, err
		}
		if err := s.audit(ctx, actor, observationAudit{scope: scope, action: model.EventActionAccept, accepted: true, job: job}); err != nil {
			return nil, err
		}
		return job, nil
	})
	if err != nil {
		return nil, s.auditRejectedControl(ctx, scope, actor, model.EventActionAccept, err)
	}
	if job.State == proofobservations.StatePending {
		if _, err := s.coordinator.Run(ctx, scope.Organization, job.Slug); err != nil {
			// Another confirmation may already own the same reservation.
			current, readErr := s.ledger.Job(ctx, scope, job.Slug)
			if readErr != nil || !xerror.Is(err, proofevidence.ErrAuthorizationDenied) || current.State == proofobservations.StatePending {
				return nil, err
			}
			return observationJob(current, nil)
		}
		return observationJob(s.ledger.Job(ctx, scope, job.Slug))
	}
	return observationJob(job, nil)
}

func (s *LiveCompiler) observationCandidate(ctx context.Context, scope ObservationScope, candidateSlug slug.Slug) (model.ProofCandidateIdentity, *pipelinespec.Pipeline, []model.ResolvedPipelineDataSourceRef, error) {
	target, err := postgres.InRead(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*proofreceipts.CandidateTarget, error) {
		return s.base.repo.FindCandidateTarget(ctx, db, scope.Organization, candidateSlug)
	})
	if err != nil {
		return model.ProofCandidateIdentity{}, nil, nil, err
	}
	if target.ProjectSlug != scope.Project || target.EnvironmentSlug != scope.Environment {
		return model.ProofCandidateIdentity{}, nil, nil, proofevidence.ErrAuthorizationDenied
	}
	pipeline, err := pipelinevalidate.Load(target.PipelinePath, target.CanonicalSpec)
	if err != nil {
		return model.ProofCandidateIdentity{}, nil, nil, proofevidence.ErrAuthorizationDenied
	}
	resolved, err := s.base.bindings.ResolvePipelineDataSourceRefs(ctx, scope.Organization, scope.Project, scope.Environment, pipeline)
	if err != nil || resolved == nil {
		return model.ProofCandidateIdentity{}, nil, nil, proofevidence.ErrAuthorizationDenied
	}
	bindings, err := proofidentity.BindingFingerprint(resolved.Resolutions)
	if err != nil {
		return model.ProofCandidateIdentity{}, nil, nil, err
	}
	capabilities, err := proofidentity.CapabilityFingerprint(pipeline, resolved.Resolutions)
	if err != nil {
		return model.ProofCandidateIdentity{}, nil, nil, err
	}
	return receiptCandidate(target, bindings, capabilities, LiveCompilerVersion, LivePolicyVersion), pipeline, resolved.Resolutions, nil
}

func (s *LiveCompiler) GetObservation(ctx context.Context, scope ObservationScope, actor, job slug.Slug) (out *ObservationJob, retErr error) {
	defer func() { recordObservationControl(model.EventActionRead, retErr) }()
	stored, err := s.ledger.Job(ctx, scope, job)
	if err != nil {
		return nil, err
	}
	if err := s.audit(ctx, actor, observationAudit{scope: scope, action: model.EventActionRead, accepted: true, job: stored}); err != nil {
		return nil, err
	}
	return observationJob(stored, nil)
}

func (s *LiveCompiler) CancelObservation(ctx context.Context, scope ObservationScope, actor, jobSlug slug.Slug) (out *ObservationJob, retErr error) {
	defer func() { recordObservationControl(model.EventActionRevoke, retErr) }()
	job, err := postgres.InTx(ctx, s.base.txm, func(ctx context.Context, db postgres.DBTX) (*proofobservations.Job, error) {
		_, err := s.ledger.Job(ctx, scope, jobSlug)
		if err != nil {
			return nil, err
		}
		if err := s.ledger.Cancel(ctx, scope.Organization, jobSlug); err != nil {
			return nil, err
		}
		job, err := s.ledger.Job(ctx, scope, jobSlug)
		if err != nil {
			return nil, err
		}
		if err := s.audit(ctx, actor, observationAudit{scope: scope, action: model.EventActionRevoke, accepted: true, job: job}); err != nil {
			return nil, err
		}
		return job, nil
	})
	return observationJob(job, err)
}
