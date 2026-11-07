package credentialssvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
)

func TestServiceCreateStaticSecretStoresEncryptedPayloadAndSafeMetadata(t *testing.T) {
	t.Parallel()

	repo := newCredentialRepo()
	svc := newTestService(t, repo, nil)

	got, err := svc.CreateStaticSecret(context.Background(), CreateStaticSecretParams{
		Owner:          testOwner(),
		CredentialType: StaticCredentialTypePostgres,
		PlaintextJSON:  []byte(`{"password":"super-secret-password"}`),
	})
	require.NoError(t, err)

	require.Len(t, repo.staticSecrets, 1)
	stored := repo.staticSecrets[0]
	assert.Equal(t, must.Slug("acme"), stored.OrgSlug)
	assert.Equal(t, must.Slug("warehouse"), stored.DataSourceSlug)
	assert.Equal(t, "static_secret", stored.Kind)
	assert.Equal(t, "postgres", stored.CredentialType)
	assert.NotContains(t, string(stored.EncryptedPayload), "super-secret-password")
	assert.Equal(t, CredentialKindStaticSecret, got.Kind)
	assert.Equal(t, CredentialStatusActive, got.Status)
}

func TestCreateOrganizationGitLabConnectionCreatesOnlyPendingSafeMetadata(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	svc := newTestService(t, repo, ProviderRegistry{"gitlab": &fakeProviderAdapter{}})
	connection, err := svc.CreateOrganizationGitLabConnection(t.Context(), CreateOrganizationGitLabConnectionParams{OrganizationSlug: must.Slug("acme"), Provider: "gitlab"})
	require.NoError(t, err)
	require.Len(t, repo.pendingOAuthCredentials, 1)
	assert.Equal(t, repocredentials.OwnerKindOrganizationGitLab, repo.pendingOAuthCredentials[0].OwnerKind)
	assert.Empty(t, repo.pendingOAuthCredentials[0].EncryptedPayload)
	assert.Equal(t, must.Slug("acme"), connection.OrganizationSlug)
	assert.Equal(t, CredentialStatusPending, connection.Credential.Status)
	assert.Equal(t, "gitlab", connection.Credential.OAuth.Provider)
}

func TestServiceIssuePipelineWebhookSecretReturnsExactASCIIOnceAndStoresOnlyEncryptedBytes(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	var encryptedPlaintext []byte
	enc := testutil.NewFakeEncryptor()
	enc.EncryptFn = func(plaintext []byte) ([]byte, error) {
		encryptedPlaintext = append([]byte(nil), plaintext...)
		return []byte("stored-ciphertext"), nil
	}
	svc, err := New(testutil.FakeTransactionManager{}, repo, enc, nil, testutil.FakeClock{}, false)
	require.NoError(t, err)
	issued, err := svc.IssuePipelineWebhookSecret(context.Background(), PipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"), TriggerSlug: must.Slug("incoming"),
	})
	require.NoError(t, err)
	require.Equal(t, 1, issued.KeyVersion)
	require.Len(t, issued.Value, 43)
	require.Equal(t, issued.Value, string(encryptedPlaintext))
	require.Len(t, repo.staticSecrets, 1)
	require.NotContains(t, string(repo.staticSecrets[0].EncryptedPayload), issued.Value)
	require.Equal(t, []byte("stored-ciphertext"), repo.staticSecrets[0].EncryptedPayload)
}

func TestServiceResolvePipelineWebhookSecretUsesExactOwnerAndKeyVersion(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	repo.resolvedWebhookCredential = repocredentials.CredentialRecord{EncryptedPayload: []byte("ciphertext")}
	issuedSecret := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq")
	require.Len(t, issuedSecret, 43)
	enc := testutil.NewFakeEncryptor()
	enc.DecryptFn = func(ciphertext []byte) ([]byte, error) {
		require.Equal(t, []byte("ciphertext"), ciphertext)
		return issuedSecret, nil
	}
	svc, err := New(testutil.FakeTransactionManager{}, repo, enc, nil, testutil.FakeClock{}, false)
	require.NoError(t, err)
	params := ResolvePipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"),
		TriggerSlug: must.Slug("incoming"), KeyVersion: 7,
	}

	secret, err := svc.ResolvePipelineWebhookSecret(t.Context(), params)

	require.NoError(t, err)
	require.Equal(t, issuedSecret, secret)
	require.Equal(t, &repocredentials.PipelineWebhookOwner{
		OrgSlug: params.OrgSlug, ProjectSlug: params.ProjectSlug,
		EnvironmentSlug: params.EnvironmentSlug, TriggerSlug: params.TriggerSlug,
	}, repo.resolvedWebhookOwner)
	require.Equal(t, params.KeyVersion, repo.resolvedWebhookKeyVersion)
}

func TestServiceResolvePipelineWebhookSecretRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	valid := ResolvePipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"),
		TriggerSlug: must.Slug("incoming"), KeyVersion: 1,
	}
	tests := map[string]ResolvePipelineWebhookSecretParams{
		"missing owner scope": {KeyVersion: 1},
		"non-positive key version": func() ResolvePipelineWebhookSecretParams {
			params := valid
			params.KeyVersion = 0
			return params
		}(),
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := newTestService(t, newCredentialRepo(), nil)
			_, err := svc.ResolvePipelineWebhookSecret(t.Context(), params)
			require.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

func TestServiceResolvePipelineWebhookSecretPropagatesRepositoryAndDecryptErrors(t *testing.T) {
	t.Parallel()
	params := ResolvePipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"),
		TriggerSlug: must.Slug("incoming"), KeyVersion: 2,
	}

	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		repo := newCredentialRepo()
		repo.resolveWebhookErr = xerror.Unexpected().Detail("resolve webhook credential")
		svc := newTestService(t, repo, nil)
		_, err := svc.ResolvePipelineWebhookSecret(t.Context(), params)
		require.ErrorIs(t, err, repo.resolveWebhookErr)
	})

	t.Run("decrypt", func(t *testing.T) {
		t.Parallel()
		repo := newCredentialRepo()
		repo.resolvedWebhookCredential = repocredentials.CredentialRecord{EncryptedPayload: []byte("ciphertext")}
		decryptErr := xerror.Unexpected().Detail("decrypt webhook credential")
		enc := testutil.NewFakeEncryptor()
		enc.DecryptFn = func([]byte) ([]byte, error) { return nil, decryptErr }
		svc, err := New(testutil.FakeTransactionManager{}, repo, enc, nil, testutil.FakeClock{}, false)
		require.NoError(t, err)
		_, err = svc.ResolvePipelineWebhookSecret(t.Context(), params)
		require.ErrorIs(t, err, decryptErr)
	})
}

func TestServiceRevokePipelineWebhookSecretsUsesCompleteOwnerScope(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	svc := newTestService(t, repo, nil)
	params := PipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"), TriggerSlug: must.Slug("incoming"),
	}

	err := svc.RevokePipelineWebhookSecrets(context.Background(), params)

	require.NoError(t, err)
	require.Equal(t, &repocredentials.PipelineWebhookOwner{
		OrgSlug: params.OrgSlug, ProjectSlug: params.ProjectSlug, EnvironmentSlug: params.EnvironmentSlug, TriggerSlug: params.TriggerSlug,
	}, repo.lockWebhookOwner)
	require.Equal(t, repo.lockWebhookOwner, repo.revokedWebhookOwner)
	require.Equal(t, []string{"lock", "revoke"}, repo.webhookCalls)
}

