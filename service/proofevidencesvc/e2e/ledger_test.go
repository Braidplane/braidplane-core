package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
)

type fixture struct {
	connectionID   string
	maintenance    *proofevidencesvc.Maintenance
	retentionAudit *retentionAuditRecorder
	cipher         crypto.AES256
	pool           *pgxpool.Pool
	ledger         *proofevidencesvc.Ledger
	repo           *proofobservations.Repository
	scope          proofobservations.Scope
	policy         proofevidence.Policy
	request        proofevidence.Request
	target         bigquery.TableMetadataObserver
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("requires the worktree PostgreSQL fixture")
	}
	pool := e2edb.OpenPostgresPool(t, "Proof observation ledger integration")
	org := slug.Slug(fmt.Sprintf("proof-ledger-%d", time.Now().UnixNano()))
	identity := proofevidencesvc.ManagedExecutorIdentity
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := pool.Exec(t.Context(), `
 WITH organization AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id),
 git_credential AS (INSERT INTO credentials(organization_id,slug,kind,status,encrypted_payload) SELECT id,'git-credential','static_secret','active','fixture'::bytea FROM organization RETURNING id,organization_id),
 git_source AS (INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active) SELECT organization_id,id,'Git','github','github','{}',true FROM git_credential RETURNING id,organization_id),
 project AS (INSERT INTO projects(organization_id,slug,name,pipeline_backend_data_source_id,pipeline_backend_repo,pipeline_backend_tracked_branch,pipeline_backend_pipeline_dir) SELECT organization_id,'etl','ETL',id,'https://github.com/fixture/pipelines','main','pipelines' FROM git_source RETURNING id,organization_id),
 environment AS (INSERT INTO environments(organization_id,slug,name) SELECT id,'staging','Staging' FROM organization RETURNING id),
 enabled AS (INSERT INTO project_environments(organization_id,project_id,environment_id,is_default) SELECT project.organization_id,project.id,environment.id,true FROM project,environment),
 warehouse_credential AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload,updated_at) SELECT id,'warehouse-credential','static_secret','bigquery','active','protected-fixture'::bytea,$2 FROM organization RETURNING id,organization_id)
 INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active) SELECT organization_id,id,'Warehouse','warehouse','bigquery','{}',true FROM warehouse_credential
 `, org, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM events WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM proof_receipts WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM proof_evidence WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM proof_observation_jobs WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM proof_observation_policies WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM proof_change_candidates WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM proof_change_candidate_mappings WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM projects WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM data_sources WHERE organization_id IN(SELECT id FROM organizations WHERE slug=$1)`,
			`DELETE FROM organizations WHERE slug=$1`,
		} {
			_, err := pool.Exec(context.Background(), statement, org)
			require.NoError(t, err)
		}
	})
	connectionID := e2edb.EnableGitHubProof(t, pool, org, "github", "42")
	candidates := proofcandidates.New()
	mapping, err := candidates.CreateMapping(t.Context(), pool, proofcandidates.CreateMappingParams{OrganizationSlug: org, ProjectSlug: "etl", EnvironmentSlug: "staging", ProviderConnectionSlug: "github", Provider: "github", ProviderConnectionID: connectionID, RepositoryID: "42", TargetRef: "refs/heads/main", PipelinePath: "pipelines/contacts.yaml"})
	require.NoError(t, err)
	_, err = candidates.Create(t.Context(), pool, org, proofcandidates.CreateCandidateParams{MappingID: mapping, Slug: "candidate-1", CandidateKey: org.String(), ChangeRequestID: "42", SourceRef: "feature", HeadSHA: strings.Repeat("a", 40), SourceSHA256: hash([]byte("fixture")), SpecHash: hash([]byte("{}")), CanonicalSpec: []byte("{}")})
	require.NoError(t, err)
	r := testutil.ProofEvidenceRequest()
	r.OrganizationSlug = org
	r.Candidate.OrganizationSlug = org
	r.Candidate.RepositoryID = "42"
	r.Candidate.TargetRef = "refs/heads/main"
	r.Worker.Identity = identity
	r.Candidate.SpecHash = hash([]byte("{}"))
	r.Claim.PipelineVersionHash = r.Candidate.SpecHash
	r.CredentialVersion = "warehouse-credential/" + now.Format(time.RFC3339Nano)
	target := bigquery.TableMetadataObserver{ProjectID: "fixture-project", DatasetID: "private_dataset", TableID: "private_table"}
	plan, err := target.Plan()
	require.NoError(t, err)
	r.TargetFingerprint, err = plan.Fingerprint()
	require.NoError(t, err)
	limits := r.Limits
	limits.MaxConcurrency = 1
	limits.MaxRequests *= 10
	limits.MaxBytes *= 10
	limits.MaxRows *= 10
	policy := proofevidence.Policy{Enabled: true, Version: r.PolicyVersion, MaxAuthorizationTTL: 5 * time.Minute, MaxEvidenceRetention: 24 * time.Hour, MaxMetadataRetention: 48 * time.Hour, Operations: []proofevidence.OperationPolicy{{ConnectorType: r.ConnectorType, Operation: r.Operation, EvidenceLevel: r.EvidenceLevel, NoWriteGuarantee: proofevidence.NoWriteReadOnlyAPI, ProviderEndpoints: r.Egress[1:], Limits: limits, RunnerTrust: proofevidence.RunnerTrustManaged}}}
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	cipher, err := crypto.NewAES256(key)
	require.NoError(t, err)
	repo := proofobservations.New()
	audit := eventrecorder.New(eventssvc.New(postgres.NewTransactionManager(pool), events.New(), clock.New()))
	ledger, err := proofevidencesvc.NewLedger(postgres.NewTransactionManager(pool), repo, cipher, audit)
	require.NoError(t, err)
	retentionAudit := &retentionAuditRecorder{Recorder: audit}
	maintenance, err := proofevidencesvc.NewMaintenance(postgres.NewTransactionManager(pool), repo, proofreceipts.New(), retentionAudit)
	require.NoError(t, err)
	f := &fixture{connectionID: connectionID, maintenance: maintenance, retentionAudit: retentionAudit, cipher: cipher, pool: pool, ledger: ledger, repo: repo, scope: proofobservations.Scope{Organization: org, Project: "etl", Environment: "staging"}, policy: policy, request: r, target: target}
	require.NoError(t, ledger.SetPolicy(t.Context(), f.scope, policy, identity))
	return f
}

func decision(t *testing.T, r proofevidence.Request) proofevidence.Authorization {
	t.Helper()
	digest, err := r.Digest()
	require.NoError(t, err)
	now := time.Now().UTC()
	return proofevidence.Authorization{ID: "intent-1", Capability: proofevidence.Capability, RequestDigest: digest, PolicyVersion: r.PolicyVersion, AuthorizedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
}

func hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestLedgerReservesOnceAndConcurrentJobsCannotBypassLimits(t *testing.T) {
	f := newFixture(t)
	auth := decision(t, f.request)
	first, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	require.NotContains(t, string(first.TargetCiphertext), "private_")
	plaintext, err := f.cipher.Decrypt(first.TargetCiphertext)
	require.NoError(t, err)
	var protected struct {
		RequestDigest string                     `json:"request_digest"`
		Plan          proofevidence.MetadataPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(plaintext, &protected))
	plan, err := f.target.Plan()
	require.NoError(t, err)
	require.Equal(t, plan, protected.Plan)
	require.Equal(t, auth.RequestDigest, protected.RequestDigest)

	again, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	require.Equal(t, first, again)
	changed := f.request
	changed.Candidate.BindingFingerprint = hash([]byte("other"))
	_, err = f.ledger.Reserve(t.Context(), changed, decision(t, changed), f.target)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.NoError(t, f.ledger.Cancel(t.Context(), f.scope.Organization, f.request.JobID))
	f.policy.Version = "concurrent-v2"
	f.policy.Operations[0].Limits.MaxRequests = f.request.Limits.MaxRequests * 2
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		r := f.request
		r.JobID = slug.Slug(fmt.Sprintf("concurrent-%d", i))
		r.PolicyVersion = f.policy.Version
		auth := decision(t, r)
		wg.Go(func() { <-start; _, err := f.ledger.Reserve(t.Context(), r, auth, f.target); results <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	success, limited := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, proofevidencesvc.ErrAdmissionLimit)
			limited++
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, limited)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&count))
	require.Equal(t, 2, count, "the canceled job's request reservation still consumes the rate budget")
	var pending slug.Slug
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT slug FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state='pending'`, f.scope.Organization).Scan(&pending))
	require.NoError(t, f.ledger.Cancel(t.Context(), f.scope.Organization, pending))
	afterCancellation := f.request
	afterCancellation.JobID = "after-cancellation"
	afterCancellation.PolicyVersion = f.policy.Version
	_, err = f.ledger.Reserve(t.Context(), afterCancellation, decision(t, afterCancellation), f.target)
	require.ErrorIs(t, err, proofevidencesvc.ErrAdmissionLimit, "completed cancellations must not refund provider rate reservations")
}

