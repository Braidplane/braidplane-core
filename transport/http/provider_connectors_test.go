package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	testfakes "github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestStartProviderConnectorInstall_UsesCredentialService(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	var got credentialssvc.StartOAuthInstallParams
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Update {
				return false, nil
			}
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			StartOAuthInstallFn: func(
				_ context.Context,
				params credentialssvc.StartOAuthInstallParams,
			) (credentialssvc.ProviderConnectorInstall, error) {
				got = params
				return credentialssvc.ProviderConnectorInstall{
					Slug:             slug.Slug("oauth-install-1"),
					Provider:         "hubspot",
					AuthorizationURL: "https://hubspot.example/oauth?state=opaque-state",
					State:            "opaque-state",
					ExpiresAt:        now,
					Intent:           credentialssvc.OAuthInstallIntentCreate,
					RequestedScopes:  []string{"contacts.read"},
				}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			FindBySlugFn: func(
				_ context.Context,
				orgSlug, dsSlug slug.Slug,
			) (*datasourcessvc.DataSource, error) {
				assert.Equal(t, slug.Slug("acme"), orgSlug)
				assert.Equal(t, slug.Slug("crm"), dsSlug)
				return &datasourcessvc.DataSource{
					Slug: dsSlug,
					Type: model.DataSourceTypeHubSpot,
					Credential: &credentialssvc.CredentialMetadata{
						Kind:   credentialssvc.CredentialKindOAuth2Grant,
						Status: credentialssvc.CredentialStatusPending,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/hubspot/installs", map[string]any{
		"data_source_slug": "crm",
		"intent":           "create_grant",
	})
	resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.ProviderConnectorInstall](t, resp)
	assert.Equal(t, api.Slug("oauth-install-1"), body.Slug)
	assert.Equal(t, "https://hubspot.example/oauth?state=opaque-state", body.AuthorizationUrl)
	assert.Equal(t, credentialssvc.CredentialOwner{
		OrgSlug:        slug.Slug("acme"),
		DataSourceSlug: slug.Slug("crm"),
	}, got.Owner)
	assert.Equal(t, "hubspot", got.Provider)
	assert.Equal(t, credentialssvc.OAuthInstallIntentCreate, got.Intent)
	assert.Empty(t, got.RequestedScopes)
	assert.True(t, got.IncludeWriteScopes)
	assertUserEventActor(t, got.Actor, slug.Slug("alice"))
}

