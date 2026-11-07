package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
)

func TestSeedScheduleFiresFromReaderSkipsDatabaseForEmptyPayload(t *testing.T) {
	t.Parallel()

	payload := `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[]}`
	err := seedScheduleFiresFromReader(context.Background(), strings.NewReader(payload), "")

	require.NoError(t, err)
}

func TestSeedScheduleFiresFromReaderRequiresConnectionStringForRows(t *testing.T) {
	t.Parallel()

	payload := `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[{"triggerSlug":"nightly-users","scheduledAt":"2026-06-15T03:45:00Z","actualAt":"2026-06-15T03:45:05Z","misfireResult":"on_time","status":"succeeded"}]}`
	err := seedScheduleFiresFromReader(context.Background(), strings.NewReader(payload), "")

	require.ErrorContains(t, err, "database connection string is required")
}

func TestDecodeScheduleFireSeedPayloadValidatesEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:  "ValidPayload",
			input: `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[]}`,
		},
		{
			name:    "RejectsMissingOrg",
			input:   `{"projectSlug":"ops","environmentSlug":"prod","fires":[]}`,
			wantErr: "orgSlug is required",
		},
		{
			name:    "RejectsUnknownEnvelopeField",
			input:   `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[],"extra":true}`,
			wantErr: "unknown field",
		},
		{
			name:    "RejectsUnknownFireField",
			input:   `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[{"triggerSlug":"nightly-users","scheduledAt":"2026-06-15T03:45:00Z","actualAt":"2026-06-15T03:45:05Z","misfireResult":"on_time","status":"succeeded","fireSlug":"ignored"}]}`,
			wantErr: "unknown field",
		},
		{
			name:    "RejectsTrailingObject",
			input:   `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[]}{"extra":true}`,
			wantErr: "trailing JSON content is not allowed",
		},
		{
			name:    "RejectsMalformedTrailingContent",
			input:   `{"orgSlug":"acme","projectSlug":"ops","environmentSlug":"prod","fires":[]} trailing`,
			wantErr: "trailing JSON content is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeScheduleFireSeedPayload(strings.NewReader(tt.input))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSeedScheduleFiresUsesRepositoryLifecycle(t *testing.T) {
	t.Parallel()

	txm := &recordingScheduleFireSeedTxManager{}
	repo := &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger()}
	payload := validScheduleFireSeedPayload()

	err := seedScheduleFires(context.Background(), txm, repo, payload)

	require.NoError(t, err)
	require.Equal(t, 1, txm.calls)
	require.Len(t, repo.findCalls, 1)
	require.Equal(t, findScheduleFireSeedTriggerCall{
		orgSlug:         must.Slug("acme"),
		projectSlug:     must.Slug("ops"),
		environmentSlug: must.Slug("prod"),
		triggerSlug:     must.Slug("nightly-users"),
	}, repo.findCalls[0])
	require.Len(t, repo.claims, 1)
	require.Equal(t, pipelinetriggers.ScheduleFireClaim{
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("ops"),
		EnvironmentSlug: must.Slug("prod"),
		TriggerSlug:     must.Slug("nightly-users"),
		TriggerRevision: 7,
		ScheduledFireAt: time.Date(2026, time.June, 15, 3, 45, 0, 0, time.UTC),
		ActualFireAt:    time.Date(2026, time.June, 15, 3, 45, 5, 0, time.UTC),
		MisfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
	}, repo.claims[0])
	require.Len(t, repo.completions, 1)
	require.Equal(t, pipelinetriggers.ScheduleFireCompletion{
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("ops"),
		EnvironmentSlug: must.Slug("prod"),
		TriggerSlug:     must.Slug("nightly-users"),
		TriggerRevision: 7,
		ScheduledFireAt: time.Date(2026, time.June, 15, 3, 45, 0, 0, time.UTC),
		Status:          "succeeded",
		RunSlug:         stringPtr("run-users-001"),
		SourceEventSlug: slugPtr("evt-schedule"),
		ErrorSummary:    stringPtr("connector timeout"),
	}, repo.completions[0])
}

func TestSeedScheduleFiresReportsFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*scheduleFireSeedPayload)
		repo      *recordingScheduleFireSeedRepository
		wantErr   string
		wantFinds int
	}{
		{
			name:    "InvalidScheduledTime",
			mutate:  func(payload *scheduleFireSeedPayload) { payload.Fires[0].ScheduledAt = "not-a-time" },
			repo:    &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger()},
			wantErr: "scheduledAt is invalid",
		},
		{
			name:    "InvalidStatus",
			mutate:  func(payload *scheduleFireSeedPayload) { payload.Fires[0].Status = "paused" },
			repo:    &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger()},
			wantErr: "status is invalid",
		},
		{
			name:    "InvalidMisfireResult",
			mutate:  func(payload *scheduleFireSeedPayload) { payload.Fires[0].MisfireResult = "late" },
			repo:    &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger()},
			wantErr: "misfireResult is invalid",
		},
		{
			name:    "InvalidRunSlug",
			mutate:  func(payload *scheduleFireSeedPayload) { payload.Fires[0].RunSlug = "not a slug" },
			repo:    &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger()},
			wantErr: "runSlug is invalid",
		},
		{
			name:      "MissingTrigger",
			repo:      &recordingScheduleFireSeedRepository{findErr: pipelinetriggers.ErrPipelineTriggerNotFound},
			wantErr:   "find schedule trigger",
			wantFinds: 1,
		},
		{
			name: "NonScheduleTrigger",
			repo: &recordingScheduleFireSeedRepository{trigger: &pipelinetriggers.PipelineTrigger{
				Slug:         must.Slug("nightly-users"),
				Revision:     7,
				PipelineSlug: must.Slug("sync-users"),
			}},
			wantErr:   "is not a schedule trigger",
			wantFinds: 1,
		},
		{
			name:      "ClaimError",
			repo:      &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger(), claimErr: xerror.Unexpected().Detail("claim unavailable")},
			wantErr:   "claim schedule fire",
			wantFinds: 1,
		},
		{
			name:      "CompleteError",
			repo:      &recordingScheduleFireSeedRepository{trigger: testScheduleFireSeedTrigger(), completeErr: xerror.Unexpected().Detail("complete unavailable")},
			wantErr:   "complete schedule fire",
			wantFinds: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload := validScheduleFireSeedPayload()
			if tt.mutate != nil {
				tt.mutate(&payload)
			}

			err := seedScheduleFires(context.Background(), &recordingScheduleFireSeedTxManager{}, tt.repo, payload)

			require.ErrorContains(t, err, tt.wantErr)
			require.Len(t, tt.repo.findCalls, tt.wantFinds)
		})
	}
}