func TestLedgerProtectsSnapshotAndFencesCancellationAndDuplicateCompletion(t *testing.T) {
	f := newFixture(t)
	f.request.EvidenceRetainUntil = f.request.EvidenceRetainUntil.Truncate(time.Microsecond).Add(999 * time.Nanosecond)
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	grant, err := f.policy.Authorize(f.request, auth)
	require.NoError(t, err)
	require.NoError(t, f.ledger.Start(t.Context(), grant))
	require.ErrorIs(t, f.ledger.Start(t.Context(), grant), proofevidence.ErrAuthorizationDenied)
	snapshot := &proofevidencesvc.Snapshot{TargetFingerprint: f.request.TargetFingerprint, Metadata: json.RawMessage(`{"table_type":"VIEW","fields":[{"name":"private_email","type":"STRING","mode":"NULLABLE"}]}`)}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: f.request.EvidenceLevel, EvidenceDigest: hash(raw), ObservedAt: time.Now().UTC(), ExpiresAt: f.request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: 2, Bytes: 100, Rows: 1, MaxConcurrency: 1}}
	observation := proofevidencesvc.Observation{Result: result, Snapshot: snapshot}
	require.NoError(t, grant.Accept(result))
	for _, mutation := range []string{"target", "metadata", "malformed", "null", "oversize", "rows", "failure"} {
		t.Run(mutation, func(t *testing.T) {
			changed := *snapshot
			changedResult := result
			switch mutation {
			case "target":
				changed.TargetFingerprint = hash([]byte("another-target"))
			case "metadata":
				changed.Metadata = json.RawMessage(`{"different":"metadata"}`)
			case "malformed":
				changed.Metadata = json.RawMessage(`{`)
			case "null":
				changed.Metadata = json.RawMessage(`null`)
				raw, err := json.Marshal(changed)
				require.NoError(t, err)
				changedResult.EvidenceDigest = hash(raw)
			case "oversize":
				changed.Metadata = json.RawMessage(`{"field":"` + strings.Repeat("x", int(f.request.Limits.MaxBytes)) + `"}`)
				raw, err := json.Marshal(changed)
				require.NoError(t, err)
				changedResult.EvidenceDigest = hash(raw)
			case "rows":
				changedResult.Usage.Rows = f.request.Limits.MaxRows + 1
			case "failure":
				changedResult.Outcome = proofevidence.OutcomePartial
			}
			require.ErrorIs(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: changedResult, Snapshot: &changed}), proofevidence.ErrAuthorizationDenied)
		})
	}

	require.NoError(t, f.ledger.Record(t.Context(), grant, observation))
	stored, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, stored.State)
	require.NotContains(t, string(stored.SnapshotCiphertext), "private_email")
	recovered, err := f.ledger.ReadSnapshot(t.Context(), f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, snapshot, recovered.Snapshot)
	require.Equal(t, result.ExpiresAt, recovered.Result.ExpiresAt)
	// A valid encrypted snapshot from a different request cannot be transplanted.
	replacement, err := json.Marshal(struct {
		RequestDigest string                     `json:"request_digest"`
		Snapshot      *proofevidencesvc.Snapshot `json:"snapshot"`
	}{hash([]byte("another-request")), snapshot})
	require.NoError(t, err)
	replacement, err = f.cipher.Encrypt(replacement)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET snapshot_ciphertext=$1 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$2) AND slug=$3`, replacement, f.scope.Organization, stored.Slug)
	require.NoError(t, err)
	_, err = f.ledger.ReadSnapshot(t.Context(), f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET snapshot_ciphertext=$1 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$2) AND slug=$3`, stored.SnapshotCiphertext, f.scope.Organization, stored.Slug)
	require.NoError(t, err)

	require.ErrorIs(t, f.ledger.Record(t.Context(), grant, observation), proofevidence.ErrAuthorizationDenied)
	_, err = f.ledger.ReadSnapshot(t.Context(), "other-tenant", f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
	f.policy.Enabled = false
	f.policy.Version = "disabled-v2"
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	_, err = f.ledger.ReadSnapshot(t.Context(), f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
}

func TestLedgerCancellationKeepsRunningReservationUntilWorkerAcknowledges(t *testing.T) {
	f := newFixture(t)
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	grant, err := f.policy.Authorize(f.request, auth)
	require.NoError(t, err)
	require.NoError(t, f.ledger.Start(t.Context(), grant))
	require.NoError(t, f.ledger.Cancel(t.Context(), f.scope.Organization, f.request.JobID))
	next := f.request
	next.JobID = "next-job"
	_, err = f.ledger.Reserve(t.Context(), next, decision(t, next), f.target)
	require.ErrorIs(t, err, proofevidencesvc.ErrAdmissionLimit)
	result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: proofevidence.OutcomeCanceled, EvidenceLevel: f.request.EvidenceLevel}
	require.ErrorIs(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result}), proofevidence.ErrAuthorizationDenied)
	stored, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, stored.State)
	require.Equal(t, proofevidence.OutcomeCanceled, stored.Result.Outcome)
	_, err = f.ledger.Reserve(t.Context(), next, decision(t, next), f.target)
	require.NoError(t, err)
}