func TestServiceRevokePipelineWebhookSecretsStopsWhenOwnerLockFails(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	repo.lockWebhookErr = xerror.Unexpected().Detail("lock webhook owner")
	svc := newTestService(t, repo, nil)

	err := svc.RevokePipelineWebhookSecrets(context.Background(), PipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"), TriggerSlug: must.Slug("incoming"),
	})

	require.ErrorIs(t, err, repo.lockWebhookErr)
	require.Nil(t, repo.revokedWebhookOwner)
}

func TestServiceRevokePipelineWebhookSecretsPropagatesRepositoryError(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	repo.revokeWebhookErr = xerror.Unexpected().Detail("revoke webhook credentials")
	svc := newTestService(t, repo, nil)

	err := svc.RevokePipelineWebhookSecrets(context.Background(), PipelineWebhookSecretParams{
		OrgSlug: must.Slug("acme"), ProjectSlug: must.Slug("ops"), EnvironmentSlug: must.Slug("prod"), TriggerSlug: must.Slug("incoming"),
	})

	require.ErrorIs(t, err, repo.revokeWebhookErr)
}

// TestServiceCreatePendingOAuthCredentialStoresNoPayload locks down the OAuth-first bootstrap boundary.
func TestServiceCreatePendingOAuthCredentialStoresNoPayload(t *testing.T) {
	t.Parallel()

	repo := newCredentialRepo()
	svc := newTestService(t, repo, ProviderRegistry{"hubspot": &fakeProviderAdapter{}})

	got, err := svc.CreatePendingOAuthCredential(context.Background(), CreatePendingOAuthCredentialParams{
		Owner:    testOwner(),
		Provider: "hubspot",
	})
	require.NoError(t, err)

	assert.Equal(t, CredentialKindOAuth2Grant, got.Kind)
	assert.Equal(t, CredentialStatusPending, got.Status)
	assert.Equal(t, "hubspot", got.OAuth.Provider)
	require.Len(t, repo.pendingOAuthCredentials, 1)
	stored := repo.pendingOAuthCredentials[0]
	assert.Equal(t, "oauth2_grant", stored.Kind)
	assert.Equal(t, "pending", stored.Status)
	assert.Empty(t, stored.CredentialType)
	assert.Empty(t, stored.EncryptedPayload)
}

// TestServiceFindOAuthInstallPreservesOwnerForExpiredState keeps callback ownership available for safe expiry redirects.
func TestServiceFindOAuthInstallPreservesOwnerForExpiredState(t *testing.T) {
	t.Parallel()

	repo := newCredentialRepo()
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	repo.installs = append(repo.installs, repocredentials.CredentialInstall{
		Slug:           must.Slug("oauth-install-warehouse"),
		OrgSlug:        must.Slug("acme"),
		OwnerKind:      repocredentials.OwnerKindDataSource,
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "hubspot",
		StateHash:      oauthflow.HashState("expired-state"),
		ExpiresAt:      now.Add(-time.Minute),
	})
	svc := newTestServiceAt(t, repo, ProviderRegistry{"hubspot": &fakeProviderAdapter{}}, now)

	install, err := svc.FindOAuthInstall(context.Background(), FindOAuthInstallParams{
		Provider: "hubspot",
		State:    "expired-state",
	})

	require.ErrorIs(t, err, ErrOAuthStateExpired)
	assert.Equal(t, testOwner(), install.Owner)
}

// TestServiceConsumeOAuthInstallPreventsLaterGrantExchange proves a denied install cannot later exchange a grant.
func TestServiceConsumeOAuthInstallPreventsLaterGrantExchange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:           must.Slug("install-1"),
		OrgSlug:        must.Slug("acme"),
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "fakecrm",
		StateHash:      oauthflow.HashState("denied-state"),
		Intent:         "create_grant",
		ExpiresAt:      now.Add(time.Minute),
	})
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": &fakeProviderAdapter{}}, now)

	install, err := svc.ConsumeOAuthInstall(context.Background(), ConsumeOAuthInstallParams{
		Provider: "fakecrm",
		State:    "denied-state",
	})
	require.NoError(t, err)
	assert.Equal(t, testOwner(), install.Owner)

	_, err = svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "denied-state",
		Code:     "replayed-code",
	})
	require.ErrorIs(t, err, ErrOAuthStateConsumed)
	assert.Empty(t, repo.grants)
}

func TestServiceCreateStaticEnvironmentSecretRotatesPreviousCredential(t *testing.T) {
	t.Parallel()

	repo := newCredentialRepo()
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	owner := testEnvironmentSecretOwner()
	repo.staticSecrets = append(repo.staticSecrets, repocredentials.CredentialRecord{
		Slug:                 must.Slug("cred-prod-api-token-old"),
		OrgSlug:              owner.OrgSlug,
		OwnerKind:            owner.Kind.String(),
		EnvironmentSlug:      owner.EnvironmentSlug,
		EnvironmentSecretKey: owner.EnvironmentSecretKey,
		Kind:                 CredentialKindStaticSecret.String(),
		CredentialType:       StaticCredentialTypeEnvironmentSecret.String(),
		Status:               CredentialStatusActive.String(),
		EncryptedPayload:     []byte("encrypted-old"),
		CreatedAt:            now.Add(-time.Hour),
		UpdatedAt:            now.Add(-time.Hour),
	})
	svc := newTestServiceAt(t, repo, nil, now)

	got, err := svc.CreateStaticSecret(context.Background(), CreateStaticSecretParams{
		Owner:          owner,
		CredentialType: StaticCredentialTypeEnvironmentSecret,
		PlaintextJSON:  []byte(`{"value":"new"}`),
	})

	require.NoError(t, err)
	require.NotEmpty(t, got.Slug)
	require.Len(t, repo.staticSecrets, 2)
	require.Equal(t, CredentialStatusRotated.String(), repo.staticSecrets[0].Status)
	require.Empty(t, repo.staticSecrets[0].EncryptedPayload)
	require.NotNil(t, repo.staticSecrets[0].RotatedAt)
	assert.Equal(t, CredentialStatusActive.String(), repo.staticSecrets[1].Status)
	assert.NotEmpty(t, repo.staticSecrets[1].EncryptedPayload)
}

