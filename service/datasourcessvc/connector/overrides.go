package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	hubspotclient "github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	bigqueryclient "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	snowflakeconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake"
)

// HubSpotDefaults configures runtime-only defaults for the HubSpot connector.
// It is intended for local development and test environments where HubSpot
// requests should be redirected to a mock service without exposing those knobs
// on the public data source contract.
type HubSpotDefaults struct {
	BaseURL           string
	AllowPrivateHosts bool
}

// BigQueryDefaults configures operator-controlled endpoint defaults for the BigQuery connector.
// It keeps private endpoint routing out of the public data source API shape.
type BigQueryDefaults struct {
	BaseURL           string
	TokenURL          string
	AllowPrivateHosts bool
}

// SnowflakeDefaults configures deployment-owned Snowflake workload identity inputs.
// These values are applied after customer config normalization and are never persisted.
type SnowflakeDefaults struct {
	WIFResolve          func(context.Context, slug.Slug, slug.Slug, string) (snowflakewif.Identity, error)
	WIFTokenFile        string
	WIFOrganizationSlug slug.Slug
	WIFSocketPath       string
}

type (
	organizationSlugContextKey     struct{}
	dataSourceSlugContextKey       struct{}
	snowflakeTokenSourceContextKey struct{}
)

// WithDataSourceIdentity carries the authorized resource identity to execution.
func WithDataSourceIdentity(ctx context.Context, organization, dataSource slug.Slug) context.Context {
	return context.WithValue(WithOrganizationSlug(ctx, organization), dataSourceSlugContextKey{}, dataSource)
}

// WithOrganizationSlug binds deployment-owned connector defaults to one tenant.
func WithOrganizationSlug(ctx context.Context, organizationSlug slug.Slug) context.Context {
	return context.WithValue(ctx, organizationSlugContextKey{}, organizationSlug)
}

// NewConnectorWithSnowflakeDefaults selects an operator-authorized customer
// identity, or the separately configured organization-bound staging fixture.
func NewConnectorWithSnowflakeDefaults(base NewConnectorFunc, defaults SnowflakeDefaults) NewConnectorFunc {
	if base == nil {
		base = NewConnector
	}
	if defaults.WIFSocketPath != "" || defaults.WIFResolve != nil {
		resolve := defaults.WIFResolve
		return func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (Connector, error) {
			if dsType != model.DataSourceTypeSnowflake {
				return base(ctx, dsType, configJSON, credentialsJSON)
			}
			var credentials snowflakeconnector.Credentials
			var cfg snowflakeconnector.Config
			if json.Unmarshal(credentialsJSON, &credentials) != nil || json.Unmarshal(configJSON, &cfg) != nil {
				return nil, xerror.InvalidInput(xerror.DataSource).Detail("invalid Snowflake configuration or credentials")
			}
			if err := snowflakeconnector.ValidateCredentials(credentials); err != nil {
				return nil, err
			}
			if strings.TrimSpace(credentials.AuthType) != snowflakeconnector.AuthTypeWorkloadIdentity {
				return base(ctx, dsType, configJSON, credentialsJSON)
			}
			if resolve == nil {
				return nil, xerror.Unavailable(xerror.DataSource).Detail("Snowflake workload identity resolver is unavailable")
			}
			organization, _ := ctx.Value(organizationSlugContextKey{}).(slug.Slug)
			dataSource, _ := ctx.Value(dataSourceSlugContextKey{}).(slug.Slug)
			// Resolve before constructing the provider, and again for every new
			// physical connection so removed bindings cannot renew a session.
			if _, err := resolve(ctx, organization, dataSource, cfg.Account); err != nil {
				return nil, err
			}
			source := func(tokenCtx context.Context) (string, error) {
				identity, err := resolve(tokenCtx, organization, dataSource, cfg.Account)
				if err != nil {
					return "", err
				}
				return snowflakewif.Token(tokenCtx, defaults.WIFSocketPath, identity)
			}
			return base(context.WithValue(ctx, snowflakeTokenSourceContextKey{}, source), dsType, configJSON, credentialsJSON)
		}
	}
	if strings.TrimSpace(defaults.WIFTokenFile) == "" || defaults.WIFOrganizationSlug == "" {
		return base
	}
	return func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (Connector, error) {
		organizationSlug, _ := ctx.Value(organizationSlugContextKey{}).(slug.Slug)
		if dsType != model.DataSourceTypeSnowflake || organizationSlug != defaults.WIFOrganizationSlug {
			return base(ctx, dsType, configJSON, credentialsJSON)
		}
		var cfg snowflakeconnector.Config
		if err := json.Unmarshal(configJSON, &cfg); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		cfg.WIFTokenFile = strings.TrimSpace(defaults.WIFTokenFile)
		patchedConfigJSON, err := json.Marshal(cfg)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return base(ctx, dsType, patchedConfigJSON, credentialsJSON)
	}
}

