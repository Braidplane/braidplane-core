package proofevidencesvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/stretchr/testify/require"
)

func TestJobAuditAcceptsOnlyCanonicalDigestAndSafeReferences(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("ab", 32)
	scope := proofobservations.Scope{Organization: "org", Project: "project", Environment: "environment"}
	for _, action := range []model.EventAction{model.EventActionRevoke, model.EventActionDelete} {
		for _, supplied := range []string{digest, strings.ToUpper(digest), "private-provider-diagnostic", "sha256:abcd"} {
			record, err := (JobAudit{Scope: scope, Reference: proofobservations.JobReference{Slug: "job", RequestDigest: supplied}, Action: action}).EventRecord()
			require.NoError(t, err)
			require.Equal(t, model.EventClassSecurityAudit, record.EventClass)
			require.Equal(t, action, record.Action)
			require.Equal(t, scope.Environment, record.EntitySlug)
			want := map[string]any{"project_slug": "project", "environment_slug": "environment", "proof_observation_job_slug": "job"}
			if supplied == digest {
				want["request_digest"] = digest
			}
			require.Equal(t, want, record.Metadata)
			if action == model.EventActionRevoke {
				require.Equal(t, "evt-proof-observation", record.SlugPrefix.String())
			} else {
				require.Equal(t, "evt-proof-retention", record.SlugPrefix.String())
			}
		}
	}
}

func TestLedgerRejectsInvalidAdmissionBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	ledger := &Ledger{}
	require.ErrorIs(t, ledger.Start(t.Context(), proofevidence.Grant{}), proofevidence.ErrAuthorizationDenied)
	request := requestFor(t, tableTarget())
	other := tableTarget()
	other.TableID = "other-table"
	invalid := tableTarget()
	invalid.TableID = ""
	for _, target := range []proofevidence.MetadataObserver{nil, other, invalid} {
		job, err := ledger.Reserve(t.Context(), request, proofevidence.Authorization{}, target)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
		require.Nil(t, job)
	}
	for _, scenario := range []string{"Version", "Worker", "AuthorizationTTL", "EvidenceRetention", "MetadataRetention"} {
		t.Run(scenario, func(t *testing.T) {
			policy := proofevidence.Policy{Version: "v1", MaxAuthorizationTTL: time.Minute, MaxEvidenceRetention: time.Hour, MaxMetadataRetention: time.Hour}
			worker := "proof_worker"
			switch scenario {
			case "Version":
				policy.Version = " "
			case "Worker":
				worker = " "
			case "AuthorizationTTL":
				policy.MaxAuthorizationTTL = 0
			case "EvidenceRetention":
				policy.MaxEvidenceRetention = 0
			case "MetadataRetention":
				policy.MaxMetadataRetention = time.Minute
			}
			require.ErrorIs(t, ledger.SetPolicy(t.Context(), proofobservations.Scope{}, policy, worker), proofevidence.ErrAuthorizationDenied)
		})
	}
}

func TestLedgerRejectsInvalidEvidenceBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	request := requestFor(t, tableTarget())
	grant := testutil.AdmitProofEvidence(t, request)
	requestDigest, err := request.Digest()
	require.NoError(t, err)
	for _, scenario := range []string{"Request", "MissingSnapshot", "Target", "Malformed", "Null", "Digest", "Rows", "Oversize", "Expired", "UnknownOutcome", "EmptyOutcome", "WrongLevel", "NoRequests", "NoConcurrency", "FailureWithSnapshot"} {
		t.Run(scenario, func(t *testing.T) {
			snapshot := &Snapshot{TargetFingerprint: request.TargetFingerprint, Metadata: json.RawMessage(`{"fields":[]}`)}
			raw, err := json.Marshal(snapshot)
			require.NoError(t, err)
			result := proofevidence.Result{RequestDigest: requestDigest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: request.EvidenceLevel, EvidenceDigest: digest(raw), ObservedAt: time.Now().UTC(), ExpiresAt: request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: 1, MaxConcurrency: 1}}
			switch scenario {
			case "Request":
				result.RequestDigest = "other-request"
			case "MissingSnapshot":
				snapshot = nil
			case "Target":
				snapshot.TargetFingerprint = "other-target"
			case "Malformed":
				snapshot.Metadata = json.RawMessage(`{`)
			case "Null":
				snapshot.Metadata = json.RawMessage(` null `)
			case "Digest":
				snapshot.Metadata = json.RawMessage(`{"fields":["changed"]}`)
			case "Rows":
				result.Usage.Rows = -1
			case "Oversize":
				snapshot.Metadata, err = json.Marshal(make([]byte, request.Limits.MaxBytes))
				require.NoError(t, err)
			case "Expired":
				result.ExpiresAt = time.Now().Add(-time.Minute)
			case "UnknownOutcome":
				result.Outcome = proofevidence.Outcome(99)
			case "EmptyOutcome":
				result.Outcome = proofevidence.OutcomeUnknown
			case "WrongLevel":
				result.EvidenceLevel = 0
			case "NoRequests":
				result.Usage.Requests = 0
			case "NoConcurrency":
				result.Usage.MaxConcurrency = 0
			case "FailureWithSnapshot":
				result.Outcome = proofevidence.OutcomePartial
			}
			require.ErrorIs(t, (&Ledger{}).Record(t.Context(), grant, Observation{Result: result, Snapshot: snapshot}), proofevidence.ErrAuthorizationDenied)
		})
	}
}

