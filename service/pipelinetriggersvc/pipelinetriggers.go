package pipelinetriggersvc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// CreatePipelineTrigger validates and persists a new environment-scoped trigger resource.
func (s *service) CreatePipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	newTrigger model.NewPipelineTrigger,
	actor model.EventActor,
) (*model.PipelineTrigger, error) {
	if err := validate.Default().Struct(newTrigger); err != nil {
		return nil, ErrInvalidPipelineTriggerInput.With(err)
	}
	if err := validateTriggerTypeConfig(newTrigger.Type, newTrigger.ScheduleConfig, newTrigger.WebhookConfig, newTrigger.CompletionConfig); err != nil {
		return nil, err
	}
	runtimeInputKeys, err := normalizeRuntimeInputKeys(newTrigger.RuntimeInputKeys)
	if err != nil {
		return nil, err
	}
	newTrigger.RuntimeInputKeys = runtimeInputKeys
	if err := validateVersionPolicy(newTrigger.VersionPolicy); err != nil {
		return nil, err
	}
	if err := validateRetryPolicy(newTrigger.RetryPolicy); err != nil {
		return nil, err
	}
	if err := validateScheduleConfig(newTrigger.ScheduleConfig); err != nil {
		return nil, err
	}
	if err := validateWebhookConfig(newTrigger.WebhookConfig, newTrigger.RuntimeInputKeys); err != nil {
		return nil, err
	}
	if newTrigger.Type == model.PipelineTriggerTypeWebhook {
		receiverID, err := randomOpaqueValue(32)
		if err != nil {
			return nil, err
		}
		newTrigger.WebhookConfig.ReceiverID = receiverID
		newTrigger.WebhookConfig.VerificationStrategy = "hmac_sha256"
	}

	slugWasProvided := newTrigger.Slug != ""
	baseSlug, err := newTrigger.Slug.ValidateOrGenerate(newTrigger.Name)
	if err != nil {
		return nil, ErrInvalidPipelineTriggerInput.Detailf("failed to generate slug: %w", err)
	}
	newTrigger.Slug = baseSlug

	var created *model.PipelineTrigger
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.validateCompletionTarget(ctx, tx, orgSlug, projectSlug, environmentSlug, "", newTrigger.PipelineSlug, newTrigger.Enabled, newTrigger.CompletionConfig); err != nil {
			return err
		}
		resolvedVersionPolicy, err := s.resolveTargetState(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			newTrigger.PipelineSlug,
			newTrigger.VersionPolicy,
			newTrigger.Enabled,
		)
		if err != nil {
			return err
		}
		newTrigger.VersionPolicy = resolvedVersionPolicy

		repoTrigger, err := s.repo.CreatePipelineTrigger(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelinetriggers.CreatePipelineTriggerParams{
			Slug:             newTrigger.Slug,
			Name:             newTrigger.Name,
			Type:             newTrigger.Type,
			Enabled:          newTrigger.Enabled,
			PipelineSlug:     newTrigger.PipelineSlug,
			VersionPolicy:    newTrigger.VersionPolicy,
			RuntimeInputKeys: newTrigger.RuntimeInputKeys,
			RetryPolicy:      newTrigger.RetryPolicy,
			ScheduleConfig:   newTrigger.ScheduleConfig,
			WebhookConfig:    newTrigger.WebhookConfig,
			CompletionConfig: newTrigger.CompletionConfig,
			Actor:            actor,
		})
		if err != nil && xerror.Is(err, ErrPipelineTriggerSlugTaken) && !slugWasProvided {
			newTrigger.Slug, err = slug.WithRandomSuffix(baseSlug, 6)
			if err != nil {
				return ErrInvalidPipelineTriggerInput.Detailf("failed to generate slug: %w", err)
			}
			repoTrigger, err = s.repo.CreatePipelineTrigger(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelinetriggers.CreatePipelineTriggerParams{
				Slug:             newTrigger.Slug,
				Name:             newTrigger.Name,
				Type:             newTrigger.Type,
				Enabled:          newTrigger.Enabled,
				PipelineSlug:     newTrigger.PipelineSlug,
				VersionPolicy:    newTrigger.VersionPolicy,
				RuntimeInputKeys: newTrigger.RuntimeInputKeys,
				RetryPolicy:      newTrigger.RetryPolicy,
				ScheduleConfig:   newTrigger.ScheduleConfig,
				WebhookConfig:    newTrigger.WebhookConfig,
				CompletionConfig: newTrigger.CompletionConfig,
				Actor:            actor,
			})
		}
		if err != nil {
			return err
		}
		created = toModelTrigger(repoTrigger)
		if created.Type == model.PipelineTriggerTypeWebhook {
			issued, err := s.webhookSecrets.IssuePipelineWebhookSecret(ctx, credentialssvc.PipelineWebhookSecretParams{
				OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, TriggerSlug: created.Slug,
			})
			if err != nil {
				return err
			}
			created.WebhookConfig.EnabledKeyVersions = []int{issued.KeyVersion}
			created.WebhookConfig.SecretConfigured = true
			created.WebhookSecret = &model.PipelineWebhookSecret{KeyVersion: issued.KeyVersion, Value: issued.Value}
		}
		return s.recordPipelineTriggerEvent(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			actor,
			created,
			model.EventActionCreate,
		)
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// GetPipelineTrigger returns a safe trigger definition from one project environment.
func (s *service) GetPipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) (*model.PipelineTrigger, error) {
	var trigger *model.PipelineTrigger
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoTrigger, err := s.repo.FindPipelineTriggerBySlug(ctx, db, orgSlug, projectSlug, environmentSlug, triggerSlug)
		if err != nil {
			return err
		}
		trigger = toModelTrigger(repoTrigger)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return trigger, nil
}