func TestLedgerBindsDispatchAndCompletionToReservedRequest(t *testing.T) {
	f := newFixture(t)
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	for _, scenario := range []string{"Executor", "Target", "Candidate", "Tenant"} {
		t.Run(scenario, func(t *testing.T) {
			request := f.request
			switch scenario {
			case "Executor":
				request.Worker.Identity = "other-executor"
			case "Target":
				request.TargetFingerprint = hash([]byte("other-target"))
			case "Candidate":
				request.Candidate.HeadSHA = strings.Repeat("c", 40)
			case "Tenant":
				request.OrganizationSlug = "other-tenant"
				request.Candidate.OrganizationSlug = request.OrganizationSlug
			}
			grant, err := f.policy.Authorize(request, decision(t, request))
			require.NoError(t, err)
			expected := proofevidence.ErrAuthorizationDenied
			if scenario == "Tenant" {
				expected = proofobservations.ErrNotFound
			}
			require.ErrorIs(t, f.ledger.Start(t.Context(), grant), expected)
			requestDigest, err := request.Digest()
			require.NoError(t, err)
			result := proofevidence.Result{RequestDigest: requestDigest, Outcome: proofevidence.OutcomeDenied, EvidenceLevel: request.EvidenceLevel}
			require.ErrorIs(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result}), expected)
		})
	}
	job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StatePending, job.State)
	grant, err := f.policy.Authorize(f.request, auth)
	require.NoError(t, err)
	result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: proofevidence.OutcomeDenied, EvidenceLevel: f.request.EvidenceLevel}
	require.ErrorIs(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result}), proofevidence.ErrAuthorizationDenied, "completion requires dispatch")
	for _, operation := range []func() error{
		func() error { return f.ledger.Start(t.Context(), grant) },
		func() error { return f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result}) },
	} {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { <-start; results <- operation() })
		}
		close(start)
		wg.Wait()
		close(results)
		accepted := 0
		for err := range results {
			if err == nil {
				accepted++
			} else {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			}
		}
		require.Equal(t, 1, accepted, "concurrent delivery must perform each transition once")
	}
}

