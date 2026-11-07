package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/worker"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

type retentionAuditRecorder struct {
	eventrecorder.Recorder
	failAction          model.EventAction
	failAfter, recorded int
}

func (r *retentionAuditRecorder) Record(ctx context.Context, org slug.Slug, actor model.EventActor, event eventrecorder.Event) (*model.Event, error) {
	record, err := event.EventRecord()
	if err != nil {
		return nil, err
	}
	if record.Action == r.failAction || r.failAfter > 0 && r.recorded == r.failAfter {
		return nil, xerror.Unexpected().Detail("private-retention-audit-diagnostic")
	}
	created, err := r.Recorder.Record(ctx, org, actor, event)
	if err == nil {
		r.recorded++
	}
	return created, err
}

func TestRetentionAuditCommitsWithErasureAndSurvivesMetadataDeletion(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	_, err = f.ledger.BeginExecution(t.Context(), f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '4 minutes',evidence_retain_until=clock_timestamp()-interval '3 minutes',budget_expires_at=clock_timestamp()-interval '2 minutes' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	f.retentionAudit.failAction = model.EventActionPurge
	err = f.maintenance.Cleanup(t.Context(), f.scope)
	require.ErrorContains(t, err, "Proof retention audit unavailable")
	require.NotContains(t, err.Error(), "private-retention-audit-diagnostic")
	job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, job.State)
	require.NotEmpty(t, job.TargetCiphertext)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&count))
	require.Equal(t, 2, count, "dispatch and expiry audits survive a later erasure failure")
	f.retentionAudit.failAction = model.EventActionUnknown
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	job, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, job.State)
	require.Empty(t, job.TargetCiphertext)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET metadata_retain_until=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	f.retentionAudit.failAction = model.EventActionDelete
	require.Error(t, f.maintenance.Cleanup(t.Context(), f.scope))
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err, "an audit failure cannot silently delete the remaining metadata")
	f.retentionAudit.failAction = model.EventActionUnknown
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
	rows, err := f.pool.Query(t.Context(), `SELECT event_class,actor_type,action,metadata FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) ORDER BY id`, f.scope.Organization)
	require.NoError(t, err)
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var class, actor, action string
		var raw []byte
		require.NoError(t, rows.Scan(&class, &actor, &action, &raw))
		require.Equal(t, "security_audit", class)
		require.Equal(t, "system", actor)
		var metadata map[string]any
		require.NoError(t, json.Unmarshal(raw, &metadata))
		digest, err := f.request.Digest()
		require.NoError(t, err)
		require.Equal(t, map[string]any{"project_slug": f.scope.Project.String(), "environment_slug": f.scope.Environment.String(), "proof_observation_job_slug": f.request.JobID.String(), "request_digest": digest}, metadata)
		actions = append(actions, action)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"execute", "expire", "purge", "delete"}, actions)
}