// ListPipelineTriggers returns paginated safe trigger definitions for one environment.
func (s *service) ListPipelineTriggers(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.PipelineTrigger], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)
	var triggers []model.PipelineTrigger
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoTriggers, err := s.repo.ListPipelineTriggers(ctx, db, orgSlug, projectSlug, environmentSlug, queryReq)
		if err != nil {
			return err
		}
		triggers = make([]model.PipelineTrigger, 0, len(repoTriggers))
		for _, repoTrigger := range repoTriggers {
			triggers = append(triggers, *toModelTrigger(repoTrigger))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.PipelineTrigger]{}, err
	}

	triggers, hasMore := model.FinalizePage(triggers, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		triggers,
		normalized,
		hasMore,
		func(item model.PipelineTrigger, _ string) (string, string) {
			return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)
	return model.ListResponse[model.PipelineTrigger]{
		Items:      triggers,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// ListPipelineScheduleFires returns paginated schedule-fire diagnostics for one trigger.
func (s *service) ListPipelineScheduleFires(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.PipelineScheduleFire], error) {
	normalized, _ := model.PrepareListQuery(req, model.ListSortScheduledAt)
	var fires []model.PipelineScheduleFire
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if _, err := s.repo.FindPipelineTriggerBySlug(ctx, db, orgSlug, projectSlug, environmentSlug, triggerSlug); err != nil {
			return err
		}
		repoFires, err := s.repo.ListScheduleFiresForTrigger(ctx, db, orgSlug, projectSlug, environmentSlug, triggerSlug, normalized)
		if err != nil {
			return err
		}
		fires = make([]model.PipelineScheduleFire, 0, len(repoFires))
		for _, repoFire := range repoFires {
			fires = append(fires, toModelScheduleFire(repoFire))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.PipelineScheduleFire]{}, err
	}

	fires, hasMore := model.FinalizePage(fires, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		fires,
		normalized,
		hasMore,
		func(item model.PipelineScheduleFire, _ string) (string, string) {
			return item.ScheduledFireAt.Format(time.RFC3339Nano), item.FireSlug.String()
		},
	)
	return model.ListResponse[model.PipelineScheduleFire]{
		Items:      fires,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// ListPipelineRunTriggerRetryAttempts returns retry diagnostics linked to a run's retry root.
func (s *service) ListPipelineRunTriggerRetryAttempts(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, runSlug slug.Slug,
) ([]model.PipelineTriggerRetryAttempt, error) {
	var attempts []model.PipelineTriggerRetryAttempt
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		repoAttempts, err := s.repo.ListTriggerRetryAttemptsForRun(ctx, db, orgSlug, projectSlug, environmentSlug, runSlug)
		if err != nil {
			return err
		}
		attempts = make([]model.PipelineTriggerRetryAttempt, 0, len(repoAttempts))
		for _, repoAttempt := range repoAttempts {
			attempts = append(attempts, toModelTriggerRetryAttempt(repoAttempt))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return attempts, nil
}

// UpdatePipelineTrigger fully replaces mutable trigger fields.
func (s *service) UpdatePipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	update model.UpdatePipelineTrigger,
	actor model.EventActor,
) (*model.PipelineTrigger, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, ErrInvalidPipelineTriggerInput.With(err)
	}
	if err := validateTriggerTypeConfig(update.Type, update.ScheduleConfig, update.WebhookConfig, update.CompletionConfig); err != nil {
		return nil, err
	}
	runtimeInputKeys, err := normalizeRuntimeInputKeys(update.RuntimeInputKeys)
	if err != nil {
		return nil, err
	}
	update.RuntimeInputKeys = runtimeInputKeys
	if err := validateVersionPolicy(update.VersionPolicy); err != nil {
		return nil, err
	}
	if err := validateRetryPolicy(update.RetryPolicy); err != nil {
		return nil, err
	}
	if err := validateScheduleConfig(update.ScheduleConfig); err != nil {
		return nil, err
	}
	if err := validateWebhookConfig(update.WebhookConfig, update.RuntimeInputKeys); err != nil {
		return nil, err
	}

	var updated *model.PipelineTrigger
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.repo.LockPipelineTriggerTopology(ctx, tx, orgSlug, projectSlug, environmentSlug); err != nil {
			return err
		}
		existingRepo, err := s.repo.FindPipelineTriggerBySlug(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug)
		if err != nil {
			return err
		}
		existing := toModelTrigger(existingRepo)
		if update.Type != existing.Type {
			return ErrInvalidPipelineTriggerInput.Detail("pipeline trigger type is immutable")
		}
		if update.Type == model.PipelineTriggerTypeWebhook {
			update.WebhookConfig.ReceiverID = existing.WebhookConfig.ReceiverID
			update.WebhookConfig.VerificationStrategy = "hmac_sha256"
		}
		if err := s.validateCompletionTarget(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug, update.PipelineSlug, update.Enabled, update.CompletionConfig); err != nil {
			return err
		}
		resolvedVersionPolicy, err := s.resolveTargetState(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			update.PipelineSlug,
			update.VersionPolicy,
			update.Enabled,
		)
		if err != nil {
			return err
		}
		update.VersionPolicy = resolvedVersionPolicy

		repoTrigger, err := s.repo.UpdatePipelineTrigger(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug, pipelinetriggers.UpdatePipelineTriggerParams{
			Name:             update.Name,
			Type:             update.Type,
			Enabled:          update.Enabled,
			PipelineSlug:     update.PipelineSlug,
			VersionPolicy:    update.VersionPolicy,
			RuntimeInputKeys: update.RuntimeInputKeys,
			RetryPolicy:      update.RetryPolicy,
			ScheduleConfig:   update.ScheduleConfig,
			WebhookConfig:    update.WebhookConfig,
			CompletionConfig: update.CompletionConfig,
			Actor:            actor,
		})
		if err != nil {
			return err
		}
		updated = toModelTrigger(repoTrigger)
		return s.recordPipelineTriggerEvent(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			actor,
			updated,
			model.EventActionUpdate,
		)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeletePipelineTrigger removes one trigger definition and records a safe delete event.
func (s *service) DeletePipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.repo.LockPipelineTriggerTopology(ctx, tx, orgSlug, projectSlug, environmentSlug); err != nil {
			return err
		}
		repoTrigger, err := s.repo.FindPipelineTriggerBySlug(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug)
		if err != nil {
			return err
		}
		trigger := toModelTrigger(repoTrigger)
		if trigger.Type == model.PipelineTriggerTypeWebhook {
			if err := s.webhookSecrets.RevokePipelineWebhookSecrets(
				ctx,
				webhookSecretParams(orgSlug, projectSlug, environmentSlug, triggerSlug),
			); err != nil {
				return err
			}
		}
		if err := s.repo.DeletePipelineTrigger(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug); err != nil {
			return err
		}
		return s.recordPipelineTriggerEvent(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			actor,
			trigger,
			model.EventActionDelete,
		)
	})
}