func TestRepositoryGitLabCallbackKeepsConsentBoundToUserAndAttempt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "denied", "missing-code", "expired", "archived", "attached", "wrong-user", "deny-organization", "deny-project", "deny-source"} {
		t.Run(scenario, func(t *testing.T) {
			completed, consumed, rejected, inspected := false, false, false, false
			install := credentialssvc.CredentialInstall{Provider: "gitlab", Owner: credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab, OrgSlug: "acme"}, Actor: model.UserEventActor("alice"), RepositorySetupSlug: "setup-one", RepositorySetupProjectSlug: "project-one"}
			current := &model.ProjectRepositorySetup{Slug: "setup-one", ProjectSlug: "project-one", Status: model.ProjectRepositorySetupStatusPending, Request: model.CreateProjectRepository{Provider: model.DataSourceTypeGitlab}}
			if scenario == "archived" {
				now := time.Now()
				current.AbandonedAt = &now
			}
			if scenario == "attached" {
				current.Status = model.ProjectRepositorySetupStatusAttached
			}
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
					denied := scenario == "deny-organization" && in.Resource == model.ResourceOrganizations || scenario == "deny-project" && in.Resource == model.ResourceProjects || scenario == "deny-source" && in.Resource == model.ResourceDataSources
					return !denied, nil
				}},
				credentialsService: &fakeCredentialsService{
					FindOAuthInstallFn: func(_ context.Context, p credentialssvc.FindOAuthInstallParams) (credentialssvc.CredentialInstall, error) {
						require.Equal(t, "gitlab", p.Provider)
						require.Equal(t, "synthetic-state", p.State)
						if scenario == "expired" {
							return install, credentialssvc.ErrOAuthStateExpired
						}
						return install, nil
					},
					ConsumeOAuthInstallFn: func(_ context.Context, p credentialssvc.ConsumeOAuthInstallParams) (credentialssvc.CredentialInstall, error) {
						consumed = true
						require.Equal(t, "synthetic-state", p.State)
						return install, nil
					},
				},
				repositorySetupService: repositorySetupFake{
					rejectGitLab: func(_ context.Context, org, project, setup slug.Slug, actor model.EventActor, state, providerError string) (*model.ProjectRepositorySetup, error) {
						rejected = true
						require.Equal(t, install.Owner.OrgSlug, org)
						require.Equal(t, install.RepositorySetupProjectSlug, project)
						require.Equal(t, install.RepositorySetupSlug, setup)
						require.Equal(t, install.Actor, actor)
						require.Equal(t, "synthetic-state", state)
						require.Equal(t, scenario == "denied", providerError == "access_denied")
						return current, nil
					},
					attempt: func(_ context.Context, org, project, setup slug.Slug) (*model.ProjectRepositorySetup, error) {
						inspected = true
						require.Equal(t, slug.Slug("acme"), org)
						require.Equal(t, install.RepositorySetupProjectSlug, project)
						require.Equal(t, install.RepositorySetupSlug, setup)
						return current, nil
					},
					completeGitLab: func(_ context.Context, org, project, setup slug.Slug, actor model.EventActor, authorization model.GitLabRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
						completed = true
						require.Equal(t, slug.Slug("acme"), org)
						require.Equal(t, install.RepositorySetupProjectSlug, project)
						require.Equal(t, install.RepositorySetupSlug, setup)
						require.Equal(t, install.Actor, actor)
						require.Equal(t, "synthetic-code", authorization.Code)
						require.Equal(t, "synthetic-state", authorization.State)
						return &model.ProjectRepositorySetup{Status: model.ProjectRepositorySetupStatusAttached}, nil
					},
				},
			})
			path := "/provider-connectors/gitlab/callback?state=synthetic-state"
			if scenario == "denied" {
				path += "&error=access_denied&error_description=private-provider-detail"
			} else if scenario != "missing-code" {
				path += "&code=synthetic-code"
			}
			user := slug.Slug("alice")
			if scenario == "wrong-user" {
				user = "bob"
			}
			resp := authenticatedRequest(t, app, store, user, jsonRequest(t, http.MethodGet, path, nil))
			defer func() { require.NoError(t, resp.Body.Close()) }()
			location := resp.Header.Get("Location")
			require.NotContains(t, location, "synthetic-state")
			require.NotContains(t, location, "synthetic-code")
			require.NotContains(t, location, "private-provider-detail")
			if scenario == "wrong-user" || scenario == "deny-organization" || scenario == "deny-project" || scenario == "deny-source" {
				require.GreaterOrEqual(t, resp.StatusCode, 400)
				require.False(t, inspected)
			} else {
				require.Equal(t, http.StatusFound, resp.StatusCode)
				require.Contains(t, location, "/organizations/acme/projects/project-one?")
				status := scenario
				if scenario == "archived" || scenario == "attached" {
					status = "stale"
				}
				if scenario == "missing-code" {
					status = "error"
				}
				require.Contains(t, location, "gitlab_setup="+status)
			}
			require.Equal(t, scenario == "success", completed)
			require.Equal(t, scenario == "denied" || scenario == "missing-code", rejected)
			require.False(t, consumed, "the repository service owns nonce consumption")
		})
	}
}

func TestStartProviderConnectorInstallRejectsMismatchedBigQuerySource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider   string
		sourceType model.DataSourceType
	}{
		{"bigquery", model.DataSourceTypeHubSpot},
		{"hubspot", model.DataSourceTypeBigQuery},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			started := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
				datasourcesService: &fakeDataSourcesService{FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
					return &datasourcessvc.DataSource{Type: tc.sourceType}, nil
				}},
				credentialsService: &fakeCredentialsService{StartOAuthInstallFn: func(context.Context, credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
					started = true
					return credentialssvc.ProviderConnectorInstall{}, nil
				}},
			})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/"+tc.provider+"/installs", map[string]any{"data_source_slug": "warehouse", "intent": "create_grant"})
			resp := authenticatedRequest(t, app, store, must.Slug("alice"), req)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.False(t, started)
		})
	}
}

// TestStartProviderConnectorInstallAuthorizesBeforeReadingDataSource prevents existence disclosure on denied requests.
func TestStartProviderConnectorInstallAuthorizesBeforeReadingDataSource(t *testing.T) {
	t.Parallel()

	found := false
	started := false
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Create {
				return false, nil
			}
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			StartOAuthInstallFn: func(
				context.Context,
				credentialssvc.StartOAuthInstallParams,
			) (credentialssvc.ProviderConnectorInstall, error) {
				started = true
				return credentialssvc.ProviderConnectorInstall{}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
				found = true
				return &datasourcessvc.DataSource{}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/fakecrm/installs", map[string]any{
		"data_source_slug": "crm",
		"intent":           "create_grant",
	})
	resp := authenticatedRequest(t, app, store, must.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, found)
	assert.False(t, started)
}