type unavailableLedgerTransaction struct {
	err error
}

func (tx unavailableLedgerTransaction) RunInTx(context.Context, func(context.Context, postgres.DBTX) error) error {
	return tx.err
}

func (tx unavailableLedgerTransaction) RunRead(context.Context, func(context.Context, postgres.DBTX) error) error {
	return tx.err
}

func TestLedgerDoesNotCompleteWhenEncryptionOrTransactionFails(t *testing.T) {
	t.Parallel()
	request := requestFor(t, tableTarget())
	grant := testutil.AdmitProofEvidence(t, request)
	requestDigest, err := request.Digest()
	require.NoError(t, err)
	snapshot := &Snapshot{TargetFingerprint: request.TargetFingerprint, Metadata: json.RawMessage(`{"fields":[]}`)}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	result := proofevidence.Result{RequestDigest: requestDigest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: request.EvidenceLevel, EvidenceDigest: digest(raw), ObservedAt: time.Now().UTC(), ExpiresAt: request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: 1, MaxConcurrency: 1}}
	require.NoError(t, grant.Accept(result))
	key := make([]byte, 32)
	cipher, err := crypto.NewAES256(key)
	require.NoError(t, err)
	storageErr := xerror.Unexpected().Detail("fixture transaction unavailable")
	ledger, err := NewLedger(unavailableLedgerTransaction{err: storageErr}, proofobservations.New(), cipher, unusedLedgerAudit{})
	require.NoError(t, err)
	require.ErrorIs(t, ledger.Record(t.Context(), grant, Observation{Result: result, Snapshot: snapshot}), storageErr)
	require.ErrorIs(t, ledger.Start(t.Context(), grant), storageErr)
	job, err := ledger.Reserve(t.Context(), request, proofevidence.Authorization{}, tableTarget())
	require.ErrorIs(t, err, storageErr)
	require.Nil(t, job)
	require.ErrorIs(t, ledger.Cancel(t.Context(), request.OrganizationSlug, request.JobID), storageErr)
	observation, err := ledger.ReadSnapshot(t.Context(), request.OrganizationSlug, request.JobID)
	require.ErrorIs(t, err, storageErr)
	require.Equal(t, Observation{}, observation)
	maintenance, err := NewMaintenance(ledger.txm, ledger.repo, proofreceipts.New(), unusedLedgerAudit{})
	require.NoError(t, err)
	require.ErrorIs(t, maintenance.Cleanup(t.Context(), proofobservations.Scope{}), storageErr)

	ledger.txm = nil // Encryption failure must return before accessing persistence.
	ledger.evidenceCipher = failingEvidenceCipher{err: storageErr}
	require.ErrorIs(t, ledger.Record(t.Context(), grant, Observation{Result: result, Snapshot: snapshot}), storageErr)
}

type failingEvidenceCipher struct {
	crypto.AES256
	err error
}

func (c failingEvidenceCipher) Encrypt([]byte) ([]byte, error) { return nil, c.err }

// The unavailable transaction must fail before an audit dependency is invoked.
type unusedLedgerAudit struct{ eventrecorder.Recorder }

type unusedProofCredentials struct{ ExecutionCredentials }

type unusedProofExecutor struct{ Executor }

func TestStorageOutageStopsProofBeforeCredentialsOrExecutorAccess(t *testing.T) {
	t.Parallel()
	storageErr := xerror.Unexpected().Detail("private-database-diagnostic")
	ledger := &Ledger{txm: unavailableLedgerTransaction{err: storageErr}}
	coordinator, err := NewCoordinator(ledger, unusedProofCredentials{}, unusedProofExecutor{})
	require.NoError(t, err)
	result, err := coordinator.Run(t.Context(), "org", "job")
	require.ErrorIs(t, err, storageErr)
	require.Equal(t, proofevidence.Result{}, result)
	scope := proofobservations.Scope{Organization: "org", Project: "project", Environment: "environment"}
	_, err = ledger.Policy(t.Context(), scope)
	require.ErrorIs(t, err, storageErr)
	_, err = ledger.Job(t.Context(), scope, "job")
	require.ErrorIs(t, err, storageErr)
	_, err = ledger.ReadForReceipt(t.Context(), scope, "job")
	require.ErrorIs(t, err, storageErr)
	_, err = ledger.ReadForReceiptDigest(t.Context(), scope, "digest")
	require.ErrorIs(t, err, storageErr)
	grant := testutil.AdmitProofEvidence(t, requestFor(t, tableTarget()))
	require.ErrorIs(t, ledger.CheckExecution(t.Context(), grant), storageErr)
	require.ErrorIs(t, coordinator.check(t.Context(), grant), storageErr)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = coordinator.recordFailure(ctx, grant, proofevidence.OutcomeCanceled)
	require.ErrorIs(t, err, storageErr)
	require.Equal(t, proofevidence.Result{}, result, "a failed durable acknowledgement must not report a completed result")

	maintenance := &Maintenance{txm: ledger.txm}
	require.ErrorIs(t, maintenance.Run(t.Context()), storageErr)
	health, err := maintenance.Health(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-database-diagnostic")
	require.Zero(t, health)
	activity, err := maintenance.Activity(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-database-diagnostic")
	require.Zero(t, activity)
}
