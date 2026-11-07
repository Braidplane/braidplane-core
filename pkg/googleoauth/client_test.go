package googleoauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var bigQueryScopes = []string{"https://www.googleapis.com/auth/bigquery", "openid", "email"}

func TestAuthorizationURLRequestsOfflineConsent(t *testing.T) {
	c, err := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.AuthorizationURL(t.Context(), AuthorizationURLParams{State: "state", Scopes: []string{"scope.one"}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if q.Query().Get("access_type") != "offline" || q.Query().Get("prompt") != "consent" || q.Query().Get("response_type") != "code" {
		t.Fatalf("query = %v", q.Query())
	}
}

func TestExchangeMapsTokenAndUserIdentity(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" {
				t.Errorf("form = %v", r.Form)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer","scope":"openid email https://www.googleapis.com/auth/bigquery"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"sub":"subject","email":"user@example.com","email_verified":true,"name":"User"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	c.tokenURL, c.userInfoURL = ts.URL+"/token", ts.URL+"/userinfo"
	got, err := c.ExchangeCode(context.Background(), "code", bigQueryScopes)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access" || got.RefreshToken != "refresh" || got.Subject != "subject" || got.Email != "user@example.com" || len(got.Scopes) != 3 {
		t.Fatalf("token = %+v", got)
	}
}

func TestRefreshAllowsGoogleToOmitRefreshToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":1800,"token_type":"Bearer","scope":"openid email https://www.googleapis.com/auth/bigquery"}`))
			return
		}
		_, _ = w.Write([]byte(`{"sub":"subject","email":"user@example.com","email_verified":true}`))
	}))
	defer ts.Close()
	c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	c.tokenURL, c.userInfoURL = ts.URL+"/token", ts.URL+"/userinfo"
	got, err := c.Refresh(context.Background(), "old-refresh", nil, bigQueryScopes)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "" {
		t.Fatalf("token = %+v", got)
	}
}

func TestRefreshPreservesAndValidatesOmittedScopes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":1800,"token_type":"Bearer"}`))
			return
		}
		_, _ = w.Write([]byte(`{"sub":"subject","email":"user@example.com","email_verified":true}`))
	}))
	defer ts.Close()
	c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	c.tokenURL, c.userInfoURL = ts.URL+"/token", ts.URL+"/userinfo"
	scopes := bigQueryScopes
	got, err := c.Refresh(t.Context(), "refresh", scopes, bigQueryScopes)
	if err != nil || !slices.Equal(got.Scopes, scopes) {
		t.Fatalf("scope preservation failed: %v", err)
	}
	if _, err := c.Refresh(t.Context(), "refresh", []string{"openid", "email"}, bigQueryScopes); err == nil {
		t.Fatal("accepted incomplete stored scopes")
	}
	if _, err := c.ExchangeCode(t.Context(), "code", bigQueryScopes); err == nil {
		t.Fatal("accepted initial exchange without scope")
	}
}

func TestExchangeDeniesRedirectAndDoesNotForwardToken(t *testing.T) {
	var forwarded bool
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	c.tokenURL = first.URL
	_, err := c.Refresh(context.Background(), "refresh", nil, bigQueryScopes)
	if err == nil || forwarded || strings.Contains(err.Error(), first.URL) {
		t.Fatalf("err=%v forwarded=%v", err, forwarded)
	}
}

