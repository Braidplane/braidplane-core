package environmentssvc

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

const (
	maxEntitySlugLength = 100
	entitySlugHashBytes = 8
)

type environmentEvent struct {
	environmentSlug      slug.Slug
	action               model.EventAction
	protectedEnvironment bool
	metadata             map[string]any
}

func (e environmentEvent) EventRecord() (eventrecorder.Record, error) {
	switch e.action {
	case model.EventActionCreate,
		model.EventActionUpdate,
		model.EventActionDelete:
	default:
		return eventrecorder.Record{}, xerror.InvalidInput(xerror.Event).
			Detail("unsupported environment event action")
	}

	metadata := map[string]any{
		"environment_slug": e.environmentSlug.String(),
	}
	for key, value := range e.metadata {
		metadata[key] = value
	}
	if e.protectedEnvironment {
		metadata["protected_environment_confirmed"] = true
	}

	return eventrecorder.Record{
		SlugPrefix:    "evt-environment-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeEnvironment,
		EntitySlug:    e.environmentSlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

type projectEnvironmentEvent struct {
	projectSlug          slug.Slug
	environmentSlug      slug.Slug
	action               model.EventAction
	protectedEnvironment bool
}

func (e projectEnvironmentEvent) EventRecord() (eventrecorder.Record, error) {
	switch e.action {
	case model.EventActionEnable,
		model.EventActionDisable,
		model.EventActionUpdate:
	default:
		return eventrecorder.Record{}, xerror.InvalidInput(xerror.Event).
			Detail("unsupported project environment event action")
	}

	entitySlug, err := projectEnvironmentEntitySlug(e.projectSlug, e.environmentSlug)
	if err != nil {
		return eventrecorder.Record{}, err
	}

	metadata := map[string]any{
		"project_slug":     e.projectSlug.String(),
		"environment_slug": e.environmentSlug.String(),
	}
	if e.protectedEnvironment {
		metadata["protected_environment_confirmed"] = true
	}

	return eventrecorder.Record{
		SlugPrefix:    "evt-project-environment-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeProjectEnvironment,
		EntitySlug:    entitySlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

func projectEnvironmentEntitySlug(projectSlug, environmentSlug slug.Slug) (slug.Slug, error) {
	base := projectSlug.String() + "-" + environmentSlug.String()
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:entitySlugHashBytes])

	maxBaseLength := maxEntitySlugLength - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}

	entitySlug, err := slug.New(base + "-" + suffix)
	if err != nil {
		return "", xerror.Wrapf("build project environment entity slug: %w", err)
	}
	return entitySlug, nil
}
