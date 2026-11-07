package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/runrecency"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/runrecencysvc"
	"github.com/stretchr/testify/require"
)

type failingRecencyNotifications struct{ notificationsvc.Service }

func (f failingRecencyNotifications) CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	return nil, xerror.Unexpected().Detail("synthetic candidate handoff failure")
}

func TestRunRecencyDurableEpisodesAndEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "run recency")
	org := uniqueNotificationE2ESlug("recency")
	project, env := slug.Slug("ops"), slug.Slug("prod")
	seedAlertPolicyScopeRows(t, ctx, pool, org, project, env, "unused")
	enableProjectEnvironment(t, ctx, pool, org, project, env)
	_, err := pool.Exec(ctx, `INSERT INTO pipeline_specs (project_id,slug,name,spec_jsonb,spec_hash,source_path,source_ref,last_applied_commit)
 SELECT p.id,'orders','Orders','{}','recency','pipelines/orders.json','refs/heads/main','0000000000000000000000000000000000000000' FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, org)
	require.NoError(t, err)
	var version, deployment int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_versions (project_id,pipeline_slug,spec_jsonb,spec_hash) SELECT p.id,'orders','{}','recency' FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 RETURNING id`, org).Scan(&version))
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_deployments (project_id,project_environment_id,pipeline_slug,current_version_id,deployed_by_actor_type,deployed_at) SELECT pe.project_id,pe.id,'orders',$2,'system',$3 FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 RETURNING id`, org, version, at).Scan(&deployment))
	txm := postgres.NewTransactionManager(pool)
	repo := runrecency.New()
	deliveries := &fakeDeliveryProducer{}
	notifications := notificationsvc.New(txm, reponotifications.New(), testutil.FakeClock{NowFn: func() time.Time { return at }}, deliveries, &testutil.FakeEmailProducer{}, notificationsvc.LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}), notificationsvc.ConnectionProviders{}, "https://app.example")
	svc := runrecencysvc.New(txm, repo, notifications)
	scope := runrecency.Scope{Org: org, Project: project, Environment: env, Pipeline: "orders"}
	kind, source, action := model.NotificationTypePipelineFreshnessSLABreached, model.EventEntityTypePipeline, model.EventActionExecute
	_, err = notifications.CreateAlertPolicy(ctx, notificationE2EPrincipal(), notificationsvc.CreateAlertPolicyParams{
		OrgSlug: org, Slug: "recency", Name: "Recency", Scope: model.AlertPolicyScopeProjectEnvironment,
		ProjectSlug: &project, EnvironmentSlug: &env, NotificationType: &kind,
		SourceKind: &source, SourceAction: &action, SourceRef: &scope.Pipeline, DestinationSlugs: []slug.Slug{"unused"},
	})
	require.NoError(t, err)
	initial, err := svc.Inspect(ctx, scope, at)
	require.NoError(t, err)
	require.Zero(t, initial.Expectation.Revision)
	require.Equal(t, model.RunRecencyOutcomeUnavailable, initial.Outcome)
	require.Equal(t, "not_configured", *initial.UnavailableReason)
	expectation, err := svc.Update(ctx, scope, 0, time.Hour, true, at)
	require.NoError(t, err)
	require.EqualValues(t, 1, expectation.Revision)
	_, err = svc.Update(ctx, scope, 0, time.Hour, true, at)
	require.ErrorIs(t, err, runrecencysvc.ErrRevisionConflict)
	require.NoError(t, svc.Evaluate(ctx, at.Add(time.Hour-time.Nanosecond)))
	count := func(table string) int {
		var n int
		// Only fixed table names supplied by this isolated fixture.
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` x JOIN project_environments pe ON pe.id=x.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, org).Scan(&n))
		return n
	}
	records := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notification_records n JOIN organizations o ON o.id=n.organization_id WHERE o.slug=$1 AND notification_type='pipeline.freshness_sla_breached'`, org).Scan(&n))
		return n
	}
	require.Zero(t, records())
	// Episode and generic notification must either both persist or both roll back.
	failing := runrecencysvc.New(txm, repo, failingRecencyNotifications{notifications})
	require.Error(t, failing.Evaluate(ctx, at.Add(time.Hour)))
	require.Zero(t, records())
	var episodes int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_recency_episodes WHERE expectation_id=$1`, expectation.ID).Scan(&episodes))
	require.Zero(t, episodes)
	require.NoError(t, svc.Evaluate(ctx, at.Add(time.Hour)))
	require.NoError(t, runrecencysvc.New(txm, repo, notifications).Evaluate(ctx, at.Add(70*time.Minute)))
	require.Equal(t, 1, records())
	breached, err := svc.Inspect(ctx, scope, at.Add(70*time.Minute))
	require.NoError(t, err)
	require.Equal(t, model.RunRecencyOutcomeBreached, breached.Outcome)
	require.EqualValues(t, 1, breached.Expectation.DeploymentRevision)
	require.Equal(t, "recency", *breached.Expectation.VersionSpecHash)
	require.Len(t, breached.Episodes, 1)
	require.EqualValues(t, 1, breached.Episodes[0].ExpectationRevision)
	require.Equal(t, []notificationsvc.NotificationDeliveryJobParams{{OrgSlug: org, RecordSlug: breached.Episodes[0].Slug, DestinationSlug: "unused", AlertPolicySlug: "recency"}}, deliveries.calls)
	revision := int64(1)
	runRepo := pipelineruns.New()
	capture := runrecencysvc.NewSuccessProducer(repo, testutil.FakeRunSucceededNotifier{})
	store := pipelineruntime.NewRunStore(txm, pool, clock.New(), capture, pipelineruns.New())
	persist := func(run string, origin model.PipelineRunSourceType, when time.Time, revision *int64, status string) error {
		source := pipelinerun.RunSourceSummary{Type: origin}
		_, err := runRepo.CreateRun(ctx, pool, org, project, env, &pipelineruns.CreateRunParams{DeploymentID: &deployment, DeploymentRevision: revision, PipelineVersionID: version, PipelineSlug: "orders", RunSlug: run, Status: "queued", StartedAt: when, Timeline: []byte(`{}`), IncrementalDiagnostics: []byte(`{}`), Source: source})
		if err != nil {
			return err
		}
		return store.PersistRun(ctx, org, project, env, &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}}, &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{RunSlug: run, PipelineVersionID: version, Status: status, Source: source}, Timeline: pipelinerun.RunTimeline{RunSlug: run, PipelineSlug: "orders", Status: status, Events: []pipelinerun.TimelineEvent{{At: when, Type: "run_completed", Status: status}}}})
	}
	for _, origin := range []model.PipelineRunSourceType{model.PipelineRunSourceTypeManual, model.PipelineRunSourceTypeAPI, model.PipelineRunSourceTypeSchedule, model.PipelineRunSourceTypeWebhook} {
		require.NoError(t, persist("success-"+origin.String(), origin, at.Add(75*time.Minute), &revision, "succeeded"))
	}

	// Synchronous schedules/webhooks do not require an accepted queued row.
	for _, origin := range []model.PipelineRunSourceType{model.PipelineRunSourceTypeManual, model.PipelineRunSourceTypeAPI, model.PipelineRunSourceTypeSchedule, model.PipelineRunSourceTypeWebhook} {
		run := "sync-" + origin.String()
		when := at.Add(75 * time.Minute)
		require.NoError(t, store.PersistRun(ctx, org, project, env, &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}}, &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{DeploymentID: &deployment, DeploymentRevision: &revision, RunSlug: run, PipelineVersionID: version, Status: "succeeded", Source: pipelinerun.RunSourceSummary{Type: origin}}, Timeline: pipelinerun.RunTimeline{RunSlug: run, PipelineSlug: "orders", Status: "succeeded", Events: []pipelinerun.TimelineEvent{{At: when, Type: "run_completed", Status: "succeeded"}}}}))
	}
	require.Equal(t, 1, count("pipeline_run_recency_successes"))
	require.NoError(t, svc.Evaluate(ctx, at.Add(80*time.Minute)))
	var active int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notification_records n JOIN organizations o ON o.id=n.organization_id WHERE o.slug=$1 AND notification_type='pipeline.freshness_sla_breached' AND lifecycle_state!='resolved'`, org).Scan(&active))
	require.Zero(t, active)
	recovered, err := svc.Inspect(ctx, scope, at.Add(80*time.Minute))
	require.NoError(t, err)
	require.Equal(t, model.RunRecencyOutcomeHealthy, recovered.Outcome)
	require.Equal(t, "qualifying_success", *recovered.Episodes[0].ResolutionReason)
	require.NotNil(t, recovered.Episodes[0].RecoveryRun)
	require.WithinDuration(t, at.Add(75*time.Minute), *recovered.Episodes[0].RecoverySucceededAt, time.Microsecond)
	// Run retention cannot erase the compact successful-run evidence.
	_, err = pool.Exec(ctx, `DELETE FROM pipeline_runs r USING project_environments pe,projects p,organizations o WHERE r.project_environment_id=pe.id AND pe.project_id=p.id AND p.organization_id=o.id AND o.slug=$1`, org)
	require.NoError(t, err)
	require.Equal(t, 1, count("pipeline_run_recency_successes"))
	require.NoError(t, svc.Evaluate(ctx, at.Add(135*time.Minute)))
	require.Equal(t, 2, records())
	// Same version, new deployment revision: old success cannot qualify it.
	_, err = pool.Exec(ctx, `UPDATE pipeline_deployments SET revision=2,deployed_at=$2 WHERE id=$1`, deployment, at.Add(140*time.Minute))
	require.NoError(t, err)
	require.NoError(t, svc.Evaluate(ctx, at.Add(150*time.Minute)))
	require.NoError(t, persist("old-deployment-late", model.PipelineRunSourceTypeAPI, at.Add(180*time.Minute), &revision, "succeeded"))
	require.NoError(t, svc.Evaluate(ctx, at.Add(200*time.Minute)))
	require.Equal(t, 3, records())
	// Historical unknown identity must stay unknown, never use current deployment.
	require.NoError(t, persist("unknown-deployment", model.PipelineRunSourceTypeManual, at.Add(201*time.Minute), nil, "succeeded"))
	require.Equal(t, 1, count("pipeline_run_recency_successes"))

	// A failed downstream handoff rolls back both the terminal row and newly captured evidence.
	originalStore := store
	store = pipelineruntime.NewRunStore(txm, pool, clock.New(), runrecencysvc.NewSuccessProducer(repo, testutil.FakeRunSucceededNotifier{CandidateFn: func(context.Context, postgres.DBTX, model.NotificationCandidate) error {
		return xerror.Unexpected().Detail("synthetic downstream failure")
	}}), pipelineruns.New())
	require.Error(t, persist("rolled-back-success", model.PipelineRunSourceTypeManual, at.Add(201*time.Minute), &revision, "succeeded"))
	store = originalStore
	var rolledBackStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_runs WHERE deployment_id=$1 AND run_slug='rolled-back-success'`, deployment).Scan(&rolledBackStatus))
	require.Equal(t, "queued", rolledBackStatus)
	// Non-success outcomes and a rolled-back successful completion cannot move evidence.
	for _, status := range []string{"failed", "canceled", "running", "queued", "partial"} {
		require.NoError(t, persist("excluded-"+status, model.PipelineRunSourceTypeManual, at.Add(201*time.Minute), &revision, status))
	}
	var lastRun string
	require.NoError(t, pool.QueryRow(ctx, `SELECT run_slug FROM pipeline_run_recency_successes WHERE deployment_id=$1 AND deployment_revision=1`, deployment).Scan(&lastRun))
	require.Equal(t, "old-deployment-late", lastRun)
	// Mutating admission identity after acceptance must fail even before completion.
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET deployment_revision=99 WHERE run_slug='excluded-queued' AND deployment_id=$1`, deployment)
	require.Error(t, err)
	// The first episode still refers to expectation revision 1 and deployment revision 1.
	var historicalRevision, historicalDeploymentRevision int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT expectation.revision,episode.deployment_revision FROM pipeline_run_recency_episodes episode JOIN pipeline_run_recency_expectations expectation ON expectation.id=episode.expectation_id WHERE expectation.id=$1 ORDER BY episode.id LIMIT 1`, expectation.ID).Scan(&historicalRevision, &historicalDeploymentRevision))
	require.EqualValues(t, 1, historicalRevision)
	require.EqualValues(t, 1, historicalDeploymentRevision)
	disabled, err := svc.Update(ctx, scope, 1, time.Hour, false, at.Add(202*time.Minute))
	require.NoError(t, err)
	require.NoError(t, svc.Evaluate(ctx, at.Add(203*time.Minute)))
	require.EqualValues(t, 2, disabled.Revision)
	_, err = svc.Update(ctx, scope, 2, time.Hour, true, at.Add(210*time.Minute))
	require.NoError(t, err)
	require.NoError(t, svc.Evaluate(ctx, at.Add(269*time.Minute)))
	require.Equal(t, 3, records())
	require.NoError(t, svc.Evaluate(ctx, at.Add(270*time.Minute)))
	require.Equal(t, 4, records())

	// Removing the deployment retires the matching episode without inventing recovery.
	_, err = pool.Exec(ctx, `DELETE FROM pipeline_deployments WHERE id=$1`, deployment)
	require.NoError(t, err)
	require.NoError(t, svc.Evaluate(ctx, at.Add(271*time.Minute)))
	var removedReason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT resolution_reason FROM pipeline_run_recency_episodes episode JOIN pipeline_run_recency_expectations expectation ON expectation.id=episode.expectation_id WHERE expectation.project_environment_id=$1 ORDER BY episode.id DESC LIMIT 1`, expectation.EnvironmentID).Scan(&removedReason))
	require.Equal(t, "deployment_unavailable", removedReason)
	// An unowned tenant with identical slugs cannot read or write this expectation.
	other := uniqueNotificationE2ESlug("recency-other")
	seedAlertPolicyScopeRows(t, ctx, pool, other, project, env, "unused")
	enableProjectEnvironment(t, ctx, pool, other, project, env)
	otherScope := scope
	otherScope.Org = other
	_, err = svc.Inspect(ctx, otherScope, at)
	require.Error(t, err)
	foreignEpisodes, err := repo.InspectEpisodes(ctx, pool, otherScope)
	require.NoError(t, err)
	require.Empty(t, foreignEpisodes)
	historical, err := runrecencysvc.New(txm, repo, notifications).Inspect(ctx, scope, at.Add(272*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "deployment_unavailable", *historical.UnavailableReason)
	require.Len(t, historical.Episodes, 4)
	require.EqualValues(t, 1, historical.Episodes[3].DeploymentRevision)
	require.EqualValues(t, 1, historical.Episodes[3].ExpectationRevision)
	require.Equal(t, "recency", historical.Episodes[3].VersionSpecHash)
	require.Equal(t, "qualifying_success", *historical.Episodes[3].ResolutionReason)
	_, err = pool.Exec(ctx, `UPDATE pipeline_specs ps SET deleted_at=$2 FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE ps.project_id=p.id AND o.slug=$1 AND ps.slug='orders'`, org, at.Add(273*time.Minute))
	require.NoError(t, err)
	_, err = svc.Inspect(ctx, scope, at.Add(273*time.Minute))
	require.ErrorIs(t, err, xerror.NotFound(xerror.Pipeline))
	retained, err := repo.InspectEpisodes(ctx, pool, scope)
	require.NoError(t, err)
	require.Len(t, retained, 4, "deleting a pipeline preserves recorded episode evidence")
	latest, err := repo.FindLatest(ctx, pool, otherScope)
	require.ErrorIs(t, err, runrecency.ErrExpectationNotFound)
	require.Nil(t, latest)
	_, err = svc.Update(ctx, otherScope, 0, time.Hour, true, at)
	require.Error(t, err)
	require.NoError(t, repo.RecordSuccess(ctx, pool, other, project, env, "unknown-deployment"))
}

type tenantFailingRecencyNotifications struct {
	notificationsvc.Service
	org slug.Slug
}

func (f tenantFailingRecencyNotifications) CreateCandidate(ctx context.Context, p notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	if p.OrgSlug == f.org {
		return nil, xerror.Unexpected().Detail("synthetic scoped candidate failure")
	}
	return f.Service.CreateCandidate(ctx, p)
}

func TestRunRecencyScopeFailureIsolationAndDisabledEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "run recency scope isolation")
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	txm := postgres.NewTransactionManager(pool)
	notifications := notificationsvc.New(txm, reponotifications.New(), testutil.FakeClock{NowFn: func() time.Time { return at.Add(time.Hour) }}, &fakeDeliveryProducer{}, &testutil.FakeEmailProducer{}, notificationsvc.LifecycleEventProducerFunc(func(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) {}), notificationsvc.ConnectionProviders{}, "https://app.example")
	repo := runrecency.New()
	bad, good := uniqueNotificationE2ESlug("recency-fail"), uniqueNotificationE2ESlug("recency-healthy")
	svc := runrecencysvc.New(txm, repo, tenantFailingRecencyNotifications{Service: notifications, org: bad})
	for _, org := range []slug.Slug{bad, good} {
		seedAlertPolicyScopeRows(t, ctx, pool, org, "ops", "prod", "unused")
		enableProjectEnvironment(t, ctx, pool, org, "ops", "prod")
		_, err := pool.Exec(ctx, `INSERT INTO pipeline_specs(project_id,slug,name,spec_jsonb,spec_hash,source_path,source_ref,last_applied_commit) SELECT p.id,'orders','Orders','{}','scope-isolation','pipelines/orders.json','refs/heads/main','0000000000000000000000000000000000000000' FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, org)
		require.NoError(t, err)
		var version int64
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_jsonb,spec_hash) SELECT p.id,'orders','{}','scope-isolation' FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 RETURNING id`, org).Scan(&version))
		_, err = pool.Exec(ctx, `INSERT INTO pipeline_deployments(project_id,project_environment_id,pipeline_slug,current_version_id,deployed_by_actor_type,deployed_at) SELECT pe.project_id,pe.id,'orders',$2,'system',$3 FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, org, version, at)
		require.NoError(t, err)
		_, err = svc.Update(ctx, runrecency.Scope{Org: org, Project: "ops", Environment: "prod", Pipeline: "orders"}, 0, time.Hour, true, at)
		require.NoError(t, err)
	}
	require.Error(t, svc.Evaluate(ctx, at.Add(time.Hour)))
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notification_records n JOIN organizations o ON o.id=n.organization_id WHERE o.slug=$1 AND notification_type='pipeline.freshness_sla_breached'`, good).Scan(&count))
	require.Equal(t, 1, count, "a failing tenant must not roll back the healthy tenant")
	_, err := pool.Exec(ctx, `UPDATE project_environments pe SET deleted_at=$2 FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE pe.project_id=p.id AND o.slug=$1`, good, at.Add(61*time.Minute))
	require.NoError(t, err)
	require.Error(t, svc.Evaluate(ctx, at.Add(62*time.Minute)))
	var state, reason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT lifecycle_state FROM notification_records n JOIN organizations o ON o.id=n.organization_id WHERE o.slug=$1 AND notification_type='pipeline.freshness_sla_breached'`, good).Scan(&state))
	require.Equal(t, "resolved", state)
	require.NoError(t, pool.QueryRow(ctx, `SELECT episode.resolution_reason FROM pipeline_run_recency_episodes episode JOIN pipeline_run_recency_expectations expectation ON expectation.id=episode.expectation_id JOIN project_environments pe ON pe.id=expectation.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, good).Scan(&reason))
	require.Equal(t, "environment_unavailable", reason)
	// A restored environment can start another episode even if the old deadline is unchanged.
	_, err = pool.Exec(ctx, `UPDATE project_environments pe SET deleted_at=NULL FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE pe.project_id=p.id AND o.slug=$1`, good)
	require.NoError(t, err)
	require.Error(t, svc.Evaluate(ctx, at.Add(63*time.Minute)))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM notification_records n JOIN organizations o ON o.id=n.organization_id WHERE o.slug=$1 AND notification_type='pipeline.freshness_sla_breached'`, good).Scan(&count))
	require.Equal(t, 2, count)
}
