// Package proofevidencesvc implements selected-resource metadata observations.
// The restricted broker operation is separate from ordinary introspection and
// is not enabled by Proof's UI flag.
package proofevidencesvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// Snapshot may contain private schema names. It belongs only in encrypted,
// tenant-protected evidence storage, never receipts, jobs, logs, or annotations.
type Snapshot struct {
	TargetFingerprint string          `json:"target_fingerprint"`
	Metadata          json.RawMessage `json:"metadata"`
}

type Observation struct {
	Result   proofevidence.Result
	Snapshot *Snapshot `json:"-"`
}

type Service struct{ credentials credentialssvc.ProofResolver }

func New(credentials credentialssvc.ProofResolver) (*Service, error) {
	if credentials == nil {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("Proof credential service is required")
	}
	return &Service{credentials: credentials}, nil
}

// Observe requires a fresh dispatch grant. The runner still owns current policy,
// durable cumulative admission, cancellation, persistence, and audit events.
func (s *Service) Observe(ctx context.Context, grant proofevidence.Grant, target proofevidence.MetadataObserver) Observation {
	transport := publicTransport(grant.Request().Limits.Timeout)
	defer transport.CloseIdleConnections()
	return s.observe(ctx, grant, target, transport)
}

func (s *Service) observe(ctx context.Context, grant proofevidence.Grant, target proofevidence.MetadataObserver, next http.RoundTripper) Observation {
	started := time.Now().UTC()
	r := grant.Request()
	requestDigest, _ := r.Digest()
	result := proofevidence.Result{RequestDigest: requestDigest, EvidenceLevel: r.EvidenceLevel, Outcome: proofevidence.OutcomeDenied}
	if target == nil {
		return Observation{Result: result}
	}
	plan, err := target.Plan()
	if err != nil || grant.Check() != nil || !matchesRequest(plan, r) {
		return Observation{Result: result}
	}
	routes := make([]route, len(plan.Routes))
	for i, request := range plan.Routes {
		routes[i] = route{request.Method, request.URL}
	}
	ctx, cancel := context.WithTimeout(ctx, grant.Remaining())
	defer cancel()
	if ctx.Err() != nil {
		result.Outcome = contextOutcome(ctx.Err())
		return Observation{Result: result}
	}
	credential, err := s.credentials.ResolveProofCredential(ctx, grant)
	defer clear(credential.PlaintextJSON)
	if ctx.Err() != nil {
		result.Outcome = contextOutcome(ctx.Err())
		result.Usage.Duration = time.Since(started)
		return Observation{Result: result}
	}
	if err != nil || credential.Version != r.CredentialVersion {
		return Observation{Result: result}
	}
	transport := &metadataTransport{grant: grant, routes: routes, next: next, maxConnections: plan.MaxConnections}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return proofevidence.ErrAuthorizationDenied
	}}
	transport.client = client
	metadata, rows, outcome := target.ReadMetadata(ctx, transport, credential.PlaintextJSON, r.Limits)
	snapshot := &Snapshot{TargetFingerprint: r.TargetFingerprint, Metadata: metadata}
	transport.mu.Lock()
	result.Usage = transport.usage
	transportOutcome, remainingRoutes := transport.outcome, len(transport.routes)
	transport.mu.Unlock()
	result.Usage.Duration = time.Since(started)
	result.Usage.Rows = rows
	switch {
	case ctx.Err() != nil:
		result.Outcome = contextOutcome(ctx.Err())
	case transportOutcome != proofevidence.OutcomeUnknown:
		result.Outcome = transportOutcome
	case remainingRoutes != 0:
		result.Outcome = proofevidence.OutcomePartial
	case outcome != proofevidence.OutcomeComplete:
		result.Outcome = outcome
	case rows < 0 || !json.Valid(metadata):
		result.Outcome = proofevidence.OutcomePartial
	case rows > r.Limits.MaxRows:
		result.Outcome = proofevidence.OutcomeOverBudget
	default:
		result.Outcome = outcome
	}
	if result.Outcome != proofevidence.OutcomeComplete {
		return Observation{Result: result}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		result.Outcome = proofevidence.OutcomePartial
		return Observation{Result: result}
	}
	result.EvidenceDigest = digest(encoded)
	result.ObservedAt = time.Now().UTC()
	result.ExpiresAt = r.EvidenceRetainUntil
	if grant.Check() != nil || grant.Accept(result) != nil {
		result.Outcome = proofevidence.OutcomeTimedOut
		result.EvidenceDigest = ""
		return Observation{Result: result}
	}
	return Observation{Result: result, Snapshot: snapshot}
}

// Admission and dispatch bind the same connector-owned plan to the request.
func matchesRequest(plan proofevidence.MetadataPlan, r proofevidence.Request) bool {
	fingerprint, err := plan.Fingerprint()
	return err == nil && fingerprint == r.TargetFingerprint && plan.ConnectorType == r.ConnectorType &&
		r.EvidenceLevel == model.ProofEvidenceLevelLiveReadOnly && r.Worker.Trust == proofevidence.RunnerTrustManaged && !r.Worker.PrivateNetwork &&
		r.Operation == plan.Operation && r.Target == plan.Target && slices.Equal(r.Egress, plan.Egress)
}

func digest(value []byte) string {
	hash := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(hash[:])
}
