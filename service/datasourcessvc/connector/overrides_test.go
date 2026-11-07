package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type noopTestConnector struct{}

func TestCustomerSnowflakeIdentityFailsClosedBeforeProviderAccess(t *testing.T) {
	authorized := map[[3]string]bool{{"alpha", "ds", "one"}: true, {"beta", "ds", "two"}: true}
	resolve := func(_ context.Context, org, ds slug.Slug, account string) (snowflakewif.Identity, error) {
		if !authorized[[3]string{org.String(), ds.String(), account}] {
			return snowflakewif.Identity{}, xerror.Unavailable(xerror.DataSource).Detail("not authorized")
		}
		return snowflakewif.NewIdentity("https://issuer.test", org, ds, account)
	}
	calls := 0
	var source func(context.Context) (string, error)
	factory := NewConnectorWithSnowflakeDefaults(func(ctx context.Context, _ model.DataSourceType, config, credentials []byte) (Connector, error) {
		calls++
		source, _ = ctx.Value(snowflakeTokenSourceContextKey{}).(func(context.Context) (string, error))
		require.NotContains(t, string(config), "token")
		require.NotContains(t, string(credentials), "token")
		return noopTestConnector{}, nil
	}, SnowflakeDefaults{WIFSocketPath: "/run/unused.sock", WIFResolve: resolve})
	for _, tc := range []struct{ org, source, account string }{
		{"alpha", "ds", "two"}, {"beta", "ds", "one"}, {"alpha", "other", "one"}, {"", "ds", "one"},
	} {
		_, err := factory(WithDataSourceIdentity(t.Context(), slug.Slug(tc.org), slug.Slug(tc.source)), model.DataSourceTypeSnowflake,
			[]byte(fmt.Sprintf(`{"account":%q}`, tc.account)), []byte(`{"auth_type":"workload_identity"}`))
		require.Error(t, err)
		require.Zero(t, calls, "unauthorized identity must not reach the provider")
	}
	manager, err := NewManager(factory, RuntimePoolSettings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	_, release, err := manager.Acquire(WithDataSourceIdentity(t.Context(), "beta", "other"),
		RuntimeIdentity{OrganizationSlug: "alpha", DataSourceSlug: "ds", DataSourceType: model.DataSourceTypeSnowflake},
		[]byte(`{"account":"one"}`), []byte(`{"auth_type":"workload_identity"}`))
	require.NoError(t, err)
	require.NoError(t, release())
	require.NotNil(t, source)
	require.Equal(t, 1, calls)
	// An already-constructed connector must check revocation before renewing.
	clear(authorized)
	_, err = source(t.Context())
	require.ErrorContains(t, err, "not authorized")
	_, err = factory(t.Context(), model.DataSourceTypeSnowflake, []byte(`{"account":"one"}`),
		[]byte(`{"auth_type":"password","username":"user","password":"password"}`))
	require.NoError(t, err)
	require.Nil(t, source)
	require.Equal(t, 2, calls)
}

func (noopTestConnector) Execute(
	_ context.Context,
	_ json.RawMessage,
	_ map[string]types.TypedParam,
	_ ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

func (noopTestConnector) TestConnection(_ context.Context) error { return nil }
func (noopTestConnector) Close() error                           { return nil }

func TestNewConnectorWithHubSpotDefaults_AppliesDefaultsToHubSpot(t *testing.T) {
	t.Parallel()

	var capturedConfig map[string]any
	factory := NewConnectorWithHubSpotDefaults(
		func(
			_ context.Context,
			dsType model.DataSourceType,
			configJSON, _ []byte,
		) (Connector, error) {
			require.Equal(t, model.DataSourceTypeHubSpot, dsType)
			require.NoError(t, json.Unmarshal(configJSON, &capturedConfig))
			return noopTestConnector{}, nil
		},
		HubSpotDefaults{
			BaseURL:           "http://localhost:38082",
			AllowPrivateHosts: true,
		},
	)

	_, err := factory(
		context.Background(),
		model.DataSourceTypeHubSpot,
		[]byte(`{"timeout_seconds":15}`),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "http://localhost:38082", capturedConfig["base_url"])
	require.Equal(t, true, capturedConfig["allow_private_hosts"])
	require.InDelta(t, 15, capturedConfig["timeout_seconds"], 0.000001)
}

func TestNewConnectorWithHubSpotDefaults_PreservesExplicitHubSpotBaseURL(t *testing.T) {
	t.Parallel()

	var capturedConfig map[string]any
	factory := NewConnectorWithHubSpotDefaults(
		func(
			_ context.Context,
			dsType model.DataSourceType,
			configJSON, _ []byte,
		) (Connector, error) {
			require.Equal(t, model.DataSourceTypeHubSpot, dsType)
			require.NoError(t, json.Unmarshal(configJSON, &capturedConfig))
			return noopTestConnector{}, nil
		},
		HubSpotDefaults{
			BaseURL:           "http://localhost:38082",
			AllowPrivateHosts: true,
		},
	)

	_, err := factory(
		context.Background(),
		model.DataSourceTypeHubSpot,
		[]byte(`{"base_url":"https://api.example.com","timeout_seconds":20}`),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com", capturedConfig["base_url"])
	require.Equal(t, true, capturedConfig["allow_private_hosts"])
	require.InDelta(t, 20, capturedConfig["timeout_seconds"], 0.000001)
}

func TestNewConnectorWithHubSpotDefaults_PassesThroughNonHubSpot(t *testing.T) {
	t.Parallel()

	var capturedConfigJSON []byte
	factory := NewConnectorWithHubSpotDefaults(
		func(
			_ context.Context,
			dsType model.DataSourceType,
			configJSON, _ []byte,
		) (Connector, error) {
			require.Equal(t, model.DataSourceTypeHTTP, dsType)
			capturedConfigJSON = append([]byte(nil), configJSON...)
			return noopTestConnector{}, nil
		},
		HubSpotDefaults{
			BaseURL:           "http://localhost:38082",
			AllowPrivateHosts: true,
		},
	)

	_, err := factory(
		context.Background(),
		model.DataSourceTypeHTTP,
		[]byte(`{"base_url":"https://api.example.com","health_path":"/health"}`),
		nil,
	)
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"base_url":"https://api.example.com","health_path":"/health"}`,
		string(capturedConfigJSON),
	)
}

func TestNewConnectorWithBigQueryDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configJSON  []byte
		wantStrings map[string]string
		wantBools   map[string]bool
		wantNumbers map[string]float64
	}{
		{
			name:       "AppliesDefaultsToBigQuery",
			configJSON: []byte(`{"project_id":"analytics-prod","timeout_seconds":15}`),
			wantStrings: map[string]string{
				"base_url":   "http://localhost:38084",
				"token_url":  "http://localhost:38084/token",
				"project_id": "analytics-prod",
			},
			wantBools: map[string]bool{
				"allow_private_hosts": true,
			},
			wantNumbers: map[string]float64{
				"timeout_seconds": 15,
			},
		},
		{
			name: "PreservesExplicitBigQueryURLs",
			configJSON: []byte(
				`{"project_id":"analytics-prod","base_url":"https://bigquery.example.com","token_url":"https://oauth.example.com/token"}`,
			),
			wantStrings: map[string]string{
				"base_url":   "https://bigquery.example.com",
				"token_url":  "https://oauth.example.com/token",
				"project_id": "analytics-prod",
			},
			wantBools: map[string]bool{
				"allow_private_hosts": true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var capturedConfig map[string]any
			factory := NewConnectorWithBigQueryDefaults(
				func(
					_ context.Context,
					dsType model.DataSourceType,
					configJSON, _ []byte,
				) (Connector, error) {
					require.Equal(t, model.DataSourceTypeBigQuery, dsType)
					require.NoError(t, json.Unmarshal(configJSON, &capturedConfig))
					return noopTestConnector{}, nil
				},
				BigQueryDefaults{
					BaseURL:           "http://localhost:38084",
					TokenURL:          "http://localhost:38084/token",
					AllowPrivateHosts: true,
				},
			)

			_, err := factory(
				context.Background(),
				model.DataSourceTypeBigQuery,
				tc.configJSON,
				nil,
			)
			require.NoError(t, err)
			for key, want := range tc.wantStrings {
				require.Equal(t, want, capturedConfig[key])
			}
			for key, want := range tc.wantBools {
				require.Equal(t, want, capturedConfig[key])
			}
			for key, want := range tc.wantNumbers {
				require.InDelta(t, want, capturedConfig[key], 0.000001)
			}
		})
	}
}

func TestNewConnectorWithSnowflakeDefaultsInjectsRuntimeTokenPath(t *testing.T) {
	t.Parallel()

	var capturedConfig map[string]any
	factory := NewConnectorWithSnowflakeDefaults(
		func(_ context.Context, dsType model.DataSourceType, configJSON, _ []byte) (Connector, error) {
			require.Equal(t, model.DataSourceTypeSnowflake, dsType)
			require.NoError(t, json.Unmarshal(configJSON, &capturedConfig))
			return noopTestConnector{}, nil
		},
		SnowflakeDefaults{
			WIFTokenFile:        " /var/run/braidplane/snowflake-identity.jwt ",
			WIFOrganizationSlug: slug.Slug("acme"),
		},
	)

	_, err := factory(
		WithOrganizationSlug(context.Background(), slug.Slug("acme")), model.DataSourceTypeSnowflake,
		[]byte(`{"account":"acme","warehouse":"WH","database":"DB","schema":"PUBLIC"}`), nil,
	)

	require.NoError(t, err)
	require.Equal(t, "/var/run/braidplane/snowflake-identity.jwt", capturedConfig["_wif_token_file"])
	require.Equal(t, "acme", capturedConfig["account"])
}

func TestNewConnectorWithSnowflakeDefaultsPreservesPasswordCredentials(t *testing.T) {
	t.Parallel()

	testPassword := uuid.NewString()
	var capturedConfig, capturedCredentials map[string]any
	factory := NewConnectorWithSnowflakeDefaults(
		func(_ context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (Connector, error) {
			require.Equal(t, model.DataSourceTypeSnowflake, dsType)
			require.NoError(t, json.Unmarshal(configJSON, &capturedConfig))
			require.NoError(t, json.Unmarshal(credentialsJSON, &capturedCredentials))
			return noopTestConnector{}, nil
		},
		SnowflakeDefaults{
			WIFTokenFile:        "/var/run/braidplane/snowflake-identity.jwt",
			WIFOrganizationSlug: slug.Slug("acme"),
		},
	)

	_, err := factory(
		WithOrganizationSlug(context.Background(), slug.Slug("acme")), model.DataSourceTypeSnowflake,
		[]byte(`{"account":"acme","warehouse":"WH","database":"DB","schema":"PUBLIC"}`),
		[]byte(fmt.Sprintf(
			`{"auth_type":"password","username":"svc","password":%q}`,
			testPassword,
		)),
	)

	require.NoError(t, err)
	require.Equal(t, "/var/run/braidplane/snowflake-identity.jwt", capturedConfig["_wif_token_file"])
	require.Equal(t, "password", capturedCredentials["auth_type"])
	require.Equal(t, "svc", capturedCredentials["username"])
	require.Equal(t, testPassword, capturedCredentials["password"])
}

func TestNewConnectorWithSnowflakeDefaultsRejectsOtherOrganization(t *testing.T) {
	t.Parallel()

	var capturedConfig map[string]any
	factory := NewConnectorWithSnowflakeDefaults(
		func(_ context.Context, _ model.DataSourceType, configJSON, _ []byte) (Connector, error) {
			require.NoError(t, json.Unmarshal(configJSON, &capturedConfig))
			return noopTestConnector{}, nil
		},
		SnowflakeDefaults{
			WIFTokenFile:        "/var/run/braidplane/snowflake-identity.jwt",
			WIFOrganizationSlug: slug.Slug("acme"),
		},
	)

	_, err := factory(
		WithOrganizationSlug(context.Background(), slug.Slug("other")),
		model.DataSourceTypeSnowflake,
		[]byte(`{"account":"acme","warehouse":"WH","database":"DB","schema":"PUBLIC"}`), nil,
	)

	require.NoError(t, err)
	require.NotContains(t, capturedConfig, "_wif_token_file")
}
