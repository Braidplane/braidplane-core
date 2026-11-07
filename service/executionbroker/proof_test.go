package executionbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryConnectorCallsCannotConsumeProofResponses(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{kindQuery, kindSchema} {
		for _, scenario := range []string{"OrdinaryResponse", "ProofResponse", "StorageError"} {
			t.Run(kind+scenario, func(t *testing.T) {
				cipher, err := crypto.NewAES256(make([]byte, 32))
				require.NoError(t, err)
				repo := &fakeRequestRepo{}
				broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, requestTimeout: testRequestTimeout}
				response := encryptedResponse{Result: result.NewHTTPResult(&result.HTTPResult{StatusCode: 200}, 0, nil), SchemaJSON: []byte(`{"tables":[]}`)}
				if scenario == "ProofResponse" {
					response = encryptedResponse{Proof: &proofExecutionResponse{Result: proofevidence.Result{Outcome: proofevidence.OutcomeComplete}}}
				}
				broker.dispatcher = proofReplyDispatcher(func(_ context.Context, _ postgres.DBTX, id uuid.UUID) error {
					raw, err := marshalResponsePlaintext(response)
					require.NoError(t, err)
					sealed, err := cipher.Encrypt(raw)
					require.NoError(t, err)
					repo.request = requestrepo.Request{ID: id, State: requestrepo.StateSucceeded, ResultCiphertext: sealed}
					if scenario == "StorageError" {
						repo.findErr = xerror.Unexpected().Detail("fixture unavailable")
					}
					return nil
				})
				if kind == kindQuery {
					got, callErr := broker.ExecuteConnector(t.Context(), queriessvc.ConnectorExecutionRequest{OrganizationSlug: "acme"})
					err = callErr
					if scenario == "OrdinaryResponse" {
						require.Equal(t, response.Result, got)
					} else {
						require.Nil(t, got)
					}
				} else {
					got, callErr := broker.IntrospectSchema(t.Context(), datasourcessvc.SchemaIntrospectionRequest{OrganizationSlug: "acme"})
					err = callErr
					if scenario == "OrdinaryResponse" {
						require.Equal(t, response.SchemaJSON, got)
					} else {
						require.Nil(t, got)
					}
				}
				if scenario == "OrdinaryResponse" {
					require.NoError(t, err)
				} else {
					var typed *xerror.Error
					require.ErrorAs(t, err, &typed)
					require.Equal(t, xerror.KindInternal, typed.Kind())
				}
				require.Equal(t, kind, repo.created.Kind)
				require.Equal(t, "acme", repo.created.OrganizationSlug)
			})
		}
	}
}

func proofRequest(t *testing.T) proofevidencesvc.ExecutionRequest {
	t.Helper()
	r := testutil.ProofEvidenceRequest()
	plan, err := (hubspot.ContactMetadataObserver{AccountID: 123}).Plan()
	require.NoError(t, err)
	r.ConnectorType, r.Operation, r.Target, r.Egress = plan.ConnectorType, plan.Operation, plan.Target, plan.Egress
	r.TargetFingerprint, err = plan.Fingerprint()
	require.NoError(t, err)
	r.Worker.Identity = proofevidencesvc.ManagedExecutorIdentity
	digest, err := r.Digest()
	require.NoError(t, err)
	now := time.Now().UTC()
	return proofevidencesvc.ExecutionRequest{
		Request: r, Plan: plan, Deadline: now.Add(10 * time.Second),
		Authorization: proofevidence.Authorization{
			ID: "intent-1", Capability: proofevidence.Capability,
			RequestDigest: digest, PolicyVersion: r.PolicyVersion, AuthorizedAt: now, ExpiresAt: now.Add(time.Minute),
		},
		Policy: proofevidence.Policy{
			Enabled: true, Version: r.PolicyVersion, MaxAuthorizationTTL: time.Minute,
			MaxEvidenceRetention: 24 * time.Hour, MaxMetadataRetention: 48 * time.Hour,
			Operations: []proofevidence.OperationPolicy{{
				ConnectorType: r.ConnectorType, Operation: r.Operation,
				EvidenceLevel: r.EvidenceLevel, NoWriteGuarantee: proofevidence.NoWriteReadOnlyAPI,
				Limits: r.Limits, RunnerTrust: proofevidence.RunnerTrustManaged,
			}},
		},
		Credential: credentialssvc.RuntimeCredential{Version: r.CredentialVersion, PlaintextJSON: []byte(`{"private_app_token":"fixture-token"}`)},
	}
}

