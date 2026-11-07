package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

func TestOAuthGrantLifecyclePersistsAndRefreshesProviderCredential(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres credential OAuth lifecycle E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "credential OAuth lifecycle service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	currentTime := now
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1", Name: "Acme CRM", Email: "ops@example.com"},
		},
		refresh: credentialssvc.OAuthTokenSet{
			AccessToken: "refreshed-access-token",
			ExpiresAt:   now.Add(time.Hour),
			Scopes:      []string{"contacts.read", "companies.read"},
			Account:     credentialssvc.ProviderAccount{ID: "acct-1", Name: "Acme CRM", Email: "ops@example.com"},
		},
	}
	svc := newCredentialServiceWithClock(t, pool, func() time.Time { return currentTime }, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-ds")
	oldCredentialSlug := seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	require.NotEmpty(t, install.State)
	assert.Equal(t, "fakecrm", install.Provider)
	assert.Equal(t, credentialssvc.OAuthInstallIntentRotate, install.Intent)
	assert.Equal(t, []string{"contacts.read"}, provider.authorizedScopes)

	storedStateHash, consumedAt := storedOAuthInstallState(t, pool, orgSlug, install.Slug)
	assert.Equal(t, oauthflow.HashState(install.State), storedStateHash)
	assert.NotEqual(t, install.State, storedStateHash)
	assert.Nil(t, consumedAt)

	completed, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code-1",
	})
	require.NoError(t, err)
	assert.Equal(t, install.Slug, completed.InstallSlug)
	assert.Equal(t, credentialssvc.CredentialKindOAuth2Grant, completed.Credential.Kind)
	assert.Equal(t, credentialssvc.CredentialStatusActive, completed.Credential.Status)
	assert.Equal(t, "provider-code-1", provider.exchangedCode)

	_, consumedAt = storedOAuthInstallState(t, pool, orgSlug, install.Slug)
	require.NotNil(t, consumedAt)
	assert.Equal(t, now, consumedAt.UTC())
	assertOAuthGrantAttached(t, pool, orgSlug, dsSlug, "fakecrm")
	status, payloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, oldCredentialSlug)
	assert.Equal(t, credentialssvc.CredentialStatusRotated.String(), status)
	assert.Zero(t, payloadLen)

	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code-2",
	})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthStateConsumed)

	runtimeCredential, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialOwner(orgSlug, dsSlug),
	})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialKindOAuth2Grant, runtimeCredential.Kind)
	assertAccessToken(t, runtimeCredential.PlaintextJSON, "initial-access-token", []string{"contacts.read"})

	versionBeforeRefresh := runtimeCredential.Version
	revisionBeforeRefresh := storedDataSourceConnectionTestRevision(t, pool, orgSlug, dsSlug)
	currentTime = now.Add(2 * time.Hour)
	runtimeCredential, err = svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialOwner(orgSlug, dsSlug),
	})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialKindOAuth2Grant, runtimeCredential.Kind)
	assertAccessToken(t, runtimeCredential.PlaintextJSON, "refreshed-access-token", []string{"contacts.read", "companies.read"})
	assert.NotEqual(t, versionBeforeRefresh, runtimeCredential.Version)
	assert.Equal(t, "initial-refresh-token", provider.refreshedRefreshToken)
	assert.Greater(t, storedDataSourceConnectionTestRevision(t, pool, orgSlug, dsSlug), revisionBeforeRefresh)
	assertStoredGrantDoesNotContainPlaintext(t, pool, orgSlug, dsSlug, "initial-access-token")
	assertStoredGrantDoesNotContainPlaintext(t, pool, orgSlug, dsSlug, "refreshed-access-token")

	rotation, err := svc.Rotate(ctx, credentialssvc.RotateParams{Owner: credentialOwner(orgSlug, dsSlug)})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.OAuthInstallIntentRotate, rotation.Intent)
	assert.Equal(t, []string{"contacts.read", "companies.read"}, rotation.RequestedScopes)

	revoked, err := svc.Revoke(ctx, credentialssvc.RevokeParams{Owner: credentialOwner(orgSlug, dsSlug)})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialStatusRevoked, revoked.Status)
	assert.NotNil(t, revoked.OAuth.RevokedAt)
	assert.Equal(t, "refreshed-access-token", provider.revokedAccessToken)
	assert.Equal(t, "initial-refresh-token", provider.revokedRefreshToken)
	assert.Zero(t, storedGrantTokenPayloadLen(t, pool, orgSlug, dsSlug))

	revokedAgain, err := svc.Revoke(ctx, credentialssvc.RevokeParams{Owner: credentialOwner(orgSlug, dsSlug)})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialStatusRevoked, revokedAgain.Status)

	_, err = svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialOwner(orgSlug, dsSlug),
	})
	require.ErrorIs(t, err, credentialssvc.ErrProviderGrantRevoked)
}

func TestOAuthCallbackActivatesAttachedPendingCredentialInPlace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pending OAuth credential E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "pending OAuth credential service integration test")
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-pending-org")
	anchorDsSlug := uniqueCredentialE2ESlug("cred-pending-anchor")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, anchorDsSlug)
	dsSlug := uniqueCredentialE2ESlug("cred-pending-ds")
	pending, err := svc.CreatePendingOAuthCredential(ctx, credentialssvc.CreatePendingOAuthCredentialParams{
		Owner:    credentialOwner(orgSlug, dsSlug),
		Provider: "fakecrm",
	})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialKindOAuth2Grant, pending.Kind)
	assert.Equal(t, credentialssvc.CredentialStatusPending, pending.Status)

	txm := postgres.NewTransactionManager(pool)
	err = txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, createErr := repods.New().Create(ctx, tx, orgSlug, &repods.CreateParams{
			Slug:           dsSlug,
			Name:           "Pending OAuth Data Source",
			Type:           model.DataSourceTypeHubSpot,
			ConfigJSON:     json.RawMessage(`{}`),
			CredentialSlug: pending.Slug,
			ReadOnly:       true,
		})
		return createErr
	})
	require.NoError(t, err)
	assert.Equal(t, pending.Slug, storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
	status, payloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, pending.Slug)
	assert.Equal(t, credentialssvc.CredentialStatusPending.String(), status)
	assert.Zero(t, payloadLen)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentCreate,
	})
	require.NoError(t, err)
	completed, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)
	assert.Equal(t, pending.Slug, completed.Credential.Slug)
	assert.Equal(t, credentialssvc.CredentialStatusActive, completed.Credential.Status)
	assert.Equal(t, pending.Slug, storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
	assertOAuthGrantAttached(t, pool, orgSlug, dsSlug, "fakecrm")
}

