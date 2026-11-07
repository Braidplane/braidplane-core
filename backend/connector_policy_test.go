package backend

import (
	"testing"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/stretchr/testify/require"
)

func TestConnectorFactoryRejectsIncompletePrivateGrantWithoutNetwork(t *testing.T) {
	grant := backendconfig.PrivatePostgresConfig{Organization: "customer", Source: "warehouse", Host: "warehouse.internal", Address: "10.20.0.8:5432"}
	for _, tc := range []struct {
		name, profile string
		change        func(*backendconfig.PrivatePostgresConfig)
		want          string
	}{
		{name: "missing source", profile: config.DeploymentProfileSelfHosted, change: func(c *backendconfig.PrivatePostgresConfig) { c.Source = "" }, want: "source must be a valid slug"},
		{name: "missing address", profile: config.DeploymentProfileSelfHosted, change: func(c *backendconfig.PrivatePostgresConfig) { c.Address = "" }, want: "private_postgres.address"},
		{name: "hosted profile", profile: config.DeploymentProfileProduction, change: func(*backendconfig.PrivatePostgresConfig) {}, want: "self-hosted profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &backendconfig.Config{Deployment: config.DeploymentConfig{Profile: tc.profile}, PrivatePostgres: grant}
			tc.change(&cfg.PrivatePostgres)
			factory := NewConnectorFactory(cfg, nil)
			result, err := factory(t.Context(), model.DataSourceTypePostgres, nil, nil)
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, result)
		})
	}
}
