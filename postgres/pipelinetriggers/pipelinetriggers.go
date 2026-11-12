package pipelinetriggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type repository struct{}

// ProjectEnvironmentEnabled reports whether the project environment exists and is enabled.
func (r *repository) ProjectEnvironmentEnabled(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (bool, error) {
	ok, err := dbgen.New(tx).PipelineTriggerProjectEnvironmentEnabled(ctx, dbgen.PipelineTriggerProjectEnvironmentEnabledParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return ok, nil
}

// PipelineExists reports whether the target pipeline exists in the requested project.
func (r *repository) PipelineExists(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (bool, error) {
	ok, err := dbgen.New(tx).PipelineTriggerPipelineExists(ctx, dbgen.PipelineTriggerPipelineExistsParams{
		OrgSlug:      orgSlug,
		ProjectSlug:  projectSlug,
		PipelineSlug: pipelineSlug,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return ok, nil
}

// ResolvePinnedVersionHash returns the canonical full hash for a full hash or unique short prefix.
func (r *repository) ResolvePinnedVersionHash(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (string, error) {
	rows, err := dbgen.New(tx).PipelineTriggerPinnedVersionsByHashPrefix(ctx, dbgen.PipelineTriggerPinnedVersionsByHashPrefixParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		PipelineSlug:      pipelineSlug.String(),
		PinnedVersionHash: specHash,
	})
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	if len(rows) == 0 {
		return "", ErrPipelineVersionNotFound
	}
	if len(rows) > 1 {
		return "", ErrPipelineVersionAmbiguous.Detail("pinned version hash prefix is ambiguous")
	}
	return rows[0], nil
}

// DeploymentExists reports whether the target pipeline has a deployed version in the environment.
func (r *repository) DeploymentExists(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (bool, error) {
	ok, err := dbgen.New(tx).PipelineTriggerDeploymentExists(ctx, dbgen.PipelineTriggerDeploymentExistsParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		PipelineSlug:    pipelineSlug.String(),
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return ok, nil
}

// CreatePipelineTrigger inserts a new pipeline trigger in the requested environment.
func (r *repository) CreatePipelineTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params CreatePipelineTriggerParams,
) (*PipelineTrigger, error) {
	retryMaxAttempts, retryInitialDelay, retryMaxDelay, retryMaxElapsed, err := retryPolicySQLValues(params.RetryPolicy)
	if err != nil {
		return nil, err
	}
	webhookInputMappings, err := webhookMappings(params.WebhookConfig)
	if err != nil {
		return nil, err
	}
	completionConfig, err := completionConfigJSON(params.CompletionConfig)
	if err != nil {
		return nil, err
	}
	row, err := dbgen.New(tx).CreatePipelineTrigger(ctx, dbgen.CreatePipelineTriggerParams{
		ProjectSlug:                 projectSlug,
		EnvironmentSlug:             environmentSlug,
		PipelineSlug:                params.PipelineSlug,
		PinnedVersionHash:           optionalString(params.VersionPolicy.PinnedVersionHash),
		OrgSlug:                     orgSlug,
		TriggerSlug:                 params.Slug,
		Name:                        params.Name,
		TriggerType:                 params.Type.String(),
		Enabled:                     params.Enabled,
		VersionPolicy:               params.VersionPolicy.Mode.String(),
		RuntimeInputKeys:            params.RuntimeInputKeys,
		RetryEnabled:                params.RetryPolicy.Enabled,
		RetryMaxAttempts:            retryMaxAttempts,
		RetryBackoffStrategy:        params.RetryPolicy.BackoffStrategy.String(),
		RetryInitialDelaySeconds:    retryInitialDelay,
		RetryMaxDelaySeconds:        retryMaxDelay,
		RetryableFailureKinds:       retryFailureKindStrings(params.RetryPolicy.RetryableFailureKinds),
		RetryMaxElapsedSeconds:      retryMaxElapsed,
		ScheduleCron:                scheduleCron(params.ScheduleConfig),
		ScheduleTimezone:            scheduleTimezone(params.ScheduleConfig),
		ScheduleMisfirePolicy:       scheduleMisfirePolicy(params.ScheduleConfig),
		ReceiverID:                  webhookReceiverID(params.WebhookConfig),
		WebhookVerificationStrategy: webhookStrategy(params.WebhookConfig),
		WebhookInputMappings:        webhookInputMappings,
		CompletionConfig:            completionConfig,
		CreatedByActorType:          params.Actor.Type.String(),
		CreatedByActorSlug:          actorSlugText(params.Actor),
		UpdatedByActorType:          params.Actor.Type.String(),
		UpdatedByActorSlug:          actorSlugText(params.Actor),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrPipelineTriggerDependencyMissing.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_pipeline_triggers_project_environment_slug") {
			return nil, ErrPipelineTriggerSlugTaken.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return triggerFromCreateRow(row)
}

// FindPipelineTriggerBySlug returns one pipeline trigger from the requested environment.
func (r *repository) FindPipelineTriggerBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) (*PipelineTrigger, error) {
	row, err := dbgen.New(tx).FindPipelineTriggerBySlug(ctx, dbgen.FindPipelineTriggerBySlugParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		TriggerSlug:     triggerSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrPipelineTriggerNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return triggerFromFindRow(row)
}

// ListPipelineTriggers returns trigger definitions for the requested environment.
func (r *repository) ListPipelineTriggers(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) ([]*PipelineTrigger, error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(queryReq.Cursor, normalized.SortBy)
	if err != nil {
		return nil, xerror.Wrapf("build pipeline trigger cursor values: %w", err)
	}
	rows, err := dbgen.New(tx).ListPipelineTriggers(ctx, dbgen.ListPipelineTriggersParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		CursorSlug:      cursorSlug,
		OrderDir:        pgtype.Text{String: queryReq.Order, Valid: queryReq.Order != ""},
		CursorTimestamp: cursorTimestamp,
		LimitCount:      int64(queryReq.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]*PipelineTrigger, 0, len(rows))
	for _, row := range rows {
		trigger, err := triggerFromListRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, trigger)
	}
	return result, nil
}

// UpdatePipelineTrigger fully replaces mutable trigger fields and increments revision.
func (r *repository) UpdatePipelineTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	params UpdatePipelineTriggerParams,
) (*PipelineTrigger, error) {
	retryMaxAttempts, retryInitialDelay, retryMaxDelay, retryMaxElapsed, err := retryPolicySQLValues(params.RetryPolicy)
	if err != nil {
		return nil, err
	}
	webhookInputMappings, err := webhookMappings(params.WebhookConfig)
	if err != nil {
		return nil, err
	}
	completionConfig, err := completionConfigJSON(params.CompletionConfig)
	if err != nil {
		return nil, err
	}
	row, err := dbgen.New(tx).UpdatePipelineTrigger(ctx, dbgen.UpdatePipelineTriggerParams{
		PipelineSlug:                params.PipelineSlug,
		PinnedVersionHash:           optionalString(params.VersionPolicy.PinnedVersionHash),
		OrgSlug:                     orgSlug,
		ProjectSlug:                 projectSlug,
		EnvironmentSlug:             environmentSlug,
		TriggerSlug:                 triggerSlug,
		VersionPolicy:               pgtype.Text{String: params.VersionPolicy.Mode.String(), Valid: true},
		Name:                        params.Name,
		TriggerType:                 params.Type.String(),
		Enabled:                     params.Enabled,
		RuntimeInputKeys:            params.RuntimeInputKeys,
		RetryEnabled:                params.RetryPolicy.Enabled,
		RetryMaxAttempts:            retryMaxAttempts,
		RetryBackoffStrategy:        params.RetryPolicy.BackoffStrategy.String(),
		RetryInitialDelaySeconds:    retryInitialDelay,
		RetryMaxDelaySeconds:        retryMaxDelay,
		RetryableFailureKinds:       retryFailureKindStrings(params.RetryPolicy.RetryableFailureKinds),
		RetryMaxElapsedSeconds:      retryMaxElapsed,
		ScheduleCron:                scheduleCron(params.ScheduleConfig),
		ScheduleTimezone:            scheduleTimezone(params.ScheduleConfig),
		ScheduleMisfirePolicy:       scheduleMisfirePolicy(params.ScheduleConfig),
		ReceiverID:                  webhookReceiverID(params.WebhookConfig),
		WebhookVerificationStrategy: webhookStrategy(params.WebhookConfig),
		WebhookInputMappings:        webhookInputMappings,
		CompletionConfig:            completionConfig,
		UpdatedByActorType:          params.Actor.Type.String(),
		UpdatedByActorSlug:          actorSlugText(params.Actor),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrPipelineTriggerNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return triggerFromUpdateRow(row)
}

// DeletePipelineTrigger deletes one trigger from the requested environment.
func (r *repository) DeletePipelineTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) error {
	rowsAffected, err := dbgen.New(tx).DeletePipelineTrigger(ctx, dbgen.DeletePipelineTriggerParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		TriggerSlug:     triggerSlug,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rowsAffected == 0 {
		return ErrPipelineTriggerNotFound
	}
	return nil
}

// triggerFromCreateRow maps a create-returning DB row into the domain trigger shape.
// It delegates enum, nullable, and schedule conversion to triggerFromFields.
func triggerFromCreateRow(row dbgen.CreatePipelineTriggerRow) (*PipelineTrigger, error) {
	return triggerFromFields(triggerFields{
		Slug:                        row.Slug,
		Name:                        row.Name,
		TriggerType:                 row.TriggerType,
		Enabled:                     row.Enabled,
		Revision:                    row.Revision,
		EnvironmentSlug:             row.EnvironmentSlug,
		PipelineSlug:                row.PipelineSlug_2,
		VersionPolicy:               row.VersionPolicy,
		PinnedVersionHash:           row.PinnedVersionHash,
		RuntimeInputKeys:            row.RuntimeInputKeys,
		RetryEnabled:                row.RetryEnabled,
		RetryMaxAttempts:            row.RetryMaxAttempts,
		RetryBackoffStrategy:        row.RetryBackoffStrategy,
		RetryInitialDelaySeconds:    row.RetryInitialDelaySeconds,
		RetryMaxDelaySeconds:        row.RetryMaxDelaySeconds,
		RetryableFailureKinds:       row.RetryableFailureKinds,
		RetryMaxElapsedSeconds:      row.RetryMaxElapsedSeconds,
		ScheduleCron:                row.ScheduleCron,
		ScheduleTimezone:            row.ScheduleTimezone,
		ScheduleMisfirePolicy:       row.ScheduleMisfirePolicy,
		ReceiverID:                  row.ReceiverID,
		WebhookVerificationStrategy: row.WebhookVerificationStrategy,
		WebhookInputMappings:        row.WebhookInputMappings,
		CompletionConfig:            row.CompletionConfig,
		EnabledKeyVersions:          row.EnabledKeyVersions,
		CreatedByActorType:          row.CreatedByActorType,
		CreatedByActorSlug:          row.CreatedByActorSlug,
		UpdatedByActorType:          row.UpdatedByActorType,
		UpdatedByActorSlug:          row.UpdatedByActorSlug,
		DisabledAt:                  row.DisabledAt,
		CreatedAt:                   row.CreatedAt,
		UpdatedAt:                   row.UpdatedAt,
	})
}

// triggerFromFindRow maps a point lookup DB row into the domain trigger shape.
// It delegates enum, nullable, and schedule conversion to triggerFromFields.
func triggerFromFindRow(row dbgen.FindPipelineTriggerBySlugRow) (*PipelineTrigger, error) {
	return triggerFromFields(triggerFields{
		Slug:                        row.Slug,
		Name:                        row.Name,
		TriggerType:                 row.TriggerType,
		Enabled:                     row.Enabled,
		Revision:                    row.Revision,
		EnvironmentSlug:             row.EnvironmentSlug,
		PipelineSlug:                row.PipelineSlug_2,
		VersionPolicy:               row.VersionPolicy,
		PinnedVersionHash:           row.PinnedVersionHash,
		RuntimeInputKeys:            row.RuntimeInputKeys,
		RetryEnabled:                row.RetryEnabled,
		RetryMaxAttempts:            row.RetryMaxAttempts,
		RetryBackoffStrategy:        row.RetryBackoffStrategy,
		RetryInitialDelaySeconds:    row.RetryInitialDelaySeconds,
		RetryMaxDelaySeconds:        row.RetryMaxDelaySeconds,
		RetryableFailureKinds:       row.RetryableFailureKinds,
		RetryMaxElapsedSeconds:      row.RetryMaxElapsedSeconds,
		ScheduleCron:                row.ScheduleCron,
		ScheduleTimezone:            row.ScheduleTimezone,
		ScheduleMisfirePolicy:       row.ScheduleMisfirePolicy,
		ReceiverID:                  row.ReceiverID,
		WebhookVerificationStrategy: row.WebhookVerificationStrategy,
		WebhookInputMappings:        row.WebhookInputMappings,
		CompletionConfig:            row.CompletionConfig,
		EnabledKeyVersions:          row.EnabledKeyVersions,
		CreatedByActorType:          row.CreatedByActorType,
		CreatedByActorSlug:          row.CreatedByActorSlug,
		UpdatedByActorType:          row.UpdatedByActorType,
		UpdatedByActorSlug:          row.UpdatedByActorSlug,
		DisabledAt:                  row.DisabledAt,
		CreatedAt:                   row.CreatedAt,
		UpdatedAt:                   row.UpdatedAt,
	})
}

// triggerFromListRow maps a list-query DB row into the domain trigger shape.
// It delegates enum, nullable, and schedule conversion to triggerFromFields.
func triggerFromListRow(row dbgen.ListPipelineTriggersRow) (*PipelineTrigger, error) {
	return triggerFromFields(triggerFields{
		Slug:                        row.Slug,
		Name:                        row.Name,
		TriggerType:                 row.TriggerType,
		Enabled:                     row.Enabled,
		Revision:                    row.Revision,
		EnvironmentSlug:             row.EnvironmentSlug,
		PipelineSlug:                row.PipelineSlug_2,
		VersionPolicy:               row.VersionPolicy,
		PinnedVersionHash:           row.PinnedVersionHash,
		RuntimeInputKeys:            row.RuntimeInputKeys,
		RetryEnabled:                row.RetryEnabled,
		RetryMaxAttempts:            row.RetryMaxAttempts,
		RetryBackoffStrategy:        row.RetryBackoffStrategy,
		RetryInitialDelaySeconds:    row.RetryInitialDelaySeconds,
		RetryMaxDelaySeconds:        row.RetryMaxDelaySeconds,
		RetryableFailureKinds:       row.RetryableFailureKinds,
		RetryMaxElapsedSeconds:      row.RetryMaxElapsedSeconds,
		ScheduleCron:                row.ScheduleCron,
		ScheduleTimezone:            row.ScheduleTimezone,
		ScheduleMisfirePolicy:       row.ScheduleMisfirePolicy,
		ReceiverID:                  row.ReceiverID,
		WebhookVerificationStrategy: row.WebhookVerificationStrategy,
		WebhookInputMappings:        row.WebhookInputMappings,
		CompletionConfig:            row.CompletionConfig,
		EnabledKeyVersions:          row.EnabledKeyVersions,
		CreatedByActorType:          row.CreatedByActorType,
		CreatedByActorSlug:          row.CreatedByActorSlug,
		UpdatedByActorType:          row.UpdatedByActorType,
		UpdatedByActorSlug:          row.UpdatedByActorSlug,
		DisabledAt:                  row.DisabledAt,
		CreatedAt:                   row.CreatedAt,
		UpdatedAt:                   row.UpdatedAt,
	})
}

// triggerFromUpdateRow maps an update-returning DB row into the domain trigger shape.
// It delegates enum, nullable, and schedule conversion to triggerFromFields.
func triggerFromUpdateRow(row dbgen.UpdatePipelineTriggerRow) (*PipelineTrigger, error) {
	return triggerFromFields(triggerFields{
		Slug:                        row.Slug,
		Name:                        row.Name,
		TriggerType:                 row.TriggerType,
		Enabled:                     row.Enabled,
		Revision:                    row.Revision,
		EnvironmentSlug:             row.EnvironmentSlug,
		PipelineSlug:                row.PipelineSlug_2,
		VersionPolicy:               row.VersionPolicy,
		PinnedVersionHash:           row.PinnedVersionHash,
		RuntimeInputKeys:            row.RuntimeInputKeys,
		RetryEnabled:                row.RetryEnabled,
		RetryMaxAttempts:            row.RetryMaxAttempts,
		RetryBackoffStrategy:        row.RetryBackoffStrategy,
		RetryInitialDelaySeconds:    row.RetryInitialDelaySeconds,
		RetryMaxDelaySeconds:        row.RetryMaxDelaySeconds,
		RetryableFailureKinds:       row.RetryableFailureKinds,
		RetryMaxElapsedSeconds:      row.RetryMaxElapsedSeconds,
		ScheduleCron:                row.ScheduleCron,
		ScheduleTimezone:            row.ScheduleTimezone,
		ScheduleMisfirePolicy:       row.ScheduleMisfirePolicy,
		ReceiverID:                  row.ReceiverID,
		WebhookVerificationStrategy: row.WebhookVerificationStrategy,
		WebhookInputMappings:        row.WebhookInputMappings,
		CompletionConfig:            row.CompletionConfig,
		EnabledKeyVersions:          row.EnabledKeyVersions,
		CreatedByActorType:          row.CreatedByActorType,
		CreatedByActorSlug:          row.CreatedByActorSlug,
		UpdatedByActorType:          row.UpdatedByActorType,
		UpdatedByActorSlug:          row.UpdatedByActorSlug,
		DisabledAt:                  row.DisabledAt,
		CreatedAt:                   row.CreatedAt,
		UpdatedAt:                   row.UpdatedAt,
	})
}

type triggerFields struct {
	Slug                        slug.Slug
	Name                        string
	TriggerType                 string
	Enabled                     bool
	Revision                    int64
	EnvironmentSlug             slug.Slug
	PipelineSlug                slug.Slug
	VersionPolicy               string
	PinnedVersionHash           pgtype.Text
	RuntimeInputKeys            []string
	RetryEnabled                bool
	RetryMaxAttempts            int32
	RetryBackoffStrategy        string
	RetryInitialDelaySeconds    int32
	RetryMaxDelaySeconds        int32
	RetryableFailureKinds       []string
	RetryMaxElapsedSeconds      pgtype.Int4
	ScheduleCron                pgtype.Text
	ScheduleTimezone            pgtype.Text
	ScheduleMisfirePolicy       pgtype.Text
	ReceiverID                  pgtype.Text
	WebhookVerificationStrategy pgtype.Text
	CompletionConfig            []byte
	WebhookInputMappings        []byte
	EnabledKeyVersions          []int32
	CreatedByActorType          string
	CreatedByActorSlug          pgtype.Text
	UpdatedByActorType          string
	UpdatedByActorSlug          pgtype.Text
	DisabledAt                  pgtype.Timestamptz
	CreatedAt                   pgtype.Timestamptz
	UpdatedAt                   pgtype.Timestamptz
}

// triggerFromFields converts raw SQLC trigger fields into validated domain values.
// It parses enum strings, pgtype nullable values, actor slugs, retry policy, and schedule config.
func triggerFromFields(fields triggerFields) (*PipelineTrigger, error) {
	triggerType, err := model.PipelineTriggerTypeString(fields.TriggerType)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	versionPolicyMode, err := model.PipelineTriggerVersionPolicyModeString(fields.VersionPolicy)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	backoffStrategy, err := model.PipelineTriggerBackoffStrategyString(fields.RetryBackoffStrategy)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	retryableKinds, err := retryFailureKindsFromStrings(fields.RetryableFailureKinds)
	if err != nil {
		return nil, err
	}
	createdBy, err := actorFromFields(fields.CreatedByActorType, fields.CreatedByActorSlug)
	if err != nil {
		return nil, err
	}
	updatedBy, err := actorFromFields(fields.UpdatedByActorType, fields.UpdatedByActorSlug)
	if err != nil {
		return nil, err
	}

	trigger := &PipelineTrigger{
		Slug:            fields.Slug,
		Name:            fields.Name,
		Type:            triggerType,
		Enabled:         fields.Enabled,
		Revision:        fields.Revision,
		EnvironmentSlug: fields.EnvironmentSlug,
		PipelineSlug:    fields.PipelineSlug,
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode:              versionPolicyMode,
			PinnedVersionHash: optionalTextString(fields.PinnedVersionHash),
		},
		RuntimeInputKeys: fields.RuntimeInputKeys,
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:               fields.RetryEnabled,
			MaxAttempts:           int(fields.RetryMaxAttempts),
			BackoffStrategy:       backoffStrategy,
			InitialDelaySeconds:   int(fields.RetryInitialDelaySeconds),
			MaxDelaySeconds:       int(fields.RetryMaxDelaySeconds),
			RetryableFailureKinds: retryableKinds,
			MaxElapsedSeconds:     optionalIntValue(fields.RetryMaxElapsedSeconds),
		},
		CreatedBy:  createdBy,
		UpdatedBy:  updatedBy,
		DisabledAt: postgres.FromNullableTimestamptz(fields.DisabledAt),
		CreatedAt:  fields.CreatedAt.Time,
		UpdatedAt:  fields.UpdatedAt.Time,
	}
	if fields.ScheduleCron.Valid || triggerType == model.PipelineTriggerTypeSchedule {
		misfirePolicy, err := model.PipelineTriggerScheduleMisfirePolicyString(fields.ScheduleMisfirePolicy.String)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		trigger.ScheduleConfig = &model.PipelineTriggerScheduleConfig{
			Cron:          fields.ScheduleCron.String,
			Timezone:      fields.ScheduleTimezone.String,
			MisfirePolicy: misfirePolicy,
		}
	}
	if triggerType == model.PipelineTriggerTypeWebhook {
		var mappings []model.PipelineWebhookInputMapping
		if err := json.Unmarshal(fields.WebhookInputMappings, &mappings); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		versions := make([]int, len(fields.EnabledKeyVersions))
		for i, version := range fields.EnabledKeyVersions {
			versions[i] = int(version)
		}
		trigger.WebhookConfig = &model.PipelineTriggerWebhookConfig{
			ReceiverID:           fields.ReceiverID.String,
			VerificationStrategy: fields.WebhookVerificationStrategy.String,
			InputMappings:        mappings,
			EnabledKeyVersions:   versions,
			SecretConfigured:     len(versions) > 0,
		}
	}
	if triggerType == model.PipelineTriggerTypeCompletion {
		trigger.CompletionConfig = new(model.PipelineTriggerCompletionConfig)
		if err := json.Unmarshal(fields.CompletionConfig, trigger.CompletionConfig); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	}
	return trigger, nil
}

// retryFailureKindStrings converts typed retry failure kinds to their persisted string values.
func retryFailureKindStrings(values []model.PipelineTriggerRetryFailureKind) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.String()
	}
	return result
}

// retryFailureKindsFromStrings parses persisted retry failure kind strings back to typed values.
// Invalid database values are surfaced as unexpected decode errors.
func retryFailureKindsFromStrings(values []string) ([]model.PipelineTriggerRetryFailureKind, error) {
	result := make([]model.PipelineTriggerRetryFailureKind, len(values))
	for i, value := range values {
		parsed, err := model.PipelineTriggerRetryFailureKindString(value)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		result[i] = parsed
	}
	return result, nil
}

// optionalString shapes optional domain strings for nullable SQL text parameters.
func optionalString(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

// optionalTextString converts nullable SQL text into optional domain strings.
func optionalTextString(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func webhookReceiverID(config *model.PipelineTriggerWebhookConfig) pgtype.Text {
	if config == nil || config.ReceiverID == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: config.ReceiverID, Valid: true}
}

func webhookStrategy(config *model.PipelineTriggerWebhookConfig) pgtype.Text {
	if config == nil || config.VerificationStrategy == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: config.VerificationStrategy, Valid: true}
}

func webhookMappings(config *model.PipelineTriggerWebhookConfig) ([]byte, error) {
	if config == nil {
		return nil, nil
	}
	value, err := json.Marshal(config.InputMappings)
	if err != nil {
		return nil, xerror.Wrapf("marshal pipeline webhook input mappings: %w", err)
	}
	return value, nil
}

// retryPolicySQLValues converts retry policy integers to int4 SQL parameters with bounds checks.
func retryPolicySQLValues(policy model.PipelineTriggerRetryPolicy) (int32, int32, int32, pgtype.Int4, error) {
	maxAttempts, err := int32PolicyValue(policy.MaxAttempts, "retry max_attempts")
	if err != nil {
		return 0, 0, 0, pgtype.Int4{}, err
	}
	initialDelay, err := int32PolicyValue(policy.InitialDelaySeconds, "retry initial_delay_seconds")
	if err != nil {
		return 0, 0, 0, pgtype.Int4{}, err
	}
	maxDelay, err := int32PolicyValue(policy.MaxDelaySeconds, "retry max_delay_seconds")
	if err != nil {
		return 0, 0, 0, pgtype.Int4{}, err
	}
	maxElapsed, err := optionalInt32PolicyValue(policy.MaxElapsedSeconds, "retry max_elapsed_seconds")
	if err != nil {
		return 0, 0, 0, pgtype.Int4{}, err
	}
	return maxAttempts, initialDelay, maxDelay, maxElapsed, nil
}

// int32PolicyValue rejects retry policy integers that cannot fit the SQL int4 fields.
func int32PolicyValue(value int, field string) (int32, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, xerror.InvalidInput(xerror.PipelineTrigger).Detailf("%s must fit in int32", field)
	}
	return int32(value), nil
}

// optionalInt32PolicyValue converts a nullable retry policy integer to a SQL int4 value.
func optionalInt32PolicyValue(value *int, field string) (pgtype.Int4, error) {
	if value == nil {
		return pgtype.Int4{}, nil
	}
	converted, err := int32PolicyValue(*value, field)
	if err != nil {
		return pgtype.Int4{}, err
	}
	return pgtype.Int4{Int32: converted, Valid: true}, nil
}

// optionalIntValue converts nullable SQL int4 values back to optional domain ints.
func optionalIntValue(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int32)
	return &result
}

// actorSlugText shapes an optional actor slug for nullable SQL text fields.
func actorSlugText(actor model.EventActor) pgtype.Text {
	if actor.Slug == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: actor.Slug.String(), Valid: true}
}

// actorFromFields parses stored actor fields and rejects invalid actor types from persisted rows.
func actorFromFields(actorType string, actorSlug pgtype.Text) (model.EventActor, error) {
	parsed, err := model.EventActorTypeString(actorType)
	if err != nil {
		return model.EventActor{}, xerror.Unexpected().With(err)
	}
	return model.EventActor{Type: parsed, Slug: optionalSlug(actorSlug)}, nil
}

// optionalSlug converts nullable SQL text into an optional slug without revalidating DB-owned data.
func optionalSlug(value pgtype.Text) *slug.Slug {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := slug.FromString(value.String)
	return &parsed
}

// scheduleCron shapes optional schedule cron config for nullable SQL text fields.
func scheduleCron(config *model.PipelineTriggerScheduleConfig) pgtype.Text {
	if config == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: config.Cron, Valid: true}
}

// scheduleTimezone shapes optional schedule timezone config for nullable SQL text fields.
func scheduleTimezone(config *model.PipelineTriggerScheduleConfig) pgtype.Text {
	if config == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: config.Timezone, Valid: true}
}

// scheduleMisfirePolicy shapes optional schedule misfire policy config for nullable SQL text fields.
func scheduleMisfirePolicy(config *model.PipelineTriggerScheduleConfig) pgtype.Text {
	if config == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: config.MisfirePolicy.String(), Valid: true}
}

func completionConfigJSON(config *model.PipelineTriggerCompletionConfig) ([]byte, error) {
	if config == nil {
		return nil, nil
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return data, nil
}