func TestLedgerRejectsCompleteEvidenceAfterDispatchDeadline(t *testing.T) {
	f := newFixture(t)
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	grant, err := f.policy.Authorize(f.request, auth)
	require.NoError(t, err)
	require.NoError(t, f.ledger.Start(t.Context(), grant))
	snapshot := &proofevidencesvc.Snapshot{TargetFingerprint: f.request.TargetFingerprint, Metadata: json.RawMessage(`{"fields":[]}`)}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: f.request.EvidenceLevel, EvidenceDigest: hash(raw), ObservedAt: time.Now().UTC(), ExpiresAt: f.request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: 1, MaxConcurrency: 1}}
	require.NoError(t, grant.Accept(result))
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '1 second' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.ErrorIs(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result, Snapshot: snapshot}), proofevidence.ErrAuthorizationDenied)
	job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, job.State)
	require.Equal(t, proofevidence.OutcomeWorkerLost, job.Result.Outcome)
	require.Empty(t, job.SnapshotCiphertext)
}

func TestLedgerExpiryErasesProtectedPayloadBeforeMetadata(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '3 minutes',evidence_retain_until=clock_timestamp()-interval '2 minutes',budget_expires_at=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	expired, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, expired.State)
	require.Equal(t, proofevidence.OutcomeWorkerLost, expired.Result.Outcome)
	require.Empty(t, expired.TargetCiphertext)
	require.Empty(t, expired.SnapshotCiphertext)
	_, err = f.ledger.ReadSnapshot(t.Context(), f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET metadata_retain_until=clock_timestamp()-interval '10 seconds' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
}

