package backend

import (
	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/stripeoauth"
)

type StripeConfig struct {
	TestInstallURL   string `toml:"test_install_url" env:"STRIPE_TEST_INSTALL_URL" flag:"stripe-test-install-url" desc:"Stripe App test mode OAuth install URL"`
	TestDeveloperKey string `toml:"test_developer_key" env:"STRIPE_TEST_DEVELOPER_KEY" flag:"stripe-test-developer-key" desc:"Stripe App developer test mode API secret key"`
	TestRedirectURI  string `toml:"test_redirect_uri" env:"STRIPE_TEST_REDIRECT_URI" flag:"stripe-test-redirect-uri" desc:"Stripe App test mode OAuth callback URI"`
	LiveInstallURL   string `toml:"live_install_url" env:"STRIPE_LIVE_INSTALL_URL" flag:"stripe-live-install-url" desc:"Stripe App live mode OAuth install URL"`
	LiveDeveloperKey string `toml:"live_developer_key" env:"STRIPE_LIVE_DEVELOPER_KEY" flag:"stripe-live-developer-key" desc:"Stripe App developer live mode API secret key"`
	LiveRedirectURI  string `toml:"live_redirect_uri" env:"STRIPE_LIVE_REDIRECT_URI" flag:"stripe-live-redirect-uri" desc:"Stripe App live mode OAuth callback URI"`
}

func (c StripeConfig) Apps() []stripeoauth.Config {
	return []stripeoauth.Config{
		{Mode: "test", InstallURL: c.TestInstallURL, DeveloperKey: c.TestDeveloperKey, RedirectURI: c.TestRedirectURI},
		{Mode: "live", InstallURL: c.LiveInstallURL, DeveloperKey: c.LiveDeveloperKey, RedirectURI: c.LiveRedirectURI},
	}
}

func (c StripeConfig) validate(profile string) error {
	for _, a := range c.Apps() {
		if !a.Enabled() {
			continue
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if config.IsNonLocalDeploymentProfile(profile) {
			if _, err := config.ValidatePublicHTTPSURL(a.RedirectURI, "stripe."+a.Mode+"_redirect_uri"); err != nil {
				return err
			}
		}
	}
	return nil
}