func TestSecondCreateIntentCannotReplaceActivatedOAuthGrant(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres stale OAuth create intent E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "stale OAuth create intent service integration test")
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-stale-create-org")
	anchorDsSlug := uniqueCredentialE2ESlug("cred-stale-create-anchor")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, anchorDsSlug)
	dsSlug := uniqueCredentialE2ESlug("cred-stale-create-ds")
	pending, err := svc.CreatePendingOAuthCredential(ctx, credentialssvc.CreatePendingOAuthCredentialParams{
		Owner:    credentialOwner(orgSlug, dsSlug),
		Provider: "fakecrm",
	})
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(pool)
	err = txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, createErr := repods.New().Create(ctx, tx, orgSlug, &repods.CreateParams{
			Slug:           dsSlug,
			Name:           "Pending OAuth Data Source",
			Type:           model.DataSourceTypeHubSpot,
			ConfigJSON:     json.RawMessage(`{}`),
			CredentialSlug: pending.Slug,
			ReadOnly:       true,
		})
		return createErr
	})
	require.NoError(t, err)

	firstInstall, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentCreate,
	})
	require.NoError(t, err)
	secondInstall, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentCreate,
	})
	require.NoError(t, err)

	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    firstInstall.State,
		Code:     "provider-code-1",
	})
	require.NoError(t, err)
	attachedCredentialSlug := storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug)

	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    secondInstall.State,
		Code:     "provider-code-2",
	})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthIntentConflict)
	assert.Equal(t, attachedCredentialSlug, storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
	assert.Equal(t, 1, provider.exchangeCallCount())
}

// TestSecondRotationIntentCannotReplaceActivatedOAuthGrant keeps a stale rotation from replacing a newer grant.
func TestSecondRotationIntentCannotReplaceActivatedOAuthGrant(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres stale OAuth rotation intent E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "stale OAuth rotation intent service integration test")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "rotated-access-token",
			RefreshToken: "rotated-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-stale-rotation-org")
	dsSlug := uniqueCredentialE2ESlug("cred-stale-rotation-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	firstInstall, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	secondInstall, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)

	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    firstInstall.State,
		Code:     "provider-code-1",
	})
	require.NoError(t, err)
	attachedCredentialSlug := storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug)

	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    secondInstall.State,
		Code:     "provider-code-2",
	})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthIntentConflict)
	assert.Equal(t, attachedCredentialSlug, storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
	assert.Equal(t, 1, provider.exchangeCallCount())
}

func TestOAuthCallbackUnknownStateReturnsInvalidState(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres credential OAuth invalid callback E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "credential OAuth invalid callback service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	svc := newCredentialService(t, pool, now, &fakeProviderAdapter{})

	_, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    "missing-state",
		Code:     "provider-code",
	})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthStateInvalid)
}

func TestOAuthInstallDenialConsumesStateWithoutCreatingGrant(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres credential OAuth denial E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "credential OAuth denial service integration test")
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{authURL: "https://fakecrm.example/oauth"}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-denial-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-denial-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)
	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)

	consumed, err := svc.ConsumeOAuthInstall(ctx, credentialssvc.ConsumeOAuthInstallParams{
		Provider: "fakecrm",
		State:    install.State,
	})
	require.NoError(t, err)
	assert.Equal(t, credentialOwner(orgSlug, dsSlug), consumed.Owner)
	_, consumedAt := storedOAuthInstallState(t, pool, orgSlug, install.Slug)
	require.NotNil(t, consumedAt)
	assert.Equal(t, now, consumedAt.UTC())

	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "replayed-code",
	})
	require.ErrorIs(t, err, credentialssvc.ErrOAuthStateConsumed)
	assert.Zero(t, provider.exchangeCalls)
}

func TestOAuthInstallConcurrentCallbackConsumesStateOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres credential OAuth concurrent consume E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := e2edb.OpenPostgresPool(t, "credential OAuth concurrent callback service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL:              "https://fakecrm.example/oauth",
		firstExchangeStarted: make(chan struct{}),
		releaseFirstExchange: make(chan struct{}),
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-race-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-race-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)

	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() {
		_, completeErr := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
			Provider: "fakecrm",
			State:    install.State,
			Code:     "provider-code-1",
		})
		firstErr <- completeErr
	}()

	select {
	case <-provider.firstExchangeStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	go func() {
		_, completeErr := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
			Provider: "fakecrm",
			State:    install.State,
			Code:     "provider-code-2",
		})
		secondErr <- completeErr
	}()

	close(provider.releaseFirstExchange)
	firstCallbackErr := <-firstErr
	secondCallbackErr := <-secondErr
	var losingCallbackErr error
	if firstCallbackErr == nil {
		losingCallbackErr = secondCallbackErr
	} else {
		require.NoError(t, secondCallbackErr)
		losingCallbackErr = firstCallbackErr
	}
	require.Error(t, losingCallbackErr)
	assert.GreaterOrEqual(t, provider.exchangeCallCount(), 1)
	assert.LessOrEqual(t, provider.exchangeCallCount(), 2)
}

