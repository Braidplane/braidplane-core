package retentionsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/retention"
)

func TestPolicyFromSettings(t *testing.T) {
	t.Parallel()

	t.Run("defaults when retention is absent", func(t *testing.T) {
		t.Parallel()

		got, err := PolicyFromSettings([]byte(`{"timezone":"Europe/Warsaw"}`))
		require.NoError(t, err)
		require.Equal(t, model.DefaultOrganizationRetentionPolicy(), got)
	})

	t.Run("reads explicit policy", func(t *testing.T) {
		t.Parallel()

		got, err := PolicyFromSettings([]byte(`{"retention":{"query_execution_retention_days":2,"replay_checkpoint_retention_days":8,"run_history_retention_days":20,"event_log_retention_days":21}}`))
		require.NoError(t, err)
		require.Equal(t, 2, got.QueryExecutionRetentionDays)
		require.Equal(t, 8, got.ReplayCheckpointRetentionDays)
		require.Equal(t, 20, got.RunHistoryRetentionDays)
		require.Equal(t, 21, got.EventLogRetentionDays)
	})

	t.Run("rejects malformed retention policy", func(t *testing.T) {
		t.Parallel()

		_, err := PolicyFromSettings([]byte(`{"retention":{"query_execution_retention_days":0}}`))
		require.ErrorContains(t, err, "query_execution_retention_days")
	})
}

func TestPruneExpiredCustomerDataDeletesOrganizationScopedRecordsAtPolicyCutoffs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	org := must.Slug("acme")
	repo := &recordingRetentionRepository{
		organizations: []retention.OrganizationSettings{{
			Slug:     org,
			Settings: []byte(`{"retention":{"query_execution_retention_days":2,"replay_checkpoint_retention_days":3,"run_history_retention_days":4,"event_log_retention_days":5}}`),
		}},
	}
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{now: now})
	execution := MaintenanceExecution{SourceJobID: 41, Attempt: 1, StartedAt: now.Add(-time.Minute)}

	result, err := svc.PruneExpiredCustomerData(t.Context(), execution)

	require.NoError(t, err)
	require.Equal(t, Result{WishlistRegistrations: 5, AbuseControlBuckets: 6, QueryExecutions: 1, ReplayCheckpoints: 2, PipelineRuns: 3, Events: 4}, result)
	require.Equal(t, []time.Time{now.AddDate(0, -wishlistRetentionMonths, 0)}, repo.wishlistRegistrations)
	require.Equal(t, []time.Time{now.Add(-abuseControlRetention)}, repo.abuseControlBuckets)
	require.Equal(t, []retentionDeleteCall{{org: org, cutoff: now.AddDate(0, 0, -2)}}, repo.queryExecutions)
	require.Equal(t, []retentionDeleteCall{{org: org, cutoff: now.AddDate(0, 0, -3)}}, repo.replayCheckpoints)
	require.Equal(t, []retentionDeleteCall{{org: org, cutoff: now.AddDate(0, 0, -4)}}, repo.pipelineRuns)
	require.Equal(t, []retentionDeleteCall{{org: org, cutoff: now.AddDate(0, 0, -5)}}, repo.events)
	require.Equal(t, []retentionDeleteCall{{org: org, cutoff: now.AddDate(0, 0, -auditHistoryRetentionDays)}}, repo.retentionRuns)
	require.Len(t, repo.createdRuns, 1)
	require.Equal(t, int64(41), repo.createdRuns[0].sourceJobID)
	require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, repo.createdRuns[0].run.Status)
	require.Equal(t, execution.StartedAt, repo.createdRuns[0].run.StartedAt)
	require.Equal(t, now, repo.createdRuns[0].run.CompletedAt)
	require.Equal(t, int64(1), repo.createdRuns[0].run.QueryExecutionsDeleted)
	require.Equal(t, int64(2), repo.createdRuns[0].run.ReplayCheckpointsDeleted)
	require.Equal(t, int64(3), repo.createdRuns[0].run.PipelineRunsDeleted)
	require.Equal(t, int64(4), repo.createdRuns[0].run.EventsDeleted)
	require.Equal(t, &model.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   2,
		ReplayCheckpointRetentionDays: 3,
		RunHistoryRetentionDays:       4,
		EventLogRetentionDays:         5,
	}, repo.createdRuns[0].run.PolicySnapshot)
}

