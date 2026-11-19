package stripe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/stripeoauth"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestCredentialGrantMapping(t *testing.T) {
	start := time.Now()
	got, err := credentialTokens(stripeoauth.TokenSet{AccountID: "acct_fixture", Scope: "stripe_apps", AccessToken: "fixture_bearer", RefreshToken: "fixture_rotation"}, nil)
	require.NoError(t, err)
	require.Equal(t, "acct_fixture", got.Account.ID)
	require.Equal(t, []string{"stripe_apps"}, got.Scopes)
	require.Equal(t, "fixture_bearer", got.AccessToken)
	require.Equal(t, "fixture_rotation", got.RefreshToken)
	require.WithinRange(t, got.ExpiresAt, start.Add(time.Hour), time.Now().Add(time.Hour))
	_, err = credentialTokens(stripeoauth.TokenSet{}, stripeoauth.ErrInvalidGrant)
	require.ErrorIs(t, err, credentialssvc.ErrProviderInvalidGrant)
}