func TestPipelineWebhookConcurrentRotateThenRevokeLeavesNoActiveVersion(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline webhook rotate-revoke E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	observerPool := e2edb.OpenPostgresPool(t, "pipeline webhook rotate-revoke observer integration test")
	marker := string(uniqueCredentialE2ESlug("webhook-revoke-race"))
	servicePool := openNamedCredentialE2EPool(t, marker)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	svc := newCredentialService(t, servicePool, now, &fakeProviderAdapter{})
	owner := seedPipelineWebhookCredentialOwner(t, observerPool)
	params := credentialssvc.PipelineWebhookSecretParams{
		OrgSlug: owner.OrgSlug, ProjectSlug: owner.ProjectSlug, EnvironmentSlug: owner.EnvironmentSlug, TriggerSlug: owner.TriggerSlug,
	}

	_, err := svc.IssuePipelineWebhookSecret(ctx, params)
	require.NoError(t, err)

	rotation, err := observerPool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = rotation.Rollback(context.Background()) }()
	_, _, err = repocredentials.New().CreatePipelineWebhookCredential(ctx, rotation, owner, repocredentials.CredentialRecord{
		Slug:             uniqueCredentialE2ESlug("webhook-rotated-credential"),
		OrgSlug:          owner.OrgSlug,
		EncryptedPayload: []byte("encrypted-rotation-payload"),
	}, now.Add(time.Minute))
	require.NoError(t, err)

	revokeDone := make(chan error, 1)
	go func() { revokeDone <- svc.RevokePipelineWebhookSecrets(ctx, params) }()
	waitForCredentialSQLLock(t, ctx, observerPool, marker, "LockPipelineWebhookCredentialOwner")
	require.NoError(t, rotation.Commit(ctx))
	require.NoError(t, <-revokeDone)

	var remaining int
	err = observerPool.QueryRow(ctx, `
SELECT COUNT(*)
FROM pipeline_webhook_secret_versions pws
JOIN credentials c ON c.id = pws.credential_id
JOIN pipeline_triggers pt ON pt.id = pws.pipeline_trigger_id
JOIN projects p ON p.id = pt.project_id
JOIN organizations o ON o.id = p.organization_id
WHERE o.slug = $1 AND pt.slug = $2 AND (pws.enabled OR c.status = 'active')`, owner.OrgSlug, owner.TriggerSlug).Scan(&remaining)
	require.NoError(t, err)
	require.Zero(t, remaining)
}

func TestOAuthGrantConcurrentResolveConvergesWithOptimisticFinalization(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres credential OAuth concurrent refresh E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := e2edb.OpenPostgresPool(t, "credential OAuth concurrent refresh service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL:             "https://fakecrm.example/oauth",
		firstRefreshStarted: make(chan struct{}),
		releaseFirstRefresh: make(chan struct{}),
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(-time.Minute),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
		refresh: credentialssvc.OAuthTokenSet{
			AccessToken:  "refreshed-access-token",
			RefreshToken: "rotated-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-refresh-race-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-refresh-race-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)

	type resolveResult struct {
		payload []byte
		err     error
	}
	firstResult := make(chan resolveResult, 1)
	secondResult := make(chan resolveResult, 1)
	resolve := func(done chan<- resolveResult) {
		runtimeCredential, resolveErr := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
			Owner: credentialOwner(orgSlug, dsSlug),
		})
		done <- resolveResult{payload: runtimeCredential.PlaintextJSON, err: resolveErr}
	}
	go resolve(firstResult)

	select {
	case <-provider.firstRefreshStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	go resolve(secondResult)
	close(provider.releaseFirstRefresh)

	first := <-firstResult
	second := <-secondResult
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assertAccessToken(t, first.payload, "refreshed-access-token", []string{"contacts.read"})
	assertAccessToken(t, second.payload, "refreshed-access-token", []string{"contacts.read"})
	assert.GreaterOrEqual(t, provider.refreshCallCount(), 1)
	assert.LessOrEqual(t, provider.refreshCallCount(), 2)
}

func TestOAuthGrantRefreshRejectsStaleSharedCredentialAttachment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres stale OAuth refresh attachment E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := e2edb.OpenPostgresPool(t, "stale OAuth refresh attachment service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL:             "https://fakecrm.example/oauth",
		firstRefreshStarted: make(chan struct{}),
		releaseFirstRefresh: make(chan struct{}),
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
		refresh: credentialssvc.OAuthTokenSet{
			AccessToken:  "stale-refreshed-access-token",
			RefreshToken: "stale-rotated-refresh-token",
			ExpiresAt:    now.Add(2 * time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-stale-attachment-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-stale-attachment-ds")
	sharedDsSlug := uniqueCredentialE2ESlug("cred-oauth-stale-attachment-shared-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	completed, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)
	seedDataSourceUsingCredential(t, pool, orgSlug, sharedDsSlug, completed.Credential.Slug)

	replacement, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          credentialOwner(orgSlug, dsSlug),
		CredentialType: credentialssvc.StaticCredentialTypeHubSpot,
		PlaintextJSON:  []byte(`{"private_app_token":"replacement-static-secret"}`),
	})
	require.NoError(t, err)

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := svc.Refresh(ctx, credentialssvc.RefreshGrantParams{
			Owner: credentialOwner(orgSlug, dsSlug),
		})
		refreshDone <- refreshErr
	}()

	select {
	case <-provider.firstRefreshStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseRefresh := provider.releaseFirstRefresh
	defer func() {
		select {
		case <-releaseRefresh:
		default:
			close(releaseRefresh)
		}
	}()

	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, updateErr := repods.New().UpdateCredentials(ctx, tx, orgSlug, dsSlug, &repods.UpdateCredentialsParams{
			CredentialSlug: replacement.Slug,
		})
		return updateErr
	})
	require.NoError(t, err)
	revisionAfterReplacement := storedDataSourceConnectionTestRevision(t, pool, orgSlug, dsSlug)

	close(releaseRefresh)
	require.ErrorIs(t, <-refreshDone, credentialssvc.ErrOAuthGrantConflict)
	assert.Equal(t, replacement.Slug, storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
	assert.Equal(t, completed.Credential.Slug, storedDataSourceCredentialSlug(t, pool, orgSlug, sharedDsSlug))
	assert.Equal(t, revisionAfterReplacement, storedDataSourceConnectionTestRevision(t, pool, orgSlug, dsSlug))
	assert.Positive(t, storedOAuthGrantTokenPayloadLenByCredentialSlug(t, pool, orgSlug, completed.Credential.Slug))
}

func TestOAuthGrantRevocationTimestampCheckIsAtomicWithTombstone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres OAuth revocation CAS E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	observerPool := e2edb.OpenPostgresPool(t, "OAuth revocation CAS observer integration test")
	marker := string(uniqueCredentialE2ESlug("cred-oauth-revoke-cas"))
	servicePool := openNamedCredentialE2EPool(t, marker)
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL:             "https://fakecrm.example/oauth",
		firstRefreshStarted: make(chan struct{}),
		releaseFirstRefresh: make(chan struct{}),
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
		refresh: credentialssvc.OAuthTokenSet{
			AccessToken:  "refreshed-access-token",
			RefreshToken: "rotated-refresh-token",
			ExpiresAt:    now.Add(2 * time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, servicePool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-revoke-cas-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-revoke-cas-ds")
	seedCredentialLifecycleDataSource(t, observerPool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	completed, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)

	blocker, err := observerPool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var blockedDataSourceID int64
	err = blocker.QueryRow(ctx, `
SELECT ds.id
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
WHERE o.slug = $1 AND ds.slug = $2
FOR UPDATE OF ds`,
		orgSlug,
		dsSlug,
	).Scan(&blockedDataSourceID)
	require.NoError(t, err)

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := svc.Refresh(ctx, credentialssvc.RefreshGrantParams{
			Owner: credentialOwner(orgSlug, dsSlug),
		})
		refreshDone <- refreshErr
	}()
	select {
	case <-provider.firstRefreshStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(provider.releaseFirstRefresh)
	waitForCredentialSQLLock(t, ctx, observerPool, marker, "UpsertOAuthGrant")

	revokeDone := make(chan error, 1)
	go func() {
		_, revokeErr := svc.Revoke(ctx, credentialssvc.RevokeParams{
			Owner: credentialOwner(orgSlug, dsSlug),
		})
		revokeDone <- revokeErr
	}()
	waitForCredentialSQLLock(t, ctx, observerPool, marker, "MarkCredentialRevoked")

	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-refreshDone)
	require.ErrorIs(t, <-revokeDone, credentialssvc.ErrOAuthGrantConflict)

	status, _ := storedCredentialStatusAndPayloadLen(t, observerPool, orgSlug, completed.Credential.Slug)
	assert.Equal(t, credentialssvc.CredentialStatusActive.String(), status)
	assert.Positive(t, storedGrantTokenPayloadLen(t, observerPool, orgSlug, dsSlug))
	active, testError := storedDataSourceHealth(t, observerPool, orgSlug, dsSlug)
	assert.True(t, active)
	assert.Empty(t, testError)
	runtimeCredential, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialOwner(orgSlug, dsSlug),
	})
	require.NoError(t, err)
	assertAccessToken(t, runtimeCredential.PlaintextJSON, "refreshed-access-token", []string{"contacts.read"})
}

