package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// CreatePipelineTrigger creates an environment-scoped pipeline trigger.
func (s *server) CreatePipelineTrigger(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Create, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	var req api.NewPipelineTrigger
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	newTrigger, err := newPipelineTriggerFromAPI(req)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline trigger request: %v", err))
	}
	if newTrigger.Type == model.PipelineTriggerTypeSchedule || newTrigger.Type == model.PipelineTriggerTypeWebhook {
		if err := s.canOrg(c, orgSlug, model.Execute, model.ResourcePipelines); err != nil {
			return err
		}
	}
	if newTrigger.Type == model.PipelineTriggerTypeCompletion {
		if err := s.canCompletionTrigger(c, orgSlug); err != nil {
			return err
		}
	}
	trigger, err := s.pipelineTriggersService.CreatePipelineTrigger(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		newTrigger,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}
	if trigger != nil && trigger.WebhookSecret != nil {
		c.Set(fiber.HeaderCacheControl, "no-store")
	}
	return c.Status(fiber.StatusCreated).JSON(pipelineTriggerIssuanceToAPI(trigger))
}

// ListPipelineTriggers lists environment-scoped pipeline trigger definitions.
func (s *server) ListPipelineTriggers(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.ListPipelineTriggersParams,
) error {
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	response, err := s.pipelineTriggersService.ListPipelineTriggers(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		listRequestFromPipelineTriggerParams(params),
	)
	if err != nil {
		return mapServiceError(err)
	}
	for _, trigger := range response.Items {
		if trigger.Type == model.PipelineTriggerTypeCompletion {
			if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
				return err
			}
			break
		}
	}
	items := make([]api.PipelineTrigger, len(response.Items))
	for i := range response.Items {
		items[i] = pipelineTriggerToAPI(&response.Items[i])
	}
	return c.JSON(api.PipelineTriggerListResponse{
		PipelineTriggers: items,
		HasMore:          response.HasMore,
		NextCursor:       response.NextCursor,
		PrevCursor:       response.PrevCursor,
	})
}

// GetPipelineTrigger returns one environment-scoped pipeline trigger definition.
func (s *server) GetPipelineTrigger(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineTriggerSlugParam api.PipelineTriggerSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineTriggerSlugParam,
	)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	trigger, err := s.pipelineTriggersService.GetPipelineTrigger(c.Context(), orgSlug, projectSlug, environmentSlug, triggerSlug)
	if err != nil {
		return mapServiceError(err)
	}
	if trigger.Type == model.PipelineTriggerTypeCompletion {
		if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelines); err != nil {
			return err
		}
	}
	return c.JSON(pipelineTriggerToAPI(trigger))
}

// ListPipelineScheduleFires returns persisted diagnostics for one schedule trigger.
func (s *server) ListPipelineScheduleFires(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineTriggerSlugParam api.PipelineTriggerSlugParam,
	params api.ListPipelineScheduleFiresParams,
) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineTriggerSlugParam,
	)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	response, err := s.pipelineTriggersService.ListPipelineScheduleFires(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		triggerSlug,
		listRequestFromPipelineScheduleFireParams(params),
	)
	if err != nil {
		return mapServiceError(err)
	}
	items := make([]api.PipelineScheduleFire, len(response.Items))
	for i := range response.Items {
		items[i] = pipelineScheduleFireToAPI(&response.Items[i])
	}
	return c.JSON(api.PipelineScheduleFireListResponse{
		ScheduleFires: items,
		HasMore:       response.HasMore,
		NextCursor:    response.NextCursor,
		PrevCursor:    response.PrevCursor,
	})
}

// UpdatePipelineTrigger fully replaces mutable trigger definition fields.
func (s *server) UpdatePipelineTrigger(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineTriggerSlugParam api.PipelineTriggerSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineTriggerSlugParam,
	)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	var req api.UpdatePipelineTrigger
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	update, err := updatePipelineTriggerFromAPI(req)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline trigger request: %v", err))
	}
	if update.Type == model.PipelineTriggerTypeSchedule || update.Type == model.PipelineTriggerTypeWebhook {
		if err := s.canOrg(c, orgSlug, model.Execute, model.ResourcePipelines); err != nil {
			return err
		}
	}
	if update.Type == model.PipelineTriggerTypeCompletion && update.Enabled && update.CompletionConfig != nil && update.CompletionConfig.ExecutionGrantEnabled {
		if err := s.canCompletionTrigger(c, orgSlug); err != nil {
			return err
		}
	}
	trigger, err := s.pipelineTriggersService.UpdatePipelineTrigger(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		triggerSlug,
		update,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(pipelineTriggerToAPI(trigger))
}

