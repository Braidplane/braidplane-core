// Package backend composes reusable Community backend components.
package backend

import (
	"context"
	"log/slog"
	"net/netip"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/snowflakewifsvc"
)

// NewConnectorFactory applies provider defaults before distribution-owned transport and tracing adapters.
func NewConnectorFactory(cfg *backendconfig.Config, txm postgres.TransactionManager) connector.NewConnectorFunc {
	private := cfg.PrivatePostgres
	if err := private.Validate(cfg.Deployment.Profile); err != nil {
		return func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
			return nil, err
		}
	}

	base := connector.NewConnectorWithHubSpotDefaults(
		connector.NewConnector,
		connector.HubSpotDefaults{
			BaseURL:           cfg.HubSpot.BaseURL,
			AllowPrivateHosts: cfg.HubSpot.AllowPrivateHosts,
		},
	)
	base = connector.NewConnectorWithBigQueryDefaults(
		base,
		connector.BigQueryDefaults{
			BaseURL:           cfg.BigQuery.BaseURL,
			TokenURL:          cfg.BigQuery.TokenURL,
			AllowPrivateHosts: cfg.BigQuery.AllowPrivateHosts,
		},
	)
	if cfg.Snowflake.WIFSocketPath != "" {
		slog.Info("customer Snowflake workload identity configured")
	}
	var resolveWIF func(context.Context, slug.Slug, slug.Slug, string) (snowflakewif.Identity, error)
	if cfg.Snowflake.WIFIssuer != "" {
		resolveWIF = func(ctx context.Context, org, ds slug.Slug, account string) (snowflakewif.Identity, error) {
			return snowflakewifsvc.Resolve(ctx, txm, cfg.Snowflake.WIFIssuer, org, ds, account)
		}
	}
	base = connector.NewConnectorWithSnowflakeDefaults(base, connector.SnowflakeDefaults{
		WIFResolve:    resolveWIF,
		WIFSocketPath: cfg.Snowflake.WIFSocketPath,
	})
	if private.Address != "" {
		base = connector.NewConnectorWithPrivatePostgres(base, connector.PrivatePostgresEndpoint{
			OrganizationSlug: slug.FromString(private.Organization), SourceSlug: slug.FromString(private.Source),
			Host: private.Host, Address: netip.MustParseAddrPort(private.Address), MaxConns: 1,
		})
	}

	return base
}
