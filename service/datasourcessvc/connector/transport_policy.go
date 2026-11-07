package connector

import (
	"context"
	"net/url"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
	postgresconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
)

// NewConnectorWithTransportPolicy carries an operator-only local policy through every execution role.
func NewConnectorWithTransportPolicy(base NewConnectorFunc, allowPlaintext bool) NewConnectorFunc {
	return func(ctx context.Context, kind model.DataSourceType, config, credentials []byte) (Connector, error) {
		if allowPlaintext {
			ctx = transportsecurity.WithPlaintext(ctx)
		}
		return base(ctx, kind, config, credentials)
	}
}

// ValidateTransportConfig rejects unsafe endpoints before a customer config can be saved or tested.
func ValidateTransportConfig(config any, allowPlaintext bool) error {
	var endpoints []string
	switch cfg := config.(type) {
	case httpconnector.Config:
		endpoints = []string{cfg.BaseURL, cfg.OpenAPIURL}
	case s3client.Config:
		endpoints = []string{cfg.Endpoint}
	case postgresconnector.Config:
		if !allowPlaintext && cfg.SSLMode != "" && cfg.SSLMode != "verify-full" {
			return xerror.ValidationFailed().Detail("PostgreSQL requires ssl_mode=verify-full")
		}
	}
	for _, endpoint := range endpoints {
		if endpoint == "" {
			continue
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return xerror.ValidationFailed().With(err)
		}
		if err := transportsecurity.ValidateURL(parsed, allowPlaintext); err != nil {
			return err
		}
	}
	return nil
}