func TestOAuthGrantInvalidGrantRevokesAndDeactivatesDataSource(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres credential OAuth invalid grant E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "credential OAuth invalid grant service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "initial-access-token",
			RefreshToken: "initial-refresh-token",
			ExpiresAt:    now.Add(-time.Minute),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
		refreshErr: credentialssvc.ErrProviderInvalidGrant,
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-invalid-grant-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-invalid-grant-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)

	_, err = svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialOwner(orgSlug, dsSlug),
	})
	require.ErrorIs(t, err, credentialssvc.ErrProviderGrantRevoked)
	assert.Zero(t, storedGrantTokenPayloadLen(t, pool, orgSlug, dsSlug))
	active, testError := storedDataSourceHealth(t, pool, orgSlug, dsSlug)
	assert.False(t, active)
	assert.Contains(t, testError, "Provider grant was revoked")
}

func TestStaticCredentialReplacementRetiresPreviousSecret(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres static credential rotation E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "static credential replacement service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	svc := newCredentialService(t, pool, now, &fakeProviderAdapter{})

	orgSlug := uniqueCredentialE2ESlug("cred-static-org")
	dsSlug := uniqueCredentialE2ESlug("cred-static-ds")
	oldCredentialSlug := seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	newCredential, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          credentialOwner(orgSlug, dsSlug),
		CredentialType: credentialssvc.StaticCredentialTypeHubSpot,
		PlaintextJSON:  []byte(`{"private_app_token":"new-secret"}`),
	})
	require.NoError(t, err)

	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, updateErr := repods.New().UpdateCredentials(ctx, tx, orgSlug, dsSlug, &repods.UpdateCredentialsParams{
			CredentialSlug: newCredential.Slug,
		})
		return updateErr
	})
	require.NoError(t, err)

	status, payloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, oldCredentialSlug)
	assert.Equal(t, credentialssvc.CredentialStatusRotated.String(), status)
	assert.Zero(t, payloadLen)
}

func TestEnvironmentSecretStaticCredentialReplacementAndRevokeLifecycle(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres environment secret credential lifecycle E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "environment secret credential lifecycle service integration test")
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	svc := newCredentialService(t, pool, now, &fakeProviderAdapter{})

	orgSlug := uniqueCredentialE2ESlug("cred-env-secret-org")
	environmentSlug := uniqueCredentialE2ESlug("cred-env-secret-env")
	owner := environmentSecretOwner(orgSlug, environmentSlug, "API_TOKEN")
	seedEnvironmentSecretOwner(t, pool, orgSlug, environmentSlug)

	firstCredential, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          owner,
		CredentialType: credentialssvc.StaticCredentialTypeEnvironmentSecret,
		PlaintextJSON:  []byte(`{"value":"first-secret"}`),
	})
	require.NoError(t, err)
	attachEnvironmentSecretCredential(t, pool, orgSlug, environmentSlug, owner.EnvironmentSecretKey, firstCredential.Slug)

	firstRuntime, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"first-secret"}`, string(firstRuntime.PlaintextJSON))

	secondCredential, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          owner,
		CredentialType: credentialssvc.StaticCredentialTypeEnvironmentSecret,
		PlaintextJSON:  []byte(`{"value":"second-secret"}`),
	})
	require.NoError(t, err)
	attachEnvironmentSecretCredential(t, pool, orgSlug, environmentSlug, owner.EnvironmentSecretKey, secondCredential.Slug)

	firstStatus, firstPayloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, firstCredential.Slug)
	assert.Equal(t, credentialssvc.CredentialStatusRotated.String(), firstStatus)
	assert.Zero(t, firstPayloadLen)
	secondRuntime, err := svc.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"second-secret"}`, string(secondRuntime.PlaintextJSON))
	assertStoredCredentialDoesNotContainPlaintext(t, pool, orgSlug, secondCredential.Slug, "second-secret")

	revoked, err := svc.Revoke(ctx, credentialssvc.RevokeParams{Owner: owner})
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialStatusRevoked, revoked.Status)
	secondStatus, secondPayloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, secondCredential.Slug)
	assert.Equal(t, credentialssvc.CredentialStatusRevoked.String(), secondStatus)
	assert.Zero(t, secondPayloadLen)
}

