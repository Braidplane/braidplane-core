package proofevidencesvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
)

// ReceiptObservation stays inside the authorized compiler. Its selector and
// snapshot must never be serialized into a public receipt or job.
type ReceiptObservation struct {
	DispatchedAt time.Time                  `json:"-"`
	Request      proofevidence.Request      `json:"-"`
	Target       proofevidence.MetadataPlan `json:"-"`
	Observation  Observation                `json:"-"`
}

func (s *Ledger) ReadForReceipt(ctx context.Context, scope proofobservations.Scope, jobSlug slug.Slug) (*ReceiptObservation, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*ReceiptObservation, error) {
		job, err := s.repo.FindScoped(ctx, db, scope, jobSlug)
		if err != nil {
			return nil, err
		}
		requestDigest, err := job.Request.Digest()
		r := job.Request
		if err != nil || requestDigest != job.RequestDigest || r.JobID != jobSlug || r.OrganizationSlug != scope.Organization || r.ProjectSlug != scope.Project || r.EnvironmentSlug != scope.Environment || job.State != proofobservations.StateFinished || !r.MetadataRetainUntil.After(time.Now()) || job.Authorization.RequestDigest != requestDigest || job.Authorization.PolicyVersion != r.PolicyVersion || job.Authorization.Capability != proofevidence.Capability {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		if job.Result.Outcome != proofevidence.OutcomeComplete {
			if !job.Result.Outcome.IsAOutcome() || job.Result.Outcome == proofevidence.OutcomeUnknown || len(job.SnapshotCiphertext) != 0 ||
				(job.Result.RequestDigest != requestDigest && (job.Result.RequestDigest != "" || (job.Result.Outcome != proofevidence.OutcomeCanceled && job.Result.Outcome != proofevidence.OutcomeWorkerLost))) {
				return nil, proofevidence.ErrAuthorizationDenied
			}
			// A terminal failure proves no target fact. Ignore worker-supplied evidence,
			// timestamps and usage; neither decrypt payloads nor require live authority.
			return &ReceiptObservation{Request: r, Observation: Observation{Result: proofevidence.Result{RequestDigest: requestDigest, Outcome: job.Result.Outcome}}}, nil
		}
		observation, err := s.ReadSnapshot(ctx, scope.Organization, jobSlug)
		if err != nil {
			return nil, err
		}
		plaintext, err := s.evidenceCipher.Decrypt(job.TargetCiphertext)
		if err != nil {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		defer clear(plaintext)
		var target protectedTarget
		if json.Unmarshal(plaintext, &target) != nil || target.RequestDigest != job.RequestDigest || !matchesRequest(target.Plan, job.Request) {
			return nil, proofevidence.ErrAuthorizationDenied
		}
		return &ReceiptObservation{DispatchedAt: job.StartedAt, Request: job.Request, Target: target.Plan, Observation: observation}, nil
	})
}

func (s *Ledger) ReadForReceiptDigest(ctx context.Context, scope proofobservations.Scope, digest string) (*ReceiptObservation, error) {
	job, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (slug.Slug, error) {
		return s.repo.FindSlugByDigest(ctx, db, scope.Organization, digest)
	})
	if err != nil {
		return nil, err
	}
	return s.ReadForReceipt(ctx, scope, job)
}