func TestRetentionBatchesAllJobsAndOmitsMalformedStoredDigests(t *testing.T) {
	f := newFixture(t)
	reserveObservationBatch(t, f)
	_, err := f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET request_digest='private-corrupt-digest',deadline_at=clock_timestamp()-interval '4 minutes',evidence_retain_until=clock_timestamp()-interval '3 minutes',budget_expires_at=clock_timestamp()-interval '2 minutes',metadata_retain_until=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	scope, err := f.repo.LockMaintenanceScope(t.Context(), tx, f.scope)
	require.NoError(t, err)
	batch, err := f.repo.Expire(t.Context(), tx, scope.OrganizationID, scope.ProjectEnvironmentID)
	require.NoError(t, err)
	require.Len(t, batch, 100)
	require.Equal(t, "private-corrupt-digest", batch[0].RequestDigest, "audit sanitation belongs in Go")
	require.NoError(t, tx.Rollback(t.Context()))
	f.retentionAudit.failAfter = 100
	require.Error(t, f.maintenance.Cleanup(t.Context(), f.scope))
	var finished, auditCount int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND state='finished'`, f.scope.Organization).Scan(&finished))
	require.Equal(t, 100, finished)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&auditCount))
	require.Equal(t, 100, auditCount)
	f.retentionAudit.failAfter = 0
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	var remaining, total, leaked int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE metadata ? 'request_digest' OR metadata::text LIKE '%private-%') FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&total, &leaked))
	require.Equal(t, 303, total)
	require.Zero(t, leaked)
}

func TestPeriodicProofMaintenanceErasesExpiredPayloadsAfterDisable(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	execution, err := f.ledger.BeginExecution(t.Context(), f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	grant, err := execution.Policy.Resume(execution.Request, execution.Authorization, execution.Deadline)
	require.NoError(t, err)
	{
		err := environments.NewRepository().Delete(t.Context(), f.pool, f.scope.Organization, f.scope.Environment, &environments.DeleteParams{})
		require.NoError(t, err)
	}
	require.Error(t, f.ledger.CheckExecution(t.Context(), grant), "deleted environments deny execution before the next sweep")
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '3 minutes',evidence_retain_until=clock_timestamp()-interval '2 minutes',budget_expires_at=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	registrar, err := worker.NewProofObservationMaintenanceRegistrar(f.maintenance, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	// Other packages run real consumers on the same database; this worker must not claim their jobs.
	schema := fmt.Sprintf("proof_river_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = f.pool.Exec(t.Context(), "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := f.pool.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		require.NoError(t, err)
	})
	driver := riverpgxv5.New(f.pool)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: schema, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	workers := river.NewWorkers()
	require.NoError(t, registrar.Register(workers))
	periodicJobs, err := registrar.PeriodicJobs()
	require.NoError(t, err)
	client, err := river.NewClient(driver, &river.Config{Schema: schema, Queues: map[string]river.QueueConfig{jobs.EventMaintenanceQueue: {MaxWorkers: 1}}, Workers: workers, PeriodicJobs: periodicJobs, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	// Finish the asynchronous worker phase before reusing its audit fixture directly.
	func() {
		require.NoError(t, client.Start(t.Context()))
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, client.Stop(ctx))
		}()
		require.Eventually(t, func() bool {
			job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
			return err == nil && job.State == proofobservations.StateFinished && len(job.TargetCiphertext) == 0
		}, 5*time.Second, 10*time.Millisecond, "the registered run-on-start periodic job must erase the protected target")
	}()
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET metadata_retain_until=clock_timestamp()-interval '10 seconds' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Run(t.Context()))
	_, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.ErrorIs(t, err, proofobservations.ErrNotFound)
	require.NoError(t, f.maintenance.Run(t.Context()), "repeated maintenance is idempotent")
}

func TestMaintenanceCleanupIsEnvironmentScoped(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	other := f.scope
	other.Environment = "alternate"
	_, err = f.pool.Exec(t.Context(), `WITH environment AS (INSERT INTO environments(organization_id,slug,name) SELECT id,'alternate','Alternate' FROM organizations WHERE slug=$1 RETURNING id,organization_id) INSERT INTO project_environments(organization_id,project_id,environment_id,is_default) SELECT p.organization_id,p.id,e.id,false FROM environment e JOIN projects p ON p.organization_id=e.organization_id WHERE p.slug=$2`, f.scope.Organization, f.scope.Project)
	require.NoError(t, err)
	mapping, err := proofcandidates.New().CreateMapping(t.Context(), f.pool, proofcandidates.CreateMappingParams{OrganizationSlug: other.Organization, ProjectSlug: other.Project, EnvironmentSlug: other.Environment, ProviderConnectionSlug: "github", Provider: "github", ProviderConnectionID: f.connectionID, RepositoryID: "42", TargetRef: "refs/heads/main", PipelinePath: "pipelines/contacts.yaml"})
	require.NoError(t, err)
	_, err = proofcandidates.New().Create(t.Context(), f.pool, other.Organization, proofcandidates.CreateCandidateParams{MappingID: mapping, Slug: "alternate-candidate", CandidateKey: other.Organization.String() + "-alternate", ChangeRequestID: "42", SourceRef: "feature", HeadSHA: f.request.Candidate.HeadSHA, SourceSHA256: hash([]byte("fixture")), SpecHash: f.request.Candidate.SpecHash, CanonicalSpec: []byte("{}")})
	require.NoError(t, err)
	request := f.request
	request.JobID = slug.Slug("alternate-job")
	request.EnvironmentSlug = other.Environment
	request.Candidate.EnvironmentSlug = other.Environment
	request.CandidateSlug = "alternate-candidate"
	{
		err := f.ledger.SetPolicy(t.Context(), other, f.policy, f.request.Worker.Identity)
		require.NoError(t, err)
	}
	_, err = f.ledger.Reserve(t.Context(), request, decision(t, request), f.target)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '3 minutes',evidence_retain_until=clock_timestamp()-interval '2 minutes',budget_expires_at=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	untouched, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StatePending, untouched.State)
	require.NotEmpty(t, untouched.TargetCiphertext)
	// Retention does not depend on decoding an operational policy document.
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_policies SET policy='{"invalid":"private-policy-diagnostic"}'::jsonb WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Run(t.Context()))
	cleaned, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished, cleaned.State)
	require.Empty(t, cleaned.TargetCiphertext)
}

func reserveObservationBatch(t *testing.T, f *fixture) {
	t.Helper()
	f.policy.Version = "retention-batch"
	f.request.PolicyVersion = f.policy.Version
	f.policy.Operations[0].Limits.MaxConcurrency = 101
	f.policy.Operations[0].Limits.MaxRequests *= 101
	f.policy.Operations[0].Limits.MaxBytes *= 101
	f.policy.Operations[0].Limits.MaxRows *= 101
	{
		err := f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity)
		require.NoError(t, err)
	}
	for i := range 101 {
		r := f.request
		r.JobID = slug.Slug(fmt.Sprintf("retention-%03d", i))
		_, err := f.ledger.Reserve(t.Context(), r, decision(t, r), f.target)
		require.NoError(t, err)
	}
}

func TestMaintenanceCancellationCommitsBoundedAuditedProgress(t *testing.T) {
	f := newFixture(t)
	reserveObservationBatch(t, f)
	_, err := f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET request_digest='private-corrupt-digest' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	// Simulate an out-of-band policy revocation that the keyless sweep repairs.
	f.policy.Enabled = false
	f.policy.Version = "revoked-batch"
	_, err = f.repo.SavePolicy(t.Context(), f.pool, f.scope, f.policy, f.request.Worker.Identity)
	require.NoError(t, err)
	f.retentionAudit.failAfter = 100
	err = f.maintenance.Cleanup(t.Context(), f.scope)
	require.ErrorContains(t, err, "Proof retention audit unavailable")
	var canceled, records int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND canceled_at IS NOT NULL`, f.scope.Organization).Scan(&canceled))
	require.Equal(t, 100, canceled)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='revoke'`, f.scope.Organization).Scan(&records))
	require.Equal(t, 100, records)
	f.retentionAudit.failAfter = 0
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	var invalid int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE event_class<>'security_audit' OR actor_type<>'system' OR metadata ? 'request_digest' OR metadata::text LIKE '%private-%') FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='revoke'`, f.scope.Organization).Scan(&records, &invalid))
	require.Equal(t, 101, records)
	require.Zero(t, invalid)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND canceled_at IS NOT NULL`, f.scope.Organization).Scan(&canceled))
	require.Equal(t, 101, canceled)
}

func TestPolicyRevocationRollsBackWhenAuditFails(t *testing.T) {
	f := newFixture(t)
	_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	ledger, err := proofevidencesvc.NewLedger(postgres.NewTransactionManager(f.pool), f.repo, f.cipher, f.retentionAudit)
	require.NoError(t, err)
	disabled := f.policy
	disabled.Enabled, disabled.Version = false, "revoked-with-audit"
	f.retentionAudit.failAction = model.EventActionRevoke
	err = ledger.SetPolicy(t.Context(), f.scope, disabled, f.request.Worker.Identity)
	require.ErrorContains(t, err, "Proof revocation audit unavailable")
	require.NotContains(t, err.Error(), "private-retention-audit-diagnostic")
	current, err := ledger.Policy(t.Context(), f.scope)
	require.NoError(t, err)
	require.Equal(t, f.policy, current.Value)
	job, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StatePending, job.State)
	f.retentionAudit.failAction = model.EventActionUnknown
	require.NoError(t, ledger.SetPolicy(t.Context(), f.scope, disabled, f.request.Worker.Identity))
	job, err = f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofevidence.OutcomeCanceled, job.Result.Outcome)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='revoke'`, f.scope.Organization).Scan(&count))
	require.Equal(t, 1, count)
}