func TestStaticCredentialReplacementTombstonesPreviousOAuthGrant(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres static credential replacement OAuth tombstone E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "static replacement OAuth tombstone service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "oauth-access-token",
			RefreshToken: "oauth-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-static-replaces-oauth-org")
	dsSlug := uniqueCredentialE2ESlug("cred-static-replaces-oauth-ds")
	seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	completed, err := svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)
	require.NotZero(t, storedOAuthGrantTokenPayloadLenByCredentialSlug(t, pool, orgSlug, completed.Credential.Slug))

	staticCredential, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          credentialOwner(orgSlug, dsSlug),
		CredentialType: credentialssvc.StaticCredentialTypeHubSpot,
		PlaintextJSON:  []byte(`{"private_app_token":"static-secret"}`),
	})
	require.NoError(t, err)
	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, updateErr := repods.New().UpdateCredentials(ctx, tx, orgSlug, dsSlug, &repods.UpdateCredentialsParams{
			CredentialSlug: staticCredential.Slug,
		})
		return updateErr
	})
	require.NoError(t, err)

	status, _ := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, completed.Credential.Slug)
	assert.Equal(t, credentialssvc.CredentialStatusRotated.String(), status)
	assert.Zero(t, storedOAuthGrantTokenPayloadLenByCredentialSlug(t, pool, orgSlug, completed.Credential.Slug))
}

func TestOAuthGrantAttachmentPreservesSharedPreviousCredential(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres shared credential OAuth attachment E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "shared credential OAuth attachment service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	provider := &fakeProviderAdapter{
		authURL: "https://fakecrm.example/oauth",
		exchange: credentialssvc.OAuthTokenSet{
			AccessToken:  "oauth-access-token",
			RefreshToken: "oauth-refresh-token",
			ExpiresAt:    now.Add(time.Hour),
			Scopes:       []string{"contacts.read"},
			Account:      credentialssvc.ProviderAccount{ID: "acct-1"},
		},
	}
	svc := newCredentialService(t, pool, now, provider)

	orgSlug := uniqueCredentialE2ESlug("cred-oauth-shared-org")
	dsSlug := uniqueCredentialE2ESlug("cred-oauth-shared-primary-ds")
	sharedDsSlug := uniqueCredentialE2ESlug("cred-oauth-shared-secondary-ds")
	oldCredentialSlug := seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)
	seedDataSourceUsingCredential(t, pool, orgSlug, sharedDsSlug, oldCredentialSlug)

	install, err := svc.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:           credentialOwner(orgSlug, dsSlug),
		Provider:        "fakecrm",
		RequestedScopes: []string{"contacts.read"},
		Intent:          credentialssvc.OAuthInstallIntentRotate,
	})
	require.NoError(t, err)
	_, err = svc.CompleteOAuthCallback(ctx, credentialssvc.CompleteOAuthCallbackParams{
		Provider: "fakecrm",
		State:    install.State,
		Code:     "provider-code",
	})
	require.NoError(t, err)

	assertOAuthGrantAttached(t, pool, orgSlug, dsSlug, "fakecrm")
	status, payloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, oldCredentialSlug)
	assert.Equal(t, credentialssvc.CredentialStatusActive.String(), status)
	assert.Positive(t, payloadLen)
	assert.Equal(t, oldCredentialSlug, storedDataSourceCredentialSlug(t, pool, orgSlug, sharedDsSlug))
}

func TestStaticCredentialReplacementPreservesSharedPreviousCredential(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres shared static credential replacement E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "shared static credential replacement service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	svc := newCredentialService(t, pool, now, &fakeProviderAdapter{})

	orgSlug := uniqueCredentialE2ESlug("cred-static-shared-org")
	dsSlug := uniqueCredentialE2ESlug("cred-static-shared-primary-ds")
	sharedDsSlug := uniqueCredentialE2ESlug("cred-static-shared-secondary-ds")
	oldCredentialSlug := seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)
	seedDataSourceUsingCredential(t, pool, orgSlug, sharedDsSlug, oldCredentialSlug)

	newCredential, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          credentialOwner(orgSlug, dsSlug),
		CredentialType: credentialssvc.StaticCredentialTypeHubSpot,
		PlaintextJSON:  []byte(`{"private_app_token":"new-secret"}`),
	})
	require.NoError(t, err)
	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, updateErr := repods.New().UpdateCredentials(ctx, tx, orgSlug, dsSlug, &repods.UpdateCredentialsParams{
			CredentialSlug: newCredential.Slug,
		})
		return updateErr
	})
	require.NoError(t, err)

	assert.Equal(t, newCredential.Slug, storedDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
	status, payloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, oldCredentialSlug)
	assert.Equal(t, credentialssvc.CredentialStatusActive.String(), status)
	assert.Positive(t, payloadLen)
	assert.Equal(t, oldCredentialSlug, storedDataSourceCredentialSlug(t, pool, orgSlug, sharedDsSlug))
}