func TestPruneExpiredCustomerDataSkipsInvalidPoliciesWithoutDeletes(t *testing.T) {
	t.Parallel()

	org := must.Slug("invalid-policy")
	repo := &recordingRetentionRepository{
		organizations: []retention.OrganizationSettings{{
			Slug:     org,
			Settings: []byte(`{"retention":{"event_log_retention_days":0}}`),
		}},
	}
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{now: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)})
	execution := MaintenanceExecution{SourceJobID: 42, Attempt: 1, StartedAt: time.Date(2026, time.July, 22, 11, 59, 0, 0, time.UTC)}

	result, err := svc.PruneExpiredCustomerData(t.Context(), execution)

	require.NoError(t, err)
	require.Equal(t, Result{WishlistRegistrations: 5, AbuseControlBuckets: 6, SkippedOrgs: []slug.Slug{org}}, result)
	require.Empty(t, repo.queryExecutions)
	require.Empty(t, repo.replayCheckpoints)
	require.Empty(t, repo.pipelineRuns)
	require.Empty(t, repo.events)
	require.Len(t, repo.createdRuns, 1)
	require.Equal(t, model.OrganizationRetentionRunStatusFailed, repo.createdRuns[0].run.Status)
	require.Equal(t, model.OrganizationRetentionRunFailureCodeInvalidPolicy, repo.createdRuns[0].run.FailureCode)
	require.Nil(t, repo.createdRuns[0].run.PolicySnapshot)
}

func TestPruneExpiredCustomerDataStopsWhenWishlistPruningFails(t *testing.T) {
	t.Parallel()

	dependencyErr := xerror.Unexpected().Detail("wishlist retention failed")
	repo := &recordingRetentionRepository{
		organizations:       []retention.OrganizationSettings{{Slug: must.Slug("acme")}},
		deleteWishlistError: dependencyErr,
	}
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{
		now: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
	})

	result, err := svc.PruneExpiredCustomerData(t.Context(), MaintenanceExecution{})

	require.Equal(t, Result{}, result)
	require.Error(t, err)
	require.True(t, xerror.Is(err, dependencyErr))
	require.Empty(t, repo.queryExecutions)
	require.Empty(t, repo.createdRuns)
}

func TestPruneExpiredCustomerDataStopsWhenAbuseControlPruningFails(t *testing.T) {
	t.Parallel()

	dependencyErr := xerror.Unexpected().Detail("abuse-control retention failed")
	repo := &recordingRetentionRepository{
		organizations:    []retention.OrganizationSettings{{Slug: must.Slug("acme")}},
		deleteAbuseError: dependencyErr,
	}
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{
		now: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
	})

	result, err := svc.PruneExpiredCustomerData(t.Context(), MaintenanceExecution{})

	require.Equal(t, Result{}, result)
	require.Error(t, err)
	require.True(t, xerror.Is(err, dependencyErr))
	require.Len(t, repo.abuseControlBuckets, 1)
	require.Empty(t, repo.queryExecutions)
	require.Empty(t, repo.createdRuns)
}

