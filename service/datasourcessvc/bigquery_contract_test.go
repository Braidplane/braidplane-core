package datasourcessvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestBigQueryUsesStaticSecretCredentialLifecycle(t *testing.T) {
	t.Parallel()

	credentialType := staticCredentialTypeForDataSource(model.DataSourceTypeBigQuery)

	require.Equal(t, credentialssvc.StaticCredentialTypeBigQuery, credentialType)
}

func TestBigQueryIsNotForcedReadOnly(t *testing.T) {
	t.Parallel()

	svc := &service{}
	forced, err := svc.shouldForceReadOnlyForDataSource(
		t.Context(),
		must.Slug("acme"),
		&datasources.DataSource{
			Slug: must.Slug("warehouse"),
			Type: model.DataSourceTypeBigQuery,
		},
	)

	require.NoError(t, err)
	require.False(t, forced)
}
