package executionbroker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
)

// Snapshot is deliberately absent from Observation's ordinary JSON. This
// explicit sidecar is restricted to the broker's encrypted response envelope.
type proofExecutionResponse struct {
	Result   proofevidence.Result       `json:"result"`
	Snapshot *proofevidencesvc.Snapshot `json:"snapshot,omitempty"`
}

func (b *Broker) ObserveProof(ctx context.Context, request proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error) {
	grant, err := request.Grant()
	if err != nil {
		return proofevidencesvc.Observation{}, err
	}
	if _, err := connector.NewMetadataObserver(request.Plan); err != nil {
		return proofevidencesvc.Observation{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, grant.Deadline())
	defer cancel()
	response, err := b.submitAndWait(ctx, request.Request.OrganizationSlug.String(), kindProofMetadata, request)
	if err != nil || response.Proof == nil || ctx.Err() != nil || grant.Check() != nil {
		// Queue/storage diagnostics must not escape into Proof evidence.
		return proofevidencesvc.Observation{}, proofevidence.ErrAuthorizationDenied
	}
	observation := proofevidencesvc.Observation{Result: response.Proof.Result, Snapshot: response.Proof.Snapshot}
	if err := proofevidencesvc.ValidateExecutionResult(grant, observation); err != nil {
		return proofevidencesvc.Observation{}, err
	}
	return observation, nil
}

func processProofMetadata(ctx context.Context, organization string, plaintext []byte, authorityTime time.Time) (encryptedResponse, bool) {
	var request proofevidencesvc.ExecutionRequest
	defer func() { clear(request.Credential.PlaintextJSON) }()
	if json.Unmarshal(plaintext, &request) != nil || request.Request.OrganizationSlug.String() != organization {
		return encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false
	}
	if _, err := request.GrantAt(authorityTime); err != nil {
		return encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false
	}
	observer, err := connector.NewMetadataObserver(request.Plan)
	if err != nil {
		return encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false
	}
	observation := proofevidencesvc.ExecuteMetadata(ctx, request, observer, authorityTime)
	return encryptedResponse{Proof: &proofExecutionResponse{Result: observation.Result, Snapshot: observation.Snapshot}}, true
}
