package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/service/oidc"
)

func TestDiscoveryWaitHonorsCallerCancellation(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(provider.Close)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	service, err := oidc.New(provider.URL, "client", "secret", "https://app.test/callback")
	require.NoError(t, err)
	first := make(chan error, 1)
	go func() {
		_, err := service.GetAuthURL(t.Context(), "first-state", "https://app.test/callback")
		first <- err
	}()
	<-entered
	for _, method := range []string{"authorization", "exchange"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started, result := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				if method == "authorization" {
					_, err := service.GetAuthURL(ctx, "state", "https://app.test/callback")
					result <- err
				} else {
					_, err := service.ExchangeCodeForToken(ctx, "code", "https://app.test/callback")
					result <- err
				}
			}()
			<-started
			cancel()
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled caller remained blocked behind provider discovery")
			}
		})
	}
	assert.EqualValues(t, 1, calls.Load())
	unblock()
	require.Error(t, <-first)
}

func TestDiscoveryIsDeferredAndRetries(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var available atomic.Bool
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if !available.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize",
			"token_endpoint": provider.URL + "/token", "jwks_uri": provider.URL + "/keys",
		}))
	}))
	t.Cleanup(provider.Close)

	service, err := oidc.New(provider.URL, "client", "secret", "https://app.test/callback")
	require.NoError(t, err)
	assert.True(t, service.Configured())
	assert.Zero(t, calls.Load())
	_, err = service.GetAuthURL(t.Context(), "state", "https://app.test/callback")
	require.Error(t, err)
	available.Store(true)
	authURL, err := service.GetAuthURL(t.Context(), "state", "https://app.test/callback")
	require.NoError(t, err)
	assert.Contains(t, authURL, provider.URL+"/authorize")
	_, err = service.GetAuthURL(t.Context(), "state", "https://app.test/callback")
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load())

	disabled, err := oidc.New(provider.URL, "", "", "")
	require.NoError(t, err)
	assert.False(t, disabled.Configured())
	_, err = disabled.GetAuthURL(t.Context(), "state", "https://app.test/callback")
	require.ErrorContains(t, err, "not configured")
	assert.EqualValues(t, 2, calls.Load())
	_, err = oidc.New(provider.URL, "client", "", "")
	require.ErrorContains(t, err, "configured together")
}

func TestConcurrentDiscoveryIsShared(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize",
			"token_endpoint": provider.URL + "/token", "jwks_uri": provider.URL + "/keys",
		}))
	}))
	t.Cleanup(provider.Close)
	service, err := oidc.New(provider.URL, "client", "secret", "https://app.test/callback")
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := service.GetAuthURL(t.Context(), "state", "https://app.test/callback")
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load())
}

func TestOIDCFlow(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, nil)
	require.NoError(t, err)
	provider := httptest.NewUnstartedServer(nil)
	tokens := make(map[string]string)
	provider.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize",
				"token_endpoint": provider.URL + "/token", "jwks_uri": provider.URL + "/keys",
				"id_token_signing_alg_values_supported": []string{"ES256"},
			}))
		case "/keys":
			assert.NoError(t, json.NewEncoder(w).Encode(jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Algorithm: "ES256", Use: "sig"}},
			}))
		case "/token":
			assert.Equal(t, http.MethodPost, r.Method)
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			assert.Equal(t, "https://app.test/callback", r.Form.Get("redirect_uri"))
			clientID, clientSecret, ok := r.BasicAuth()
			if !ok {
				clientID, clientSecret = r.Form.Get("client_id"), r.Form.Get("client_secret")
			}
			assert.Equal(t, "login-client", clientID)
			assert.Equal(t, "login-secret", clientSecret)
			if r.Form.Get("code") == "provider-error" {
				w.WriteHeader(http.StatusBadRequest)
				_, err := w.Write([]byte(`{"error":"invalid_grant","error_description":"access-secret refresh-secret login-secret","access_token":"access-secret","refresh_token":"refresh-secret"}`))
				assert.NoError(t, err)
				return
			}
			response := map[string]any{"access_token": "access-secret", "token_type": "Bearer"}
			if token, ok := tokens[r.Form.Get("code")]; ok {
				response["id_token"] = token
			}
			assert.NoError(t, json.NewEncoder(w).Encode(response))
		default:
			http.NotFound(w, r)
		}
	})
	provider.Start()
	t.Cleanup(provider.Close)

	claims := jwt.Claims{
		Issuer: provider.URL, Subject: "google-user", Audience: jwt.Audience{"login-client"},
		Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	profile := map[string]any{
		"email": "user@example.com", "email_verified": true, "name": "Test User",
		"picture": "https://app.test/photo", "given_name": "Test", "family_name": "User",
		"locale": "pl", "preferred_username": "test-user",
	}
	tokens["valid"], err = jwt.Signed(signer).Claims(claims).Claims(profile).Serialize()
	require.NoError(t, err)
	claims.Audience = jwt.Audience{"connector-client"}
	tokens["wrong-audience"], err = jwt.Signed(signer).Claims(claims).Claims(profile).Serialize()
	require.NoError(t, err)
	tokens["invalid-token"] = "invalid-id-token"

	svc, err := oidc.New(provider.URL, "login-client", "login-secret", "https://default.test/callback")
	require.NoError(t, err)

	t.Run("authorization preserves scopes and per-request redirect", func(t *testing.T) {
		for _, redirect := range []string{"https://app.test/callback", "https://other.test/callback"} {
			rawURL, err := svc.GetAuthURL(t.Context(), "browser-state", redirect)
			require.NoError(t, err)
			authURL, err := url.Parse(rawURL)
			require.NoError(t, err)
			assert.Equal(t, provider.URL+"/authorize", authURL.Scheme+"://"+authURL.Host+authURL.Path)
			assert.Equal(t, "login-client", authURL.Query().Get("client_id"))
			assert.Equal(t, redirect, authURL.Query().Get("redirect_uri"))
			assert.Equal(t, "browser-state", authURL.Query().Get("state"))
			assert.Equal(t, "openid profile email", authURL.Query().Get("scope"))
			assert.Equal(t, "code", authURL.Query().Get("response_type"))
			assert.Equal(t, "offline", authURL.Query().Get("access_type"))
		}
	})

	t.Run("verifies ID token and maps profile without a refresh token", func(t *testing.T) {
		user, err := svc.ExchangeCodeForToken(t.Context(), "valid", "https://app.test/callback")
		require.NoError(t, err)
		assert.Equal(t, &oidc.User{
			ProviderID: "google-user", Email: "user@example.com", EmailVerified: true, Name: "Test User",
			Picture: "https://app.test/photo", GivenName: "Test", FamilyName: "User", Locale: "pl", PreferredUsername: "test-user",
		}, user)
	})

	for _, code := range []string{"provider-error", "missing-id-token", "invalid-token", "wrong-audience"} {
		t.Run(code, func(t *testing.T) {
			user, err := svc.ExchangeCodeForToken(t.Context(), code, "https://app.test/callback")
			require.Error(t, err)
			assert.Nil(t, user)
			if code == "provider-error" {
				require.ErrorContains(t, err, "invalid_grant")
				for _, secret := range []string{"access-secret", "refresh-secret", "login-secret"} {
					assert.NotContains(t, err.Error(), secret)
				}
			}
		})
	}
}