type proofReplyDispatcher func(context.Context, postgres.DBTX, uuid.UUID) error

func (f proofReplyDispatcher) Dispatch(ctx context.Context, tx postgres.DBTX, id uuid.UUID) error {
	return f(ctx, tx, id)
}

type proofRetentionRepo struct {
	fakeRequestRepo
	retainedUntil time.Time
}

func (r *proofRetentionRepo) Create(ctx context.Context, tx postgres.DBTX, request requestrepo.Request, retainedUntil time.Time) error {
	r.retainedUntil = retainedUntil
	return r.fakeRequestRepo.Create(ctx, tx, request, retainedUntil)
}

func TestProofDeliveryBoundsRetentionAndRejectsUntrustedCompletion(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"Complete", "MismatchedDigest", "MissingSnapshot", "MissingResponse", "StorageError"} {
		t.Run(scenario, func(t *testing.T) {
			r := proofRequest(t)
			r.Request.EvidenceRetainUntil = time.Now().Add(time.Minute)
			r.Authorization.RequestDigest, _ = r.Request.Digest()
			cipher, err := crypto.NewAES256(make([]byte, 32))
			require.NoError(t, err)
			repo := &proofRetentionRepo{}
			calls := 0
			broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, requestTimeout: time.Minute}
			broker.dispatcher = proofReplyDispatcher(func(_ context.Context, _ postgres.DBTX, id uuid.UUID) error {
				calls++
				snapshot := &proofevidencesvc.Snapshot{TargetFingerprint: r.Request.TargetFingerprint, Metadata: json.RawMessage(`{"fields":["private-field"]}`)}
				raw, err := json.Marshal(snapshot)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				response := encryptedResponse{Proof: &proofExecutionResponse{Snapshot: snapshot, Result: proofevidence.Result{RequestDigest: r.Authorization.RequestDigest, EvidenceLevel: r.Request.EvidenceLevel, Outcome: proofevidence.OutcomeComplete, EvidenceDigest: "sha256:" + hex.EncodeToString(sum[:]), ObservedAt: time.Now().UTC(), ExpiresAt: r.Request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: 2, MaxConcurrency: 1, Rows: 1, Bytes: int64(len(raw))}}}}
				switch scenario {
				case "MismatchedDigest":
					response.Proof.Result.RequestDigest = "other-request"
				case "MissingSnapshot":
					response.Proof.Snapshot = nil
				case "MissingResponse":
					response.Proof = nil
				case "StorageError":
					repo.findErr = xerror.Unexpected().Detail("private-storage-diagnostic")
				}
				raw, err = marshalResponsePlaintext(response)
				require.NoError(t, err)
				encrypted, err := cipher.Encrypt(raw)
				require.NoError(t, err)
				repo.request = requestrepo.Request{ID: id, State: requestrepo.StateSucceeded, ResultCiphertext: encrypted}
				return nil
			})
			observation, err := broker.ObserveProof(t.Context(), r)
			require.Equal(t, 1, calls)
			require.Equal(t, r.Deadline, repo.created.DeadlineAt)
			require.Equal(t, r.Request.EvidenceRetainUntil, repo.retainedUntil)
			require.Equal(t, kindProofMetadata, repo.created.Kind)
			require.Equal(t, r.Request.OrganizationSlug.String(), repo.created.OrganizationSlug)
			require.NotContains(t, string(repo.created.RequestCiphertext), "fixture-token")
			if scenario == "Complete" {
				require.NoError(t, err)
				require.Equal(t, proofevidence.OutcomeComplete, observation.Result.Outcome)
				require.Equal(t, r.Request.TargetFingerprint, observation.Snapshot.TargetFingerprint)
			} else {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
				require.NotContains(t, err.Error(), "private-storage-diagnostic")
				require.Nil(t, observation.Snapshot)
			}
		})
	}
}

