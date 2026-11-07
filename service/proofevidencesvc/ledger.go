package proofevidencesvc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

var ErrAdmissionLimit = xerror.Unavailable(xerror.Pipeline).Detail("Proof observation admission limit reached")

// Ledger is backend orchestration, separate from the metadata executor. Its
// caller must prepare canonical candidate/binding/claim identities and perform
// environment-policy and user permission checks before issuing authorization.
type Ledger struct {
	txm            postgres.TransactionManager
	repo           *proofobservations.Repository
	evidenceCipher crypto.AES256
	audit          eventrecorder.Recorder
}

func NewLedger(txm postgres.TransactionManager, repo *proofobservations.Repository, evidenceCipher crypto.AES256, audit eventrecorder.Recorder) (*Ledger, error) {
	if txm == nil || repo == nil || evidenceCipher == nil || audit == nil {
		return nil, xerror.Unexpected().Detail("Proof ledger dependencies are required")
	}
	return &Ledger{txm: txm, repo: repo, evidenceCipher: evidenceCipher, audit: audit}, nil
}

// SetPolicy serializes policy replacement with reservations. A changed policy
// needs a new version; cancellation prevents an old version being resurrected.
func (s *Ledger) SetPolicy(ctx context.Context, scope proofobservations.Scope, policy proofevidence.Policy, workerIdentity string) error {
	if strings.TrimSpace(policy.Version) == "" || strings.TrimSpace(workerIdentity) == "" || policy.MaxAuthorizationTTL <= 0 || policy.MaxEvidenceRetention <= 0 || policy.MaxMetadataRetention < policy.MaxEvidenceRetention {
		return proofevidence.ErrAuthorizationDenied
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		stored, err := s.repo.SavePolicy(ctx, db, scope, policy, workerIdentity)
		if xerror.Is(err, proofobservations.ErrNotFound) {
			// A visible policy with no returned update means its version was reused.
			if _, err := s.repo.LockPolicy(ctx, db, scope); err != nil {
				return err
			}
			return proofevidence.ErrAuthorizationDenied
		}
		if err != nil {
			return err
		}
		for {
			references, err := s.repo.CancelObsolete(ctx, db, stored.OrganizationID, stored.ProjectEnvironmentID)
			if err != nil {
				return err
			}
			for _, reference := range references {
				if _, err := s.audit.Record(ctx, scope.Organization, model.SystemEventActor(), JobAudit{Scope: scope, Reference: reference, Action: model.EventActionRevoke}); err != nil {
					return xerror.Unavailable(xerror.Event).Detail("Proof revocation audit unavailable")
				}
			}
			if len(references) == 0 {
				return nil
			}
		}
	})
}

type protectedTarget struct {
	RequestDigest string                     `json:"request_digest"`
	Plan          proofevidence.MetadataPlan `json:"plan"`
}

type protectedSnapshot struct {
	RequestDigest string   `json:"request_digest"`
	Snapshot      Snapshot `json:"snapshot"`
}

// Reserve commits worst-case usage before dispatch. The reservation survives
// cancellation and host loss until the deadline plus the entire rate window.
// The per-operation policy limits are shared by all jobs in this environment.
func (s *Ledger) Reserve(ctx context.Context, request proofevidence.Request, authorization proofevidence.Authorization, target proofevidence.MetadataObserver) (*proofobservations.Job, error) {
	if target == nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	plan, err := target.Plan()
	if err != nil || !matchesRequest(plan, request) {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofobservations.Job, error) {
		policy, err := s.repo.LockPolicy(ctx, db, proofobservations.Scope{Organization: request.OrganizationSlug, Project: request.ProjectSlug, Environment: request.EnvironmentSlug})
		if err != nil {
			return nil, err
		}
		grant, err := policy.Value.Authorize(request, authorization)
		if err != nil {
			return nil, err
		}
		if request.Worker.Identity != policy.WorkerIdentity {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		requestDigest, err := request.Digest()
		if err != nil {
			return nil, err
		}
		existing, err := s.repo.Find(ctx, db, request.OrganizationSlug, request.JobID)
		if err == nil {
			if existing.RequestDigest != requestDigest {
				return nil, proofevidence.ErrAuthorizationDenied
			}
			return existing, nil
		}
		if !xerror.Is(err, proofobservations.ErrNotFound) {
			return nil, err
		}
		usage, err := s.repo.Budget(ctx, db, policy, request)
		if err != nil {
			return nil, err
		}
		var maximum proofevidence.Limits
		for _, operation := range policy.Value.Operations {
			if operation.ConnectorType == request.ConnectorType && operation.Operation == request.Operation && operation.EvidenceLevel == request.EvidenceLevel {
				maximum = operation.Limits
				break
			}
		}
		if usage.ActiveJobs >= maximum.MaxConcurrency ||
			request.Limits.MaxRequests > maximum.MaxRequests-usage.Requests || request.Limits.MaxRows > maximum.MaxRows-usage.Rows ||
			request.Limits.MaxBytes > maximum.MaxBytes-usage.Bytes || request.Limits.MaxCostUnits > maximum.MaxCostUnits-usage.Cost {
			return nil, ErrAdmissionLimit
		}
		raw, err := json.Marshal(protectedTarget{RequestDigest: requestDigest, Plan: plan})
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		ciphertext, err := s.evidenceCipher.Encrypt(raw)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return s.repo.Create(ctx, db, policy, request, authorization, ciphertext, grant.Deadline())
	})
}