func TestServiceRevokeEnvironmentSecretTombstonesStaticCredential(t *testing.T) {
	t.Parallel()

	repo := newCredentialRepo()
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	owner := testEnvironmentSecretOwner()
	repo.staticSecrets = append(repo.staticSecrets, repocredentials.CredentialRecord{
		Slug:                 must.Slug("cred-prod-api-token"),
		OrgSlug:              owner.OrgSlug,
		OwnerKind:            owner.Kind.String(),
		EnvironmentSlug:      owner.EnvironmentSlug,
		EnvironmentSecretKey: owner.EnvironmentSecretKey,
		Kind:                 CredentialKindStaticSecret.String(),
		CredentialType:       StaticCredentialTypeEnvironmentSecret.String(),
		Status:               CredentialStatusActive.String(),
		EncryptedPayload:     []byte("encrypted-secret"),
		CreatedAt:            now.Add(-time.Hour),
		UpdatedAt:            now.Add(-time.Hour),
	})
	svc := newTestServiceAt(t, repo, nil, now)

	got, err := svc.Revoke(context.Background(), RevokeParams{Owner: owner})

	require.NoError(t, err)
	require.Equal(t, CredentialStatusRevoked, got.Status)
	require.Len(t, repo.staticSecrets, 1)
	require.Equal(t, CredentialStatusRevoked.String(), repo.staticSecrets[0].Status)
	require.Empty(t, repo.staticSecrets[0].EncryptedPayload)
	require.NotNil(t, repo.staticSecrets[0].RevokedAt)
}

func TestServiceStartOAuthInstallStoresOnlyStateHash(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	provider := &fakeProviderAdapter{authURL: "https://fakecrm.example/oauth"}
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

	got, err := svc.StartOAuthInstall(context.Background(), StartOAuthInstallParams{
		Owner:           testOwner(),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          OAuthInstallIntentCreate,
	})
	require.NoError(t, err)

	require.Len(t, repo.installs, 1)
	stored := repo.installs[0]
	assert.Equal(t, "fakecrm", stored.Provider)
	assert.NotEmpty(t, got.State)
	assert.NotContains(t, stored.StateHash, got.State)
	assert.Equal(t, oauthflow.HashState(got.State), stored.StateHash)
	assert.Equal(t, now.Add(DefaultInstallTTL), stored.ExpiresAt)
}

func TestRepositorySetupConsentStoresExactContextAndRequiresStartingUser(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	provider := &fakeProviderAdapter{authURL: "https://gitlab.example/oauth", exchange: OAuthTokenSet{AccessToken: "synthetic-token", Scopes: []string{"api"}}}
	svc := newTestService(t, repo, ProviderRegistry{"gitlab": provider})
	params := StartOAuthInstallParams{
		Owner:    CredentialOwner{Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: "acme"},
		Provider: "gitlab", Intent: OAuthInstallIntentCreate, IncludeWriteScopes: true,
		Actor: model.UserEventActor("alice"), RepositorySetupSlug: "setup-one", RepositorySetupProjectSlug: "project-one",
	}
	started, err := svc.StartOAuthInstall(t.Context(), params)
	require.NoError(t, err)
	found, err := svc.FindOAuthInstall(t.Context(), FindOAuthInstallParams{Provider: "gitlab", State: started.State})
	require.NoError(t, err)
	require.Equal(t, params.RepositorySetupSlug, found.RepositorySetupSlug)
	require.Equal(t, params.RepositorySetupProjectSlug, found.RepositorySetupProjectSlug)
	require.Equal(t, params.Actor, found.Actor)
	require.Equal(t, oauthflow.HashState(started.State), repo.installs[0].StateHash)
	for _, actor := range []model.EventActor{model.UserEventActor("bob"), {}} {
		_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "gitlab", State: started.State, Code: "synthetic-code", Actor: actor})
		require.ErrorIs(t, err, ErrOAuthStateInvalid)
	}
	require.Zero(t, provider.exchangeCalls)
	require.Nil(t, repo.installs[0].ConsumedAt)
	_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "gitlab", State: started.State, Code: "synthetic-code", Actor: params.Actor})
	require.NoError(t, err)
	require.Equal(t, 1, provider.exchangeCalls)
}

func TestRepositorySetupConsentRejectsIncompleteOrUnprivilegedContext(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"missing-setup", "missing-project", "different-provider", "source-owner", "no-user", "read-only"} {
		t.Run(invalid, func(t *testing.T) {
			repo := newCredentialRepo()
			svc := newTestService(t, repo, ProviderRegistry{"gitlab": &fakeProviderAdapter{}})
			params := StartOAuthInstallParams{Owner: CredentialOwner{Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: "acme"}, Provider: "gitlab", Intent: OAuthInstallIntentCreate, IncludeWriteScopes: true, Actor: model.UserEventActor("alice"), RepositorySetupSlug: "setup-one", RepositorySetupProjectSlug: "project-one"}
			switch invalid {
			case "missing-setup":
				params.RepositorySetupSlug = ""
			case "missing-project":
				params.RepositorySetupProjectSlug = ""
			case "different-provider":
				params.Provider = "hubspot"
			case "source-owner":
				params.Owner = testOwner()
			case "no-user":
				params.Actor = model.EventActor{}
			case "read-only":
				params.IncludeWriteScopes = false
			}
			_, err := svc.StartOAuthInstall(t.Context(), params)
			require.ErrorIs(t, err, ErrInvalidInput)
			require.Empty(t, repo.installs)
		})
	}
}

func TestRepositorySetupConsentRechecksStartingUserAfterExchange(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	provider := &fakeProviderAdapter{authURL: "https://gitlab.example/oauth", exchange: OAuthTokenSet{AccessToken: "synthetic-token", Scopes: []string{"api"}}}
	svc := newTestService(t, repo, ProviderRegistry{"gitlab": provider})
	actor := model.UserEventActor("alice")
	started, err := svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{
		Owner:    CredentialOwner{Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: "acme"},
		Provider: "gitlab", Intent: OAuthInstallIntentCreate, IncludeWriteScopes: true,
		Actor: actor, RepositorySetupSlug: "setup-one", RepositorySetupProjectSlug: "project-one",
	})
	require.NoError(t, err)
	provider.exchangeHook = func() {
		other := slug.Slug("bob")
		repo.installs[0].ActorSlug = &other
	}
	_, err = svc.CompleteOAuthCallback(t.Context(), CompleteOAuthCallbackParams{Provider: "gitlab", State: started.State, Code: "synthetic-code", Actor: actor})
	require.ErrorIs(t, err, ErrOAuthStateInvalid)
	require.Equal(t, 1, provider.exchangeCalls)
	require.Empty(t, repo.grants)
}

// TestServiceStartOAuthInstallUsesProviderOwnedScopePlan verifies providers, not callers, select OAuth scopes.
func TestServiceStartOAuthInstallUsesProviderOwnedScopePlan(t *testing.T) {
	t.Parallel()

	provider := &plannedProviderAdapter{
		fakeProviderAdapter: &fakeProviderAdapter{authURL: "https://fakecrm.example/oauth"},
		plan:                OAuthScopePlan{Required: []string{"contacts.read"}, Optional: []string{"companies.read"}},
	}
	svc := newTestService(t, newCredentialRepo(), ProviderRegistry{"hubspot": provider})

	install, err := svc.StartOAuthInstall(context.Background(), StartOAuthInstallParams{
		Owner:           testOwner(),
		Provider:        "hubspot",
		RequestedScopes: []string{"admin.escalation"},
		Intent:          OAuthInstallIntentCreate,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"contacts.read", "companies.read"}, install.RequestedScopes)
	assert.Equal(t, []string{"contacts.read"}, provider.authorization.Scopes)
	assert.Equal(t, []string{"companies.read"}, provider.authorization.OptionalScopes)
}