// TestStartProviderConnectorInstallCannotUseCreateIntentToReplaceActiveCredentials verifies canonical intent enforcement.
func TestStartProviderConnectorInstallCannotUseCreateIntentToReplaceActiveCredentials(t *testing.T) {
	t.Parallel()

	started := false
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Update {
				return false, nil
			}
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			StartOAuthInstallFn: func(
				context.Context,
				credentialssvc.StartOAuthInstallParams,
			) (credentialssvc.ProviderConnectorInstall, error) {
				started = true
				return credentialssvc.ProviderConnectorInstall{}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
				return &datasourcessvc.DataSource{
					IsActive: true,
					Credential: &credentialssvc.CredentialMetadata{
						Kind:   credentialssvc.CredentialKindStaticSecret,
						Status: credentialssvc.CredentialStatusActive,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/fakecrm/installs", map[string]any{
		"data_source_slug": "crm",
		"intent":           "create_grant",
	})
	resp := authenticatedRequest(t, app, store, must.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, started)
}

// TestStartProviderDataSourceInstallCreatesPendingSourceWithoutCredentials verifies the OAuth-first API boundary.
func TestStartProviderDataSourceInstallCreatesPendingSourceWithoutCredentials(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	var got *datasourcessvc.StartProviderDataSourceInstallParams
	checkedEntitlement := false
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Update {
				return false, nil
			}
			return true, nil
		}},
		admissionService: &testfakes.AdmissionService{
			CheckDataSourceAdmissionFn: func(_ context.Context, orgSlug slug.Slug) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				checkedEntitlement = true
				return nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			StartProviderDataSourceInstallFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				actor model.EventActor,
				provider string,
				params *datasourcessvc.StartProviderDataSourceInstallParams,
			) (*datasourcessvc.ProviderDataSourceInstall, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assertUserEventActor(t, actor, must.Slug("alice"))
				assert.Equal(t, "hubspot", provider)
				got = params
				credential := credentialssvc.CredentialMetadata{
					Slug:      must.Slug("oauth-credential-crm"),
					Kind:      credentialssvc.CredentialKindOAuth2Grant,
					Status:    credentialssvc.CredentialStatusPending,
					UpdatedAt: now,
					OAuth:     credentialssvc.CredentialOAuthMetadata{Provider: "hubspot"},
				}
				return &datasourcessvc.ProviderDataSourceInstall{
					DataSource: &datasourcessvc.DataSource{
						Slug:       must.Slug("crm"),
						Name:       "CRM",
						Type:       model.DataSourceTypeHubSpot,
						Config:     []byte(`{"timeout_seconds":5}`),
						Credential: &credential,
						ReadOnly:   false,
						CreatedAt:  now,
						UpdatedAt:  now,
					},
					Install: credentialssvc.ProviderConnectorInstall{
						Slug:             must.Slug("oauth-install-crm"),
						Provider:         "hubspot",
						AuthorizationURL: "https://app.hubspot.com/oauth/authorize?state=opaque",
						ExpiresAt:        now.Add(10 * time.Minute),
						Intent:           credentialssvc.OAuthInstallIntentCreate,
						RequestedScopes:  []string{"oauth", "crm.objects.contacts.read"},
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/hubspot/data-source-installs", map[string]any{
		"name":      "CRM",
		"slug":      "crm",
		"type":      "hubspot",
		"config":    map[string]any{"timeout_seconds": 5},
		"read_only": false,
	})
	resp := authenticatedRequest(t, app, store, must.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.ProviderDataSourceInstall](t, resp)
	assert.Equal(t, api.Slug("crm"), body.DataSource.Slug)
	assert.False(t, body.DataSource.IsActive)
	assert.False(t, body.DataSource.ReadOnly)
	require.NotNil(t, body.DataSource.Credential)
	assert.Equal(t, api.Oauth2Grant, body.DataSource.Credential.Kind)
	assert.Equal(t, api.CredentialStatusPending, body.DataSource.Credential.Status)
	require.NotNil(t, body.DataSource.Credential.Oauth)
	assert.Equal(t, []string{}, body.DataSource.Credential.Oauth.Scopes)
	assert.Equal(t, "https://app.hubspot.com/oauth/authorize?state=opaque", body.Install.AuthorizationUrl)
	require.NotNil(t, got)
	assert.Equal(t, "CRM", got.Name)
	assert.Equal(t, must.Slug("crm"), got.Slug)
	assert.Equal(t, model.DataSourceTypeHubSpot, got.Type)
	assert.JSONEq(t, `{"timeout_seconds":5}`, string(got.ConfigJSON))
	require.NotNil(t, got.ReadOnly)
	assert.False(t, *got.ReadOnly)
	assert.True(t, checkedEntitlement)
}