// Start marks the backend's reserved observation as dispatched. Delivery and
// executor leases belong to the existing execution broker, not this ledger.
func (s *Ledger) Start(ctx context.Context, grant proofevidence.Grant) error {
	if err := grant.Check(); err != nil {
		return err
	}
	started, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
		policy, job, err := s.lockRequest(ctx, db, grant.Request())
		if err != nil {
			return false, err
		}
		if _, err := policy.Value.Authorize(job.Request, job.Authorization); err != nil {
			return false, err
		}
		if policy.WorkerIdentity != job.Request.Worker.Identity {
			return false, proofevidence.ErrAuthorizationDenied
		}
		return s.start(ctx, db, job)
	})
	if err != nil {
		return err
	}
	if !started {
		return proofevidence.ErrAuthorizationDenied
	}
	return nil
}

func (s *Ledger) start(ctx context.Context, db postgres.DBTX, job *proofobservations.Job) (bool, error) {
	started, err := s.repo.Start(ctx, db, job)
	if err != nil || !started {
		return started, err
	}
	return true, s.auditExecution(ctx, job, model.EventActionExecute, proofevidence.OutcomeUnknown)
}

func (s *Ledger) Cancel(ctx context.Context, org, job slug.Slug) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		stored, err := s.repo.Find(ctx, db, org, job)
		if err != nil {
			return err
		}
		if _, err := s.repo.LockPolicyForCleanup(ctx, db, proofobservations.Scope{Organization: org, Project: stored.Request.ProjectSlug, Environment: stored.Request.EnvironmentSlug}); err != nil {
			return err
		}
		return s.repo.Cancel(ctx, db, org, job)
	})
}

// lockRequest serializes lifecycle decisions with policy replacement and
// cleanup. Request identity is checked before any state transition.
func (s *Ledger) lockRequest(ctx context.Context, db postgres.DBTX, request proofevidence.Request) (*proofobservations.Policy, *proofobservations.Job, error) {
	policy, err := s.repo.LockPolicy(ctx, db, proofobservations.Scope{Organization: request.OrganizationSlug, Project: request.ProjectSlug, Environment: request.EnvironmentSlug})
	if err != nil {
		return nil, nil, err
	}
	job, err := s.repo.Find(ctx, db, request.OrganizationSlug, request.JobID)
	if err != nil {
		return nil, nil, err
	}
	requestDigest, err := request.Digest()
	if err != nil {
		return nil, nil, err
	}
	if job.RequestDigest != requestDigest {
		return nil, nil, proofevidence.ErrAuthorizationDenied
	}
	return policy, job, nil
}