func TestPruneExpiredCustomerDataRecordsSafeFailureAfterTransactionRollback(t *testing.T) {
	t.Parallel()

	transactionErr := xerror.Unexpected().Detail("commit retention pruning transaction")
	repo := &recordingRetentionRepository{organizations: []retention.OrganizationSettings{{Slug: must.Slug("acme")}}}
	txm := &failingRetentionTransactionManager{err: transactionErr}
	svc := New(txm, repo, repo, fixedClock{now: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)})
	execution := MaintenanceExecution{SourceJobID: 43, Attempt: 2, StartedAt: time.Date(2026, time.July, 22, 11, 59, 0, 0, time.UTC)}

	result, err := svc.PruneExpiredCustomerData(t.Context(), execution)

	require.Error(t, err)
	require.True(t, xerror.Is(err, transactionErr))
	require.Equal(t, Result{WishlistRegistrations: 5, AbuseControlBuckets: 6}, result)
	require.Len(t, repo.queryExecutions, 1)
	require.Len(t, repo.replayCheckpoints, 1)
	require.Len(t, repo.pipelineRuns, 1)
	require.Len(t, repo.events, 1)
	require.Len(t, repo.createdRuns, 2)
	require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, repo.createdRuns[0].run.Status)
	require.Equal(t, model.OrganizationRetentionRunStatusFailed, repo.createdRuns[1].run.Status)
	require.Equal(t, model.OrganizationRetentionRunFailureCodeMaintenanceFailed, repo.createdRuns[1].run.FailureCode)
	require.Zero(t, repo.createdRuns[1].run.QueryExecutionsDeleted)
	require.NotNil(t, repo.createdRuns[1].run.PolicySnapshot)
	require.Equal(t, 3, txm.writeCalls)
}

func TestPruneExpiredCustomerDataSkipsOrganizationAlreadySucceededForWorkerJob(t *testing.T) {
	t.Parallel()

	org := must.Slug("acme")
	repo := &recordingRetentionRepository{
		organizations:  []retention.OrganizationSettings{{Slug: org}},
		succeededByJob: map[retentionJobKey]bool{{org: org, sourceJobID: 44}: true},
	}
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{now: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)})

	result, err := svc.PruneExpiredCustomerData(t.Context(), MaintenanceExecution{
		SourceJobID: 44,
		Attempt:     2,
		StartedAt:   time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
	})

	require.NoError(t, err)
	require.Equal(t, Result{WishlistRegistrations: 5, AbuseControlBuckets: 6}, result)
	require.Empty(t, repo.queryExecutions)
	require.Empty(t, repo.createdRuns)
}

func TestPruneExpiredCustomerDataContinuesAfterOrganizationFailure(t *testing.T) {
	t.Parallel()

	failedOrg := must.Slug("acme")
	successfulOrg := must.Slug("globex")
	dependencyErr := xerror.Unexpected().Detail("private database detail")
	repo := &recordingRetentionRepository{
		organizations: []retention.OrganizationSettings{
			{Slug: failedOrg},
			{Slug: successfulOrg},
		},
		deleteEventsErrors: map[slug.Slug]error{failedOrg: dependencyErr},
	}
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{now: now})

	result, err := svc.PruneExpiredCustomerData(t.Context(), MaintenanceExecution{
		SourceJobID: 45,
		Attempt:     1,
		StartedAt:   now.Add(-time.Minute),
	})

	require.Error(t, err)
	require.True(t, xerror.Is(err, dependencyErr))
	require.Equal(t, Result{WishlistRegistrations: 5, AbuseControlBuckets: 6, QueryExecutions: 1, ReplayCheckpoints: 2, PipelineRuns: 3, Events: 4}, result)
	require.Len(t, repo.createdRuns, 2)
	require.Equal(t, failedOrg, repo.createdRuns[0].org)
	require.Equal(t, model.OrganizationRetentionRunStatusFailed, repo.createdRuns[0].run.Status)
	require.Equal(t, model.OrganizationRetentionRunFailureCodeMaintenanceFailed, repo.createdRuns[0].run.FailureCode)
	require.Equal(t, successfulOrg, repo.createdRuns[1].org)
	require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, repo.createdRuns[1].run.Status)
}

