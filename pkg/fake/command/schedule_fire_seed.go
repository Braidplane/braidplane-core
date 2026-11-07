package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
)

type scheduleFireSeedPayload struct {
	OrgSlug         string                `json:"orgSlug"`
	ProjectSlug     string                `json:"projectSlug"`
	EnvironmentSlug string                `json:"environmentSlug"`
	Fires           []scheduleFireSeedRow `json:"fires"`
}

type scheduleFireSeedRow struct {
	TriggerSlug     string `json:"triggerSlug"`
	ScheduledAt     string `json:"scheduledAt"`
	ActualAt        string `json:"actualAt"`
	MisfireResult   string `json:"misfireResult"`
	Status          string `json:"status"`
	RunSlug         string `json:"runSlug"`
	SourceEventSlug string `json:"sourceEventSlug"`
	ErrorSummary    string `json:"errorSummary"`
}

type scheduleFireSeedTarget struct {
	orgSlug         slug.Slug
	projectSlug     slug.Slug
	environmentSlug slug.Slug
}

type scheduleFireSeedTransactionManager interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context, tx postgres.DBTX) error) error
}

type scheduleFireSeedRepository interface {
	FindPipelineTriggerBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	) (*pipelinetriggers.PipelineTrigger, error)
	ClaimScheduleFire(
		ctx context.Context,
		tx postgres.DBTX,
		claim pipelinetriggers.ScheduleFireClaim,
	) (pipelinetriggers.ScheduleFireClaimResult, error)
	CompleteScheduleFire(
		ctx context.Context,
		tx postgres.DBTX,
		completion pipelinetriggers.ScheduleFireCompletion,
	) error
}

// seedScheduleFiresFromReader reads Playwright fixture rows and writes schedule-fire diagnostics.
func seedScheduleFiresFromReader(ctx context.Context, input io.Reader, connectionString string) error {
	payload, err := decodeScheduleFireSeedPayload(input)
	if err != nil {
		return err
	}
	if len(payload.Fires) == 0 {
		return nil
	}
	if strings.TrimSpace(connectionString) == "" {
		return xerror.ValidationFailed().Detail("database connection string is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, connectionString)
	if err != nil {
		return xerror.Unexpected().Detailf("open database pool: %w", err)
	}
	defer pool.Close()

	txm := postgres.NewTransactionManager(pool)
	repo := pipelinetriggers.NewRepository()
	return seedScheduleFires(ctx, txm, repo, payload)
}

// decodeScheduleFireSeedPayload validates the JSON envelope shared by the Playwright fixture.
func decodeScheduleFireSeedPayload(input io.Reader) (scheduleFireSeedPayload, error) {
	var payload scheduleFireSeedPayload
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return scheduleFireSeedPayload{}, xerror.ValidationFailed().Detailf("decode schedule-fire seed payload: %w", err)
	}
	if err := rejectTrailingScheduleFireSeedJSON(decoder); err != nil {
		return scheduleFireSeedPayload{}, err
	}
	if strings.TrimSpace(payload.OrgSlug) == "" {
		return scheduleFireSeedPayload{}, xerror.ValidationFailed().Detail("orgSlug is required")
	}
	if strings.TrimSpace(payload.ProjectSlug) == "" {
		return scheduleFireSeedPayload{}, xerror.ValidationFailed().Detail("projectSlug is required")
	}
	if strings.TrimSpace(payload.EnvironmentSlug) == "" {
		return scheduleFireSeedPayload{}, xerror.ValidationFailed().Detail("environmentSlug is required")
	}
	return payload, nil
}

// rejectTrailingScheduleFireSeedJSON rejects non-whitespace content after the payload object.
func rejectTrailingScheduleFireSeedJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == nil {
		return xerror.ValidationFailed().Detail("decode schedule-fire seed payload: trailing JSON content is not allowed")
	}
	if err != io.EOF {
		return xerror.ValidationFailed().Detailf("decode schedule-fire seed payload: trailing JSON content is invalid: %w", err)
	}
	return nil
}

// seedScheduleFires claims then completes each requested diagnostic inside one transaction.
func seedScheduleFires(
	ctx context.Context,
	txm scheduleFireSeedTransactionManager,
	repo scheduleFireSeedRepository,
	payload scheduleFireSeedPayload,
) error {
	target, err := parseScheduleFireSeedTarget(payload)
	if err != nil {
		return err
	}

	return txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		for _, fire := range payload.Fires {
			if err := seedScheduleFire(ctx, tx, repo, target, fire); err != nil {
				return err
			}
		}
		return nil
	})
}

// parseScheduleFireSeedTarget parses tenant and environment slugs before opening a transaction.
func parseScheduleFireSeedTarget(payload scheduleFireSeedPayload) (scheduleFireSeedTarget, error) {
	orgSlug, err := parseScheduleFireSeedSlug("orgSlug", payload.OrgSlug)
	if err != nil {
		return scheduleFireSeedTarget{}, err
	}
	projectSlug, err := parseScheduleFireSeedSlug("projectSlug", payload.ProjectSlug)
	if err != nil {
		return scheduleFireSeedTarget{}, err
	}
	environmentSlug, err := parseScheduleFireSeedSlug("environmentSlug", payload.EnvironmentSlug)
	if err != nil {
		return scheduleFireSeedTarget{}, err
	}
	return scheduleFireSeedTarget{
		orgSlug:         orgSlug,
		projectSlug:     projectSlug,
		environmentSlug: environmentSlug,
	}, nil
}