// DeletePipelineTrigger deletes one environment-scoped pipeline trigger definition.
func (s *server) DeletePipelineTrigger(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineTriggerSlugParam api.PipelineTriggerSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
		pipelineTriggerSlugParam,
	)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	if err := s.pipelineTriggersService.DeletePipelineTrigger(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		triggerSlug,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) RotatePipelineWebhookSecret(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineTriggerSlugParam api.PipelineTriggerSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(slugParam, projectSlugParam, environmentSlugParam, pipelineTriggerSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	secret, err := s.pipelineTriggersService.RotatePipelineWebhookSecret(c.Context(), orgSlug, projectSlug, environmentSlug, triggerSlug, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(api.PipelineWebhookSecretIssuance{KeyVersion: secret.KeyVersion, Value: secret.Value})
}

func newPipelineTriggerFromAPI(req api.NewPipelineTrigger) (model.NewPipelineTrigger, error) {
	triggerSlug, err := optionalAPISlug(req.Slug)
	if err != nil {
		return model.NewPipelineTrigger{}, err
	}
	triggerType, err := pipelineTriggerTypeFromAPI(req.Type)
	if err != nil {
		return model.NewPipelineTrigger{}, err
	}
	pipelineSlug, err := slug.New(req.PipelineSlug)
	if err != nil {
		return model.NewPipelineTrigger{}, err
	}
	versionPolicy, err := pipelineTriggerVersionPolicyFromAPI(req.VersionPolicy)
	if err != nil {
		return model.NewPipelineTrigger{}, err
	}
	retryPolicy, err := pipelineTriggerRetryPolicyFromAPI(req.RetryPolicy)
	if err != nil {
		return model.NewPipelineTrigger{}, err
	}
	var schedule *model.PipelineTriggerScheduleConfig
	if req.Schedule != nil {
		schedule, err = pipelineTriggerScheduleConfigFromAPI(req.Schedule)
		if err != nil {
			return model.NewPipelineTrigger{}, err
		}
	}
	completion, err := pipelineTriggerCompletionConfigFromAPI(req.Completion)
	if err != nil {
		return model.NewPipelineTrigger{}, err
	}
	return model.NewPipelineTrigger{
		Name:             req.Name,
		Slug:             triggerSlug,
		Type:             triggerType,
		Enabled:          req.Enabled,
		PipelineSlug:     pipelineSlug,
		VersionPolicy:    versionPolicy,
		RuntimeInputKeys: req.RuntimeInputKeys,
		RetryPolicy:      retryPolicy,
		ScheduleConfig:   schedule,
		CompletionConfig: completion,
		WebhookConfig:    pipelineTriggerWebhookConfigFromAPI(req.Webhook),
	}, nil
}

func updatePipelineTriggerFromAPI(req api.UpdatePipelineTrigger) (model.UpdatePipelineTrigger, error) {
	triggerType, err := pipelineTriggerTypeFromAPI(req.Type)
	if err != nil {
		return model.UpdatePipelineTrigger{}, err
	}
	pipelineSlug, err := slug.New(req.PipelineSlug)
	if err != nil {
		return model.UpdatePipelineTrigger{}, err
	}
	versionPolicy, err := pipelineTriggerVersionPolicyFromAPI(req.VersionPolicy)
	if err != nil {
		return model.UpdatePipelineTrigger{}, err
	}
	retryPolicy, err := pipelineTriggerRetryPolicyFromAPI(req.RetryPolicy)
	if err != nil {
		return model.UpdatePipelineTrigger{}, err
	}
	var schedule *model.PipelineTriggerScheduleConfig
	if req.Schedule != nil {
		schedule, err = pipelineTriggerScheduleConfigFromAPI(req.Schedule)
		if err != nil {
			return model.UpdatePipelineTrigger{}, err
		}
	}
	completion, err := pipelineTriggerCompletionConfigFromAPI(req.Completion)
	if err != nil {
		return model.UpdatePipelineTrigger{}, err
	}
	return model.UpdatePipelineTrigger{
		Name:             req.Name,
		Type:             triggerType,
		Enabled:          req.Enabled,
		PipelineSlug:     pipelineSlug,
		VersionPolicy:    versionPolicy,
		RuntimeInputKeys: req.RuntimeInputKeys,
		RetryPolicy:      retryPolicy,
		ScheduleConfig:   schedule,
		CompletionConfig: completion,
		WebhookConfig:    pipelineTriggerWebhookConfigFromAPI(req.Webhook),
	}, nil
}

func optionalAPISlug(value *api.Slug) (slug.Slug, error) {
	if value == nil {
		return "", nil
	}
	return slug.New(*value)
}

func pipelineTriggerTypeFromAPI(value api.PipelineTriggerType) (model.PipelineTriggerType, error) {
	parsed, err := model.PipelineTriggerTypeString(string(value))
	if err != nil || parsed == model.PipelineTriggerTypeUnknown {
		return model.PipelineTriggerTypeUnknown, xerror.InvalidInput(xerror.PipelineTrigger).
			Detailf("unsupported pipeline trigger type %q", value)
	}
	return parsed, nil
}

func pipelineTriggerVersionPolicyFromAPI(value api.PipelineTriggerVersionPolicy) (model.PipelineTriggerVersionPolicy, error) {
	mode, err := model.PipelineTriggerVersionPolicyModeString(string(value.Mode))
	if err != nil || mode == model.PipelineTriggerVersionPolicyModeUnknown {
		return model.PipelineTriggerVersionPolicy{}, xerror.InvalidInput(xerror.PipelineTrigger).
			Detailf("unsupported pipeline trigger version_policy mode %q", value.Mode)
	}
	return model.PipelineTriggerVersionPolicy{
		Mode:              mode,
		PinnedVersionHash: value.PinnedVersionHash,
	}, nil
}

func pipelineTriggerRetryPolicyFromAPI(value api.PipelineTriggerRetryPolicy) (model.PipelineTriggerRetryPolicy, error) {
	strategy, err := model.PipelineTriggerBackoffStrategyString(string(value.BackoffStrategy))
	if err != nil || strategy == model.PipelineTriggerBackoffStrategyUnknown {
		return model.PipelineTriggerRetryPolicy{}, xerror.InvalidInput(xerror.PipelineTrigger).
			Detailf("unsupported pipeline trigger retry backoff_strategy %q", value.BackoffStrategy)
	}
	kinds, err := pipelineTriggerRetryFailureKindsFromAPI(value.RetryableFailureKinds)
	if err != nil {
		return model.PipelineTriggerRetryPolicy{}, err
	}
	return model.PipelineTriggerRetryPolicy{
		Enabled:               value.Enabled,
		MaxAttempts:           value.MaxAttempts,
		BackoffStrategy:       strategy,
		InitialDelaySeconds:   value.InitialDelaySeconds,
		MaxDelaySeconds:       value.MaxDelaySeconds,
		RetryableFailureKinds: kinds,
		MaxElapsedSeconds:     value.MaxElapsedSeconds,
	}, nil
}

func pipelineTriggerRetryFailureKindsFromAPI(
	values []api.PipelineTriggerRetryFailureKind,
) ([]model.PipelineTriggerRetryFailureKind, error) {
	result := make([]model.PipelineTriggerRetryFailureKind, 0, len(values))
	for _, value := range values {
		parsed, err := model.PipelineTriggerRetryFailureKindString(string(value))
		if err != nil || parsed == model.PipelineTriggerRetryFailureKindUnknown {
			return nil, xerror.InvalidInput(xerror.PipelineTrigger).
				Detailf("unsupported pipeline trigger retryable failure kind %q", value)
		}
		result = append(result, parsed)
	}
	return result, nil
}

func pipelineTriggerScheduleConfigFromAPI(
	value *api.PipelineTriggerScheduleConfig,
) (*model.PipelineTriggerScheduleConfig, error) {
	misfirePolicy, err := model.PipelineTriggerScheduleMisfirePolicyString(string(value.MisfirePolicy))
	if err != nil || misfirePolicy == model.PipelineTriggerScheduleMisfirePolicyUnknown {
		return nil, xerror.InvalidInput(xerror.PipelineTrigger).
			Detailf("unsupported pipeline trigger schedule misfire_policy %q", value.MisfirePolicy)
	}
	return &model.PipelineTriggerScheduleConfig{
		Cron:          value.Cron,
		Timezone:      value.Timezone,
		MisfirePolicy: misfirePolicy,
	}, nil
}

func pipelineTriggerWebhookConfigFromAPI(value *api.PipelineWebhookConfigInput) *model.PipelineTriggerWebhookConfig {
	if value == nil {
		return nil
	}
	mappings := make([]model.PipelineWebhookInputMapping, len(value.InputMappings))
	for i, mapping := range value.InputMappings {
		mappings[i] = model.PipelineWebhookInputMapping{JSONPointer: mapping.JsonPointer, RuntimeInputKey: mapping.RuntimeInputKey}
	}
	return &model.PipelineTriggerWebhookConfig{InputMappings: mappings}
}

func pipelineTriggerToAPI(trigger *model.PipelineTrigger) api.PipelineTrigger {
	if trigger == nil {
		return api.PipelineTrigger{}
	}
	result := api.PipelineTrigger{
		Slug:               trigger.Slug.String(),
		Name:               trigger.Name,
		Type:               api.PipelineTriggerType(trigger.Type.String()),
		Enabled:            trigger.Enabled,
		Revision:           trigger.Revision,
		EnvironmentSlug:    trigger.EnvironmentSlug.String(),
		PipelineSlug:       trigger.PipelineSlug.String(),
		VersionPolicy:      pipelineTriggerResolvedVersionPolicyToAPI(trigger.VersionPolicy),
		RuntimeInputKeys:   append([]string{}, trigger.RuntimeInputKeys...),
		RetryPolicy:        pipelineTriggerRetryPolicyToAPI(trigger.RetryPolicy),
		Schedule:           pipelineTriggerScheduleConfigToAPI(trigger.ScheduleConfig),
		Completion:         pipelineTriggerCompletionConfigToAPI(trigger.CompletionConfig),
		CreatedByActorType: api.EventActorType(trigger.CreatedBy.Type.String()),
		CreatedByActorSlug: nullableSlug(trigger.CreatedBy.Slug),
		UpdatedByActorType: api.EventActorType(trigger.UpdatedBy.Type.String()),
		UpdatedByActorSlug: nullableSlug(trigger.UpdatedBy.Slug),
		DisabledAt:         nullableTimestamp(trigger.DisabledAt),
		CreatedAt:          trigger.CreatedAt,
		UpdatedAt:          trigger.UpdatedAt,
	}
	if trigger.WebhookConfig != nil {
		mappings := make([]api.PipelineWebhookInputMapping, len(trigger.WebhookConfig.InputMappings))
		for i, mapping := range trigger.WebhookConfig.InputMappings {
			mappings[i] = api.PipelineWebhookInputMapping{JsonPointer: mapping.JSONPointer, RuntimeInputKey: mapping.RuntimeInputKey}
		}
		result.Webhook = &api.PipelineWebhookConfig{ReceiverId: trigger.WebhookConfig.ReceiverID, VerificationStrategy: api.PipelineWebhookVerificationStrategy(trigger.WebhookConfig.VerificationStrategy), InputMappings: mappings, EnabledKeyVersions: append([]int{}, trigger.WebhookConfig.EnabledKeyVersions...), SecretConfigured: trigger.WebhookConfig.SecretConfigured}
	}
	return result
}

func pipelineTriggerIssuanceToAPI(trigger *model.PipelineTrigger) api.PipelineTriggerIssuanceResponse {
	result := api.PipelineTriggerIssuanceResponse{PipelineTrigger: pipelineTriggerToAPI(trigger)}
	if trigger != nil && trigger.WebhookSecret != nil {
		result.WebhookSecret = &api.PipelineWebhookSecretIssuance{KeyVersion: trigger.WebhookSecret.KeyVersion, Value: trigger.WebhookSecret.Value}
	}
	return result
}

func pipelineTriggerResolvedVersionPolicyToAPI(value model.PipelineTriggerVersionPolicy) api.PipelineTriggerResolvedVersionPolicy {
	return api.PipelineTriggerResolvedVersionPolicy{
		Mode:              api.PipelineTriggerVersionPolicyMode(value.Mode.String()),
		PinnedVersionHash: value.PinnedVersionHash,
	}
}

func pipelineTriggerRetryPolicyToAPI(value model.PipelineTriggerRetryPolicy) api.PipelineTriggerRetryPolicy {
	return api.PipelineTriggerRetryPolicy{
		Enabled:               value.Enabled,
		MaxAttempts:           value.MaxAttempts,
		BackoffStrategy:       api.PipelineTriggerBackoffStrategy(value.BackoffStrategy.String()),
		InitialDelaySeconds:   value.InitialDelaySeconds,
		MaxDelaySeconds:       value.MaxDelaySeconds,
		RetryableFailureKinds: pipelineTriggerRetryFailureKindsToAPI(value.RetryableFailureKinds),
		MaxElapsedSeconds:     value.MaxElapsedSeconds,
	}
}

func pipelineTriggerRetryFailureKindsToAPI(
	values []model.PipelineTriggerRetryFailureKind,
) []api.PipelineTriggerRetryFailureKind {
	result := make([]api.PipelineTriggerRetryFailureKind, len(values))
	for i, value := range values {
		result[i] = api.PipelineTriggerRetryFailureKind(value.String())
	}
	return result
}

func pipelineTriggerScheduleConfigToAPI(
	value *model.PipelineTriggerScheduleConfig,
) *api.PipelineTriggerScheduleConfig {
	if value == nil {
		return nil
	}
	return &api.PipelineTriggerScheduleConfig{
		Cron:          value.Cron,
		Timezone:      value.Timezone,
		MisfirePolicy: api.PipelineTriggerScheduleMisfirePolicy(value.MisfirePolicy.String()),
	}
}

// pipelineScheduleFireToAPI converts a service schedule-fire diagnostic into the API shape.
func pipelineScheduleFireToAPI(fire *model.PipelineScheduleFire) api.PipelineScheduleFire {
	if fire == nil {
		return api.PipelineScheduleFire{}
	}
	return api.PipelineScheduleFire{
		FireSlug:        fire.FireSlug.String(),
		TriggerSlug:     fire.TriggerSlug.String(),
		TriggerRevision: fire.TriggerRevision,
		PipelineSlug:    fire.PipelineSlug.String(),
		ScheduledFireAt: fire.ScheduledFireAt,
		ActualFireAt:    fire.ActualFireAt,
		Status:          api.PipelineScheduleFireStatus(fire.Status.String()),
		MisfireResult:   api.PipelineScheduleFireMisfireResult(fire.MisfireResult.String()),
		RunSlug:         nullableSlug(fire.RunSlug),
		SourceEventSlug: nullableSlug(fire.SourceEventSlug),
		ErrorSummary:    fire.ErrorSummary,
		CreatedAt:       fire.CreatedAt,
		UpdatedAt:       fire.UpdatedAt,
	}
}

func listRequestFromPipelineTriggerParams(params api.ListPipelineTriggersParams) model.ListRequest {
	return model.ListRequest{
		Limit:  int(ptr.Value(params.Limit)),
		Cursor: ptr.Value(params.Cursor),
	}
}

// listRequestFromPipelineScheduleFireParams maps fire-history paging parameters to the shared list request.
func listRequestFromPipelineScheduleFireParams(params api.ListPipelineScheduleFiresParams) model.ListRequest {
	return model.ListRequest{
		Limit:     int(ptr.Value(params.Limit)),
		Cursor:    ptr.Value(params.Cursor),
		Direction: string(ptr.Value(params.Direction)),
	}
}

func parsePipelineTriggerSlugs(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	pipelineTriggerSlugParam api.PipelineTriggerSlugParam,
) (slug.Slug, slug.Slug, slug.Slug, slug.Slug, error) {
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if err != nil {
		return "", "", "", "", err
	}
	triggerSlug, err := slug.New(pipelineTriggerSlugParam)
	if err != nil {
		return "", "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline trigger slug: %v", err))
	}
	return orgSlug, projectSlug, environmentSlug, triggerSlug, nil
}