func TestPolicyDisableCannotResurrectAnInFlightJob(t *testing.T) {
	f := newFixture(t)
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	grant, err := f.policy.Authorize(f.request, auth)
	require.NoError(t, err)
	require.NoError(t, f.ledger.Start(t.Context(), grant))
	original := f.policy
	f.policy.Enabled = false
	f.policy.Version = "emergency-disable-v2"
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, original, f.request.Worker.Identity))
	require.ErrorIs(t, f.ledger.Start(t.Context(), grant), proofevidence.ErrAuthorizationDenied)
	result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: proofevidence.OutcomeDenied, EvidenceLevel: f.request.EvidenceLevel}
	require.ErrorIs(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result}), proofevidence.ErrAuthorizationDenied)
	job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, job.State)
	require.Equal(t, proofevidence.OutcomeCanceled, job.Result.Outcome)
}

func TestLedgerCompletionRequiresDispatchDespiteExecutorClockSkew(t *testing.T) {
	for _, skew := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		for _, beforeDispatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("BeforeDispatch=%t/Skew=%s", beforeDispatch, skew), func(t *testing.T) {
				f := newFixture(t)
				auth := decision(t, f.request)
				job, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
				require.NoError(t, err)
				grant, err := f.policy.Authorize(f.request, auth)
				require.NoError(t, err)
				observedAt := time.Now().UTC().Add(skew)
				if !beforeDispatch {
					started, err := f.repo.Start(t.Context(), f.pool, job)
					require.NoError(t, err)
					require.True(t, started)
					stored, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
					require.NoError(t, err)
					require.False(t, stored.StartedAt.IsZero())
				}

				snapshot := &proofevidencesvc.Snapshot{TargetFingerprint: f.request.TargetFingerprint, Metadata: json.RawMessage(`{"fields":[]}`)}
				raw, err := json.Marshal(snapshot)
				require.NoError(t, err)
				result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: f.request.EvidenceLevel, EvidenceDigest: hash(raw), ObservedAt: observedAt, ExpiresAt: f.request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: 1, Bytes: int64(len(raw)), MaxConcurrency: 1}}
				err = f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result, Snapshot: snapshot})
				if beforeDispatch {
					require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
					stored, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
					require.NoError(t, err)
					require.Equal(t, proofobservations.StatePending, stored.State)
					require.Empty(t, stored.SnapshotCiphertext)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestLedgerRejectsDifferentObserverBeforeReservation(t *testing.T) {
	f := newFixture(t)
	changed := f.target
	changed.TableID += "-other"
	for _, observer := range []proofevidence.MetadataObserver{nil, changed, hubspot.ContactMetadataObserver{AccountID: 123}} {
		_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), observer)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	}
	_, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
}