func TestExchangeRejectsInvalidTokenMetadataAndIdentity(t *testing.T) {
	cases := []struct{ name, token, identity string }{
		{"missing scope", `{"access_token":"a","refresh_token":"r","expires_in":10,"token_type":"Bearer","scope":"openid email"}`, `{"sub":"s","email":"e@example.com","email_verified":true}`},
		{"invalid expiry", `{"access_token":"a","refresh_token":"r","expires_in":0,"token_type":"Bearer","scope":"openid email https://www.googleapis.com/auth/bigquery"}`, `{"sub":"s","email":"e@example.com","email_verified":true}`},
		{"unverified identity", `{"access_token":"a","refresh_token":"r","expires_in":10,"token_type":"Bearer","scope":"openid email https://www.googleapis.com/auth/bigquery"}`, `{"sub":"s","email":"e@example.com","email_verified":false}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(tt.token))
					return
				}
				_, _ = w.Write([]byte(tt.identity))
			}))
			defer ts.Close()
			c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
			c.tokenURL, c.userInfoURL = ts.URL+"/token", ts.URL+"/userinfo"
			if _, err := c.ExchangeCode(context.Background(), "code", bigQueryScopes); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestProviderErrorDoesNotExposeResponseBody(t *testing.T) {
	secret := "access-secret"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","description":"` + secret + `"}`))
	}))
	defer ts.Close()
	c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	c.tokenURL = ts.URL
	_, err := c.Refresh(context.Background(), "refresh", nil, bigQueryScopes)
	if !xerror.Is(err, ErrInvalidGrant) || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}

func TestRevokeAndCancellationAreSafe(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; _, _ = w.Write([]byte(`{}`)) }))
	defer ts.Close()
	c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	c.revokeURL = ts.URL
	if err := c.Revoke(context.Background(), "refresh"); err != nil || !called {
		t.Fatalf("revoke err=%v called=%v", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Refresh(ctx, "refresh", nil, bigQueryScopes); err == nil || strings.Contains(err.Error(), "refresh") {
		t.Fatalf("cancellation err=%v", err)
	}
}

func TestProviderSpecificScopesAndRefresh(t *testing.T) {
	const drive = "https://www.googleapis.com/auth/drive.file"
	const metadata = "https://www.googleapis.com/auth/drive.metadata.readonly"
	const read = "https://www.googleapis.com/auth/spreadsheets.readonly"
	const write = "https://www.googleapis.com/auth/spreadsheets"
	for _, required := range [][]string{{"openid", "email", drive}, {"openid", "email", metadata, read}, {"openid", "email", metadata, write}, bigQueryScopes} {
		t.Run(required[len(required)-1], func(t *testing.T) {
			responseScopes := strings.Join(required, " ")
			omitScopes := false
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					scopes := ""
					if !omitScopes {
						scopes = `,"scope":"` + responseScopes + `"`
					}
					_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"` + scopes + `}`))
					return
				}
				_, _ = w.Write([]byte(`{"sub":"subject","email":"user@example.com","email_verified":true}`))
			}))
			defer ts.Close()
			c, _ := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
			c.tokenURL, c.userInfoURL = ts.URL+"/token", ts.URL+"/userinfo"
			if _, err := c.ExchangeCode(t.Context(), "code", required); err != nil {
				t.Fatal(err)
			}
			responseScopes = strings.ReplaceAll(responseScopes, "email", "https://www.googleapis.com/auth/userinfo.email")
			if _, err := c.ExchangeCode(t.Context(), "code", required); err != nil {
				t.Fatal("email alias rejected:", err)
			}
			responseScopes = "openid email"
			if _, err := c.ExchangeCode(t.Context(), "code", required); err == nil {
				t.Fatal("accepted partial consent")
			}
			if _, err := c.Refresh(t.Context(), "refresh", required, required); err == nil {
				t.Fatal("accepted narrowed refresh scopes")
			}
			omitScopes = true
			got, err := c.Refresh(t.Context(), "refresh", required, required)
			if err != nil || !slices.Equal(got.Scopes, required) {
				t.Fatal("refresh did not preserve stored scopes", err)
			}
			if _, err := c.Refresh(t.Context(), "refresh", []string{"openid", "email"}, required); err == nil {
				t.Fatal("broadened incomplete stored grant")
			}
			if _, err := c.ExchangeCode(t.Context(), "code", required); err == nil {
				t.Fatal("accepted initial consent without scopes")
			}
			if _, err := c.ExchangeCode(t.Context(), "code", nil); err == nil {
				t.Fatal("accepted missing provider scope plan")
			}
		})
	}
}
