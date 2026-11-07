package proofevidencesvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type Executor interface {
	ObserveProof(context.Context, ExecutionRequest) (Observation, error)
}

type ExecutionCredentials interface {
	credentialssvc.ProofResolver
	CheckProofCredential(context.Context, proofevidence.Grant) error
}

// Coordinator belongs to the backend. Executors never receive this ledger or
// credential-store access; they receive one encrypted, bounded operation.
type Coordinator struct {
	ledger      *Ledger
	credentials ExecutionCredentials
	executor    Executor
}

func NewCoordinator(ledger *Ledger, credentials ExecutionCredentials, executor Executor) (*Coordinator, error) {
	if ledger == nil || credentials == nil || executor == nil {
		return nil, xerror.Unexpected().Detail("Proof execution dependencies are required")
	}
	return &Coordinator{ledger: ledger, credentials: credentials, executor: executor}, nil
}

// Run consumes a durable reservation once. User intent, candidate/binding
// resolution, and permission checks precede Reserve; this API grants none.
func (s *Coordinator) Run(ctx context.Context, org, job slug.Slug) (proofevidence.Result, error) {
	request, err := s.ledger.BeginExecution(ctx, org, job)
	if err != nil {
		return proofevidence.Result{}, err
	}
	grant, err := request.Policy.Resume(request.Request, request.Authorization, request.Deadline)
	if err != nil {
		return proofevidence.Result{}, err
	}
	ctx, cancel := context.WithDeadline(postgres.WithoutTransaction(ctx), grant.Deadline())
	defer cancel()
	request.Credential, err = s.credentials.ResolveProofCredential(ctx, grant)
	defer clear(request.Credential.PlaintextJSON)
	if err != nil {
		return s.recordFailure(ctx, grant, proofevidence.OutcomeDenied)
	}
	if err := s.check(ctx, grant); err != nil {
		return s.recordFailure(ctx, grant, proofevidence.OutcomeDenied)
	}
	stopped := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.check(ctx, grant) != nil {
					cancel()
					return
				}
			}
		}
	}()
	observation, executionErr := s.executor.ObserveProof(ctx, request)
	close(stopped)
	<-monitorDone
	if ctx.Err() != nil {
		return s.recordFailure(ctx, grant, contextOutcome(ctx.Err()))
	}
	if executionErr != nil {
		return s.recordFailure(ctx, grant, proofevidence.OutcomeWorkerLost)
	}
	if s.check(ctx, grant) != nil {
		return s.recordFailure(ctx, grant, proofevidence.OutcomeDenied)
	}
	if err := s.ledger.Record(ctx, grant, observation); err != nil {
		return proofevidence.Result{}, err
	}
	return observation.Result, nil
}

func (s *Coordinator) check(ctx context.Context, grant proofevidence.Grant) error {
	if err := s.ledger.CheckExecution(ctx, grant); err != nil {
		return err
	}
	return s.credentials.CheckProofCredential(ctx, grant)
}

func (s *Coordinator) recordFailure(ctx context.Context, grant proofevidence.Grant, outcome proofevidence.Outcome) (proofevidence.Result, error) {
	r := grant.Request()
	digest, _ := r.Digest()
	result := proofevidence.Result{RequestDigest: digest, EvidenceLevel: r.EvidenceLevel, Outcome: outcome}
	// A canceled caller can acknowledge completion but cannot make provider calls.
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.ledger.Record(completionCtx, grant, Observation{Result: result}); err != nil {
		return proofevidence.Result{}, err
	}
	return result, nil
}
