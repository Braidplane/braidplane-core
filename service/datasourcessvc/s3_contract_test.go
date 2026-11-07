package datasourcessvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestS3UsesStaticSecretCredentialLifecycle(t *testing.T) {
	t.Parallel()

	credentialType := staticCredentialTypeForDataSource(model.DataSourceTypeS3)

	require.Equal(t, credentialssvc.StaticCredentialTypeS3, credentialType)
}

func TestS3IsNotForcedReadOnly(t *testing.T) {
	t.Parallel()

	svc := &service{}
	forced, err := svc.shouldForceReadOnlyForDataSource(
		t.Context(),
		must.Slug("acme"),
		&datasources.DataSource{
			Slug: must.Slug("object-store"),
			Type: model.DataSourceTypeS3,
		},
	)

	require.NoError(t, err)
	require.False(t, forced)
}