func TestServiceStartOAuthInstallPassesWriteIntentAndFileAccessModeToProviderScopePlan(t *testing.T) {
	t.Parallel()

	provider := &contextualPlannedProviderAdapter{
		plannedProviderAdapter: &plannedProviderAdapter{
			fakeProviderAdapter: &fakeProviderAdapter{authURL: "https://fakecrm.example/oauth"},
			plan:                OAuthScopePlan{Required: []string{"contacts.read"}},
		},
	}
	svc := newTestService(t, newCredentialRepo(), ProviderRegistry{"google_workspace": provider})

	_, err := svc.StartOAuthInstall(context.Background(), StartOAuthInstallParams{
		Owner:              testOwner(),
		Provider:           "google_workspace",
		IncludeWriteScopes: true,
		AccessMode:         "selected_files",
		Intent:             OAuthInstallIntentCreate,
	})
	require.NoError(t, err)
	assert.True(t, provider.params.IncludeWriteScopes)
	assert.Equal(t, "selected_files", provider.params.AccessMode)
	assert.Equal(t, []string{"contacts.read", "contacts.write"}, provider.authorization.OptionalScopes)
}

func TestServiceCompleteOAuthCallbackConsumesStateAndStoresGrant(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:            must.Slug("install-1"),
		OrgSlug:         must.Slug("acme"),
		DataSourceSlug:  must.Slug("warehouse"),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		StateHash:       oauthflow.HashState("raw-state"),
		Intent:          "create_grant",
		ExpiresAt:       now.Add(time.Minute),
	})
	provider := &fakeProviderAdapter{
		exchange: OAuthTokenSet{
			AccessToken:  "access-secret",
			RefreshToken: "refresh-secret",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

	got, err := svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "raw-state",
		Code:     "code",
	})
	require.NoError(t, err)

	assert.Equal(t, must.Slug("install-1"), got.InstallSlug)
	assert.Equal(t, testOwner(), got.Owner)
	assert.Equal(t, CredentialStatusActive, got.Credential.Status)
	assert.Equal(t, []string{"contacts.read"}, provider.exchangeParams.RequestedScopes)
	require.Len(t, repo.grants, 1)
	assert.NotContains(t, string(repo.grants[0].EncryptedTokenSet), "access-secret")
	assert.NotContains(t, string(repo.grants[0].EncryptedTokenSet), "refresh-secret")

	_, err = svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "raw-state",
		Code:     "code",
	})
	require.ErrorIs(t, err, ErrOAuthStateConsumed)
}

func TestServiceCompleteOAuthCallbackExchangesCodeOutsideTransaction(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:           must.Slug("install-outside-tx"),
		OrgSlug:        must.Slug("acme"),
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "fakecrm",
		StateHash:      oauthflow.HashState("outside-tx-state"),
		Intent:         "create_grant",
		ExpiresAt:      now.Add(time.Minute),
	})
	txm := &trackingCredentialTransactionManager{}
	provider := &fakeProviderAdapter{
		exchange: OAuthTokenSet{
			AccessToken: "access-secret",
			ExpiresAt:   now.Add(time.Hour),
		},
		exchangeHook: func() {
			require.False(t, txm.inTx, "provider exchange must run outside database transactions")
		},
	}
	svc, err := New(
		txm,
		repo,
		testutil.NewFakeEncryptor(),
		ProviderRegistry{"fakecrm": provider},
		testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
	require.NoError(t, err)

	_, err = svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "outside-tx-state",
		Code:     "provider-code",
	})

	require.NoError(t, err)
	assert.Equal(t, 2, txm.transactions)
	assert.Equal(t, 1, provider.exchangeCalls)
}

func TestServiceCompleteOAuthCallbackRevalidatesIntentAfterExchange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:           must.Slug("install-revalidate"),
		OrgSlug:        must.Slug("acme"),
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "fakecrm",
		StateHash:      oauthflow.HashState("revalidate-state"),
		Intent:         OAuthInstallIntentCreate.String(),
		ExpiresAt:      now.Add(time.Minute),
	})
	provider := &fakeProviderAdapter{
		exchange: OAuthTokenSet{
			AccessToken: "access-secret",
			ExpiresAt:   now.Add(time.Hour),
		},
		exchangeHook: func() {
			repo.oauthGrantIntentState.Status = CredentialStatusActive.String()
		},
	}
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

	_, err := svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "revalidate-state",
		Code:     "provider-code",
	})

	require.ErrorIs(t, err, ErrOAuthIntentConflict)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Empty(t, repo.grants)
}

func TestServiceCompleteOAuthCallbackRejectsInstallExpiredDuringExchange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	currentTime := now
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:           must.Slug("install-expires-during-exchange"),
		OrgSlug:        must.Slug("acme"),
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "fakecrm",
		StateHash:      oauthflow.HashState("expires-during-exchange-state"),
		Intent:         OAuthInstallIntentCreate.String(),
		ExpiresAt:      now.Add(time.Minute),
	})
	provider := &fakeProviderAdapter{
		exchange: OAuthTokenSet{
			AccessToken: "access-secret",
			ExpiresAt:   now.Add(time.Hour),
		},
		exchangeHook: func() {
			currentTime = now.Add(2 * time.Minute)
		},
	}
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeEncryptor(),
		ProviderRegistry{"fakecrm": provider},
		testutil.FakeClock{NowFn: func() time.Time { return currentTime }}, false)
	require.NoError(t, err)

	_, err = svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "expires-during-exchange-state",
		Code:     "provider-code",
	})

	require.ErrorIs(t, err, ErrOAuthStateExpired)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Empty(t, repo.grants)
}