func TestLedgerRoundTripsConnectorOwnedMetadata(t *testing.T) {
	serviceAccount, err := os.ReadFile("../../../pkg/testutil/testdata/bigquery-service-account.json")
	require.NoError(t, err)
	bigQueryCredential, err := json.Marshal(map[string]string{"service_account_json": string(serviceAccount)})
	require.NoError(t, err)
	for _, scenario := range []struct {
		name       string
		observer   proofevidence.MetadataObserver
		credential []byte
		responses  []string
	}{
		{
			name:       "BigQuery",
			observer:   bigquery.TableMetadataObserver{ProjectID: "fixture-project", DatasetID: "private_dataset", TableID: "private_table"},
			credential: bigQueryCredential,
			responses:  []string{`{"access_token":"fixture-token","expires_in":3600}`, `{"tableReference":{"projectId":"fixture-project","datasetId":"private_dataset","tableId":"private_table"},"type":"VIEW","schema":{"fields":[{"name":"private_email","type":"STRING"}]}}`},
		},
		{
			name:       "HubSpot",
			observer:   hubspot.ContactMetadataObserver{AccountID: 123},
			credential: []byte(`{"private_app_token":"fixture-token"}`),
			responses:  []string{`{"portalId":123}`, `{"results":[{"name":"private_email","type":"string","label":"discard-this-label"}]}`},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture(t)
			plan, err := scenario.observer.Plan()
			require.NoError(t, err)
			f.request.TargetFingerprint, err = plan.Fingerprint()
			require.NoError(t, err)
			f.request.ConnectorType, f.request.Operation = plan.ConnectorType, plan.Operation
			f.request.Target, f.request.Egress = plan.Target, plan.Egress
			f.policy.Operations[0].ConnectorType = plan.ConnectorType
			f.policy.Operations[0].Operation = plan.Operation
			f.policy.Operations[0].ProviderEndpoints = plan.Egress[1:]
			f.policy.Version = "connector-v2"
			f.request.PolicyVersion = f.policy.Version
			_, err = f.pool.Exec(t.Context(), `UPDATE data_sources SET type=$1 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$2) AND slug='warehouse'`, plan.ConnectorType.String(), f.scope.Organization)
			require.NoError(t, err)
			_, err = f.pool.Exec(t.Context(), `UPDATE credentials SET credential_type=$1 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$2) AND slug='warehouse-credential'`, plan.ConnectorType.String(), f.scope.Organization)
			require.NoError(t, err)
			require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
			auth := decision(t, f.request)
			_, err = f.ledger.Reserve(t.Context(), f.request, auth, scenario.observer)
			require.NoError(t, err)
			grant, err := f.policy.Authorize(f.request, auth)
			require.NoError(t, err)
			require.NoError(t, f.ledger.Start(t.Context(), grant))
			// Database dispatch time is independent of the executor observation clock.
			_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET started_at=clock_timestamp()+interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
			require.NoError(t, err)

			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				call := calls.Add(1) - 1
				if call >= int64(len(scenario.responses)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(scenario.responses[call]))
			}))
			defer server.Close()
			metadata, rows, outcome := scenario.observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), scenario.credential, f.request.Limits)
			require.Equal(t, proofevidence.OutcomeComplete, outcome)
			require.Equal(t, int64(len(plan.Routes)), calls.Load())
			require.NotContains(t, string(metadata), "discard-this-label")
			snapshot := &proofevidencesvc.Snapshot{TargetFingerprint: f.request.TargetFingerprint, Metadata: metadata}
			raw, err := json.Marshal(snapshot)
			require.NoError(t, err)
			result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: outcome, EvidenceLevel: f.request.EvidenceLevel, EvidenceDigest: hash(raw), ObservedAt: time.Now().UTC(), ExpiresAt: f.request.EvidenceRetainUntil, Usage: proofevidence.Usage{Requests: calls.Load(), Bytes: int64(len(raw)), Rows: rows, MaxConcurrency: 1}}
			require.NoError(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result, Snapshot: snapshot}))
			recovered, err := f.ledger.ReadSnapshot(t.Context(), f.scope.Organization, f.request.JobID)
			require.NoError(t, err)
			require.Equal(t, snapshot, recovered.Snapshot)
			require.Equal(t, rows, recovered.Result.Usage.Rows)
		})
	}
}

func TestInactiveDataSourceDeniesNewReservation(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE data_sources SET is_active=false WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, f.request.DataSourceSlug)
	require.NoError(t, err)
	job, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
	require.Nil(t, job)
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
}

func TestPolicyEditsRequireNewVersion(t *testing.T) {
	f := newFixture(t)
	for _, scenario := range []string{"Policy", "Worker", "Enabled"} {
		t.Run(scenario, func(t *testing.T) {
			policy, worker := f.policy, f.request.Worker.Identity
			switch scenario {
			case "Policy":
				policy.MaxAuthorizationTTL += time.Minute
			case "Worker":
				worker = "other-executor"
			case "Enabled":
				policy.Enabled = false
			}
			require.ErrorIs(t, f.ledger.SetPolicy(t.Context(), f.scope, policy, worker), proofevidence.ErrAuthorizationDenied)
		})
	}
	stored, err := f.repo.LockPolicy(t.Context(), f.pool, f.scope)
	require.NoError(t, err)
	require.Equal(t, f.policy, stored.Value)
	require.Equal(t, f.request.Worker.Identity, stored.WorkerIdentity)
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
}