func TestCredentialReplacementPreservesSoftDeletedSharedPreviousCredential(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres soft-deleted shared credential replacement E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "soft-deleted shared credential replacement service integration test")
	now := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	svc := newCredentialService(t, pool, now, &fakeProviderAdapter{})

	orgSlug := uniqueCredentialE2ESlug("cred-soft-deleted-shared-org")
	dsSlug := uniqueCredentialE2ESlug("cred-soft-deleted-primary-ds")
	sharedDsSlug := uniqueCredentialE2ESlug("cred-soft-deleted-secondary-ds")
	oldCredentialSlug := seedCredentialLifecycleDataSource(t, pool, orgSlug, dsSlug)
	seedDataSourceUsingCredential(t, pool, orgSlug, sharedDsSlug, oldCredentialSlug)
	softDeleteDataSource(t, pool, orgSlug, sharedDsSlug)

	staticCredential, err := svc.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
		Owner:          credentialOwner(orgSlug, dsSlug),
		CredentialType: credentialssvc.StaticCredentialTypeHubSpot,
		PlaintextJSON:  []byte(`{"private_app_token":"static-secret"}`),
	})
	require.NoError(t, err)
	err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, updateErr := repods.New().UpdateCredentials(ctx, tx, orgSlug, dsSlug, &repods.UpdateCredentialsParams{
			CredentialSlug: staticCredential.Slug,
		})
		return updateErr
	})
	require.NoError(t, err)

	status, payloadLen := storedCredentialStatusAndPayloadLen(t, pool, orgSlug, oldCredentialSlug)
	assert.Equal(t, credentialssvc.CredentialStatusActive.String(), status)
	assert.Positive(t, payloadLen)
}

func newCredentialService(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
	provider credentialssvc.ProviderAdapter,
) credentialssvc.FullService {
	t.Helper()
	return newCredentialServiceWithClock(t, pool, func() time.Time { return now }, provider)
}

// newCredentialServiceWithClock constructs a credential service with a controllable clock for expiry tests.
func newCredentialServiceWithClock(
	t *testing.T,
	pool *pgxpool.Pool,
	now func() time.Time,
	provider credentialssvc.ProviderAdapter,
) credentialssvc.FullService {
	t.Helper()

	encryptionKey := []byte("0123456789abcdef0123456789abcdef")
	enc, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": encryptionKey}, encryptionKey)
	require.NoError(t, err)
	svc, err := credentialssvc.New(
		postgres.NewTransactionManager(pool),
		repocredentials.New(),
		enc,
		credentialssvc.ProviderRegistry{"fakecrm": provider},
		testutil.FakeClock{NowFn: now}, false)
	require.NoError(t, err)
	return svc
}

func openNamedCredentialE2EPool(t *testing.T, applicationName string) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(e2edb.PostgresConfigFromEnv().DatabaseURL)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	t.Cleanup(pool.Close)
	return pool
}

func waitForCredentialSQLLock(
	t *testing.T,
	ctx context.Context,
	observerPool *pgxpool.Pool,
	applicationName, queryName string,
) {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := observerPool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1
	FROM pg_stat_activity
	WHERE
		application_name = $1
		AND wait_event_type = 'Lock'
		AND query LIKE ('%' || $2 || '%')
)`,
			applicationName,
			queryName,
		).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s to block: %v", queryName, ctx.Err())
		case <-ticker.C:
		}
	}
}

func seedCredentialLifecycleDataSource(t *testing.T, pool *pgxpool.Pool, orgSlug, dsSlug slug.Slug) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, "Credential OAuth E2E Org", orgSlug)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)
	})

	placeholderSlug := slug.Slug(fmt.Sprintf("%s-placeholder", dsSlug))
	_, err = pool.Exec(ctx, `
WITH org AS (
	SELECT id FROM organizations WHERE slug = $1
),
placeholder_credential AS (
	INSERT INTO credentials (organization_id, slug, kind, credential_type, status, encrypted_payload)
	SELECT id, $2, 'static_secret', 'hubspot', 'active', decode('706c616365686f6c646572', 'hex')
	FROM org
	RETURNING id, organization_id
)
INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active, read_only)
SELECT organization_id, id, 'Credential OAuth E2E Data Source', $3, 'hubspot', '{}'::jsonb, true, true
FROM placeholder_credential`,
		orgSlug,
		placeholderSlug,
		dsSlug,
	)
	require.NoError(t, err)
	return placeholderSlug
}

func seedPipelineWebhookCredentialOwner(t *testing.T, pool *pgxpool.Pool) repocredentials.PipelineWebhookOwner {
	t.Helper()
	owner := repocredentials.PipelineWebhookOwner{
		OrgSlug: uniqueCredentialE2ESlug("webhook-org"), ProjectSlug: uniqueCredentialE2ESlug("webhook-project"),
		EnvironmentSlug: uniqueCredentialE2ESlug("webhook-env"), TriggerSlug: uniqueCredentialE2ESlug("webhook-trigger"),
	}
	pipelineSlug := uniqueCredentialE2ESlug("webhook-pipeline")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `
WITH org AS (
    INSERT INTO organizations (name, slug) VALUES ('Webhook credential E2E org', $1) RETURNING id
), project AS (
    INSERT INTO projects (organization_id, slug, name) SELECT id, $2, 'Webhook project' FROM org RETURNING id, organization_id
), environment AS (
    INSERT INTO environments (organization_id, slug, name) SELECT id, $3, 'Webhook environment' FROM org RETURNING id
), project_environment AS (
    INSERT INTO project_environments (organization_id, project_id, environment_id) SELECT project.organization_id, project.id, environment.id FROM project, environment RETURNING id, project_id
), pipeline AS (
    INSERT INTO pipeline_specs (project_id, slug, name, spec_jsonb, spec_hash, source_path, source_ref, last_applied_commit)
    SELECT project.id, $4::text, 'Webhook pipeline', '{}'::jsonb, 'webhook-test-' || $4::text, 'pipelines/' || $4::text || '.pipeline.json',
        'refs/heads/main', '0123456789abcdef0123456789abcdef01234567'
    FROM project RETURNING id, project_id, slug
)
INSERT INTO pipeline_triggers (
    project_id, project_environment_id, pipeline_id, pipeline_slug, slug, name, trigger_type, enabled,
    version_policy, schedule_cron, schedule_timezone, schedule_misfire_policy, receiver_id,
    webhook_verification_strategy, webhook_input_mappings, created_by_actor_type, updated_by_actor_type
)
SELECT project_environment.project_id, project_environment.id, pipeline.id, pipeline.slug, $5, 'Webhook trigger', 'webhook', true,
    'latest_deployed', NULL, NULL, NULL, $6, 'hmac_sha256', '[]'::jsonb, 'system', 'system'