// TestServiceCompleteOAuthCallbackValidatesLockedGrantIntentBeforeExchange keeps stale create callbacks away from the provider.
func TestServiceCompleteOAuthCallbackValidatesLockedGrantIntentBeforeExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		intent        OAuthInstallIntent
		state         repocredentials.OAuthGrantIntentState
		expectedState *repocredentials.OAuthGrantIntentState
		lockError     error
		wantError     error
	}{
		{
			name:   "CreatePendingOAuthWithoutGrant",
			intent: OAuthInstallIntentCreate,
			state: repocredentials.OAuthGrantIntentState{
				Kind:   CredentialKindOAuth2Grant.String(),
				Status: CredentialStatusPending.String(),
			},
		},
		{
			name:   "RotateExistingCredential",
			intent: OAuthInstallIntentRotate,
			state: repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Kind:           CredentialKindStaticSecret.String(),
				Status:         CredentialStatusActive.String(),
			},
		},
		{
			name:   "RotateExistingOAuthCredential",
			intent: OAuthInstallIntentRotate,
			state: repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Kind:           CredentialKindOAuth2Grant.String(),
				Status:         CredentialStatusActive.String(),
				HasGrant:       true,
			},
		},
		{
			name:   "RotateReplacedCredential",
			intent: OAuthInstallIntentRotate,
			state: repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-b"),
				Kind:           CredentialKindOAuth2Grant.String(),
				Status:         CredentialStatusActive.String(),
				HasGrant:       true,
			},
			expectedState: &repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Status:         CredentialStatusActive.String(),
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:   "RotateOAuthCredentialWithoutGrant",
			intent: OAuthInstallIntentRotate,
			state: repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Kind:           CredentialKindOAuth2Grant.String(),
				Status:         CredentialStatusActive.String(),
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:   "RotateStaticCredentialWithGrant",
			intent: OAuthInstallIntentRotate,
			state: repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Kind:           CredentialKindStaticSecret.String(),
				Status:         CredentialStatusActive.String(),
				HasGrant:       true,
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:   "RotateCredentialStatusChanged",
			intent: OAuthInstallIntentRotate,
			state: repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Kind:           CredentialKindOAuth2Grant.String(),
				Status:         CredentialStatusRevoked.String(),
				HasGrant:       true,
			},
			expectedState: &repocredentials.OAuthGrantIntentState{
				CredentialSlug: must.Slug("credential-a"),
				Status:         CredentialStatusActive.String(),
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:   "CreatePendingOAuthWithGrant",
			intent: OAuthInstallIntentCreate,
			state: repocredentials.OAuthGrantIntentState{
				Kind:     CredentialKindOAuth2Grant.String(),
				Status:   CredentialStatusPending.String(),
				HasGrant: true,
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:   "CreateActiveOAuthWithoutGrant",
			intent: OAuthInstallIntentCreate,
			state: repocredentials.OAuthGrantIntentState{
				Kind:   CredentialKindOAuth2Grant.String(),
				Status: CredentialStatusActive.String(),
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:   "CreatePendingStaticCredential",
			intent: OAuthInstallIntentCreate,
			state: repocredentials.OAuthGrantIntentState{
				Kind:   CredentialKindStaticSecret.String(),
				Status: CredentialStatusPending.String(),
			},
			wantError: ErrOAuthIntentConflict,
		},
		{
			name:      "LockFailure",
			intent:    OAuthInstallIntentCreate,
			lockError: ErrCredentialNotFound,
			wantError: ErrCredentialNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
			repo := newCredentialRepo()
			repo.oauthGrantIntentState = tt.state
			repo.oauthGrantIntentErr = tt.lockError
			expectedState := tt.state
			if tt.expectedState != nil {
				expectedState = *tt.expectedState
			}
			if expectedState.Status == "" {
				expectedState.Status = CredentialStatusPending.String()
			}
			repo.seedInstall(repocredentials.CredentialInstall{
				Slug:                     must.Slug("intent-install"),
				OrgSlug:                  must.Slug("acme"),
				DataSourceSlug:           must.Slug("warehouse"),
				Provider:                 "fakecrm",
				StateHash:                oauthflow.HashState("intent-state"),
				Intent:                   tt.intent.String(),
				ExpectedCredentialSlug:   expectedState.CredentialSlug,
				ExpectedCredentialStatus: expectedState.Status,
				ExpiresAt:                now.Add(time.Minute),
			})
			provider := &fakeProviderAdapter{exchange: OAuthTokenSet{
				AccessToken:  "access-secret",
				RefreshToken: "refresh-secret",
				ExpiresAt:    now.Add(time.Hour),
				Scopes:       []string{"contacts.read"},
				Account:      ProviderAccount{ID: "acct-1"},
			}}
			svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

			_, err := svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
				Provider: "fakecrm",
				State:    "intent-state",
				Code:     "provider-code",
			})
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
				assert.Equal(t, 0, provider.exchangeCalls)
				assert.Empty(t, repo.grants)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, 1, provider.exchangeCalls)
			require.Len(t, repo.grants, 1)
		})
	}
}

// TestServiceCompleteOAuthCallbackPreservesTypedProviderError keeps callback remediation status from the provider.
func TestServiceCompleteOAuthCallbackPreservesTypedProviderError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:           must.Slug("install-provider-error"),
		OrgSlug:        must.Slug("acme"),
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "fakecrm",
		StateHash:      oauthflow.HashState("provider-error-state"),
		Intent:         "create_grant",
		ExpiresAt:      now.Add(time.Minute),
	})
	providerErr := xerror.InvalidCredentials().Detail("provider rejected client credentials")
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": &fakeProviderAdapter{exchangeErr: providerErr}}, now)

	_, err := svc.CompleteOAuthCallback(context.Background(), CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "provider-error-state",
		Code:     "code",
	})
	require.Error(t, err)
	assert.True(t, xerror.Is(err, providerErr))
	assert.False(t, xerror.Is(err, ErrProviderUnavailable))
}

func TestServiceCompleteOAuthCallbackFinalizesAfterCallerCancellation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.seedInstall(repocredentials.CredentialInstall{
		Slug:           must.Slug("install-canceled-after-exchange"),
		OrgSlug:        must.Slug("acme"),
		DataSourceSlug: must.Slug("warehouse"),
		Provider:       "fakecrm",
		StateHash:      oauthflow.HashState("canceled-after-exchange-state"),
		Intent:         OAuthInstallIntentCreate.String(),
		ExpiresAt:      now.Add(time.Minute),
	})
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProviderAdapter{
		exchangeHook: cancel,
		exchange: OAuthTokenSet{
			AccessToken:  "access-secret",
			RefreshToken: "refresh-secret",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      ProviderAccount{ID: "acct-1"},
		},
	}
	svc, err := New(
		cancelAwareTransactionManager{},
		repo,
		testutil.NewFakeEncryptor(),
		ProviderRegistry{"fakecrm": provider},
		testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
	require.NoError(t, err)

	_, err = svc.CompleteOAuthCallback(ctx, CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "canceled-after-exchange-state",
		Code:     "provider-code",
	})

	require.NoError(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Len(t, repo.grants, 1)
}

