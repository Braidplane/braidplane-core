package proofevidencesvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// ManagedExecutorIdentity identifies the hosted connector execution pool, not
// an individual River job or database session. Remote assignments remain denied.
const ManagedExecutorIdentity = "managed-connector-executor"

// ExecutionRequest is private delivery material. Only authenticated, encrypted
// transport may carry it; River jobs contain only the broker's opaque ID.
type ExecutionRequest struct {
	Request       proofevidence.Request            `json:"request"`
	Authorization proofevidence.Authorization      `json:"authorization"`
	Policy        proofevidence.Policy             `json:"policy"`
	Deadline      time.Time                        `json:"deadline"`
	Plan          proofevidence.MetadataPlan       `json:"plan"`
	Credential    credentialssvc.RuntimeCredential `json:"credential"`
}

func (r ExecutionRequest) Grant() (proofevidence.Grant, error) {
	return r.GrantAt(time.Now())
}

// GrantAt requires a clock sample from the authenticated delivery authority.
func (r ExecutionRequest) GrantAt(now time.Time) (proofevidence.Grant, error) {
	if r.Request.Worker.Identity != ManagedExecutorIdentity || r.Credential.Version != r.Request.CredentialVersion ||
		len(r.Credential.PlaintextJSON) == 0 || !matchesRequest(r.Plan, r.Request) {
		return proofevidence.Grant{}, proofevidence.ErrAuthorizationDenied
	}
	return r.Policy.ResumeAt(r.Request, r.Authorization, r.Deadline, now)
}

// ExecuteMetadata has no persistence or credential-store dependency. The
// backend must monitor current authorization and cancel delivery on revocation.
func ExecuteMetadata(ctx context.Context, request ExecutionRequest, target proofevidence.MetadataObserver, authorityTime time.Time) Observation {
	grant, err := request.GrantAt(authorityTime)
	if err != nil {
		digest, _ := request.Request.Digest()
		return Observation{Result: proofevidence.Result{
			RequestDigest: digest,
			EvidenceLevel: request.Request.EvidenceLevel, Outcome: proofevidence.OutcomeDenied,
		}}
	}
	service := Service{credentials: deliveredCredential{request.Credential}}
	return service.Observe(ctx, grant, target)
}

type deliveredCredential struct {
	credential credentialssvc.RuntimeCredential
}

// ValidateExecutionResult applies the same boundary at delivery and storage.
// Non-complete results cannot smuggle a protected snapshot into either store.
func ValidateExecutionResult(grant proofevidence.Grant, observation Observation) error {
	request := grant.Request()
	digest, err := request.Digest()
	if err != nil || observation.Result.RequestDigest != digest ||
		!observation.Result.Outcome.IsAOutcome() || observation.Result.Outcome == proofevidence.OutcomeUnknown ||
		observation.Result.EvidenceLevel != request.EvidenceLevel {
		return proofevidence.ErrAuthorizationDenied
	}
	if observation.Result.Outcome == proofevidence.OutcomeComplete {
		if observation.Snapshot == nil || grant.Accept(observation.Result) != nil ||
			observation.Result.Usage.Requests < 1 || observation.Result.Usage.MaxConcurrency < 1 ||
			!validSnapshot(observation.Snapshot, request, observation.Result) {
			return proofevidence.ErrAuthorizationDenied
		}
	} else if observation.Snapshot != nil {
		return proofevidence.ErrAuthorizationDenied
	}
	return nil
}

func (c deliveredCredential) ResolveProofCredential(ctx context.Context, grant proofevidence.Grant) (credentialssvc.RuntimeCredential, error) {
	if ctx.Err() != nil || grant.Check() != nil || c.credential.Version != grant.Request().CredentialVersion {
		return credentialssvc.RuntimeCredential{}, proofevidence.ErrAuthorizationDenied
	}
	value := c.credential
	value.PlaintextJSON = append([]byte(nil), value.PlaintextJSON...)
	return value, nil
}
