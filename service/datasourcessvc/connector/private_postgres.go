package connector

import (
	"context"
	"net"
	"net/netip"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	postgresconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
)

// PrivatePostgresEndpoint is deployment-owned routing, never customer source configuration.
type PrivatePostgresEndpoint struct {
	Authorize        func(context.Context) error
	MaxConns         int
	OrganizationSlug slug.Slug
	SourceSlug       slug.Slug
	Host             string
	Address          netip.AddrPort
}

type (
	privatePostgresContextKey struct{}
	policyPostgresContextKey  struct{}
)

// PrivatePostgresPolicy grants deployment-owned routing after checking the resource identity and payload.
type PrivatePostgresPolicy func(context.Context, slug.Slug, slug.Slug, model.DataSourceType, []byte, []byte) (PrivatePostgresEndpoint, error)

func NewConnectorWithPrivatePostgresPolicy(base NewConnectorFunc, policy PrivatePostgresPolicy) NewConnectorFunc {
	return func(ctx context.Context, kind model.DataSourceType, config, credentials []byte) (Connector, error) {
		if base == nil || policy == nil {
			return nil, xerror.Unexpected().Detail("private PostgreSQL policy dependencies are required")
		}
		ctx = context.WithValue(ctx, policyPostgresContextKey{}, PrivatePostgresEndpoint{})
		org, _ := ctx.Value(organizationSlugContextKey{}).(slug.Slug)
		source, _ := ctx.Value(dataSourceSlugContextKey{}).(slug.Slug)
		grant, err := policy(ctx, org, source, kind, config, credentials)
		if err != nil {
			return nil, err
		}
		if grant.Address.IsValid() && grant.Authorize == nil {
			return nil, xerror.Unexpected().Detail("private PostgreSQL policy grant requires authorization")
		}
		return base(context.WithValue(ctx, policyPostgresContextKey{}, grant), kind, config, credentials)
	}
}

// NewConnectorWithPrivatePostgres grants only the configured tenant and source
// destination access to the pinned socket. Pool identities retain tenant scoping.
func NewConnectorWithPrivatePostgres(base NewConnectorFunc, endpoint PrivatePostgresEndpoint) NewConnectorFunc {
	return func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (Connector, error) {
		// Do not carry an earlier factory call's exception into another connector.
		ctx = context.WithValue(ctx, privatePostgresContextKey{}, PrivatePostgresEndpoint{})
		organization, _ := ctx.Value(organizationSlugContextKey{}).(slug.Slug)
		source, _ := ctx.Value(dataSourceSlugContextKey{}).(slug.Slug)
		if dsType == model.DataSourceTypePostgres && endpoint.OrganizationSlug != "" && organization == endpoint.OrganizationSlug && (endpoint.SourceSlug == "" || source == endpoint.SourceSlug) && endpoint.Address.IsValid() {
			cfg, err := decodeConnectorPayload[postgresconnector.Config](configJSON)
			if err != nil {
				return nil, err
			}
			if cfg.Host == endpoint.Host && cfg.Port == int(endpoint.Address.Port()) {
				ctx = context.WithValue(ctx, privatePostgresContextKey{}, endpoint)
			}
		}
		return base(ctx, dsType, configJSON, credentialsJSON)
	}
}

func (e PrivatePostgresEndpoint) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return e.dial(ctx, network, address, (&net.Dialer{}).DialContext)
}

func (e PrivatePostgresEndpoint) dial(ctx context.Context, network, address string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	// pgx resolves the hostname before dialing. Check that literal socket address,
	// so DNS changes cannot expand this exception to another private service.
	actual, err := netip.ParseAddrPort(address)
	if err != nil || !e.Address.IsValid() || actual != e.Address || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, xerror.Unexpected().Detail("PostgreSQL destination does not match the operator-pinned endpoint")
	}
	return dial(ctx, network, e.Address.String())
}