func TestDeletedEnvironmentDeniesAdmissionButStillAllowsCleanup(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE environments SET deleted_at=clock_timestamp() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, f.scope.Environment)
	require.NoError(t, err)
	f.policy.Version = "deleted-v2"
	require.ErrorIs(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity), proofobservations.ErrNotFound)
	next := f.request
	next.JobID = "after-delete"
	_, err = f.ledger.Reserve(t.Context(), next, decision(t, next), f.target)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '3 minutes',evidence_retain_until=clock_timestamp()-interval '2 minutes',budget_expires_at=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, job.State)
	require.Empty(t, job.TargetCiphertext)
}

func TestLedgerCleanupStaysWithinLockedEnvironment(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	otherScope := f.scope
	otherScope.Environment = "sandbox"
	_, err = f.pool.Exec(t.Context(), `WITH environment AS (
 INSERT INTO environments(organization_id,slug,name) SELECT id,'sandbox','Sandbox' FROM organizations WHERE slug=$1 RETURNING id,organization_id
 ) INSERT INTO project_environments(organization_id,project_id,environment_id,is_default) SELECT p.organization_id,p.id,e.id,false FROM projects p JOIN environment e ON e.organization_id=p.organization_id WHERE p.slug='etl'`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.ledger.SetPolicy(t.Context(), otherScope, f.policy, f.request.Worker.Identity))
	candidates := proofcandidates.New()
	mapping, err := candidates.CreateMapping(t.Context(), f.pool, proofcandidates.CreateMappingParams{OrganizationSlug: f.scope.Organization, ProjectSlug: "etl", EnvironmentSlug: "sandbox", ProviderConnectionSlug: "github", Provider: "github", ProviderConnectionID: f.connectionID, RepositoryID: "42", TargetRef: "refs/heads/main", PipelinePath: "pipelines/contacts.yaml"})
	require.NoError(t, err)
	_, err = candidates.Create(t.Context(), f.pool, f.scope.Organization, proofcandidates.CreateCandidateParams{MappingID: mapping, Slug: "candidate-2", CandidateKey: f.scope.Organization.String() + "-sandbox", ChangeRequestID: "42", SourceRef: "feature", HeadSHA: f.request.Candidate.HeadSHA, SourceSHA256: hash([]byte("fixture")), SpecHash: f.request.Candidate.SpecHash, CanonicalSpec: []byte("{}")})
	require.NoError(t, err)
	other := f.request
	other.EnvironmentSlug = "sandbox"
	other.Candidate.EnvironmentSlug = "sandbox"
	other.CandidateSlug = "candidate-2"
	other.JobID = "sandbox-job"
	_, err = f.ledger.Reserve(t.Context(), other, decision(t, other), f.target)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '3 minutes',evidence_retain_until=clock_timestamp()-interval '2 minutes',budget_expires_at=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	untouched, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, other.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StatePending, untouched.State)
	require.NotEmpty(t, untouched.TargetCiphertext)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), otherScope))
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET metadata_retain_until=clock_timestamp()-interval '10 seconds' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
	untouched, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, other.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, untouched.State)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), otherScope))
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, other.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
}