func (s *server) canCompletionTrigger(c fiber.Ctx, org slug.Slug) error {
	if err := s.canOrg(c, org, model.Update, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	return s.canOrg(c, org, model.Execute, model.ResourcePipelines)
}

func pipelineTriggerCompletionConfigFromAPI(value *api.PipelineTriggerCompletionConfig) (*model.PipelineTriggerCompletionConfig, error) {
	if value == nil {
		return nil, nil //nolint:nilnil // Non-completion triggers omit this optional configuration.
	}
	upstream, err := slug.New(value.UpstreamPipelineSlug)
	if err != nil {
		return nil, err
	}
	condition, err := model.PipelineCompletionConditionString(string(value.Condition))
	if err != nil || condition == model.PipelineCompletionConditionUnknown {
		return nil, xerror.InvalidInput(xerror.PipelineTrigger).Detail("completion condition must be success or final_failure")
	}
	return &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: upstream, Condition: condition, ExecutionGrantEnabled: value.ExecutionGrantEnabled}, nil
}

func pipelineTriggerCompletionConfigToAPI(value *model.PipelineTriggerCompletionConfig) *api.PipelineTriggerCompletionConfig {
	if value == nil {
		return nil
	}
	return &api.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: value.UpstreamPipelineSlug.String(), Condition: api.PipelineCompletionCondition(value.Condition.String()), ExecutionGrantEnabled: value.ExecutionGrantEnabled}
}