// Record is a backend completion boundary. The execution broker must authenticate
// and fence executor delivery before returning an observation to this service.
// Go validates the result; SQL atomically rejects cancellation, expiry, or replay.
func (s *Ledger) Record(ctx context.Context, grant proofevidence.Grant, observation Observation) error {
	request := grant.Request()
	requestDigest, err := request.Digest()
	if err != nil {
		return err
	}
	if err := ValidateExecutionResult(grant, observation); err != nil {
		return err
	}
	var ciphertext []byte
	if observation.Result.Outcome == proofevidence.OutcomeComplete {
		protected, err := json.Marshal(protectedSnapshot{RequestDigest: requestDigest, Snapshot: *observation.Snapshot})
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		ciphertext, err = s.evidenceCipher.Encrypt(protected)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
	} else {
		observation.Result.EvidenceDigest = ""
		observation.Result.ObservedAt = time.Time{}
		observation.Result.ExpiresAt = time.Time{}
	}
	accepted, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
		policy, job, err := s.lockRequest(ctx, db, request)
		if err != nil {
			return false, err
		}
		interrupted, err := s.repo.FinishInterrupted(ctx, db, job)
		if err != nil {
			return false, err
		}
		if interrupted {
			finished, err := s.repo.Find(ctx, db, request.OrganizationSlug, job.Slug)
			if err != nil {
				return false, err
			}
			return false, s.auditExecution(ctx, finished, model.EventActionAcknowledge, finished.Result.Outcome)
		}
		if !policy.Value.Enabled || policy.Value.Version != request.PolicyVersion || policy.WorkerIdentity != request.Worker.Identity {
			return false, proofevidence.ErrAuthorizationDenied
		}
		if observation.Result.Outcome == proofevidence.OutcomeComplete && job.StartedAt.IsZero() {
			return false, proofevidence.ErrAuthorizationDenied
		}
		// The SQL state transition proves dispatch ordering; executor timestamps do not.
		finished, err := s.repo.Finish(ctx, db, job, observation.Result, ciphertext)
		if err != nil || !finished {
			return finished, err
		}
		return true, s.auditExecution(ctx, job, model.EventActionAcknowledge, observation.Result.Outcome)
	})
	if err != nil {
		return err
	}
	if !accepted {
		return proofevidence.ErrAuthorizationDenied
	}
	return nil
}

// ReadSnapshot is for the protected receipt compiler, not a public receipt
// response. Expiry, policy replacement, or ciphertext substitution is non-pass.
func (s *Ledger) ReadSnapshot(ctx context.Context, org, jobSlug slug.Slug) (Observation, error) {
	job, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofobservations.Job, error) {
		return s.repo.Find(ctx, db, org, jobSlug)
	})
	if err != nil {
		return Observation{}, err
	}
	if job.State != proofobservations.StateFinished || job.Result.Outcome != proofevidence.OutcomeComplete || job.Result.RequestDigest != job.RequestDigest || job.Result.EvidenceLevel != job.Request.EvidenceLevel || !job.Result.ExpiresAt.After(time.Now()) || len(job.SnapshotCiphertext) == 0 {
		return Observation{}, proofevidence.ErrAuthorizationDenied
	}
	// Locking the policy makes this read linearizable with emergency disable.
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (Observation, error) {
		policy, err := s.repo.LockPolicy(ctx, db, proofobservations.Scope{Organization: org, Project: job.Request.ProjectSlug, Environment: job.Request.EnvironmentSlug})
		if err != nil {
			return Observation{}, err
		}
		if !policy.Value.Enabled || policy.Value.Version != job.Request.PolicyVersion || policy.WorkerIdentity != job.Request.Worker.Identity {
			return Observation{}, proofevidence.ErrAuthorizationDenied
		}
		plaintext, err := s.evidenceCipher.Decrypt(job.SnapshotCiphertext)
		if err != nil {
			return Observation{}, proofevidence.ErrAuthorizationDenied
		}
		defer clear(plaintext)
		var protected protectedSnapshot
		if json.Unmarshal(plaintext, &protected) != nil || protected.RequestDigest != job.RequestDigest {
			return Observation{}, proofevidence.ErrAuthorizationDenied
		}
		requestDigest, err := job.Request.Digest()
		if err != nil || requestDigest != job.RequestDigest {
			return Observation{}, proofevidence.ErrAuthorizationDenied
		}
		if !validSnapshot(&protected.Snapshot, job.Request, job.Result) || !job.Result.ExpiresAt.After(time.Now()) {
			return Observation{}, proofevidence.ErrAuthorizationDenied
		}
		return Observation{Result: job.Result, Snapshot: &protected.Snapshot}, nil
	})
}

// Connector observers own metadata validation and row counting. The ledger
// preserves their opaque payload and checks its admitted identity and integrity.
func validSnapshot(snapshot *Snapshot, request proofevidence.Request, result proofevidence.Result) bool {
	if snapshot.TargetFingerprint != request.TargetFingerprint || !json.Valid(snapshot.Metadata) || strings.TrimSpace(string(snapshot.Metadata)) == "null" ||
		result.Usage.Rows < 0 || result.Usage.Rows > request.Limits.MaxRows {
		return false
	}
	raw, err := json.Marshal(snapshot)
	return err == nil && int64(len(raw)) <= request.Limits.MaxBytes && digest(raw) == result.EvidenceDigest
}