// TestHandleProviderConnectorCallback_CreateIntentUsesCreatePermission verifies OAuth onboarding does not require update access.
func TestHandleProviderConnectorCallback_CreateIntentUsesCreatePermission(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Update {
				return false, nil
			}
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(
				_ context.Context,
				_ credentialssvc.FindOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				return credentialssvc.CredentialInstall{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        must.Slug("acme"),
						DataSourceSlug: must.Slug("crm"),
					},
					Intent: credentialssvc.OAuthInstallIntentCreate,
				}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			CompleteProviderOAuthCallbackFn: func(
				_ context.Context,
				params credentialssvc.CompleteOAuthCallbackParams,
			) (credentialssvc.CompleteOAuthCallbackResult, error) {
				return credentialssvc.CompleteOAuthCallbackResult{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        must.Slug("acme"),
						DataSourceSlug: must.Slug("crm"),
					},
					Intent: credentialssvc.OAuthInstallIntentCreate,
					Actor:  params.Actor,
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/provider-connectors/hubspot/callback?code=code-1&state=state-1", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "http://localhost:3000/orgs/acme/data-sources?provider_oauth=success&selected=crm", resp.Header.Get("Location"))
}

func TestHandleProviderConnectorCallback_CompletesWithoutReturningSecrets(t *testing.T) {
	t.Parallel()

	var found credentialssvc.FindOAuthInstallParams
	var got credentialssvc.CompleteOAuthCallbackParams
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Create {
				return false, nil
			}
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(
				_ context.Context,
				params credentialssvc.FindOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				found = params
				return credentialssvc.CredentialInstall{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        slug.Slug("acme"),
						DataSourceSlug: slug.Slug("crm"),
					},
					Intent: credentialssvc.OAuthInstallIntentRotate,
				}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			CompleteProviderOAuthCallbackFn: func(
				_ context.Context,
				params credentialssvc.CompleteOAuthCallbackParams,
			) (credentialssvc.CompleteOAuthCallbackResult, error) {
				got = params
				return credentialssvc.CompleteOAuthCallbackResult{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        slug.Slug("acme"),
						DataSourceSlug: slug.Slug("crm"),
					},
					Intent: credentialssvc.OAuthInstallIntentRotate,
					Actor:  model.UserEventActor(slug.Slug("alice")),
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/provider-connectors/fakecrm/callback?code=code-1&state=state-1&iss=https%3A%2F%2Fissuer.example", nil)
	resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "http://localhost:3000/orgs/acme/data-sources?provider_oauth=success&selected=crm", resp.Header.Get("Location"))
	assert.Equal(t, "fakecrm", found.Provider)
	assert.Equal(t, "state-1", found.State)
	assert.Equal(t, "fakecrm", got.Provider)
	assert.Equal(t, "code-1", got.Code)
	assert.Equal(t, "https://issuer.example", got.Issuer)
	assert.Equal(t, "state-1", got.State)
	assertUserEventActor(t, got.Actor, slug.Slug("alice"))
}

func TestHandleProviderConnectorCallback_AbuseLimitRedirectsToSafeResult(t *testing.T) {
	t.Parallel()

	completed := false
	app, store := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{func(c fiber.Ctx) error {
			c.Locals(authorizedAbuseControlLocalKey, &authorizedAbuseControl{
				limiter:            deniedAbuseLimiter{},
				logger:             slog.New(slog.DiscardHandler),
				credentialMax:      1,
				expensiveTenantMax: 1,
				expensiveWindow:    time.Minute,
			})
			return c.Next()
		}},
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(
				_ context.Context,
				_ credentialssvc.FindOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				return credentialssvc.CredentialInstall{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        slug.Slug("acme"),
						DataSourceSlug: slug.Slug("crm"),
					},
				}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			CompleteProviderOAuthCallbackFn: func(
				context.Context,
				credentialssvc.CompleteOAuthCallbackParams,
			) (credentialssvc.CompleteOAuthCallbackResult, error) {
				completed = true
				return credentialssvc.CompleteOAuthCallbackResult{}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/provider-connectors/fakecrm/callback?code=code-1&state=state-1", nil)
	resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "http://localhost:3000/orgs/acme/data-sources?provider_oauth=error&selected=crm", resp.Header.Get("Location"))
	assert.False(t, completed)
}

type deniedAbuseLimiter struct{}

func (deniedAbuseLimiter) Attempt(
	context.Context,
	httpsecurity.AbuseLimit,
) (httpsecurity.AbuseDecision, error) {
	return httpsecurity.AbuseDecision{Allowed: false, ResetAt: time.Now().Add(time.Minute)}, nil
}

func TestHandleProviderConnectorCallback_DenialUsesAuthorizedOrganizationRedirect(t *testing.T) {
	t.Parallel()

	completed := false
	consumed := false
	install := credentialssvc.CredentialInstall{
		Owner: credentialssvc.CredentialOwner{
			OrgSlug:        slug.Slug("acme"),
			DataSourceSlug: slug.Slug("crm"),
		},
	}
	app, store := newTestApp(t, testServerConfig{
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(
				_ context.Context,
				params credentialssvc.FindOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				require.Equal(t, "fakecrm", params.Provider)
				require.Equal(t, "state-1", params.State)
				if consumed {
					return install, credentialssvc.ErrOAuthStateConsumed
				}
				return install, nil
			},
			ConsumeOAuthInstallFn: func(
				_ context.Context,
				params credentialssvc.ConsumeOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				require.Equal(t, "fakecrm", params.Provider)
				require.Equal(t, "state-1", params.State)
				consumed = true
				return install, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			CompleteProviderOAuthCallbackFn: func(
				context.Context,
				credentialssvc.CompleteOAuthCallbackParams,
			) (credentialssvc.CompleteOAuthCallbackResult, error) {
				completed = true
				return credentialssvc.CompleteOAuthCallbackResult{}, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/provider-connectors/fakecrm/callback?error=access_denied&error_description=do-not-echo&state=state-1",
		nil,
	)
	resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "http://localhost:3000/orgs/acme/data-sources?provider_oauth=denied&selected=crm", resp.Header.Get("Location"))
	assert.NotContains(t, resp.Header.Get("Location"), "do-not-echo")
	assert.True(t, consumed)
	assert.False(t, completed)

	replayReq := jsonRequest(t, http.MethodGet, "/provider-connectors/fakecrm/callback?code=replayed-code&state=state-1", nil)
	replayResp := authenticatedRequest(t, app, store, slug.Slug("alice"), replayReq)
	defer func() { _ = replayResp.Body.Close() }()

	require.Equal(t, http.StatusFound, replayResp.StatusCode)
	assert.Equal(t, "http://localhost:3000/orgs/acme/data-sources?provider_oauth=error&selected=crm", replayResp.Header.Get("Location"))
	assert.False(t, completed)
}

func TestHandleProviderConnectorCallback_ExpiredStateReturnsToRecoverableSource(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Update {
				return false, nil
			}
			return true, nil
		}},
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(
				_ context.Context,
				params credentialssvc.FindOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				require.Equal(t, "state-1", params.State)
				return credentialssvc.CredentialInstall{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        slug.Slug("acme"),
						DataSourceSlug: slug.Slug("crm"),
					},
				}, credentialssvc.ErrOAuthStateExpired
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/provider-connectors/fakecrm/callback?code=code-1&state=state-1", nil)
	resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "http://localhost:3000/orgs/acme/data-sources?provider_oauth=expired&selected=crm", resp.Header.Get("Location"))
}