func TestPruneExpiredCustomerDataHandlesDeletedOrganization(t *testing.T) {
	t.Parallel()

	dependencyErr := xerror.Unexpected().Detail("retention database unavailable")
	for _, tc := range []struct {
		name        string
		settings    []byte
		deleteErr   error
		createErr   error
		wantErr     error
		wantSkipped bool
	}{
		{name: "DeletedBeforeSuccessRecord", createErr: retention.ErrOrganizationNotFound, wantSkipped: true},
		{name: "DeletedBeforeInvalidPolicyRecord", settings: []byte(`{"retention":{"event_log_retention_days":0}}`), createErr: retention.ErrOrganizationNotFound, wantSkipped: true},
		{name: "DeletionDoesNotHidePruningFailure", deleteErr: dependencyErr, createErr: retention.ErrOrganizationNotFound, wantErr: dependencyErr},
		{name: "HistoryWriteFailureStillFails", createErr: dependencyErr, wantErr: dependencyErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deletedOrg, remainingOrg := must.Slug("deleted-org"), must.Slug("remaining-org")
			repo := &recordingRetentionRepository{
				organizations:      []retention.OrganizationSettings{{Slug: deletedOrg, Settings: tc.settings}, {Slug: remainingOrg}},
				deleteEventsErrors: map[slug.Slug]error{deletedOrg: tc.deleteErr},
				createRunErrors:    map[slug.Slug]error{deletedOrg: tc.createErr},
			}
			now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
			svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{now: now})
			result, err := svc.PruneExpiredCustomerData(t.Context(), MaintenanceExecution{SourceJobID: 46, Attempt: 1, StartedAt: now})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			want := Result{WishlistRegistrations: 5, AbuseControlBuckets: 6, QueryExecutions: 1, ReplayCheckpoints: 2, PipelineRuns: 3, Events: 4}
			if tc.wantSkipped {
				want.SkippedOrgs = []slug.Slug{deletedOrg}
			}
			require.Equal(t, want, result)
			require.Len(t, repo.createdRuns, 1)
			require.Equal(t, remainingOrg, repo.createdRuns[0].org)
			require.Equal(t, model.OrganizationRetentionRunStatusSucceeded, repo.createdRuns[0].run.Status)
		})
	}
}

func TestListMaintenanceRunsUsesOrganizationScopedRepositoryRead(t *testing.T) {
	t.Parallel()

	org := must.Slug("acme")
	want := []model.OrganizationRetentionRun{{
		Slug:        must.Slug("retention-run-001"),
		Attempt:     1,
		Status:      model.OrganizationRetentionRunStatusSucceeded,
		CompletedAt: time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
	}}
	repo := &recordingRetentionRepository{listedRuns: map[slug.Slug][]model.OrganizationRetentionRun{org: want}}
	svc := New(testutil.FakeTransactionManager{}, repo, repo, fixedClock{})

	got, err := svc.ListMaintenanceRuns(t.Context(), org)

	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, []slug.Slug{org}, repo.listedRunOrgs)
}

func TestNextRunAtUsesNextUTCMidnight(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 22, 23, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	require.Equal(t, time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC), NextRunAt(now))
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

type retentionDeleteCall struct {
	org    slug.Slug
	cutoff time.Time
}

type retentionRunCall struct {
	org         slug.Slug
	sourceJobID int64
	run         model.OrganizationRetentionRun
}

type retentionJobKey struct {
	org         slug.Slug
	sourceJobID int64
}

type recordingRetentionRepository struct {
	organizations         []retention.OrganizationSettings
	listedRuns            map[slug.Slug][]model.OrganizationRetentionRun
	listedRunOrgs         []slug.Slug
	succeededByJob        map[retentionJobKey]bool
	createdRuns           []retentionRunCall
	createRunErrors       map[slug.Slug]error
	retentionRuns         []retentionDeleteCall
	deleteEventsErrors    map[slug.Slug]error
	deleteWishlistError   error
	deleteAbuseError      error
	queryExecutions       []retentionDeleteCall
	replayCheckpoints     []retentionDeleteCall
	pipelineRuns          []retentionDeleteCall
	events                []retentionDeleteCall
	wishlistRegistrations []time.Time
	abuseControlBuckets   []time.Time
}

