package googleworkspace

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/googleoauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestConsentModes(t *testing.T) {
	a, err := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, mode string
		write      bool
		want       []string
	}{
		{"default read", "", false, []string{"openid", "email", driveFile}},
		{"default write", "", true, []string{"openid", "email", driveFile}},
		{"selected read", SelectedFiles, false, []string{"openid", "email", driveFile}},
		{"selected write", SelectedFiles, true, []string{"openid", "email", driveFile}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := a.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{AccessMode: tt.mode, IncludeWriteScopes: tt.write})
			if !slices.Equal(plan.Required, tt.want) || len(plan.Optional) != 0 {
				t.Fatalf("plan = %v", plan)
			}
			raw, err := a.AuthorizationURL(t.Context(), credentialssvc.AuthorizationURLParams{State: "state", Scopes: plan.Required})
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(strings.Fields(u.Query().Get("scope")), tt.want) || u.Query().Get("prompt") != "consent" || u.Query().Has("include_granted_scopes") {
				t.Fatalf("consent query = %v", u.Query())
			}
		})
	}
	for _, scopes := range [][]string{nil, {"openid", "email"}, {"openid", "email", "https://www.googleapis.com/auth/drive.metadata.readonly"}, {"openid", "email", "https://www.googleapis.com/auth/spreadsheets"}, {"openid", "email", "https://www.googleapis.com/auth/drive.file", "https://www.googleapis.com/auth/spreadsheets"}} {
		if _, err := a.AuthorizationURL(t.Context(), credentialssvc.AuthorizationURLParams{Scopes: scopes}); err == nil {
			t.Fatal("accepted unsupported consent scopes")
		}
		if _, err := a.ExchangeCode(t.Context(), credentialssvc.ExchangeCodeParams{Code: "secret-code", RequestedScopes: scopes}); err == nil || strings.Contains(err.Error(), "secret-code") {
			t.Fatal("accepted unsupported install or exposed code")
		}
	}
	if _, err := a.AuthorizationURL(t.Context(), credentialssvc.AuthorizationURLParams{Scopes: []string{"openid", "email", "https://www.googleapis.com/auth/drive.file"}, OptionalScopes: []string{"https://www.googleapis.com/auth/spreadsheets"}}); err == nil {
		t.Fatal("accepted optional scope escalation")
	}
	if plan := a.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{AccessMode: "invalid"}); len(plan.Required) != 0 {
		t.Fatal("invalid mode defaulted to a valid grant")
	}
}

func TestRefreshRejectsPartialStoredGrant(t *testing.T) {
	a, err := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scopes := range [][]string{nil, {"openid", "email"}, {"openid", "email", "https://www.googleapis.com/auth/drive.metadata.readonly"}, {"openid", "email", "https://www.googleapis.com/auth/spreadsheets.readonly"}, {"openid", "email", "https://www.googleapis.com/auth/drive"}, {"openid", "email", driveFile, "https://www.googleapis.com/auth/drive.readonly"}} {
		if _, err := a.Refresh(t.Context(), credentialssvc.RefreshParams{RefreshToken: "secret-refresh", Scopes: scopes}); err == nil || strings.Contains(err.Error(), "secret-refresh") {
			t.Fatal("accepted partial grant or exposed refresh token")
		}
	}
	for _, scopes := range [][]string{{"openid", "email", driveFile}, {"openid", "https://www.googleapis.com/auth/userinfo.email", driveFile}} {
		if !validGrantScopes(scopes) {
			t.Fatal("rejected valid stored grant")
		}
	}
}

func TestTokenMappingAndInvalidGrant(t *testing.T) {
	got, err := tokenSet(googleoauth.TokenSet{AccessToken: "access", RefreshToken: "refresh", Subject: "account", Email: "user@example.com", Scopes: []string{"https://www.googleapis.com/auth/drive.file"}}, nil)
	if err != nil || got.Account.ID != "account" || got.AccessToken != "access" || got.RefreshToken != "refresh" || !slices.Equal(got.Scopes, []string{"https://www.googleapis.com/auth/drive.file"}) {
		t.Fatal("incorrect token mapping")
	}
	_, err = tokenSet(googleoauth.TokenSet{}, googleoauth.ErrInvalidGrant)
	if !xerror.Is(err, credentialssvc.ErrProviderInvalidGrant) {
		t.Fatal("lost invalid grant classification")
	}
}

func TestWorkspaceRejectsBroadGrantAtExchangeBoundary(t *testing.T) {
	for _, scopes := range [][]string{{"openid", "email", "https://www.googleapis.com/auth/drive"}, {"openid", "email", driveFile, "https://www.googleapis.com/auth/drive.readonly"}} {
		if _, err := tokenSet(googleoauth.TokenSet{AccessToken: "secret-access", Scopes: scopes}, nil); err == nil || strings.Contains(err.Error(), "secret-access") {
			t.Fatal("accepted broad grant or exposed token")
		}
	}
}