FROM project_environment, pipeline`, owner.OrgSlug, owner.ProjectSlug, owner.EnvironmentSlug, pipelineSlug, owner.TriggerSlug, markerReceiverID(owner.TriggerSlug))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM projects WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, owner.OrgSlug)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, owner.OrgSlug)
		require.NoError(t, cleanupErr)
	})
	return owner
}

func markerReceiverID(triggerSlug slug.Slug) string {
	return fmt.Sprintf("%032s", triggerSlug.String())
}

func seedDataSourceUsingCredential(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, dsSlug, credentialSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active, read_only)
SELECT o.id, c.id, 'Credential OAuth E2E Shared Data Source', $3, 'hubspot', '{}'::jsonb, true, true
FROM organizations o
JOIN credentials c ON c.organization_id = o.id
WHERE o.slug = $1 AND c.slug = $2`,
		orgSlug,
		credentialSlug,
		dsSlug,
	)
	require.NoError(t, err)
}

func softDeleteDataSource(t *testing.T, pool *pgxpool.Pool, orgSlug, dsSlug slug.Slug) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tag, err := pool.Exec(ctx, `
UPDATE data_sources ds
SET deleted_at = NOW(), updated_at = NOW()
FROM organizations o
WHERE o.id = ds.organization_id AND o.slug = $1 AND ds.slug = $2`,
		orgSlug,
		dsSlug,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())
}

func seedEnvironmentSecretOwner(t *testing.T, pool *pgxpool.Pool, orgSlug, environmentSlug slug.Slug) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, "Credential Environment Secret E2E Org", orgSlug)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, cleanupErr := pool.Exec(cleanupCtx, `
DELETE FROM environment_secrets es
USING credentials c, organizations o
WHERE es.credential_id = c.id AND c.organization_id = o.id AND o.slug = $1`,
			orgSlug,
		)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)
	})

	_, err = pool.Exec(ctx, `
INSERT INTO environments (organization_id, name, slug)
SELECT id, 'Credential Environment Secret E2E Environment', $2
FROM organizations
WHERE slug = $1`,
		orgSlug,
		environmentSlug,
	)
	require.NoError(t, err)
}

