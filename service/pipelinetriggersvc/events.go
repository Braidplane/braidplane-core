package pipelinetriggersvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

const (
	maxTriggerEntitySlugLength = 100
	triggerEntitySlugHashBytes = 8
)

type pipelineTriggerEvent struct {
	projectSlug     slug.Slug
	environmentSlug slug.Slug
	trigger         *model.PipelineTrigger
	action          model.EventAction
}

// EventRecord converts a trigger change into a durable, sanitized event record.
func (e pipelineTriggerEvent) EventRecord() (eventrecorder.Record, error) {
	switch e.action {
	case model.EventActionCreate, model.EventActionUpdate, model.EventActionDelete:
	default:
		return eventrecorder.Record{}, xerror.InvalidInput(xerror.Event).
			Detail("unsupported pipeline trigger event action")
	}
	if e.trigger == nil {
		return eventrecorder.Record{}, xerror.Unexpected().Detail("pipeline trigger event requires trigger")
	}
	entitySlug, err := pipelineTriggerEntitySlug(e.projectSlug, e.environmentSlug, e.trigger.Slug)
	if err != nil {
		return eventrecorder.Record{}, err
	}

	metadata := map[string]any{
		"project_slug":       e.projectSlug.String(),
		"environment_slug":   e.environmentSlug.String(),
		"pipeline_slug":      e.trigger.PipelineSlug.String(),
		"trigger_slug":       e.trigger.Slug.String(),
		"trigger_type":       e.trigger.Type.String(),
		"enabled":            e.trigger.Enabled,
		"revision":           e.trigger.Revision,
		"version_policy":     e.trigger.VersionPolicy.Mode.String(),
		"runtime_input_keys": e.trigger.RuntimeInputKeys,
		"retry_enabled":      e.trigger.RetryPolicy.Enabled,
	}
	if e.trigger.VersionPolicy.PinnedVersionHash != nil {
		metadata["pinned_version_hash"] = *e.trigger.VersionPolicy.PinnedVersionHash
	}
	if e.trigger.ScheduleConfig != nil {
		metadata["schedule_timezone"] = e.trigger.ScheduleConfig.Timezone
		metadata["schedule_misfire_policy"] = e.trigger.ScheduleConfig.MisfirePolicy.String()
	}

	return eventrecorder.Record{
		SlugPrefix: "evt-pipeline-trigger-" + slug.Slug(e.action.String()),
		EntityType: model.EventEntityTypePipelineTrigger,
		EntitySlug: entitySlug,
		Action:     e.action,
		Metadata:   metadata,
	}, nil
}

func pipelineTriggerEntitySlug(projectSlug, environmentSlug, triggerSlug slug.Slug) (slug.Slug, error) {
	base := projectSlug.String() + "-" + environmentSlug.String() + "-" + triggerSlug.String()
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:triggerEntitySlugHashBytes])
	maxBaseLength := maxTriggerEntitySlugLength - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}
	entitySlug, err := slug.New(base + "-" + suffix)
	if err != nil {
		return "", xerror.Wrapf("build pipeline trigger entity slug: %w", err)
	}
	return entitySlug, nil
}

func (s *service) recordPipelineTriggerEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	actor model.EventActor,
	trigger *model.PipelineTrigger,
	action model.EventAction,
) error {
	if trigger == nil {
		return xerror.Unexpected().Detail("pipeline trigger event requires trigger")
	}
	s.eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, pipelineTriggerEvent{
		projectSlug:     projectSlug,
		environmentSlug: environmentSlug,
		trigger:         trigger,
		action:          action,
	})
	return nil
}
