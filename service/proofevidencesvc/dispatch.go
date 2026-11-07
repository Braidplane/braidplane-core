package proofevidencesvc

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
)

// BeginExecution loads only a reserved target and atomically spends its single
// dispatch. A backend crash after this point cannot authorize another attempt.
func (s *Ledger) BeginExecution(ctx context.Context, org, jobSlug slug.Slug) (ExecutionRequest, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (ExecutionRequest, error) {
		stored, err := s.repo.Find(ctx, db, org, jobSlug)
		if err != nil {
			return ExecutionRequest{}, err
		}
		policy, job, err := s.lockRequest(ctx, db, stored.Request)
		if err != nil {
			return ExecutionRequest{}, err
		}
		if policy.WorkerIdentity != ManagedExecutorIdentity || job.Request.Worker.Identity != policy.WorkerIdentity {
			return ExecutionRequest{}, proofevidence.ErrAuthorizationDenied
		}
		if _, err := policy.Value.Resume(job.Request, job.Authorization, job.Deadline); err != nil {
			return ExecutionRequest{}, err
		}
		plaintext, err := s.evidenceCipher.Decrypt(job.TargetCiphertext)
		if err != nil {
			return ExecutionRequest{}, proofevidence.ErrAuthorizationDenied
		}
		defer clear(plaintext)
		var target protectedTarget
		if json.Unmarshal(plaintext, &target) != nil || target.RequestDigest != job.RequestDigest || !matchesRequest(target.Plan, job.Request) {
			return ExecutionRequest{}, proofevidence.ErrAuthorizationDenied
		}
		started, err := s.start(ctx, db, job)
		if err != nil {
			return ExecutionRequest{}, err
		}
		if !started {
			return ExecutionRequest{}, proofevidence.ErrAuthorizationDenied
		}
		active, err := s.repo.IsActive(ctx, db, job)
		if err != nil {
			return ExecutionRequest{}, err
		}
		if !active {
			return ExecutionRequest{}, proofevidence.ErrAuthorizationDenied
		}
		return ExecutionRequest{
			Request: job.Request, Authorization: job.Authorization, Policy: policy.Value,
			Deadline: job.Deadline, Plan: target.Plan,
		}, nil
	})
}

// CheckExecution is backend-only and returns no credential or evidence payload.
func (s *Ledger) CheckExecution(ctx context.Context, grant proofevidence.Grant) error {
	if err := grant.Check(); err != nil {
		return err
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		policy, job, err := s.lockRequest(ctx, db, grant.Request())
		if err != nil {
			return err
		}
		if policy.WorkerIdentity != job.Request.Worker.Identity || job.State != proofobservations.StateRunning {
			return proofevidence.ErrAuthorizationDenied
		}
		if _, err := policy.Value.Resume(job.Request, job.Authorization, job.Deadline); err != nil {
			return err
		}
		active, err := s.repo.IsActive(ctx, db, job)
		if err != nil {
			return err
		}
		if !active {
			return proofevidence.ErrAuthorizationDenied
		}
		return nil
	})
}
