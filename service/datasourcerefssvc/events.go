package datasourcerefssvc

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
	maxEntitySlugLength = 100
	entitySlugHashBytes = 8
)

type dataSourceRefEvent struct {
	projectSlug    slug.Slug
	pipelineSlug   slug.Slug
	refSlug        slug.Slug
	dataSourceType model.DataSourceType
	capabilities   []model.DataSourceRefCapability
	action         model.EventAction
}

func (e dataSourceRefEvent) EventRecord() (eventrecorder.Record, error) {
	switch e.action {
	case model.EventActionCreate, model.EventActionUpdate, model.EventActionDelete:
	default:
		return eventrecorder.Record{}, xerror.InvalidInput(xerror.Event).
			Detail("unsupported data source ref event action")
	}
	entitySlug, err := dataSourceRefEntitySlug(e.projectSlug, e.pipelineSlug, e.refSlug)
	if err != nil {
		return eventrecorder.Record{}, err
	}
	metadata := map[string]any{
		"project_slug":         e.projectSlug.String(),
		"pipeline_slug":        e.pipelineSlug.String(),
		"data_source_ref_slug": e.refSlug.String(),
	}
	if e.dataSourceType != model.DataSourceTypeUnknown {
		metadata["data_source_type"] = e.dataSourceType.String()
	}
	if len(e.capabilities) > 0 {
		values := make([]string, len(e.capabilities))
		for i, capability := range e.capabilities {
			values[i] = capability.String()
		}
		metadata["required_capabilities"] = values
	}
	return eventrecorder.Record{
		SlugPrefix:    "evt-data-source-ref-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeDataSourceRef,
		EntitySlug:    entitySlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

type dataSourceBindingEvent struct {
	projectSlug          slug.Slug
	pipelineSlug         slug.Slug
	environmentSlug      slug.Slug
	refSlug              slug.Slug
	dataSourceSlug       slug.Slug
	dataSourceType       model.DataSourceType
	readOnly             bool
	active               bool
	action               model.EventAction
	protectedEnvironment bool
}

func (e dataSourceBindingEvent) EventRecord() (eventrecorder.Record, error) {
	switch e.action {
	case model.EventActionUpdate, model.EventActionDelete:
	default:
		return eventrecorder.Record{}, xerror.InvalidInput(xerror.Event).
			Detail("unsupported data source binding event action")
	}
	entitySlug, err := bindingEntitySlug(e.projectSlug, e.pipelineSlug, e.environmentSlug, e.refSlug)
	if err != nil {
		return eventrecorder.Record{}, err
	}
	metadata := map[string]any{
		"project_slug":         e.projectSlug.String(),
		"pipeline_slug":        e.pipelineSlug.String(),
		"environment_slug":     e.environmentSlug.String(),
		"data_source_ref_slug": e.refSlug.String(),
	}
	if e.dataSourceSlug != "" {
		metadata["data_source_slug"] = e.dataSourceSlug.String()
	}
	if e.dataSourceType != model.DataSourceTypeUnknown {
		metadata["data_source_type"] = e.dataSourceType.String()
	}
	metadata["data_source_read_only"] = e.readOnly
	metadata["data_source_active"] = e.active
	if e.protectedEnvironment {
		metadata["protected_environment_confirmed"] = true
	}
	return eventrecorder.Record{
		SlugPrefix:    "evt-data-source-binding-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeDataSourceBinding,
		EntitySlug:    entitySlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

func dataSourceRefEntitySlug(projectSlug, pipelineSlug, refSlug slug.Slug) (slug.Slug, error) {
	return scopedEntitySlug("build data source ref entity slug", projectSlug, pipelineSlug, refSlug)
}

func bindingEntitySlug(projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug) (slug.Slug, error) {
	return scopedEntitySlug("build data source binding entity slug", projectSlug, pipelineSlug, environmentSlug, refSlug)
}

func scopedEntitySlug(errorPrefix string, parts ...slug.Slug) (slug.Slug, error) {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		values = append(values, part.String())
	}
	base := strings.Join(values, "-")
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:entitySlugHashBytes])
	maxBaseLength := maxEntitySlugLength - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}
	entitySlug, err := slug.New(base + "-" + suffix)
	if err != nil {
		return "", xerror.Wrapf("%s: %w", errorPrefix, err)
	}
	return entitySlug, nil
}

func (s *service) recordDataSourceRefEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	actor model.EventActor,
	ref *model.DataSourceRef,
	action model.EventAction,
) error {
	if ref == nil {
		return xerror.Unexpected().Detail("data source ref event requires ref")
	}
	if ref.PipelineSlug == "" {
		return xerror.Unexpected().Detail("data source ref event requires pipeline slug")
	}
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, dataSourceRefEvent{
		projectSlug:    projectSlug,
		pipelineSlug:   ref.PipelineSlug,
		refSlug:        ref.Slug,
		dataSourceType: ref.DataSourceType,
		capabilities:   ref.RequiredCapabilities,
		action:         action,
	}); err != nil {
		return xerror.Wrapf("enqueue required data source ref audit event: %w", err)
	}
	return nil
}

func (s *service) recordDataSourceBindingEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
	binding *model.DataSourceBinding,
	action model.EventAction,
	protectedEnvironment bool,
) error {
	if binding == nil {
		return xerror.Unexpected().Detail("data source binding event requires binding")
	}
	if pipelineSlug == "" {
		pipelineSlug = binding.PipelineSlug
	}
	if pipelineSlug == "" {
		return xerror.Unexpected().Detail("data source binding event requires pipeline slug")
	}
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, dataSourceBindingEvent{
		projectSlug:          projectSlug,
		pipelineSlug:         pipelineSlug,
		environmentSlug:      binding.EnvironmentSlug,
		refSlug:              binding.DataSourceRefSlug,
		dataSourceSlug:       binding.DataSourceSlug,
		dataSourceType:       binding.DataSourceType,
		readOnly:             binding.DataSourceReadOnly,
		active:               binding.DataSourceActive,
		action:               action,
		protectedEnvironment: protectedEnvironment,
	}); err != nil {
		return xerror.Wrapf("enqueue required data source binding audit event: %w", err)
	}
	return nil
}