func TestServiceResolveRuntimeCredentialRefreshesExpiredGrant(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.grant = activeRepoGrant(t, now.Add(-time.Minute))
	provider := &fakeProviderAdapter{
		refresh: OAuthTokenSet{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

	got, err := svc.ResolveRuntimeCredential(context.Background(), ResolveRuntimeCredentialParams{Owner: testOwner()})
	require.NoError(t, err)

	assert.Equal(t, CredentialKindOAuth2Grant, got.Kind)
	assert.JSONEq(t, `{"access_token":"new-access","bearer_token":"new-access","granted_scopes":["contacts.read"]}`, string(got.PlaintextJSON))
	assert.Equal(t, 1, provider.refreshCalls)
	assert.Equal(t, []string{"contacts.read"}, provider.refreshParams.Scopes)
	assert.Equal(t, now.Add(time.Hour), repo.grant.AccessTokenExpires)
}

func TestServiceResolveRuntimeCredentialPreservesRefreshTokenWhenProviderOmitsReplacement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.grant = activeRepoGrant(t, now.Add(-time.Minute))
	provider := &fakeProviderAdapter{
		refresh: OAuthTokenSet{
			AccessToken: "new-access",
			ExpiresAt:   now.Add(time.Hour),
			Scopes:      []string{"contacts.read"},
			Account:     ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

	_, err := svc.ResolveRuntimeCredential(context.Background(), ResolveRuntimeCredentialParams{Owner: testOwner()})
	require.NoError(t, err)

	storedTokenSet, err := svc.(*service).decryptTokenSet(fromRepoCredentialGrant(repo.grant))
	require.NoError(t, err)
	assert.Equal(t, "old-refresh", storedTokenSet.RefreshToken)
	assert.Equal(t, "new-access", storedTokenSet.AccessToken)
}

func TestServiceRevokeOAuthGrantDoesNotCallProviderAfterConflict(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	repo := newCredentialRepo()
	repo.grant = activeRepoGrant(t, now.Add(time.Hour))
	repo.markOAuthGrantRevokedErr = ErrOAuthGrantConflict
	provider := &fakeProviderAdapter{}
	svc := newTestServiceAt(t, repo, ProviderRegistry{"fakecrm": provider}, now)

	_, err := svc.Revoke(context.Background(), RevokeParams{Owner: testOwner()})

	require.ErrorIs(t, err, ErrOAuthGrantConflict)
	assert.Equal(t, 0, provider.revokeCalls)
	assert.Equal(t, CredentialStatusActive.String(), repo.grant.Status)
}

func newTestService(t *testing.T, repo repocredentials.Repository, providers ProviderRegistry) FullService {
	t.Helper()
	return newTestServiceAt(t, repo, providers, time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC))
}

func newTestServiceAt(
	t *testing.T,
	repo repocredentials.Repository,
	providers ProviderRegistry,
	now time.Time,
) FullService {
	t.Helper()
	svc, err := New(
		testutil.FakeTransactionManager{},
		repo,
		testutil.NewFakeEncryptor(),
		providers,
		testutil.FakeClock{NowFn: func() time.Time { return now }}, false)
	require.NoError(t, err)
	return svc
}

func testOwner() CredentialOwner {
	return CredentialOwner{OrgSlug: must.Slug("acme"), DataSourceSlug: must.Slug("warehouse")}
}

func testEnvironmentSecretOwner() CredentialOwner {
	return CredentialOwner{
		Kind:                 CredentialOwnerKindEnvironmentSecret,
		OrgSlug:              must.Slug("acme"),
		EnvironmentSlug:      must.Slug("prod"),
		EnvironmentSecretKey: "API_TOKEN",
	}
}

func activeRepoGrant(t *testing.T, expiresAt time.Time) repocredentials.CredentialGrant {
	t.Helper()
	tokenSet := OAuthTokenSet{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    expiresAt,
		Scopes:       []string{"contacts.read"},
		Account:      ProviderAccount{ID: "acct-1"},
	}
	payload, err := marshalOAuthTokenSet(tokenSet)
	require.NoError(t, err)

	return repocredentials.CredentialGrant{
		Slug:               must.Slug("grant-1"),
		OrgSlug:            must.Slug("acme"),
		DataSourceSlug:     must.Slug("warehouse"),
		Kind:               "oauth2_grant",
		Provider:           "fakecrm",
		Status:             "active",
		EncryptedTokenSet:  []byte("encrypted:" + string(payload)),
		AccessTokenExpires: expiresAt,
		Scopes:             []string{"contacts.read"},
		Account:            []byte(`{"ID":"acct-1"}`),
		CreatedAt:          expiresAt.Add(-time.Hour),
		UpdatedAt:          expiresAt.Add(-time.Hour),
	}
}

type credentialRepo struct {
	httpRefreshClaimed        bool
	httpApps                  map[repocredentials.ResolveRuntimeCredentialParams][]byte
	staticSecrets             []repocredentials.CredentialRecord
	pendingOAuthCredentials   []repocredentials.CredentialRecord
	installs                  []repocredentials.CredentialInstall
	oauthGrantIntentState     repocredentials.OAuthGrantIntentState
	oauthGrantIntentErr       error
	grant                     repocredentials.CredentialGrant
	grants                    []repocredentials.CredentialGrant
	markOAuthGrantRevokedErr  error
	revokedWebhookOwner       *repocredentials.PipelineWebhookOwner
	revokeWebhookErr          error
	lockWebhookOwner          *repocredentials.PipelineWebhookOwner
	lockWebhookErr            error
	webhookCalls              []string
	resolvedWebhookOwner      *repocredentials.PipelineWebhookOwner
	resolvedWebhookKeyVersion int
	resolvedWebhookCredential repocredentials.CredentialRecord
	resolveWebhookErr         error
}

// newCredentialRepo returns an in-memory repository with a valid pending OAuth default.
func newCredentialRepo() *credentialRepo {
	return &credentialRepo{oauthGrantIntentState: repocredentials.OAuthGrantIntentState{
		Kind:   CredentialKindOAuth2Grant.String(),
		Status: CredentialStatusPending.String(),
	}}
}

func (r *credentialRepo) CreateStaticSecret(
	_ context.Context,
	_ postgres.DBTX,
	record repocredentials.CredentialRecord,
) (repocredentials.CredentialRecord, error) {
	record.Kind = "static_secret"
	record.Status = "active"
	r.staticSecrets = append(r.staticSecrets, record)
	return record, nil
}

func (r *credentialRepo) CreatePipelineWebhookCredential(
	_ context.Context,
	_ postgres.DBTX,
	_ repocredentials.PipelineWebhookOwner,
	record repocredentials.CredentialRecord,
	_ time.Time,
) (repocredentials.CredentialRecord, int, error) {
	record.Kind = "static_secret"
	record.CredentialType = "pipeline_webhook"
	record.Status = "active"
	r.staticSecrets = append(r.staticSecrets, record)
	return record, len(r.staticSecrets), nil
}

func (r *credentialRepo) ResolvePipelineWebhookCredential(
	_ context.Context,
	_ postgres.DBTX,
	owner repocredentials.PipelineWebhookOwner,
	keyVersion int,
) (repocredentials.CredentialRecord, error) {
	r.resolvedWebhookOwner = &owner
	r.resolvedWebhookKeyVersion = keyVersion
	if r.resolveWebhookErr != nil {
		return repocredentials.CredentialRecord{}, r.resolveWebhookErr
	}
	return r.resolvedWebhookCredential, nil
}

func (r *credentialRepo) RevokePipelineWebhookCredentials(
	_ context.Context,
	_ postgres.DBTX,
	owner repocredentials.PipelineWebhookOwner,
	_ time.Time,
) error {
	r.webhookCalls = append(r.webhookCalls, "revoke")
	r.revokedWebhookOwner = &owner
	return r.revokeWebhookErr
}

func (r *credentialRepo) LockPipelineWebhookCredentialOwner(
	_ context.Context,
	_ postgres.DBTX,
	owner repocredentials.PipelineWebhookOwner,
) error {
	r.webhookCalls = append(r.webhookCalls, "lock")
	r.lockWebhookOwner = &owner
	return r.lockWebhookErr
}

// CreatePendingOAuthCredential records a payload-free pending OAuth credential for service tests.
func (r *credentialRepo) CreatePendingOAuthCredential(
	_ context.Context,
	_ postgres.DBTX,
	record repocredentials.CredentialRecord,
) (repocredentials.CredentialRecord, error) {
	record.Kind = "oauth2_grant"
	record.Status = "pending"
	record.EncryptedPayload = nil
	r.pendingOAuthCredentials = append(r.pendingOAuthCredentials, record)
	return record, nil
}

func (r *credentialRepo) CreateOrganizationGitLabConnection(_ context.Context, _ postgres.DBTX, record repocredentials.CredentialRecord) (repocredentials.CredentialRecord, error) {
	record.Kind, record.Status = "oauth2_grant", "pending"
	r.pendingOAuthCredentials = append(r.pendingOAuthCredentials, record)
	return record, nil
}

func (r *credentialRepo) AttachOrganizationGitLabConnection(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) error {
	return nil
}

func (r *credentialRepo) FindOrganizationGitLabConnection(_ context.Context, _ postgres.DBTX, org slug.Slug) (repocredentials.CredentialRecord, error) {
	for _, record := range r.pendingOAuthCredentials {
		if record.OrgSlug == org && record.OwnerKind == repocredentials.OwnerKindOrganizationGitLab {
			return record, nil
		}
	}
	return repocredentials.CredentialRecord{}, ErrCredentialNotFound
}

func (r *credentialRepo) FindStaticSecretByDataSource(
	context.Context,
	postgres.DBTX,
	repocredentials.ResolveRuntimeCredentialParams,
) (repocredentials.CredentialRecord, error) {
	return repocredentials.CredentialRecord{}, ErrCredentialNotFound
}

func (r *credentialRepo) FindStaticSecretByEnvironmentSecret(
	context.Context,
	postgres.DBTX,
	repocredentials.ResolveRuntimeCredentialParams,
) (repocredentials.CredentialRecord, error) {
	return repocredentials.CredentialRecord{}, ErrCredentialNotFound
}

func (r *credentialRepo) MarkStaticEnvironmentSecretRotated(
	_ context.Context,
	_ postgres.DBTX,
	params repocredentials.ResolveRuntimeCredentialParams,
	replacementSlug slug.Slug,
	rotatedAt time.Time,
) ([]repocredentials.CredentialRecord, error) {
	var rotated []repocredentials.CredentialRecord
	for i, record := range r.staticSecrets {
		if record.OrgSlug != params.OrgSlug ||
			record.EnvironmentSlug != params.EnvironmentSlug ||
			record.EnvironmentSecretKey != params.EnvironmentSecretKey ||
			record.Slug == replacementSlug ||
			record.Status != "active" {
			continue
		}
		record.Status = "rotated"
		record.EncryptedPayload = nil
		record.RotatedAt = &rotatedAt
		r.staticSecrets[i] = record
		rotated = append(rotated, record)
	}
	return rotated, nil
}

func (r *credentialRepo) MarkStaticEnvironmentSecretRevoked(
	_ context.Context,
	_ postgres.DBTX,
	params repocredentials.ResolveRuntimeCredentialParams,
	revokedAt time.Time,
) (repocredentials.CredentialRecord, error) {
	for i, record := range r.staticSecrets {
		if record.OrgSlug != params.OrgSlug ||
			record.EnvironmentSlug != params.EnvironmentSlug ||
			record.EnvironmentSecretKey != params.EnvironmentSecretKey ||
			record.Status != "active" {
			continue
		}
		record.Status = "revoked"
		record.EncryptedPayload = nil
		record.RevokedAt = &revokedAt
		r.staticSecrets[i] = record
		return record, nil
	}
	return repocredentials.CredentialRecord{}, ErrCredentialNotFound
}

func (r *credentialRepo) CreateOAuthInstall(
	_ context.Context,
	_ postgres.DBTX,
	install repocredentials.CredentialInstall,
) (repocredentials.CredentialInstall, error) {
	install.ExpectedCredentialSlug = r.oauthGrantIntentState.CredentialSlug
	install.ExpectedCredentialStatus = r.oauthGrantIntentState.Status
	if install.Provider == "http" {
		revision := r.oauthGrantIntentState.SourceRevision
		install.ExpectedSourceRevision = &revision
	}
	r.installs = append(r.installs, install)
	return install, nil
}

func (r *credentialRepo) FindOAuthInstallByState(
	_ context.Context,
	_ postgres.DBTX,
	provider, stateHash string,
	now time.Time,
) (repocredentials.CredentialInstall, error) {
	for _, install := range r.installs {
		if install.Provider == provider && install.StateHash == stateHash {
			if install.ConsumedAt != nil {
				return install, ErrOAuthStateConsumed
			}
			if now.After(install.ExpiresAt) {
				return install, ErrOAuthStateExpired
			}
			return install, nil
		}
	}
	return repocredentials.CredentialInstall{}, ErrOAuthStateInvalid
}

func (r *credentialRepo) ConsumeOAuthInstall(
	_ context.Context,
	_ postgres.DBTX,
	provider, stateHash string,
	now time.Time,
) (repocredentials.CredentialInstall, error) {
	for i, install := range r.installs {
		if install.Provider == provider && install.StateHash == stateHash {
			if install.ConsumedAt != nil {
				return repocredentials.CredentialInstall{}, ErrOAuthStateConsumed
			}
			if now.After(install.ExpiresAt) {
				return repocredentials.CredentialInstall{}, ErrOAuthStateExpired
			}
			install.ConsumedAt = &now
			r.installs[i] = install
			return install, nil
		}
	}
	return repocredentials.CredentialInstall{}, ErrOAuthStateInvalid
}

func (r *credentialRepo) CreateOAuthGrant(
	_ context.Context,
	_ postgres.DBTX,
	grant repocredentials.CredentialGrant,
	_ string,
) (repocredentials.CredentialGrant, error) {
	grant.Kind = "oauth2_grant"
	grant.Status = "active"
	r.grant = grant
	r.grants = append(r.grants, grant)
	return grant, nil
}

func (r *credentialRepo) UpsertOAuthGrant(
	ctx context.Context,
	tx postgres.DBTX,
	grant repocredentials.CredentialGrant,
) (repocredentials.CredentialGrant, error) {
	return r.CreateOAuthGrant(ctx, tx, grant, "rotate_grant")
}

// LockOAuthGrantIntent returns the configured locked credential state for callback intent tests.
func (r *credentialRepo) LockOAuthGrantIntent(
	context.Context,
	postgres.DBTX,
	repocredentials.ResolveRuntimeCredentialParams,
) (repocredentials.OAuthGrantIntentState, error) {
	return r.oauthGrantIntentState, r.oauthGrantIntentErr
}

func (r *credentialRepo) FindOAuthGrant(
	context.Context,
	postgres.DBTX,
	repocredentials.ResolveRuntimeCredentialParams,
) (repocredentials.CredentialGrant, error) {
	if r.grant.Slug == "" {
		return repocredentials.CredentialGrant{}, ErrCredentialNotFound
	}
	return r.grant, nil
}

func (r *credentialRepo) MarkOAuthGrantRevoked(
	_ context.Context,
	_ postgres.DBTX,
	grant repocredentials.CredentialGrant,
) (repocredentials.CredentialGrant, error) {
	if r.markOAuthGrantRevokedErr != nil {
		return repocredentials.CredentialGrant{}, r.markOAuthGrantRevokedErr
	}
	r.grant = grant
	return grant, nil
}

func (r *credentialRepo) seedInstall(install repocredentials.CredentialInstall) {
	if install.ExpectedCredentialStatus == "" {
		install.ExpectedCredentialSlug = r.oauthGrantIntentState.CredentialSlug
		install.ExpectedCredentialStatus = r.oauthGrantIntentState.Status
		if install.Provider == "http" {
			revision := r.oauthGrantIntentState.SourceRevision
			install.ExpectedSourceRevision = &revision
		}
	}
	r.installs = append(r.installs, install)
}

type fakeProviderAdapter struct {
	authURL        string
	exchange       OAuthTokenSet
	exchangeErr    error
	exchangeHook   func()
	exchangeCalls  int
	exchangeParams ExchangeCodeParams
	refresh        OAuthTokenSet
	refreshCalls   int
	refreshParams  RefreshParams
	revokeCalls    int
}

type cancelAwareTransactionManager struct{}

func (cancelAwareTransactionManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx, nil)
}

func (cancelAwareTransactionManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx, nil)
}