func (s *service) resolveTargetState(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	versionPolicy model.PipelineTriggerVersionPolicy,
	enabled bool,
) (model.PipelineTriggerVersionPolicy, error) {
	// Environment and pipeline existence are checked before version checks so callers
	// receive clear resource errors instead of ambiguous insert failures.
	environmentEnabled, err := s.repo.ProjectEnvironmentEnabled(ctx, tx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return versionPolicy, err
	}
	if !environmentEnabled {
		return versionPolicy, ErrProjectEnvironmentNotFound
	}
	pipelineExists, err := s.repo.PipelineExists(ctx, tx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return versionPolicy, err
	}
	if !pipelineExists {
		return versionPolicy, ErrPipelineNotFound
	}

	switch versionPolicy.Mode {
	case model.PipelineTriggerVersionPolicyModePinned:
		if versionPolicy.PinnedVersionHash == nil {
			return versionPolicy, ErrInvalidPipelineTriggerInput.Detail("pinned_version_hash is required for pinned version_policy")
		}
		resolvedHash, err := s.repo.ResolvePinnedVersionHash(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			pipelineSlug,
			*versionPolicy.PinnedVersionHash,
		)
		if err != nil {
			if xerror.Is(err, pipelinetriggers.ErrPipelineVersionNotFound) {
				return versionPolicy, ErrPipelineVersionNotFound.With(err)
			}
			if xerror.Is(err, pipelinetriggers.ErrPipelineVersionAmbiguous) {
				return versionPolicy, ErrInvalidPipelineTriggerInput.With(err).Detail("pinned_version_hash must be a full hash or unique short prefix")
			}
			return versionPolicy, err
		}
		versionPolicy.PinnedVersionHash = &resolvedHash
	case model.PipelineTriggerVersionPolicyModeLatestDeployed:
		if enabled {
			ok, err := s.repo.DeploymentExists(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
			if err != nil {
				return versionPolicy, err
			}
			if !ok {
				return versionPolicy, ErrPipelineDeploymentNotFound
			}
		}
	default:
		return versionPolicy, ErrInvalidPipelineTriggerInput.Detail("unsupported version_policy mode")
	}
	return versionPolicy, nil
}

func validateTriggerTypeConfig(
	triggerType model.PipelineTriggerType,
	schedule *model.PipelineTriggerScheduleConfig,
	webhook *model.PipelineTriggerWebhookConfig,
	completion *model.PipelineTriggerCompletionConfig,
) error {
	if triggerType != model.PipelineTriggerTypeCompletion && completion != nil {
		return ErrInvalidPipelineTriggerInput.Detail("completion config must be omitted for other trigger types")
	}
	switch triggerType {
	case model.PipelineTriggerTypeCompletion:
		if completion == nil || schedule != nil || webhook != nil {
			return ErrInvalidPipelineTriggerInput.Detail("completion triggers require only completion config")
		}
		if completion.Condition != model.PipelineCompletionConditionSuccess && completion.Condition != model.PipelineCompletionConditionFinalFailure {
			return ErrInvalidPipelineTriggerInput.Detail("completion condition must be success or final_failure")
		}
	case model.PipelineTriggerTypeSchedule:
		if schedule == nil {
			return ErrInvalidPipelineTriggerInput.Detail("schedule config is required for schedule triggers")
		}
		if webhook != nil {
			return ErrInvalidPipelineTriggerInput.Detail("webhook config must be omitted for schedule triggers")
		}
	case model.PipelineTriggerTypeWebhook:
		if webhook == nil {
			return ErrInvalidPipelineTriggerInput.Detail("webhook config is required for webhook triggers")
		}
		if schedule != nil {
			return ErrInvalidPipelineTriggerInput.Detail("schedule config must be omitted for webhook triggers")
		}
	default:
		return ErrInvalidPipelineTriggerInput.Detail("unsupported trigger type")
	}
	return nil
}

func validateWebhookConfig(config *model.PipelineTriggerWebhookConfig, runtimeInputKeys []string) error {
	if config == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(runtimeInputKeys))
	for _, key := range runtimeInputKeys {
		allowed[key] = struct{}{}
	}
	pointers := map[string]struct{}{}
	keys := map[string]struct{}{}
	for _, mapping := range config.InputMappings {
		if !validJSONPointer(mapping.JSONPointer) {
			return ErrInvalidPipelineTriggerInput.Detail("webhook json_pointer is invalid")
		}
		if _, ok := allowed[mapping.RuntimeInputKey]; !ok {
			return ErrInvalidPipelineTriggerInput.Detailf("webhook mapping runtime input %q is not declared", mapping.RuntimeInputKey)
		}
		if _, ok := pointers[mapping.JSONPointer]; ok {
			return ErrInvalidPipelineTriggerInput.Detail("webhook json_pointer mappings must be unique")
		}
		if _, ok := keys[mapping.RuntimeInputKey]; ok {
			return ErrInvalidPipelineTriggerInput.Detail("webhook runtime input mappings must be unique")
		}
		pointers[mapping.JSONPointer] = struct{}{}
		keys[mapping.RuntimeInputKey] = struct{}{}
	}
	return nil
}