func TestHandleProviderConnectorCallback_RequiresUserPrincipalBeforeCompletion(t *testing.T) {
	t.Parallel()

	completed := false
	app, _ := newTestApp(t, testServerConfig{
		credentialsService: &fakeCredentialsService{
			FindOAuthInstallFn: func(
				_ context.Context,
				params credentialssvc.FindOAuthInstallParams,
			) (credentialssvc.CredentialInstall, error) {
				require.Equal(t, "fakecrm", params.Provider)
				require.Equal(t, "state-1", params.State)
				return credentialssvc.CredentialInstall{
					Owner: credentialssvc.CredentialOwner{
						OrgSlug:        slug.Slug("acme"),
						DataSourceSlug: slug.Slug("crm"),
					},
				}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			CompleteProviderOAuthCallbackFn: func(
				context.Context,
				credentialssvc.CompleteOAuthCallbackParams,
			) (credentialssvc.CompleteOAuthCallbackResult, error) {
				completed = true
				return credentialssvc.CompleteOAuthCallbackResult{}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/provider-connectors/fakecrm/callback?code=code-1&state=state-1", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.False(t, completed)
}

func TestProviderGrantEndpoints_ReturnSafeCredentialMetadata(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	lastRefreshedAt := now.Add(-time.Minute)
	metadata := credentialssvc.CredentialMetadata{
		Kind:      credentialssvc.CredentialKindOAuth2Grant,
		Status:    credentialssvc.CredentialStatusActive,
		UpdatedAt: now,
		OAuth: credentialssvc.CredentialOAuthMetadata{
			Provider:             "fakecrm",
			Scopes:               []string{"contacts.read"},
			Account:              credentialssvc.ProviderAccount{ID: "acct-1", Name: "Acme CRM", Email: "ops@example.com"},
			AccessTokenExpiresAt: expiresAt,
			LastRefreshedAt:      &lastRefreshedAt,
		},
	}
	var refreshed credentialssvc.RefreshGrantParams
	var revoked credentialssvc.RevokeParams
	var rotated credentialssvc.RotateParams
	var recorded []model.EventAction
	app, store := newTestApp(t, testServerConfig{
		credentialsService: &fakeCredentialsService{
			RefreshFn: func(
				_ context.Context,
				params credentialssvc.RefreshGrantParams,
			) (credentialssvc.CredentialMetadata, error) {
				refreshed = params
				return metadata, nil
			},
			RevokeFn: func(
				_ context.Context,
				params credentialssvc.RevokeParams,
			) (credentialssvc.CredentialMetadata, error) {
				revoked = params
				revokedMetadata := metadata
				revokedMetadata.Status = credentialssvc.CredentialStatusRevoked
				revokedMetadata.OAuth.RevokedAt = &now
				return revokedMetadata, nil
			},
			RotateFn: func(
				_ context.Context,
				params credentialssvc.RotateParams,
			) (credentialssvc.ProviderConnectorInstall, error) {
				rotated = params
				return credentialssvc.ProviderConnectorInstall{
					Slug:             slug.Slug("oauth-install-2"),
					Provider:         "fakecrm",
					AuthorizationURL: "https://fakecrm.example/oauth?state=rotate-state",
					State:            "rotate-state",
					ExpiresAt:        now,
					Intent:           credentialssvc.OAuthInstallIntentRotate,
					RequestedScopes:  []string{"contacts.read"},
				}, nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			FindBySlugFn: func(
				_ context.Context,
				orgSlug, dsSlug slug.Slug,
			) (*datasourcessvc.DataSource, error) {
				assert.Equal(t, slug.Slug("acme"), orgSlug)
				assert.Equal(t, slug.Slug("crm"), dsSlug)
				return &datasourcessvc.DataSource{Slug: dsSlug}, nil
			},
			RecordCredentialLifecycleEventFn: func(
				_ context.Context,
				orgSlug, dsSlug slug.Slug,
				actor model.EventActor,
				action model.EventAction,
			) error {
				assert.Equal(t, slug.Slug("acme"), orgSlug)
				assert.Equal(t, slug.Slug("crm"), dsSlug)
				assert.Equal(t, model.EventActorTypeUser, actor.Type)
				recorded = append(recorded, action)
				return nil
			},
		},
	})

	refreshResp := authenticatedRequest(t, app, store, slug.Slug("alice"), jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme/data-sources/crm/provider-grant/refresh",
		nil,
	))
	defer func() { _ = refreshResp.Body.Close() }()
	require.Equal(t, http.StatusOK, refreshResp.StatusCode)
	refreshBody := parseResponse[api.CredentialMetadata](t, refreshResp)
	assert.Equal(t, api.CredentialKind("oauth2_grant"), refreshBody.Kind)
	assert.Equal(t, api.CredentialStatus("active"), refreshBody.Status)
	require.NotNil(t, refreshBody.Oauth)
	assert.Equal(t, "fakecrm", refreshBody.Oauth.Provider)
	assert.Equal(t, []string{"contacts.read"}, refreshBody.Oauth.Scopes)
	require.NotNil(t, refreshBody.Oauth.Account)
	assert.Equal(t, "acct-1", *refreshBody.Oauth.Account.Id)
	assert.Equal(t, "Acme CRM", *refreshBody.Oauth.Account.Name)

	revokeResp := authenticatedRequest(t, app, store, slug.Slug("alice"), jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme/data-sources/crm/provider-grant/revoke",
		nil,
	))
	defer func() { _ = revokeResp.Body.Close() }()
	require.Equal(t, http.StatusOK, revokeResp.StatusCode)
	revokeBody := parseResponse[api.CredentialMetadata](t, revokeResp)
	assert.Equal(t, api.CredentialStatus("revoked"), revokeBody.Status)
	require.NotNil(t, revokeBody.Oauth)
	assert.NotNil(t, revokeBody.Oauth.RevokedAt)

	rotateResp := authenticatedRequest(t, app, store, slug.Slug("alice"), jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme/data-sources/crm/provider-grant/rotate",
		nil,
	))
	defer func() { _ = rotateResp.Body.Close() }()
	require.Equal(t, http.StatusCreated, rotateResp.StatusCode)
	rotateBody := parseResponse[api.ProviderConnectorInstall](t, rotateResp)
	assert.Equal(t, api.Slug("oauth-install-2"), rotateBody.Slug)
	assert.Equal(t, api.ProviderInstallIntent("rotate_grant"), rotateBody.Intent)

	wantOwner := credentialssvc.CredentialOwner{
		OrgSlug:        slug.Slug("acme"),
		DataSourceSlug: slug.Slug("crm"),
	}
	assert.Equal(t, wantOwner, refreshed.Owner)
	assert.Equal(t, wantOwner, revoked.Owner)
	assert.Equal(t, wantOwner, rotated.Owner)
	assert.Equal(t, model.EventActorTypeUser, refreshed.Actor.Type)
	assert.Equal(t, model.EventActorTypeUser, revoked.Actor.Type)
	assert.Equal(t, model.EventActorTypeUser, rotated.Actor.Type)
	assert.True(t, rotated.IncludeWriteScopes)
	assert.Equal(t, []model.EventAction{
		model.EventActionRefresh,
		model.EventActionRevoke,
	}, recorded)
}

func TestStartBigQueryDataSourceInstallCreatesPendingSourceWithoutCredentials(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	var got *datasourcessvc.StartProviderDataSourceInstallParams
	checkedEntitlement := false
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			if input.Resource == model.ResourceDataSources && input.Action == model.Update {
				return false, nil
			}
			return true, nil
		}},
		admissionService: &testfakes.AdmissionService{
			CheckDataSourceAdmissionFn: func(_ context.Context, orgSlug slug.Slug) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				checkedEntitlement = true
				return nil
			},
		},
		datasourcesService: &fakeDataSourcesService{
			StartProviderDataSourceInstallFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				actor model.EventActor,
				provider string,
				params *datasourcessvc.StartProviderDataSourceInstallParams,
			) (*datasourcessvc.ProviderDataSourceInstall, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assertUserEventActor(t, actor, must.Slug("alice"))
				assert.Equal(t, "bigquery", provider)
				got = params
				credential := credentialssvc.CredentialMetadata{
					Slug:      must.Slug("oauth-credential-crm"),
					Kind:      credentialssvc.CredentialKindOAuth2Grant,
					Status:    credentialssvc.CredentialStatusPending,
					UpdatedAt: now,
					OAuth:     credentialssvc.CredentialOAuthMetadata{Provider: "bigquery"},
				}
				return &datasourcessvc.ProviderDataSourceInstall{
					DataSource: &datasourcessvc.DataSource{
						Slug:       must.Slug("crm"),
						Name:       "CRM",
						Type:       model.DataSourceTypeBigQuery,
						Config:     []byte(`{"project_id":"fixture","default_dataset":"crm"}`),
						Credential: &credential,
						ReadOnly:   false,
						CreatedAt:  now,
						UpdatedAt:  now,
					},
					Install: credentialssvc.ProviderConnectorInstall{
						Slug:             must.Slug("oauth-install-crm"),
						Provider:         "bigquery",
						AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth?state=opaque",
						ExpiresAt:        now.Add(10 * time.Minute),
						Intent:           credentialssvc.OAuthInstallIntentCreate,
						RequestedScopes:  []string{"openid", "email", "https://www.googleapis.com/auth/bigquery"},
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/bigquery/data-source-installs", map[string]any{
		"name":      "CRM",
		"slug":      "crm",
		"type":      "bigquery",
		"config":    map[string]any{"project_id": "fixture", "default_dataset": "crm"},
		"read_only": false,
	})
	resp := authenticatedRequest(t, app, store, must.Slug("alice"), req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.ProviderDataSourceInstall](t, resp)
	assert.Equal(t, api.Slug("crm"), body.DataSource.Slug)
	assert.False(t, body.DataSource.IsActive)
	assert.False(t, body.DataSource.ReadOnly)
	require.NotNil(t, body.DataSource.Credential)
	assert.Equal(t, api.Oauth2Grant, body.DataSource.Credential.Kind)
	assert.Equal(t, api.CredentialStatusPending, body.DataSource.Credential.Status)
	require.NotNil(t, body.DataSource.Credential.Oauth)
	assert.Equal(t, []string{}, body.DataSource.Credential.Oauth.Scopes)
	assert.Equal(t, "https://accounts.google.com/o/oauth2/v2/auth?state=opaque", body.Install.AuthorizationUrl)
	require.NotNil(t, got)
	assert.Equal(t, "CRM", got.Name)
	assert.Equal(t, must.Slug("crm"), got.Slug)
	assert.Equal(t, model.DataSourceTypeBigQuery, got.Type)
	assert.JSONEq(t, `{"project_id":"fixture","default_dataset":"crm"}`, string(got.ConfigJSON))
	require.NotNil(t, got.ReadOnly)
	assert.False(t, *got.ReadOnly)
	assert.True(t, checkedEntitlement)
}

func TestWorkspaceReconnectDefaultsToSelectedFilesAndRejectsInvalidMode(t *testing.T) {
	for _, mode := range []string{"", "invalid", "all_files", "selected_files"} {
		t.Run(mode, func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
				datasourcesService: &fakeDataSourcesService{FindBySlugFn: func(_ context.Context, org, ds slug.Slug) (*datasourcessvc.DataSource, error) {
					require.Equal(t, slug.Slug("acme"), org)
					return &datasourcessvc.DataSource{Slug: ds, Type: model.DataSourceTypeGoogleWorkspace, Config: json.RawMessage(`{"access_mode":"selected_files"}`), ReadOnly: true, Credential: &credentialssvc.CredentialMetadata{Kind: credentialssvc.CredentialKindOAuth2Grant, Status: credentialssvc.CredentialStatusActive}}, nil
				}},
				credentialsService: &fakeCredentialsService{StartOAuthInstallFn: func(_ context.Context, p credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
					called = true
					require.Equal(t, "selected_files", p.AccessMode)
					require.False(t, p.IncludeWriteScopes)
					return credentialssvc.ProviderConnectorInstall{Provider: "google_workspace", Intent: credentialssvc.OAuthInstallIntentRotate}, nil
				}},
			})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/provider-connectors/google_workspace/installs", map[string]any{"data_source_slug": "sheet", "intent": "rotate_grant", "access_mode": mode})
			resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
			defer func() { _ = resp.Body.Close() }()
			if mode == "" || mode == "selected_files" {
				require.Equal(t, http.StatusCreated, resp.StatusCode)
				require.True(t, called)
			} else {
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				require.False(t, called)
			}
		})
	}
}