type plannedProviderAdapter struct {
	*fakeProviderAdapter
	plan          OAuthScopePlan
	authorization AuthorizationURLParams
}

type contextualPlannedProviderAdapter struct {
	*plannedProviderAdapter
	params OAuthScopePlanParams
}

func (f *contextualPlannedProviderAdapter) OAuthScopePlanForInstall(params OAuthScopePlanParams) OAuthScopePlan {
	f.params = params
	return OAuthScopePlan{Optional: []string{"contacts.read", "contacts.write"}}
}

// AuthorizationURL records the provider-selected scopes passed to the test adapter.
func (f *plannedProviderAdapter) AuthorizationURL(ctx context.Context, params AuthorizationURLParams) (string, error) {
	f.authorization = params
	return f.fakeProviderAdapter.AuthorizationURL(ctx, params)
}

// OAuthScopePlan returns the provider-selected test scope plan.
func (f *plannedProviderAdapter) OAuthScopePlan() OAuthScopePlan {
	return f.plan
}

func (f *fakeProviderAdapter) AuthorizationURL(context.Context, AuthorizationURLParams) (string, error) {
	return f.authURL, nil
}

func (f *fakeProviderAdapter) ExchangeCode(_ context.Context, params ExchangeCodeParams) (OAuthTokenSet, error) {
	f.exchangeParams = params
	if f.exchangeHook != nil {
		f.exchangeHook()
	}
	f.exchangeCalls++
	return f.exchange, f.exchangeErr
}