func attachEnvironmentSecretCredential(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, environmentSlug slug.Slug,
	key string,
	credentialSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
INSERT INTO environment_secrets (organization_id, environment_id, credential_id, key)
SELECT e.organization_id, e.id, c.id, $3
FROM organizations o
JOIN environments e ON e.organization_id = o.id
JOIN credentials c ON c.organization_id = o.id
WHERE o.slug = $1 AND e.slug = $2 AND c.slug = $4
ON CONFLICT (environment_id, key)
DO UPDATE SET credential_id = EXCLUDED.credential_id, updated_at = NOW()`,
		orgSlug,
		environmentSlug,
		key,
		credentialSlug,
	)
	require.NoError(t, err)
}

func storedOAuthInstallState(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, installSlug slug.Slug,
) (string, *time.Time) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stateHash string
	var consumedAt *time.Time
	err := pool.QueryRow(ctx, `
SELECT i.state_hash, i.consumed_at
FROM credential_oauth_installs i
JOIN organizations o ON o.id = i.organization_id
WHERE o.slug = $1 AND i.slug = $2`,
		orgSlug,
		installSlug,
	).Scan(&stateHash, &consumedAt)
	require.NoError(t, err)
	return stateHash, consumedAt
}

func assertOAuthGrantAttached(t *testing.T, pool *pgxpool.Pool, orgSlug, dsSlug slug.Slug, provider string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var kind, status, storedProvider string
	err := pool.QueryRow(ctx, `
SELECT c.kind, c.status, g.provider
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
JOIN credentials c ON c.id = ds.credential_id
JOIN credential_oauth_grants g ON g.credential_id = c.id
WHERE o.slug = $1 AND ds.slug = $2 AND ds.deleted_at IS NULL`,
		orgSlug,
		dsSlug,
	).Scan(&kind, &status, &storedProvider)
	require.NoError(t, err)
	assert.Equal(t, credentialssvc.CredentialKindOAuth2Grant.String(), kind)
	assert.Equal(t, credentialssvc.CredentialStatusActive.String(), status)
	assert.Equal(t, provider, storedProvider)
}

func assertStoredGrantDoesNotContainPlaintext(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, dsSlug slug.Slug,
	plaintext string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var encryptedTokenSet []byte
	err := pool.QueryRow(ctx, `
SELECT g.encrypted_token_set
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
JOIN credential_oauth_grants g ON g.credential_id = ds.credential_id
WHERE o.slug = $1 AND ds.slug = $2`,
		orgSlug,
		dsSlug,
	).Scan(&encryptedTokenSet)
	require.NoError(t, err)
	assert.NotContains(t, string(encryptedTokenSet), plaintext)
}

func storedCredentialStatusAndPayloadLen(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, credentialSlug slug.Slug,
) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var status string
	var payloadLen int
	err := pool.QueryRow(ctx, `
SELECT c.status, COALESCE(octet_length(c.encrypted_payload), 0)
FROM credentials c
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = $1 AND c.slug = $2`,
		orgSlug,
		credentialSlug,
	).Scan(&status, &payloadLen)
	require.NoError(t, err)
	return status, payloadLen
}

func assertStoredCredentialDoesNotContainPlaintext(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, credentialSlug slug.Slug,
	plaintext string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var encryptedPayload []byte
	err := pool.QueryRow(ctx, `
SELECT c.encrypted_payload
FROM credentials c
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = $1 AND c.slug = $2`,
		orgSlug,
		credentialSlug,
	).Scan(&encryptedPayload)
	require.NoError(t, err)
	assert.NotContains(t, string(encryptedPayload), plaintext)
}

func storedGrantTokenPayloadLen(t *testing.T, pool *pgxpool.Pool, orgSlug, dsSlug slug.Slug) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var payloadLen int
	err := pool.QueryRow(ctx, `
SELECT COALESCE(octet_length(g.encrypted_token_set), 0)
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
JOIN credential_oauth_grants g ON g.credential_id = ds.credential_id
WHERE o.slug = $1 AND ds.slug = $2`,
		orgSlug,
		dsSlug,
	).Scan(&payloadLen)
	require.NoError(t, err)
	return payloadLen
}

func storedDataSourceConnectionTestRevision(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, dsSlug slug.Slug,
) int64 {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var revision int64
	err := pool.QueryRow(ctx, `
SELECT ds.connection_test_revision
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
WHERE o.slug = $1 AND ds.slug = $2`,
		orgSlug,
		dsSlug,
	).Scan(&revision)
	require.NoError(t, err)
	return revision
}

func storedOAuthGrantTokenPayloadLenByCredentialSlug(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, credentialSlug slug.Slug,
) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var payloadLen int
	err := pool.QueryRow(ctx, `
SELECT COALESCE(octet_length(g.encrypted_token_set), 0)
FROM credential_oauth_grants g
JOIN credentials c ON c.id = g.credential_id
JOIN organizations o ON o.id = c.organization_id
WHERE o.slug = $1 AND c.slug = $2`,
		orgSlug,
		credentialSlug,
	).Scan(&payloadLen)
	require.NoError(t, err)
	return payloadLen
}

func storedDataSourceCredentialSlug(t *testing.T, pool *pgxpool.Pool, orgSlug, dsSlug slug.Slug) slug.Slug {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var credentialSlug string
	err := pool.QueryRow(ctx, `
SELECT c.slug
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
JOIN credentials c ON c.id = ds.credential_id
WHERE o.slug = $1 AND ds.slug = $2 AND ds.deleted_at IS NULL`,
		orgSlug,
		dsSlug,
	).Scan(&credentialSlug)
	require.NoError(t, err)
	return slug.Slug(credentialSlug)
}

func storedDataSourceHealth(t *testing.T, pool *pgxpool.Pool, orgSlug, dsSlug slug.Slug) (bool, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var active bool
	var testError *string
	err := pool.QueryRow(ctx, `
SELECT ds.is_active, ds.test_error
FROM data_sources ds
JOIN organizations o ON o.id = ds.organization_id
WHERE o.slug = $1 AND ds.slug = $2`,
		orgSlug,
		dsSlug,
	).Scan(&active, &testError)
	require.NoError(t, err)
	if testError == nil {
		return active, ""
	}
	return active, *testError
}

func assertAccessToken(t *testing.T, payload []byte, want string, wantScopes []string) {
	t.Helper()

	var decoded struct {
		AccessToken   string   `json:"access_token"`
		BearerToken   string   `json:"bearer_token"`
		GrantedScopes []string `json:"granted_scopes"`
	}
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.Equal(t, want, decoded.AccessToken)
	assert.Equal(t, want, decoded.BearerToken)
	assert.Equal(t, wantScopes, decoded.GrantedScopes)
}

func credentialOwner(orgSlug, dsSlug slug.Slug) credentialssvc.CredentialOwner {
	return credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug}
}

func environmentSecretOwner(orgSlug, environmentSlug slug.Slug, key string) credentialssvc.CredentialOwner {
	return credentialssvc.CredentialOwner{
		Kind:                 credentialssvc.CredentialOwnerKindEnvironmentSecret,
		OrgSlug:              orgSlug,
		EnvironmentSlug:      environmentSlug,
		EnvironmentSecretKey: key,
	}
}

func uniqueCredentialE2ESlug(prefix string) slug.Slug {
	return slug.Slug(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}

type fakeProviderAdapter struct {
	mu                    sync.Mutex
	authURL               string
	exchange              credentialssvc.OAuthTokenSet
	refresh               credentialssvc.OAuthTokenSet
	refreshErr            error
	firstExchangeStarted  chan struct{}
	releaseFirstExchange  chan struct{}
	firstRefreshStarted   chan struct{}
	releaseFirstRefresh   chan struct{}
	exchangeCalls         int
	refreshCalls          int
	authorizedScopes      []string
	exchangedCode         string
	refreshedRefreshToken string
	revokedAccessToken    string
	revokedRefreshToken   string
}

func (f *fakeProviderAdapter) AuthorizationURL(
	_ context.Context,
	params credentialssvc.AuthorizationURLParams,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorizedScopes = append([]string(nil), params.Scopes...)
	return f.authURL + "?state=" + params.State, nil
}

func (f *fakeProviderAdapter) ExchangeCode(
	_ context.Context,
	params credentialssvc.ExchangeCodeParams,
) (credentialssvc.OAuthTokenSet, error) {
	f.mu.Lock()
	f.exchangeCalls++
	exchangeCalls := f.exchangeCalls
	firstExchangeStarted := f.firstExchangeStarted
	releaseFirstExchange := f.releaseFirstExchange
	f.exchangedCode = params.Code
	exchange := f.exchange
	f.mu.Unlock()

	if exchangeCalls == 1 && firstExchangeStarted != nil {
		close(firstExchangeStarted)
		<-releaseFirstExchange
	}
	return exchange, nil
}

func (f *fakeProviderAdapter) Refresh(
	_ context.Context,
	params credentialssvc.RefreshParams,
) (credentialssvc.OAuthTokenSet, error) {
	f.mu.Lock()
	f.refreshCalls++
	refreshCalls := f.refreshCalls
	firstRefreshStarted := f.firstRefreshStarted
	releaseFirstRefresh := f.releaseFirstRefresh
	f.refreshedRefreshToken = params.RefreshToken
	refresh := f.refresh
	refreshErr := f.refreshErr
	f.mu.Unlock()

	if refreshCalls == 1 && firstRefreshStarted != nil {
		close(firstRefreshStarted)
		<-releaseFirstRefresh
	}
	if refreshErr != nil {
		return credentialssvc.OAuthTokenSet{}, refreshErr
	}
	return refresh, nil
}

func (f *fakeProviderAdapter) Revoke(
	_ context.Context,
	params credentialssvc.RevokeProviderGrantParams,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokedAccessToken = params.AccessToken
	f.revokedRefreshToken = params.RefreshToken
	return nil
}

func (f *fakeProviderAdapter) exchangeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchangeCalls
}

func (f *fakeProviderAdapter) refreshCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshCalls
}

var _ credentialssvc.ProviderAdapter = (*fakeProviderAdapter)(nil)
