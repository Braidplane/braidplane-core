package datasourcessvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestSnowflakeUsesDedicatedStaticCredentialLifecycle(t *testing.T) {
	t.Parallel()

	credentialType := staticCredentialTypeForDataSource(model.DataSourceTypeSnowflake)

	require.Equal(t, credentialssvc.StaticCredentialTypeSnowflake, credentialType)
}

func TestSnowflakeIsNotForcedReadOnly(t *testing.T) {
	t.Parallel()

	svc := &service{}
	forced, err := svc.shouldForceReadOnlyForDataSource(
		t.Context(),
		must.Slug("acme"),
		&datasources.DataSource{
			Slug: must.Slug("warehouse"),
			Type: model.DataSourceTypeSnowflake,
		},
	)

	require.NoError(t, err)
	require.False(t, forced)
}
