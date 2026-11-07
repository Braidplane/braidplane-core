package protectedenv

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrConfirmationRequired = xerror.New(
	xerror.KindInvalidInput,
	"PROTECTED_ENVIRONMENT_CONFIRMATION_REQUIRED",
	"protected environment confirmation required",
)

func Confirm(protection *model.ProjectEnvironmentProtection, confirmation *slug.Slug) error {
	if protection == nil {
		return xerror.NotFound(xerror.ProjectEnvironment)
	}
	if !protection.Protected {
		return nil
	}
	if confirmation != nil && *confirmation == protection.EnvironmentSlug {
		return nil
	}

	return ErrConfirmationRequired.
		WithPublicDetail("Pass protected_environment_confirmation with the exact environment slug.").
		Detailf("confirmation must match environment %s", protection.EnvironmentSlug)
}

func ConfirmEnvironment(environment *model.Environment, confirmation *slug.Slug) error {
	if environment == nil {
		return xerror.NotFound(xerror.Environment)
	}
	if !environment.Protected {
		return nil
	}
	if confirmation != nil && *confirmation == environment.Slug {
		return nil
	}

	return ErrConfirmationRequired.
		WithPublicDetail("Pass protected_environment_confirmation with the exact environment slug.").
		Detailf("confirmation must match environment %s", environment.Slug)
}
