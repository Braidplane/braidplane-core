package snowflakewif

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestIdentityClaims(t *testing.T) {
	t.Parallel()
	alpha, err := NewIdentity("https://issuer.test", "alpha", "source", "one")
	require.NoError(t, err)
	beta, err := NewIdentity("https://issuer.test", "beta", "source", "one")
	require.NoError(t, err)
	require.NotEqual(t, alpha.Subject, beta.Subject)
	require.Equal(t, "spiffe://issuer.test/tenant/alpha/snowflake/one/connection/source", alpha.Subject.String())
	require.Equal(t, "https://one.snowflakecomputing.com", alpha.Audience)
	for _, tc := range []struct{ issuer, org, ds, account string }{
		{"http://issuer.test", "alpha", "source", "one"},
		{"https://issuer.test/path", "alpha", "source", "one"},
		{"https://issuer.test:443", "alpha", "source", "one"},
		{"https://issuer.test", "", "source", "one"},
		{"https://issuer.test", "alpha", "", "one"},
		{"https://issuer.test", "alpha", "source", "other/host"},
		{"https://issuer.test", "alpha", "source", "ONE"},
		{"https://issuer.test", "alpha", "source", "one.snowflakecomputing.com"},
	} {
		_, err := NewIdentity(tc.issuer, slug.Slug(tc.org), slug.Slug(tc.ds), tc.account)
		require.ErrorContains(t, err, "claims are invalid")
	}
}

func TestTokenValidationAndKeyRotationOverlap(t *testing.T) {
	identity, err := NewIdentity("https://issuer.test", "alpha", "ds", "one")
	require.NoError(t, err)
	oldKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	bundle := jwtbundle.FromJWTAuthorities(identity.Subject.TrustDomain(), map[string]crypto.PublicKey{
		"old": &oldKey.PublicKey, "new": &newKey.PublicKey,
	})
	claims := func() jwt.Claims {
		now := time.Now()
		return jwt.Claims{
			Issuer: identity.Issuer, Subject: identity.Subject.String(), Audience: jwt.Audience{identity.Audience},
			IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		}
	}
	sign := func(key *ecdsa.PrivateKey, id string, c jwt.Claims) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: id}}, nil)
		require.NoError(t, err)
		token, err := jwt.Signed(signer).Claims(c).Serialize()
		require.NoError(t, err)
		return token
	}
	oldToken, newToken := sign(oldKey, "old", claims()), sign(newKey, "new", claims())
	for _, token := range []string{oldToken, newToken} {
		_, err := validateToken(token, bundle, identity)
		require.NoError(t, err)
	}
	bundle.RemoveJWTAuthority("old")
	_, err = validateToken(oldToken, bundle, identity)
	require.ErrorContains(t, err, "runtime identity is unavailable or invalid")
	_, err = validateToken(newToken, bundle, identity)
	require.NoError(t, err)
	for name, change := range map[string]func(*jwt.Claims){
		"issuer":            func(c *jwt.Claims) { c.Issuer = "https://other.test" },
		"tenant":            func(c *jwt.Claims) { c.Subject = "spiffe://issuer.test/tenant/beta/snowflake/one/connection/ds" },
		"audience":          func(c *jwt.Claims) { c.Audience = jwt.Audience{"https://two.snowflakecomputing.com"} },
		"extra audience":    func(c *jwt.Claims) { c.Audience = append(c.Audience, "other") },
		"expired":           func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Minute)) },
		"long lived":        func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(time.Now().Add(time.Hour)) },
		"missing issued at": func(c *jwt.Claims) { c.IssuedAt = nil },
		"future":            func(c *jwt.Claims) { c.IssuedAt = jwt.NewNumericDate(time.Now().Add(time.Minute)) },
	} {
		t.Run(name, func(t *testing.T) {
			c := claims()
			change(&c)
			token := sign(newKey, "new", c)
			_, err := validateToken(token, bundle, identity)
			require.ErrorContains(t, err, "runtime identity is unavailable or invalid")
			require.NotContains(t, err.Error(), token)
			require.NotContains(t, xerror.PublicDetail(err), token)
		})
	}
}
