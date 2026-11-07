package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestNotificationConnectionConnectChecksAlertDestinationPermission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.ResourceAlertDestinations, input.Resource)
					require.Equal(t, model.Update, input.Action)
					return allowed, nil
				}},
				credentialsService: &fakeCredentialsService{ConnectNotificationConnectionFn: func(_ context.Context, owner credentialssvc.CredentialOwner, actor model.EventActor) (credentialssvc.ProviderConnectorInstall, error) {
					called = true
					require.Equal(t, credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: must.Slug("acme"), NotificationConnectionSlug: must.Slug("ops")}, owner)
					assertUserEventActor(t, actor, must.Slug("alice"))
					return credentialssvc.ProviderConnectorInstall{Slug: must.Slug("slack-install"), Provider: "slack", AuthorizationURL: "https://slack.com/oauth/v2/authorize?state=test-state", ExpiresAt: time.Now()}, nil
				}},
			})
			response := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, "/organizations/acme/notification-connections/ops/connect", nil))
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, allowed, called)
			if allowed {
				require.Equal(t, http.StatusCreated, response.StatusCode)
			} else {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
			}
		})
	}
}

func TestNotificationConnectionsListReturnsSafeTenantMetadata(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
		credentialsService: &fakeCredentialsService{ListNotificationConnectionsFn: func(_ context.Context, org slug.Slug) ([]credentialssvc.NotificationConnection, error) {
			require.Equal(t, must.Slug("acme"), org)
			return []credentialssvc.NotificationConnection{{Slug: must.Slug("ops"), Name: "Operations", Provider: "slack", Status: credentialssvc.CredentialStatusActive, Account: credentialssvc.ProviderAccount{ID: "T123", Name: "Workspace", ChannelID: "C123", ChannelName: "alerts"}}}, nil
		}},
	})
	response := authenticatedRequest(t, app, store, must.Slug("alice"), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/notification-connections", nil))
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	connections := parseResponse[[]api.NotificationConnection](t, response)
	require.Len(t, connections, 1)
	metadata, err := connections[0].Metadata.AsSlackNotificationConnectionMetadata()
	require.NoError(t, err)
	require.Equal(t, "alerts", metadata.ChannelName)
	require.Equal(t, api.NotificationProviderSlack, connections[0].Provider)
}

func TestSlackCallbackUsesNotificationConnectionLifecycle(t *testing.T) {
	owner := credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: must.Slug("acme"), NotificationConnectionSlug: must.Slug("ops")}
	completed := false
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			require.Equal(t, model.ResourceAlertDestinations, input.Resource)
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(context.Context, credentialssvc.FindOAuthInstallParams) (credentialssvc.CredentialInstall, error) {
				return credentialssvc.CredentialInstall{Provider: "slack", Owner: owner}, nil
			},
			CompleteOAuthCallbackFn: func(_ context.Context, params credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error) {
				completed = true
				require.Equal(t, "slack", params.Provider)
				return credentialssvc.CompleteOAuthCallbackResult{Owner: owner}, nil
			},
		},
	})
	response := authenticatedRequest(t, app, store, must.Slug("alice"), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/provider-connectors/slack/callback?state=local-state&code=local-code", nil))
	defer func() { _ = response.Body.Close() }()
	require.True(t, completed)
	require.Equal(t, http.StatusFound, response.StatusCode)
	require.Contains(t, response.Header.Get("Location"), "/orgs/acme/alerts/connections?provider_oauth=success")
	require.NotContains(t, response.Header.Get("Location"), "local-state")
	require.NotContains(t, response.Header.Get("Location"), "local-code")
}

func TestCreateNotificationConnectionRequiresExplicitProvider(t *testing.T) {
	for _, provider := range []string{"", "pagerduty", "slack"} {
		t.Run(provider, func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
				credentialsService: &fakeCredentialsService{CreateNotificationConnectionFn: func(_ context.Context, params credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
					called = true
					require.Equal(t, "slack", params.Provider)
					return credentialssvc.NotificationConnection{Slug: params.Slug, Name: params.Name, Provider: params.Provider}, nil
				}},
			})
			body := map[string]any{"name": "Operations", "slug": "ops"}
			if provider != "" {
				body["provider"] = provider
			}
			response := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, "/organizations/acme/notification-connections", body))
			defer func() { _ = response.Body.Close() }()
			if provider == "slack" {
				require.Equal(t, http.StatusCreated, response.StatusCode)
				require.True(t, called)
			} else {
				require.Equal(t, http.StatusBadRequest, response.StatusCode)
				require.False(t, called)
			}
		})
	}
}

func TestNotificationConnectionCreationChargesAuthorizedCredentialLimit(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "PermissionCheckedFirst", true: "RateLimited"}[allowed], func(t *testing.T) {
			t.Parallel()
			called := false
			app, store := newTestApp(t, testServerConfig{
				middleware: []fiber.Handler{func(c fiber.Ctx) error {
					c.Locals(authorizedAbuseControlLocalKey, &authorizedAbuseControl{limiter: deniedAbuseLimiter{}, logger: slog.New(slog.DiscardHandler), credentialMax: 1, expensiveTenantMax: 1, expensiveWindow: time.Minute})
					return c.Next()
				}},
				authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return allowed, nil }},
				credentialsService: &fakeCredentialsService{CreateNotificationConnectionFn: func(context.Context, credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
					called = true
					return credentialssvc.NotificationConnection{}, nil
				}},
			})
			response := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, "/organizations/acme/notification-connections", map[string]any{"name": "Operations", "slug": "ops", "provider": "slack"}))
			defer func() { _ = response.Body.Close() }()
			want := http.StatusForbidden
			if allowed {
				want = http.StatusTooManyRequests
			}
			require.Equal(t, want, response.StatusCode)
			require.False(t, called)
		})
	}
}

func TestCreateNotificationConnectionOptionalSlug(t *testing.T) {
	for _, custom := range []string{"omitted", "custom", "", "INVALID"} {
		t.Run(custom, func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{credentialsService: &fakeCredentialsService{CreateNotificationConnectionFn: func(_ context.Context, params credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
				called = true
				require.Equal(t, slug.Slug("acme"), params.OrgSlug)
				if custom == "omitted" {
					require.Empty(t, params.Slug)
				} else {
					require.Equal(t, slug.Slug(custom), params.Slug)
				}
				return credentialssvc.NotificationConnection{Slug: "generated", Name: params.Name, Provider: params.Provider}, nil
			}}})
			body := map[string]any{"name": "Operations", "provider": "slack"}
			if custom != "omitted" {
				body["slug"] = custom
			}
			response := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, "/organizations/acme/notification-connections", body))
			defer func() { _ = response.Body.Close() }()
			if custom == "" || custom == "INVALID" {
				require.Equal(t, 400, response.StatusCode)
				require.False(t, called)
			} else {
				require.Equal(t, 201, response.StatusCode)
				require.True(t, called)
			}
		})
	}
}