func validJSONPointer(value string) bool {
	if value == "" {
		return true
	}
	if value[0] != '/' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] == '~' && (i+1 == len(value) || (value[i+1] != '0' && value[i+1] != '1')) {
			return false
		}
	}
	return true
}

func randomOpaqueValue(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", xerror.Wrapf("generate opaque webhook identity: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func webhookSecretParams(org, project, environment, trigger slug.Slug) credentialssvc.PipelineWebhookSecretParams {
	return credentialssvc.PipelineWebhookSecretParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, TriggerSlug: trigger}
}

func (s *service) RotatePipelineWebhookSecret(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, actor model.EventActor) (model.PipelineWebhookSecret, error) {
	var result model.PipelineWebhookSecret
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		trigger, err := s.repo.FindPipelineTriggerBySlug(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug)
		if err != nil {
			return err
		}
		if trigger.Type != model.PipelineTriggerTypeWebhook {
			return ErrInvalidPipelineTriggerInput.Detail("webhook secret rotation requires a webhook trigger")
		}
		issued, err := s.webhookSecrets.IssuePipelineWebhookSecret(ctx, webhookSecretParams(orgSlug, projectSlug, environmentSlug, triggerSlug))
		if err != nil {
			return err
		}
		result = model.PipelineWebhookSecret{KeyVersion: issued.KeyVersion, Value: issued.Value}
		return nil
	})
	return result, err
}

