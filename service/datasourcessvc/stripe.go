package datasourcessvc

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/stripe"
)

// StripeOAuthProvider selects the deployment's app from the source's explicit mode.
func StripeOAuthProvider(raw json.RawMessage) (string, error) {
	cfg, err := stripeSourceConfig(raw)
	if err != nil {
		return "", err
	}
	return "stripe_" + cfg.Mode, nil
}

func stripeSourceConfig(raw json.RawMessage) (stripe.Config, error) {
	var cfg stripe.Config
	config, _, err := connector.NormalizeCustomerConfig(model.DataSourceTypeStripe, raw)
	if err != nil {
		return cfg, ErrInvalidInput.With(err)
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return cfg, ErrInvalidInput.With(err)
	}
	return cfg, nil
}

func (s *service) validateStaticConfigAndCredentials(typ model.DataSourceType, configJSON, credentialsJSON []byte) error {
	if typ != model.DataSourceTypeStripe {
		return s.validateConfigAndCredentials(typ, configJSON, credentialsJSON)
	}
	var credentials map[string]json.RawMessage
	if err := json.Unmarshal(credentialsJSON, &credentials); err != nil || len(credentials) != 1 || credentials["api_key"] == nil {
		return xerror.InvalidCredentials().Detail("Stripe requires a restricted API key")
	}
	cfg, err := stripeSourceConfig(configJSON)
	if err != nil {
		return err
	}
	var creds stripe.Credentials
	if err := json.Unmarshal(credentialsJSON, &creds); err != nil {
		return xerror.InvalidCredentials().Detail("invalid Stripe restricted API key")
	}
	return stripe.ValidateRestrictedKey(&cfg, creds)
}