func TestWorkspaceDiscoveryUsesTenantReadPermissionAndSafeMetadata(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Read, input.Action)
					return allowed, nil
				}},
				datasourcesService: &fakeDataSourcesService{ListGoogleWorkspaceFilesFn: func(_ context.Context, org, ds slug.Slug, page workspace.ListOptions) (*workspace.Discovery, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("sheet"), ds)
					require.Equal(t, "next-page", page.PageToken)
					return &workspace.Discovery{Files: []workspace.File{{FileID: "sheet-1", Name: "Synthetic sheet", MIMEType: "application/vnd.google-apps.spreadsheet"}}, NextPageToken: "more"}, nil
				}},
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/data-sources/sheet/google-workspace/files?page_token=next-page", nil)
			resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
			defer func() { _ = resp.Body.Close() }()
			if !allowed {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				require.False(t, called)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			payload := parseResponse[api.GoogleWorkspaceFileDiscovery](t, resp)
			require.Equal(t, "sheet-1", payload.Files[0].FileId)
			require.Equal(t, "more", *payload.NextPageToken)
		})
	}
}

func TestWorkspaceSelectionEnforcesTenantPermission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					return allowed, nil
				}},
				datasourcesService: &fakeDataSourcesService{VerifyGoogleWorkspaceFilesFn: func(_ context.Context, org, source slug.Slug, _ model.EventActor, params datasourcessvc.WorkspaceVerificationParams) (*workspace.Discovery, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("sheet"), source)
					require.Equal(t, int64(7), params.ExpectedRevision)
					return &workspace.Discovery{Files: []workspace.File{{FileID: "authorized", Name: "Fixture", MIMEType: "application/json"}}}, nil
				}},
			})
			response := authenticatedRequest(t, app, store, slug.Slug("alice"), jsonRequest(t, http.MethodPost, "/organizations/acme/data-sources/sheet/google-workspace/files/verify", map[string]any{"file_ids": []string{"authorized"}, "expected_revision": 7}))
			defer func() { _ = response.Body.Close() }()
			if allowed {
				require.Equal(t, 200, response.StatusCode)
			} else {
				require.Equal(t, 403, response.StatusCode)
			}
			require.Equal(t, allowed, called)
		})
	}
}