func TestDispatchAuditIsAtomicAndNotRepeated(t *testing.T) {
	for _, begin := range []bool{false, true} {
		t.Run(fmt.Sprintf("BeginExecution=%t", begin), func(t *testing.T) {
			f := newFixture(t)
			_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
			require.NoError(t, err)
			ledger, err := proofevidencesvc.NewLedger(postgres.NewTransactionManager(f.pool), f.repo, f.cipher, f.retentionAudit)
			require.NoError(t, err)
			grant, err := f.policy.Authorize(f.request, decision(t, f.request))
			require.NoError(t, err)
			dispatch := func() error {
				if begin {
					_, err := ledger.BeginExecution(t.Context(), f.scope.Organization, f.request.JobID)
					return err
				}
				return ledger.Start(t.Context(), grant)
			}
			f.retentionAudit.failAction = model.EventActionExecute
			err = dispatch()
			require.ErrorContains(t, err, "Proof execution audit unavailable")
			require.NotContains(t, err.Error(), "private-retention-audit-diagnostic")
			job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
			require.NoError(t, err)
			require.Equal(t, proofobservations.StatePending, job.State)
			require.True(t, job.StartedAt.IsZero())
			f.retentionAudit.failAction = model.EventActionUnknown
			require.NoError(t, dispatch())
			require.ErrorIs(t, dispatch(), proofevidence.ErrAuthorizationDenied)
			var count int
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='execute' AND event_class='security_audit' AND actor_type='system' AND metadata->>'proof_observation_job_slug'=$2 AND metadata->>'request_digest'=$3`, f.scope.Organization, f.request.JobID, decision(t, f.request).RequestDigest).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestCompletionAuditIsAtomicAndRecordsPersistedOutcome(t *testing.T) {
	for _, outcome := range []proofevidence.Outcome{proofevidence.OutcomeComplete, proofevidence.OutcomeDenied, proofevidence.OutcomeCanceled, proofevidence.OutcomeTimedOut} {
		t.Run(outcome.String(), func(t *testing.T) {
			f := newFixture(t)
			auth := decision(t, f.request)
			_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
			require.NoError(t, err)
			grant, err := f.policy.Authorize(f.request, auth)
			require.NoError(t, err)
			require.NoError(t, f.ledger.Start(t.Context(), grant))
			expectedOutcome := outcome
			observation := proofevidencesvc.Observation{Result: proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: outcome, EvidenceLevel: f.request.EvidenceLevel}}
			switch outcome {
			case proofevidence.OutcomeComplete:
				observation.Snapshot = &proofevidencesvc.Snapshot{TargetFingerprint: f.request.TargetFingerprint, Metadata: json.RawMessage(`{"table_type":"VIEW"}`)}
				raw, err := json.Marshal(observation.Snapshot)
				require.NoError(t, err)
				observation.Result.EvidenceDigest = hash(raw)
				observation.Result.ObservedAt = time.Now().UTC()
				observation.Result.ExpiresAt = f.request.EvidenceRetainUntil
				observation.Result.Usage = proofevidence.Usage{Requests: 1, MaxConcurrency: 1, Rows: 1, Bytes: int64(len(raw))}
			case proofevidence.OutcomeCanceled:
				require.NoError(t, f.ledger.Cancel(t.Context(), f.scope.Organization, f.request.JobID))
			case proofevidence.OutcomeTimedOut:
				expectedOutcome = proofevidence.OutcomeWorkerLost
				_, err := f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '1 second' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, f.request.JobID)
				require.NoError(t, err)
			default:
				require.Equal(t, proofevidence.OutcomeDenied, outcome)
			}
			ledger, err := proofevidencesvc.NewLedger(postgres.NewTransactionManager(f.pool), f.repo, f.cipher, f.retentionAudit)
			require.NoError(t, err)
			f.retentionAudit.failAction = model.EventActionAcknowledge
			err = ledger.Record(t.Context(), grant, observation)
			require.ErrorContains(t, err, "Proof execution audit unavailable")
			require.NotContains(t, err.Error(), "private-retention-audit-diagnostic")
			job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
			require.NoError(t, err)
			require.Equal(t, proofobservations.StateRunning, job.State)
			require.Empty(t, job.SnapshotCiphertext)
			f.retentionAudit.failAction = model.EventActionUnknown
			err = ledger.Record(t.Context(), grant, observation)
			if outcome == proofevidence.OutcomeCanceled || outcome == proofevidence.OutcomeTimedOut {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			} else {
				require.NoError(t, err)
			}
			require.ErrorIs(t, ledger.Record(t.Context(), grant, observation), proofevidence.ErrAuthorizationDenied)
			job, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
			require.NoError(t, err)
			require.Equal(t, proofobservations.StateFinished, job.State)
			require.Equal(t, expectedOutcome, job.Result.Outcome)
			var records []map[string]any
			rows, err := f.pool.Query(t.Context(), `SELECT metadata FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='acknowledge' AND event_class='security_audit' AND actor_type='system'`, f.scope.Organization)
			require.NoError(t, err)
			for rows.Next() {
				var metadata map[string]any
				require.NoError(t, rows.Scan(&metadata))
				records = append(records, metadata)
			}
			require.NoError(t, rows.Err())
			rows.Close()
			require.Equal(t, []map[string]any{{"project_slug": f.scope.Project.String(), "environment_slug": f.scope.Environment.String(), "proof_observation_job_slug": f.request.JobID.String(), "request_digest": auth.RequestDigest, "observation_outcome": expectedOutcome.String()}}, records)
		})
	}
}
