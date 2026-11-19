package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripeModeConfiguration(t *testing.T) {
	var cfg StripeConfig
	require.NoError(t, cfg.validate("production"))
	cfg.TestInstallURL = "https://marketplace.stripe.com/oauth/v2/authorize?client_id=test"
	require.Error(t, cfg.validate("local"))
	cfg.TestDeveloperKey = "sk_test_fixture"
	cfg.TestRedirectURI = "http://localhost:3000/provider-connectors/stripe_test/callback"
	require.NoError(t, cfg.validate("local"))
	require.Error(t, cfg.validate("production"))
	cfg.TestRedirectURI = "https://core.example.com/provider-connectors/stripe_test/callback"
	require.NoError(t, cfg.validate("production"))
	cfg.LiveInstallURL = "https://marketplace.stripe.com/oauth/v2/authorize?client_id=live"
	cfg.LiveDeveloperKey = "sk_live_fixture"
	cfg.LiveRedirectURI = "https://core.example.com/provider-connectors/stripe_live/callback"
	require.NoError(t, cfg.validate("production"))
	cfg.LiveDeveloperKey = cfg.TestDeveloperKey
	require.Error(t, cfg.validate("production"))
}

func TestStripeOAuthDeploymentSettings(t *testing.T) {
	setTestGitHubAppEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[security]
encryption_key_hex = "` + testEncryptionKeyHex + `"
[stripe]
test_install_url = "https://marketplace.stripe.com/oauth/v2/authorize?client_id=fixture"
test_redirect_uri = "http://localhost/provider-connectors/stripe_test/callback"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv("STRIPE_TEST_DEVELOPER_KEY", "sk_test_fixture")
	cfg, err := LoadWithArgs(path, nil)
	require.NoError(t, err)
	apps := cfg.Stripe.Apps()
	require.True(t, apps[0].Enabled())
	require.Equal(t, "sk_test_fixture", apps[0].DeveloperKey)
	require.False(t, apps[1].Enabled())
}