func (r *recordingRetentionRepository) ListOrganizationSettings(context.Context, postgres.DBTX) ([]retention.OrganizationSettings, error) {
	return r.organizations, nil
}

func (r *recordingRetentionRepository) ListRuns(
	_ context.Context,
	_ postgres.DBTX,
	org slug.Slug,
) ([]model.OrganizationRetentionRun, error) {
	r.listedRunOrgs = append(r.listedRunOrgs, org)
	return r.listedRuns[org], nil
}

func (r *recordingRetentionRepository) HasSucceededRunForJob(
	_ context.Context,
	_ postgres.DBTX,
	org slug.Slug,
	sourceJobID int64,
) (bool, error) {
	return r.succeededByJob[retentionJobKey{org: org, sourceJobID: sourceJobID}], nil
}

func (r *recordingRetentionRepository) CreateRun(
	_ context.Context,
	_ postgres.DBTX,
	org slug.Slug,
	sourceJobID int64,
	run model.OrganizationRetentionRun,
) error {
	if err := r.createRunErrors[org]; err != nil {
		return err
	}
	r.createdRuns = append(r.createdRuns, retentionRunCall{org: org, sourceJobID: sourceJobID, run: run})
	return nil
}

func (r *recordingRetentionRepository) DeleteRunsBefore(
	_ context.Context,
	_ postgres.DBTX,
	org slug.Slug,
	cutoff time.Time,
) (int64, error) {
	r.retentionRuns = append(r.retentionRuns, retentionDeleteCall{org: org, cutoff: cutoff})
	return 0, nil
}

func (r *recordingRetentionRepository) DeleteQueryExecutionsBefore(_ context.Context, _ postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	r.queryExecutions = append(r.queryExecutions, retentionDeleteCall{org: org, cutoff: cutoff})
	return 1, nil
}

func (r *recordingRetentionRepository) DeleteReplayCheckpointsBefore(_ context.Context, _ postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	r.replayCheckpoints = append(r.replayCheckpoints, retentionDeleteCall{org: org, cutoff: cutoff})
	return 2, nil
}

func (r *recordingRetentionRepository) DeletePipelineRunsBefore(_ context.Context, _ postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	r.pipelineRuns = append(r.pipelineRuns, retentionDeleteCall{org: org, cutoff: cutoff})
	return 3, nil
}

func (r *recordingRetentionRepository) DeleteEventsBefore(_ context.Context, _ postgres.DBTX, org slug.Slug, cutoff time.Time) (int64, error) {
	r.events = append(r.events, retentionDeleteCall{org: org, cutoff: cutoff})
	if err := r.deleteEventsErrors[org]; err != nil {
		return 0, err
	}
	return 4, nil
}

func (r *recordingRetentionRepository) DeleteAbuseControlBucketsBefore(
	_ context.Context,
	_ postgres.DBTX,
	cutoff time.Time,
) (int64, error) {
	r.abuseControlBuckets = append(r.abuseControlBuckets, cutoff)
	if r.deleteAbuseError != nil {
		return 0, r.deleteAbuseError
	}
	return 6, nil
}

func (r *recordingRetentionRepository) DeleteExpired(
	_ context.Context,
	_ postgres.DBTX,
	cutoff time.Time,
) (int64, error) {
	r.wishlistRegistrations = append(r.wishlistRegistrations, cutoff)
	if r.deleteWishlistError != nil {
		return 0, r.deleteWishlistError
	}
	return 5, nil
}

type failingRetentionTransactionManager struct {
	err        error
	writeCalls int
}

func (m *failingRetentionTransactionManager) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	m.writeCalls++
	if err := fn(ctx, nil); err != nil {
		return err
	}
	if m.writeCalls == 2 {
		return m.err
	}
	return nil
}

func (m *failingRetentionTransactionManager) RunRead(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

func (r *recordingRetentionRepository) PruneCompletionDetailsBefore(context.Context, postgres.DBTX, slug.Slug, time.Time) error {
	return nil
}