func TestOptionalScheduleFireSeedString(t *testing.T) {
	t.Parallel()

	require.Nil(t, optionalScheduleFireSeedString(" "))
	require.Equal(t, stringPtr("connector timeout"), optionalScheduleFireSeedString(" connector timeout "))
}

// validScheduleFireSeedPayload returns the baseline seed payload shared by lifecycle tests.
func validScheduleFireSeedPayload() scheduleFireSeedPayload {
	return scheduleFireSeedPayload{
		OrgSlug:         "acme",
		ProjectSlug:     "ops",
		EnvironmentSlug: "prod",
		Fires: []scheduleFireSeedRow{{
			TriggerSlug:     "nightly-users",
			ScheduledAt:     "2026-06-15T03:45:00Z",
			ActualAt:        "2026-06-15T03:45:05Z",
			MisfireResult:   "on_time",
			Status:          "succeeded",
			RunSlug:         "run-users-001",
			SourceEventSlug: "evt-schedule",
			ErrorSummary:    " connector timeout ",
		}},
	}
}

// testScheduleFireSeedTrigger returns a schedule trigger snapshot with a non-default revision.
func testScheduleFireSeedTrigger() *pipelinetriggers.PipelineTrigger {
	return &pipelinetriggers.PipelineTrigger{
		Slug:         must.Slug("nightly-users"),
		Revision:     7,
		PipelineSlug: must.Slug("sync-users"),
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "*/15 * * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}
}

// stringPtr returns a pointer to a stable string value for exact struct assertions.
func stringPtr(value string) *string {
	return &value
}

// slugPtr returns a pointer to a parsed slug for exact struct assertions.
func slugPtr(value string) *slug.Slug {
	parsed := must.Slug(value)
	return &parsed
}

type recordingScheduleFireSeedTxManager struct {
	tx    postgres.DBTX
	calls int
}

// RunInTx records transaction usage and executes the supplied function with the fake handle.
func (m *recordingScheduleFireSeedTxManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	m.calls++
	return fn(ctx, m.tx)
}

type findScheduleFireSeedTriggerCall struct {
	orgSlug         slug.Slug
	projectSlug     slug.Slug
	environmentSlug slug.Slug
	triggerSlug     slug.Slug
}

type recordingScheduleFireSeedRepository struct {
	trigger     *pipelinetriggers.PipelineTrigger
	findErr     error
	claimErr    error
	completeErr error

	findCalls   []findScheduleFireSeedTriggerCall
	claims      []pipelinetriggers.ScheduleFireClaim
	completions []pipelinetriggers.ScheduleFireCompletion
}

// FindPipelineTriggerBySlug records the lookup and returns the configured trigger snapshot.
func (r *recordingScheduleFireSeedRepository) FindPipelineTriggerBySlug(
	_ context.Context,
	_ postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) (*pipelinetriggers.PipelineTrigger, error) {
	r.findCalls = append(r.findCalls, findScheduleFireSeedTriggerCall{
		orgSlug:         orgSlug,
		projectSlug:     projectSlug,
		environmentSlug: environmentSlug,
		triggerSlug:     triggerSlug,
	})
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.trigger, nil
}

// ClaimScheduleFire records the claim and returns the deterministic fire slug.
func (r *recordingScheduleFireSeedRepository) ClaimScheduleFire(
	_ context.Context,
	_ postgres.DBTX,
	claim pipelinetriggers.ScheduleFireClaim,
) (pipelinetriggers.ScheduleFireClaimResult, error) {
	r.claims = append(r.claims, claim)
	if r.claimErr != nil {
		return pipelinetriggers.ScheduleFireClaimResult{}, r.claimErr
	}
	return pipelinetriggers.ScheduleFireClaimResult{Claimed: true, FireSlug: claim.FireSlug()}, nil
}

// CompleteScheduleFire records the completion requested by the seed helper.
func (r *recordingScheduleFireSeedRepository) CompleteScheduleFire(
	_ context.Context,
	_ postgres.DBTX,
	completion pipelinetriggers.ScheduleFireCompletion,
) error {
	r.completions = append(r.completions, completion)
	return r.completeErr
}