func (f *fakeProviderAdapter) Refresh(_ context.Context, params RefreshParams) (OAuthTokenSet, error) {
	f.refreshCalls++
	f.refreshParams = params
	return f.refresh, nil
}

func (f *fakeProviderAdapter) Revoke(context.Context, RevokeProviderGrantParams) error {
	f.revokeCalls++
	return nil
}

type trackingCredentialTransactionManager struct {
	inTx         bool
	transactions int
}

func (m *trackingCredentialTransactionManager) RunInTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	m.transactions++
	m.inTx = true
	defer func() {
		m.inTx = false
	}()
	return fn(ctx, nil)
}

func (m *trackingCredentialTransactionManager) RunRead(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	return fn(ctx, nil)
}

func TestPendingSourceCredentialRejectsOrganizationOwner(t *testing.T) {
	t.Parallel()
	repo := newCredentialRepo()
	svc := newTestService(t, repo, ProviderRegistry{"gitlab": &fakeProviderAdapter{}})
	_, err := svc.CreatePendingOAuthCredential(t.Context(), CreatePendingOAuthCredentialParams{Provider: "gitlab", Owner: CredentialOwner{Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: "acme"}})
	require.ErrorIs(t, err, ErrInvalidInput)
	require.Empty(t, repo.pendingOAuthCredentials)
}

func TestOAuthFileAccessModeValidation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ provider, mode string }{{"google_workspace", "unsupported"}, {"hubspot", "selected_files"}, {"bigquery", "selected_files"}} {
		t.Run(tt.provider+tt.mode, func(t *testing.T) {
			repo := newCredentialRepo()
			provider := &fakeProviderAdapter{authURL: "https://provider.test/oauth"}
			svc := newTestService(t, repo, ProviderRegistry{tt.provider: provider})
			_, err := svc.StartOAuthInstall(t.Context(), StartOAuthInstallParams{Owner: testOwner(), Provider: tt.provider, AccessMode: tt.mode, Intent: OAuthInstallIntentCreate})
			require.ErrorIs(t, err, ErrInvalidInput)
			assert.Empty(t, repo.installs)
		})
	}
}

func TestWorkspaceRotationUsesSelectedFiles(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "selected_files"} {
		t.Run(mode, func(t *testing.T) {
			repo := newCredentialRepo()
			repo.grant = repocredentials.CredentialGrant{Slug: must.Slug("grant-1"), OrgSlug: testOwner().OrgSlug, DataSourceSlug: testOwner().DataSourceSlug, Provider: "google_workspace", Kind: "oauth2_grant", Status: "active", Scopes: []string{"openid", "email", "https://www.googleapis.com/auth/drive.file"}}
			provider := &contextualPlannedProviderAdapter{plannedProviderAdapter: &plannedProviderAdapter{fakeProviderAdapter: &fakeProviderAdapter{authURL: "https://provider.test/oauth"}}}
			svc := newTestService(t, repo, ProviderRegistry{"google_workspace": provider})
			_, err := svc.Rotate(t.Context(), RotateParams{Owner: testOwner(), AccessMode: mode})
			require.NoError(t, err)
			assert.Equal(t, mode, provider.params.AccessMode)
			require.Len(t, repo.installs, 1)
		})
	}
}

func (r *credentialRepo) SaveHTTPOAuthApp(_ context.Context, _ postgres.DBTX, owner repocredentials.ResolveRuntimeCredentialParams, encrypted []byte) error {
	if r.httpApps == nil {
		r.httpApps = make(map[repocredentials.ResolveRuntimeCredentialParams][]byte)
	}
	r.httpApps[owner] = append([]byte(nil), encrypted...)
	r.oauthGrantIntentState.SourceRevision++
	return nil
}

func (r *credentialRepo) FindHTTPOAuthApp(_ context.Context, _ postgres.DBTX, owner repocredentials.ResolveRuntimeCredentialParams) ([]byte, error) {
	encrypted, ok := r.httpApps[owner]
	if !ok {
		return nil, repocredentials.ErrCredentialNotFound
	}
	return append([]byte(nil), encrypted...), nil
}

func (r *credentialRepo) ClaimHTTPOAuthRefresh(context.Context, postgres.DBTX, repocredentials.CredentialGrant, string) (bool, error) {
	if r.httpRefreshClaimed {
		return false, nil
	}
	r.httpRefreshClaimed = true
	return true, nil
}

func (r *credentialRepo) ReleaseHTTPOAuthRefresh(context.Context, postgres.DBTX, repocredentials.ResolveRuntimeCredentialParams, string) error {
	r.httpRefreshClaimed = false
	return nil
}
