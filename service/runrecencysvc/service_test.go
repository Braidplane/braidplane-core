package runrecencysvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/runrecency"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/stretchr/testify/require"
)

func TestEvaluateInitialGraceAndBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		now      time.Time
		created  int
		deadline time.Time
	}{
		{name: "InitialGraceWithoutSuccess", now: testNow.Add(-time.Nanosecond), deadline: testNow},
		{name: "ExactBoundaryCreatesBreach", now: testNow, created: 1, deadline: testNow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, notifications := &evaluatorRepository{expectations: []runrecency.Expectation{testExpectation(testNow)}}, &evaluatorNotifications{}

			require.NoError(t, New(&evaluatorTransactions{}, repo, notifications).Evaluate(t.Context(), tc.now))
			require.Len(t, repo.created, tc.created)
			require.Len(t, notifications.created, tc.created)
			if tc.created == 1 {
				require.True(t, model.AlertPolicySupportsEventSource(notifications.created[0].SourceEntityType, &notifications.created[0].SourceAction), "breach observations must support an exact alert policy source selector")
				require.Equal(t, repo.expectations[0].Pipeline.String(), notifications.created[0].SourceRefs["source_ref"], "pipeline-scoped alert policies match the canonical source ref")
				require.Equal(t, tc.deadline, repo.created[0].deadline)
				require.Equal(t, tc.now, notifications.created[0].ObservedAt)
				require.Equal(t, tc.deadline.Format(time.RFC3339Nano), notifications.created[0].SourceRefs["deadline_at"])
			}
		})
	}
}

func TestEvaluateRepeatedScanDedupesActiveEpisode(t *testing.T) {
	t.Parallel()
	repo, notifications := &evaluatorRepository{expectations: []runrecency.Expectation{testExpectation(testNow)}}, &evaluatorNotifications{}
	svc := New(&evaluatorTransactions{}, repo, notifications)

	require.NoError(t, svc.Evaluate(t.Context(), testNow))
	require.NoError(t, svc.Evaluate(t.Context(), testNow.Add(time.Minute)))
	require.Len(t, repo.created, 1)
	require.Len(t, notifications.created, 1)
}

func TestEvaluateRecoveryThenRebreach(t *testing.T) {
	t.Parallel()
	success := testNow
	e := testExpectation(testNow)
	e.SucceededAt = &success
	oldSuccess := testNow.Add(-2 * time.Hour)
	repo := &evaluatorRepository{expectations: []runrecency.Expectation{e}, episodes: map[runrecency.Scope][]runrecency.Episode{
		e.Scope: {{Slug: "recency-old", ExpectationID: e.ID, DeploymentID: e.DeploymentID, DeploymentRevision: e.DeploymentRevision, LastSuccessAt: &oldSuccess}},
	}}
	notifications := &evaluatorNotifications{}
	svc := New(&evaluatorTransactions{}, repo, notifications)

	require.NoError(t, svc.Evaluate(t.Context(), testNow))
	require.Equal(t, []string{"qualifying_success"}, repo.resolutionReasons())
	require.Len(t, notifications.resolved, 1)
	require.Empty(t, notifications.created)

	require.NoError(t, svc.Evaluate(t.Context(), testNow.Add(time.Hour)))
	require.Len(t, repo.created, 1)
	require.Len(t, notifications.created, 1)
}

func TestEvaluateResolvesSupersededEpisodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		e       runrecency.Expectation
		episode runrecency.Episode
		reason  string
		created int
	}{
		{
			name:    "DeploymentChange",
			e:       testExpectation(testNow),
			episode: runrecency.Episode{Slug: "recency-old", ExpectationID: 1, DeploymentID: 9, DeploymentRevision: 1},
			reason:  "deployment_changed", created: 1,
		},
		{
			name:    "ExpectationReplacement",
			e:       testExpectation(testNow),
			episode: runrecency.Episode{Slug: "recency-old", ExpectationID: 9, DeploymentID: 10, DeploymentRevision: 2},
			reason:  "expectation_replaced", created: 1,
		},
		{
			name:    "DisableResolvesWithoutReplacement",
			e:       func() runrecency.Expectation { e := testExpectation(testNow); e.Enabled = false; return e }(),
			episode: runrecency.Episode{Slug: "recency-old", ExpectationID: 1, DeploymentID: 10, DeploymentRevision: 2},
			reason:  "expectation_disabled",
		},
		{
			name:    "ReenableStartsNewGraceWindow",
			e:       func() runrecency.Expectation { e := testExpectation(testNow.Add(time.Hour)); e.ID = 2; return e }(),
			episode: runrecency.Episode{Slug: "recency-old", ExpectationID: 1, DeploymentID: 10, DeploymentRevision: 2},
			reason:  "expectation_replaced",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := &evaluatorRepository{expectations: []runrecency.Expectation{tc.e}, episodes: map[runrecency.Scope][]runrecency.Episode{tc.e.Scope: {tc.episode}}}
			notifications := &evaluatorNotifications{}

			require.NoError(t, New(&evaluatorTransactions{}, repo, notifications).Evaluate(t.Context(), testNow))
			require.Equal(t, []string{tc.reason}, repo.resolutionReasons())
			require.Len(t, notifications.resolved, 1)
			require.Len(t, repo.created, tc.created)
		})
	}
}

