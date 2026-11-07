package protectedenv

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestConfirmRequiresExactSlugForProtectedProjectEnvironment(t *testing.T) {
	t.Parallel()

	protection := &model.ProjectEnvironmentProtection{
		EnvironmentSlug: must.Slug("prod"),
		Protected:       true,
	}

	require.ErrorIs(t, Confirm(protection, nil), ErrConfirmationRequired)

	wrong := must.Slug("stg")
	require.ErrorIs(t, Confirm(protection, &wrong), ErrConfirmationRequired)

	confirmed := must.Slug("prod")
	require.NoError(t, Confirm(protection, &confirmed))
}

func TestConfirmAllowsUnprotectedProjectEnvironment(t *testing.T) {
	t.Parallel()

	require.NoError(t, Confirm(&model.ProjectEnvironmentProtection{
		EnvironmentSlug: must.Slug("dev"),
		Protected:       false,
	}, nil))
}