func TestPickerConfigurationUsesTenantUpdatePermission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.NotNil(t, input.OrgSlug)
				require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
				require.Equal(t, model.Update, input.Action)
				require.Equal(t, model.ResourceDataSources, input.Resource)
				return allowed, nil
			}}, datasourcesService: &fakeDataSourcesService{}})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/organizations/acme/data-sources/workspace/google-workspace/picker", nil)
			resp := authenticatedRequest(t, app, store, slug.Slug("alice"), req)
			defer func() { _ = resp.Body.Close() }()
			if !allowed {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
			body := parseResponse[api.GoogleWorkspacePicker](t, resp)
			require.Equal(t, "picker-client", body.ClientId)
			require.Equal(t, "test@example.com", body.AccountEmail)
			require.EqualValues(t, 7, body.ExpectedRevision)
		})
	}
}

func TestConnectDataSourceHTTPOAuthRequiresUpdateAndReturnsSafeInstall(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			t.Parallel()
			calls := 0
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Update, input.Action)
					require.Equal(t, model.ResourceDataSources, input.Resource)
					return allowed, nil
				}},
				datasourcesService: &fakeDataSourcesService{ConnectHTTPOAuthFn: func(_ context.Context, org, source slug.Slug, actor model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error) {
					calls++
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("http-app"), source)
					assertUserEventActor(t, actor, "alice")
					return &datasourcessvc.HTTPOAuthConnection{DataSource: &datasourcessvc.DataSource{Slug: source, Name: "HTTP app", Type: model.DataSourceTypeHTTP, Config: []byte(`{"base_url":"https://api.example/v1"}`)}, Install: &credentialssvc.ProviderConnectorInstall{Provider: "http", AuthorizationURL: "https://issuer.example/authorize?state=opaque"}}, nil
				}},
			})
			resp := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodPost, "/organizations/acme/data-sources/http-app/http-oauth/connect", nil))
			defer func() { _ = resp.Body.Close() }()
			if !allowed {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				require.Zero(t, calls)
				return
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, 1, calls)
			var result api.HTTPOAuthConnection
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
			require.NotNil(t, result.Install)
			require.Equal(t, "http", result.Install.Provider)
			require.Equal(t, "http-app", result.DataSource.Slug)
		})
	}
}

func TestHTTPOAuthPermissionsRequireUpdateForInitialAndRotatedGrants(t *testing.T) {
	t.Parallel()
	owner := credentialssvc.CredentialOwner{OrgSlug: "acme", DataSourceSlug: "http-app"}
	for _, intent := range []credentialssvc.OAuthInstallIntent{credentialssvc.OAuthInstallIntentCreate, credentialssvc.OAuthInstallIntentRotate} {
		require.Equal(t, model.Update, oauthInstallAction("http", intent))
		action, resource := oauthCallbackPermission("http", owner, intent)
		require.Equal(t, model.Update, action)
		require.Equal(t, model.ResourceDataSources, resource)
	}
	require.Equal(t, model.Create, oauthInstallAction("hubspot", credentialssvc.OAuthInstallIntentCreate))
}