// NewConnectorWithHubSpotDefaults applies runtime HubSpot defaults before
// delegating to the provided connector factory.
func NewConnectorWithHubSpotDefaults(
	base NewConnectorFunc,
	defaults HubSpotDefaults,
) NewConnectorFunc {
	if base == nil {
		base = NewConnector
	}

	if strings.TrimSpace(defaults.BaseURL) == "" && !defaults.AllowPrivateHosts {
		return base
	}

	return func(
		ctx context.Context,
		dsType model.DataSourceType,
		configJSON, credentialsJSON []byte,
	) (Connector, error) {
		if dsType != model.DataSourceTypeHubSpot {
			return base(ctx, dsType, configJSON, credentialsJSON)
		}

		cfg, err := decodeHubSpotConfig(configJSON)
		if err != nil {
			return nil, err
		}

		if cfg.BaseURL == "" {
			cfg.BaseURL = strings.TrimSpace(defaults.BaseURL)
		}
		if defaults.AllowPrivateHosts {
			cfg.AllowPrivateHosts = true
		}

		patchedConfigJSON, err := json.Marshal(cfg)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}

		return base(ctx, dsType, patchedConfigJSON, credentialsJSON)
	}
}

// NewConnectorWithBigQueryDefaults applies runtime BigQuery defaults before
// delegating to the provided connector factory.
func NewConnectorWithBigQueryDefaults(
	base NewConnectorFunc,
	defaults BigQueryDefaults,
) NewConnectorFunc {
	if base == nil {
		base = NewConnector
	}

	if strings.TrimSpace(defaults.BaseURL) == "" &&
		strings.TrimSpace(defaults.TokenURL) == "" &&
		!defaults.AllowPrivateHosts {
		return base
	}

	return func(
		ctx context.Context,
		dsType model.DataSourceType,
		configJSON, credentialsJSON []byte,
	) (Connector, error) {
		if dsType != model.DataSourceTypeBigQuery {
			return base(ctx, dsType, configJSON, credentialsJSON)
		}

		cfg, err := decodeBigQueryConfig(configJSON)
		if err != nil {
			return nil, err
		}

		if cfg.BaseURL == "" {
			cfg.BaseURL = strings.TrimSpace(defaults.BaseURL)
		}
		if cfg.TokenURL == "" {
			cfg.TokenURL = strings.TrimSpace(defaults.TokenURL)
		}
		if defaults.AllowPrivateHosts {
			cfg.AllowPrivateHosts = true
		}

		patchedConfigJSON, err := json.Marshal(cfg)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}

		return base(ctx, dsType, patchedConfigJSON, credentialsJSON)
	}
}

func decodeHubSpotConfig(configJSON []byte) (hubspotclient.Config, error) {
	var cfg hubspotclient.Config
	trimmed := bytes.TrimSpace(configJSON)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return cfg, nil
	}

	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return hubspotclient.Config{}, xerror.Unexpected().With(err)
	}

	return cfg, nil
}

func decodeBigQueryConfig(configJSON []byte) (bigqueryclient.Config, error) {
	var cfg bigqueryclient.Config
	trimmed := bytes.TrimSpace(configJSON)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return cfg, nil
	}

	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return bigqueryclient.Config{}, xerror.Unexpected().With(err)
	}

	return cfg, nil
}