func TestEvaluatePropagatesDependencyFailuresFromTransactionCallback(t *testing.T) {
	t.Parallel()
	errDependency := xerror.Unexpected().Detail("dependency unavailable")
	for _, tc := range []struct {
		name  string
		setup func(*evaluatorRepository, *evaluatorNotifications)
	}{
		{name: "List", setup: func(r *evaluatorRepository, _ *evaluatorNotifications) { r.listErr = errDependency }},
		{name: "LockScope", setup: func(r *evaluatorRepository, _ *evaluatorNotifications) { r.lockErr = errDependency }},
		{name: "FindCurrent", setup: func(r *evaluatorRepository, _ *evaluatorNotifications) { r.findErr = errDependency }},
		{name: "ActiveEpisodes", setup: func(r *evaluatorRepository, _ *evaluatorNotifications) { r.activeErr = errDependency }},
		{name: "CreateEpisode", setup: func(r *evaluatorRepository, _ *evaluatorNotifications) { r.createErr = errDependency }},
		{name: "CreateCandidate", setup: func(_ *evaluatorRepository, n *evaluatorNotifications) { n.createErr = errDependency }},
		{name: "ResolveEpisode", setup: func(r *evaluatorRepository, _ *evaluatorNotifications) { r.resolveErr = errDependency }},
		{name: "ResolveNotification", setup: func(_ *evaluatorRepository, n *evaluatorNotifications) { n.resolveErr = errDependency }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := testExpectation(testNow)
			repo := &evaluatorRepository{expectations: []runrecency.Expectation{e}}
			if tc.name == "ResolveEpisode" || tc.name == "ResolveNotification" {
				repo.episodes = map[runrecency.Scope][]runrecency.Episode{e.Scope: {{Slug: "recency-old", ExpectationID: e.ID, DeploymentID: 9, DeploymentRevision: e.DeploymentRevision}}}
			}
			notifications, txm := &evaluatorNotifications{}, &evaluatorTransactions{}
			tc.setup(repo, notifications)

			err := New(txm, repo, notifications).Evaluate(t.Context(), testNow)
			require.ErrorIs(t, err, errDependency)
			if tc.name == "List" {
				require.ErrorIs(t, txm.readCallbackErr, errDependency)
			} else {
				require.ErrorIs(t, txm.callbackErr, errDependency)
			}
		})
	}
}

func TestEvaluateRetiresDisabledEnvironmentWithoutNewBreach(t *testing.T) {
	t.Parallel()
	e := testExpectation(testNow)
	e.ScopeDisabled = true
	repo := &evaluatorRepository{expectations: []runrecency.Expectation{e}, episodes: map[runrecency.Scope][]runrecency.Episode{
		e.Scope: {{Slug: "recency-old", ExpectationID: e.ID, DeploymentID: e.DeploymentID, DeploymentRevision: e.DeploymentRevision}},
	}}
	notifications := &evaluatorNotifications{}

	require.NoError(t, New(&evaluatorTransactions{}, repo, notifications).Evaluate(t.Context(), testNow))
	require.Equal(t, []string{"environment_unavailable"}, repo.resolutionReasons())
	require.Len(t, notifications.resolved, 1)
	require.Empty(t, notifications.created)
}