func TestProofProcessorRejectsSubstitutionBeforeProviderCalls(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"tenant", "assignment", "private-network", "credential", "expired", "policy", "route"} {
		t.Run(scenario, func(t *testing.T) {
			r := proofRequest(t)
			org := r.Request.OrganizationSlug.String()
			switch scenario {
			case "tenant":
				org = "other"
			case "assignment":
				r.Request.Worker.Identity = "other-executor"
			case "private-network":
				r.Request.Worker.PrivateNetwork = true
			case "credential":
				r.Credential.Version = "rotated"
			case "expired":
				r.Deadline = time.Now().Add(-time.Second)
			case "policy":
				r.Policy.Enabled = false
			case "route":
				r.Plan.Routes[0].URL += "/records"
			}
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			response, success := processProofMetadata(t.Context(), org, raw, time.Now())
			require.False(t, success)
			require.Equal(t, errorCodeInvalidRequest, response.ErrorCode)
			require.Nil(t, response.Proof)
		})
	}
}

func TestProofUsesTheExistingProcessorAndAcknowledgesCancellation(t *testing.T) {
	t.Parallel()
	r := proofRequest(t)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(raw)
	require.NoError(t, err)
	repo := &fakeRequestRepo{request: requestrepo.Request{
		ID: uuid.New(), OrganizationSlug: r.Request.OrganizationSlug.String(),
		Kind: kindProofMetadata, State: requestrepo.StateRunning, RequestCiphertext: encrypted, DeadlineAt: r.Deadline,
		ObservedAt: time.Now(), ObservedLocallyAt: time.Now(),
	}}
	broker := &Broker{admission: testutil.FakeOperationAdmission{}, txm: fakeTXM{}, repo: repo, cipher: cipher, heartbeatEvery: time.Second}
	configureTestProcessor(t, broker)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = broker.Process(ctx, repo.request.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, repo.completeCalls.Load())
	response, done, err := broker.readResponse(t.Context(), repo.request.ID, r.Request.OrganizationSlug.String())
	require.NoError(t, err)
	require.True(t, done)
	require.NotNil(t, response.Proof)
	require.Equal(t, proofevidence.OutcomeCanceled, response.Proof.Result.Outcome)
	require.Nil(t, response.Proof.Snapshot)
	// configureTestProcessor's connector factory fails if ordinary execution is reached.
	// Although read-only, Proof must use non-replayable delivery: retries would
	// spend provider budgets that were reserved for one observation only.
	require.True(t, executionMayHaveExternalSideEffects(kindProofMetadata))
}

func TestProofSnapshotHasOnlyAnEncryptedDeliveryRepresentation(t *testing.T) {
	t.Parallel()
	observation := proofevidencesvc.Observation{Snapshot: &proofevidencesvc.Snapshot{
		TargetFingerprint: "fingerprint", Metadata: json.RawMessage(`{"fields":["private-schema"]}`),
	}}
	ordinary, err := json.Marshal(observation)
	require.NoError(t, err)
	require.NotContains(t, string(ordinary), "private-schema")
	response := encryptedResponse{Proof: &proofExecutionResponse{Result: observation.Result, Snapshot: observation.Snapshot}}
	raw, err := marshalResponsePlaintext(response)
	require.NoError(t, err)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(raw)
	require.NoError(t, err)
	require.NotContains(t, string(encrypted), "private-schema")
	decoded, err := cipher.Decrypt(encrypted)
	require.NoError(t, err)
	restored, err := decodeResponsePlaintext(decoded)
	require.NoError(t, err)
	require.Equal(t, observation.Snapshot, restored.Proof.Snapshot)
}