// seedScheduleFire writes one schedule-fire history row through the trigger repository lifecycle.
func seedScheduleFire(
	ctx context.Context,
	tx postgres.DBTX,
	repo scheduleFireSeedRepository,
	target scheduleFireSeedTarget,
	fire scheduleFireSeedRow,
) error {
	triggerSlug, err := parseScheduleFireSeedSlug("triggerSlug", fire.TriggerSlug)
	if err != nil {
		return err
	}
	scheduledAt, err := parseScheduleFireSeedTime("scheduledAt", fire.ScheduledAt)
	if err != nil {
		return err
	}
	actualAt, err := parseScheduleFireSeedTime("actualAt", fire.ActualAt)
	if err != nil {
		return err
	}
	misfireResult, err := parseScheduleFireSeedMisfireResult(fire.MisfireResult)
	if err != nil {
		return err
	}
	status, err := parseScheduleFireSeedStatus(fire.Status)
	if err != nil {
		return err
	}
	runSlugValue, hasRunSlug, err := parseOptionalScheduleFireSeedSlug("runSlug", fire.RunSlug)
	if err != nil {
		return err
	}
	var runSlug *string
	if hasRunSlug {
		runSlugText := runSlugValue.String()
		runSlug = &runSlugText
	}
	sourceEventSlugValue, hasSourceEventSlug, err := parseOptionalScheduleFireSeedSlug("sourceEventSlug", fire.SourceEventSlug)
	if err != nil {
		return err
	}
	var sourceEventSlug *slug.Slug
	if hasSourceEventSlug {
		sourceEventSlug = &sourceEventSlugValue
	}

	trigger, err := repo.FindPipelineTriggerBySlug(
		ctx,
		tx,
		target.orgSlug,
		target.projectSlug,
		target.environmentSlug,
		triggerSlug,
	)
	if err != nil {
		return xerror.Wrapf("find schedule trigger %q: %w", triggerSlug, err)
	}
	if trigger == nil {
		return xerror.Unexpected().Detailf("find schedule trigger %q returned nil", triggerSlug)
	}
	if trigger.ScheduleConfig == nil {
		return xerror.ValidationFailed().Detailf("trigger %q is not a schedule trigger", triggerSlug)
	}

	claim := pipelinetriggers.ScheduleFireClaim{
		OrgSlug:         target.orgSlug,
		ProjectSlug:     target.projectSlug,
		EnvironmentSlug: target.environmentSlug,
		TriggerSlug:     triggerSlug,
		TriggerRevision: trigger.Revision,
		ScheduledFireAt: scheduledAt,
		ActualFireAt:    actualAt,
		MisfireResult:   misfireResult,
	}
	if _, err := repo.ClaimScheduleFire(ctx, tx, claim); err != nil {
		return xerror.Wrapf("claim schedule fire %q at %s: %w", triggerSlug, scheduledAt.Format(time.RFC3339), err)
	}

	completion := pipelinetriggers.ScheduleFireCompletion{
		OrgSlug:         target.orgSlug,
		ProjectSlug:     target.projectSlug,
		EnvironmentSlug: target.environmentSlug,
		TriggerSlug:     triggerSlug,
		TriggerRevision: trigger.Revision,
		ScheduledFireAt: scheduledAt,
		Status:          status.String(),
		RunSlug:         runSlug,
		SourceEventSlug: sourceEventSlug,
		ErrorSummary:    optionalScheduleFireSeedString(fire.ErrorSummary),
	}
	if err := repo.CompleteScheduleFire(ctx, tx, completion); err != nil {
		return xerror.Wrapf("complete schedule fire %q at %s: %w", triggerSlug, scheduledAt.Format(time.RFC3339), err)
	}
	return nil
}

// parseScheduleFireSeedSlug parses a fixture slug field with a field-specific error.
func parseScheduleFireSeedSlug(field, value string) (slug.Slug, error) {
	parsed, err := slug.New(strings.TrimSpace(value))
	if err != nil {
		return "", xerror.ValidationFailed().Detailf("%s is invalid: %w", field, err)
	}
	return parsed, nil
}

// parseScheduleFireSeedTime parses fixture timestamps before they enter persistence.
func parseScheduleFireSeedTime(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, xerror.ValidationFailed().Detailf("%s is invalid: %w", field, err)
	}
	return parsed, nil
}

// parseScheduleFireSeedStatus parses the desired persisted schedule-fire status.
func parseScheduleFireSeedStatus(value string) (model.PipelineScheduleFireStatus, error) {
	status, err := model.PipelineScheduleFireStatusString(strings.TrimSpace(value))
	if err != nil || status == model.PipelineScheduleFireStatusUnknown {
		return model.PipelineScheduleFireStatusUnknown, xerror.ValidationFailed().With(err).Detail(fmt.Sprintf("status is invalid: %q", value))
	}
	return status, nil
}

// parseScheduleFireSeedMisfireResult parses the misfire result recorded with the fire claim.
func parseScheduleFireSeedMisfireResult(value string) (model.PipelineTriggerScheduleMisfireResult, error) {
	result, err := model.PipelineTriggerScheduleMisfireResultString(strings.TrimSpace(value))
	if err != nil || result == model.PipelineTriggerScheduleMisfireResultUnknown {
		return model.PipelineTriggerScheduleMisfireResultUnknown, xerror.ValidationFailed().With(err).Detail(fmt.Sprintf("misfireResult is invalid: %q", value))
	}
	return result, nil
}

// parseOptionalScheduleFireSeedSlug validates an optional slug and returns a presence flag.
func parseOptionalScheduleFireSeedSlug(field, value string) (slug.Slug, bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false, nil
	}
	parsed, err := parseScheduleFireSeedSlug(field, trimmed)
	if err != nil {
		return "", false, err
	}
	return parsed, true, nil
}

// optionalScheduleFireSeedString trims optional diagnostic text.
func optionalScheduleFireSeedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