func TestEvaluateRefreshesCurrentExpectationAfterLock(t *testing.T) {
	t.Parallel()
	selected := testExpectation(testNow)
	current := testExpectation(testNow.Add(time.Hour))
	current.Scope = selected.Scope
	repo := &evaluatorRepository{expectations: []runrecency.Expectation{selected}, current: map[runrecency.Scope]runrecency.Expectation{selected.Scope: current}}
	notifications := &evaluatorNotifications{}

	require.NoError(t, New(&evaluatorTransactions{}, repo, notifications).Evaluate(t.Context(), testNow))
	require.Equal(t, []runrecency.Scope{selected.Scope}, repo.locked)
	require.Empty(t, repo.created)
	require.Empty(t, notifications.created)
}

func TestEvaluateContinuesAfterTenantFailure(t *testing.T) {
	t.Parallel()
	failed, processed := testExpectation(testNow), testExpectation(testNow)
	processed.Org = "other"
	repo := &evaluatorRepository{expectations: []runrecency.Expectation{failed, processed}, lockErrors: map[runrecency.Scope]error{failed.Scope: xerror.Unexpected().Detail("first tenant unavailable")}}
	notifications, txm := &evaluatorNotifications{}, &evaluatorTransactions{}

	err := New(txm, repo, notifications).Evaluate(t.Context(), testNow)
	require.ErrorIs(t, err, repo.lockErrors[failed.Scope])
	require.Equal(t, 2, txm.writeCalls)
	require.Len(t, repo.created, 1)
	require.Equal(t, processed.Org, repo.created[0].expectation.Org)
	require.Len(t, notifications.created, 1)
}

var testNow = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

func testExpectation(deadline time.Time) runrecency.Expectation {
	return runrecency.Expectation{ID: 1, Scope: runrecency.Scope{Org: "acme", Project: "ops", Environment: "prod", Pipeline: "sync"}, EnvironmentID: 2, Revision: 3, MaximumAge: time.Hour, Enabled: true, EnabledAt: deadline.Add(-time.Hour), DeploymentID: 10, DeploymentRevision: 2, VersionID: 11, DeployedAt: deadline.Add(-2 * time.Hour)}
}

type evaluatorTransactions struct {
	calls, readCalls, writeCalls int
	callbackErr                  error
	readCallbackErr              error
}

func (m *evaluatorTransactions) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	m.calls++
	m.writeCalls++
	m.callbackErr = fn(ctx, nil)
	return m.callbackErr
}

func (m *evaluatorTransactions) RunRead(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	m.calls++
	m.readCalls++
	m.readCallbackErr = fn(ctx, nil)
	return m.readCallbackErr
}

type evaluatorEpisodeCreate struct {
	expectation runrecency.Expectation
	slug        slug.Slug
	deadline    time.Time
	breachedAt  time.Time
}

type evaluatorEpisodeResolution struct {
	episode runrecency.Episode
	reason  string
}

type evaluatorRepository struct {
	expectations                                                []runrecency.Expectation
	current                                                     map[runrecency.Scope]runrecency.Expectation
	episodes                                                    map[runrecency.Scope][]runrecency.Episode
	created                                                     []evaluatorEpisodeCreate
	resolved                                                    []evaluatorEpisodeResolution
	locked                                                      []runrecency.Scope
	lockErrors                                                  map[runrecency.Scope]error
	calls                                                       int
	listErr, lockErr, findErr, activeErr, createErr, resolveErr error
}

func (r *evaluatorRepository) LockScope(_ context.Context, _ postgres.DBTX, scope runrecency.Scope) error {
	r.calls++
	r.locked = append(r.locked, scope)
	if r.lockErrors != nil && r.lockErrors[scope] != nil {
		return r.lockErrors[scope]
	}
	return r.lockErr
}

func (r *evaluatorRepository) FindCurrent(_ context.Context, _ postgres.DBTX, scope runrecency.Scope) (runrecency.Expectation, error) {
	r.calls++
	if r.findErr != nil {
		return runrecency.Expectation{}, r.findErr
	}
	if r.current != nil {
		if e, ok := r.current[scope]; ok {
			return e, nil
		}
	}
	for _, e := range r.expectations {
		if e.Scope == scope {
			return e, nil
		}
	}
	return runrecency.Expectation{}, runrecency.ErrExpectationNotFound
}

func (*evaluatorRepository) FindLatest(context.Context, postgres.DBTX, runrecency.Scope) (*runrecency.Expectation, error) {
	panic("unexpected settings call")
}