func validateVersionPolicy(policy model.PipelineTriggerVersionPolicy) error {
	switch policy.Mode {
	case model.PipelineTriggerVersionPolicyModeLatestDeployed:
		if policy.PinnedVersionHash != nil {
			return ErrInvalidPipelineTriggerInput.Detail("pinned_version_hash must be omitted for latest_deployed version_policy")
		}
	case model.PipelineTriggerVersionPolicyModePinned:
		if policy.PinnedVersionHash == nil || strings.TrimSpace(*policy.PinnedVersionHash) == "" {
			return ErrInvalidPipelineTriggerInput.Detail("pinned_version_hash is required for pinned version_policy")
		}
	default:
		return ErrInvalidPipelineTriggerInput.Detail("unsupported version_policy mode")
	}
	return nil
}

func validateRetryPolicy(policy model.PipelineTriggerRetryPolicy) error {
	if !policy.BackoffStrategy.IsAPipelineTriggerBackoffStrategy() ||
		policy.BackoffStrategy == model.PipelineTriggerBackoffStrategyUnknown {
		return ErrInvalidPipelineTriggerInput.Detail("retry backoff_strategy is required")
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 10 {
		return ErrInvalidPipelineTriggerInput.Detail("retry max_attempts must be between 1 and 10")
	}
	if policy.InitialDelaySeconds < 0 || policy.MaxDelaySeconds < 0 {
		return ErrInvalidPipelineTriggerInput.Detail("retry delays must be non-negative")
	}
	if policy.BackoffStrategy == model.PipelineTriggerBackoffStrategyNone {
		if policy.InitialDelaySeconds != 0 || policy.MaxDelaySeconds != 0 || policy.Jitter {
			return ErrInvalidPipelineTriggerInput.Detail("retry delays and jitter require fixed or exponential backoff")
		}
	}
	if policy.BackoffStrategy == model.PipelineTriggerBackoffStrategyFixed ||
		policy.BackoffStrategy == model.PipelineTriggerBackoffStrategyExponential {
		if policy.InitialDelaySeconds <= 0 {
			return ErrInvalidPipelineTriggerInput.Detail("retry initial_delay_seconds must be positive when backoff is enabled")
		}
		if policy.MaxDelaySeconds < policy.InitialDelaySeconds {
			return ErrInvalidPipelineTriggerInput.Detail("retry max_delay_seconds must be greater than or equal to initial_delay_seconds")
		}
	}
	for _, kind := range policy.RetryableFailureKinds {
		switch kind {
		case model.PipelineTriggerRetryFailureKindDataSource, model.PipelineTriggerRetryFailureKindPlatform:
		default:
			return ErrInvalidPipelineTriggerInput.Detail("unsupported retryable failure kind")
		}
	}
	if policy.Enabled && policy.MaxAttempts > 1 && len(policy.RetryableFailureKinds) == 0 {
		return ErrInvalidPipelineTriggerInput.Detail("retryable_failure_kinds are required when retry is enabled")
	}
	if policy.MaxElapsedSeconds != nil && *policy.MaxElapsedSeconds < policy.InitialDelaySeconds {
		return ErrInvalidPipelineTriggerInput.Detail("retry max_elapsed_seconds must cover the initial delay")
	}
	return nil
}

func validateScheduleConfig(config *model.PipelineTriggerScheduleConfig) error {
	if config == nil {
		return nil
	}
	if _, err := cron.ParseStandard(config.Cron); err != nil {
		return ErrInvalidPipelineTriggerInput.Detailf("schedule cron is invalid: %w", err)
	}
	if _, err := time.LoadLocation(config.Timezone); err != nil {
		return ErrInvalidPipelineTriggerInput.Detailf("schedule timezone is invalid: %w", err)
	}
	switch config.MisfirePolicy {
	case model.PipelineTriggerScheduleMisfirePolicySkip, model.PipelineTriggerScheduleMisfirePolicyFireOnce:
	default:
		return ErrInvalidPipelineTriggerInput.Detail("unsupported schedule misfire_policy")
	}
	return nil
}

func normalizeRuntimeInputKeys(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return []string{}, nil
	}
	result := make([]string, 0, len(keys))
	seen := map[string]struct{}{}
	for _, key := range keys {
		if err := pipelineruntimevars.ValidateInputKey(key); err != nil {
			return nil, ErrInvalidPipelineTriggerInput.With(err)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func toModelTrigger(trigger *pipelinetriggers.PipelineTrigger) *model.PipelineTrigger {
	if trigger == nil {
		return nil
	}
	result := &model.PipelineTrigger{
		Slug:             trigger.Slug,
		Name:             trigger.Name,
		Type:             trigger.Type,
		Enabled:          trigger.Enabled,
		Revision:         trigger.Revision,
		EnvironmentSlug:  trigger.EnvironmentSlug,
		PipelineSlug:     trigger.PipelineSlug,
		VersionPolicy:    trigger.VersionPolicy,
		RuntimeInputKeys: append([]string{}, trigger.RuntimeInputKeys...),
		RetryPolicy:      trigger.RetryPolicy,
		CreatedBy:        trigger.CreatedBy,
		UpdatedBy:        trigger.UpdatedBy,
		DisabledAt:       trigger.DisabledAt,
		Timestamps: model.Timestamps{
			CreatedAt: trigger.CreatedAt,
			UpdatedAt: trigger.UpdatedAt,
		},
	}
	if trigger.CompletionConfig != nil {
		config := *trigger.CompletionConfig
		result.CompletionConfig = &config
	}
	if trigger.ScheduleConfig != nil {
		result.ScheduleConfig = &model.PipelineTriggerScheduleConfig{
			Cron:          trigger.ScheduleConfig.Cron,
			Timezone:      trigger.ScheduleConfig.Timezone,
			MisfirePolicy: trigger.ScheduleConfig.MisfirePolicy,
		}
	}
	if trigger.WebhookConfig != nil {
		result.WebhookConfig = &model.PipelineTriggerWebhookConfig{
			ReceiverID:           trigger.WebhookConfig.ReceiverID,
			VerificationStrategy: trigger.WebhookConfig.VerificationStrategy,
			InputMappings:        append([]model.PipelineWebhookInputMapping(nil), trigger.WebhookConfig.InputMappings...),
			EnabledKeyVersions:   append([]int(nil), trigger.WebhookConfig.EnabledKeyVersions...),
			SecretConfigured:     trigger.WebhookConfig.SecretConfigured,
		}
	}
	return result
}

// toModelScheduleFire copies a repository schedule-fire diagnostic into the shared service model.
func toModelScheduleFire(fire pipelinetriggers.ScheduleFireDiagnostic) model.PipelineScheduleFire {
	return model.PipelineScheduleFire{
		FireSlug:        fire.FireSlug,
		TriggerSlug:     fire.TriggerSlug,
		TriggerRevision: fire.TriggerRevision,
		PipelineSlug:    fire.PipelineSlug,
		ScheduledFireAt: fire.ScheduledFireAt,
		ActualFireAt:    fire.ActualFireAt,
		Status:          fire.Status,
		MisfireResult:   fire.MisfireResult,
		RunSlug:         ptrSlugCopy(fire.RunSlug),
		SourceEventSlug: ptrSlugCopy(fire.SourceEventSlug),
		ErrorSummary:    ptrStringCopy(fire.ErrorSummary),
		Timestamps: model.Timestamps{
			CreatedAt: fire.CreatedAt,
			UpdatedAt: fire.UpdatedAt,
		},
	}
}

// toModelTriggerRetryAttempt copies a repository retry diagnostic into the shared service model.
func toModelTriggerRetryAttempt(attempt pipelinetriggers.TriggerRetryAttemptDiagnostic) model.PipelineTriggerRetryAttempt {
	return model.PipelineTriggerRetryAttempt{
		AttemptSlug:     attempt.AttemptSlug,
		Status:          attempt.Status,
		DecisionReason:  ptrStringCopy(attempt.DecisionReason),
		TriggerSlug:     attempt.TriggerSlug,
		TriggerRevision: attempt.TriggerRevision,
		PipelineSlug:    attempt.PipelineSlug,
		RootRunSlug:     attempt.RootRunSlug,
		RetryOfRunSlug:  attempt.RetryOfRunSlug,
		RunSlug:         attempt.RunSlug,
		AttemptNumber:   attempt.AttemptNumber,
		FailureKind:     retryFailureKindCopy(attempt.FailureKind),
		SourceEventSlug: ptrSlugCopy(attempt.SourceEventSlug),
		ScheduledAt:     attempt.ScheduledAt,
		EnqueuedAt:      ptrTimeCopy(attempt.EnqueuedAt),
		StartedAt:       ptrTimeCopy(attempt.StartedAt),
		CompletedAt:     ptrTimeCopy(attempt.CompletedAt),
		ErrorSummary:    ptrStringCopy(attempt.ErrorSummary),
		Timestamps: model.Timestamps{
			CreatedAt: attempt.CreatedAt,
			UpdatedAt: attempt.UpdatedAt,
		},
	}
}

// ptrStringCopy preserves optional string ownership across repository/service boundaries.
func ptrStringCopy(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// ptrTimeCopy preserves optional timestamp ownership across repository/service boundaries.
func ptrTimeCopy(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// ptrSlugCopy preserves optional slug ownership across repository/service boundaries.
func ptrSlugCopy(value *slug.Slug) *slug.Slug {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// retryFailureKindCopy preserves optional retry failure kinds across repository/service boundaries.
func retryFailureKindCopy(value *model.PipelineTriggerRetryFailureKind) *model.PipelineTriggerRetryFailureKind {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