func (*evaluatorRepository) CreateExpectation(context.Context, postgres.DBTX, runrecency.Expectation) (*runrecency.Expectation, error) {
	panic("unexpected settings call")
}

func (r *evaluatorRepository) ListForSystem(context.Context, postgres.DBTX) ([]runrecency.Expectation, error) {
	r.calls++
	return r.expectations, r.listErr
}

func (r *evaluatorRepository) ActiveEpisodes(_ context.Context, _ postgres.DBTX, e runrecency.Expectation) ([]runrecency.Episode, error) {
	r.calls++
	return append([]runrecency.Episode(nil), r.episodes[e.Scope]...), r.activeErr
}

func (r *evaluatorRepository) CreateEpisode(_ context.Context, _ postgres.DBTX, e runrecency.Expectation, record slug.Slug, deadline, now time.Time) error {
	r.calls++
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, evaluatorEpisodeCreate{expectation: e, slug: record, deadline: deadline, breachedAt: now})
	if r.episodes == nil {
		r.episodes = make(map[runrecency.Scope][]runrecency.Episode)
	}
	r.episodes[e.Scope] = append(r.episodes[e.Scope], runrecency.Episode{Slug: record, ExpectationID: e.ID, DeploymentID: e.DeploymentID, DeploymentRevision: e.DeploymentRevision, Deadline: deadline, BreachedAt: now, LastSuccessAt: e.SucceededAt, LastSuccessRun: e.SuccessRun})
	return nil
}

func (r *evaluatorRepository) ResolveEpisode(_ context.Context, _ postgres.DBTX, e runrecency.Expectation, episode runrecency.Episode, reason string, _ time.Time) error {
	r.calls++
	if r.resolveErr != nil {
		return r.resolveErr
	}
	r.resolved = append(r.resolved, evaluatorEpisodeResolution{episode: episode, reason: reason})
	for i := range r.episodes[e.Scope] {
		if r.episodes[e.Scope][i].Slug == episode.Slug {
			r.episodes[e.Scope] = append(r.episodes[e.Scope][:i], r.episodes[e.Scope][i+1:]...)
			break
		}
	}
	return nil
}

func (r *evaluatorRepository) resolutionReasons() []string {
	result := make([]string, len(r.resolved))
	for i := range r.resolved {
		result[i] = r.resolved[i].reason
	}
	return result
}

type evaluatorNotifications struct {
	created               []notificationsvc.CreateCandidateParams
	resolved              []slug.Slug
	createErr, resolveErr error
}

func (n *evaluatorNotifications) CreateCandidate(_ context.Context, candidate notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	if n.createErr != nil {
		return nil, n.createErr
	}
	n.created = append(n.created, candidate)
	return &notificationsvc.CreateCandidateResult{}, nil
}

func (n *evaluatorNotifications) ResolveNotification(_ context.Context, _ notificationsvc.AlertManagementPrincipal, _, record slug.Slug) (*model.NotificationListItem, error) {
	if n.resolveErr != nil {
		return nil, n.resolveErr
	}
	n.resolved = append(n.resolved, record)
	return &model.NotificationListItem{}, nil
}

func TestLatePersistedSuccessDoesNotRecoverExpiredWindow(t *testing.T) {
	e := testExpectation(testNow)
	episode := runrecency.Episode{Slug: "recency-existing", ExpectationID: e.ID, DeploymentID: e.DeploymentID, DeploymentRevision: e.DeploymentRevision, Deadline: testNow}
	baseline := testNow.Add(-e.MaximumAge)
	for _, at := range []time.Time{baseline.Add(-time.Second), baseline} {
		e.SucceededAt = &at
		require.Empty(t, resolutionReason(e, episode))
	}
	at := baseline.Add(time.Second)
	e.SucceededAt = &at
	require.Equal(t, "qualifying_success", resolutionReason(e, episode))
}

func (r *evaluatorRepository) Inspect(ctx context.Context, db postgres.DBTX, scope runrecency.Scope) (runrecency.Expectation, error) {
	return r.FindCurrent(ctx, db, scope)
}

func (*evaluatorRepository) InspectEpisodes(context.Context, postgres.DBTX, runrecency.Scope) ([]runrecency.EpisodeEvidence, error) {
	return nil, nil
}
