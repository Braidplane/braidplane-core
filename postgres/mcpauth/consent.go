package mcpauth

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type PendingConsent struct {
	RedirectURI, Challenge, ClientState string
	GrantID                             int64
}

func CreatePendingConsent(ctx context.Context, tx postgres.DBTX, pending PendingConsent, nonceHash, sessionHash string, lifetimeSeconds int32) error {
	return mapError(dbgen.New(tx).CreateMCPOAuthPendingConsent(ctx, dbgen.CreateMCPOAuthPendingConsentParams{
		NonceHash: nonceHash, GrantID: pending.GrantID, SessionHash: sessionHash, RedirectUri: pending.RedirectURI,
		Challenge: pending.Challenge, ClientState: pending.ClientState, LifetimeSeconds: lifetimeSeconds,
	}))
}

func ConsumePendingConsent(ctx context.Context, tx postgres.DBTX, issuance Issuance, nonceHash, sessionHash string, user, org slug.Slug, authVersion int64) (PendingConsent, error) {
	row, err := dbgen.New(tx).ConsumeMCPOAuthPendingConsent(ctx, dbgen.ConsumeMCPOAuthPendingConsentParams{
		NonceHash: nonceHash, SessionHash: sessionHash, Issuer: issuance.Issuer, Audience: issuance.Audience,
		UserSlug: user.String(), OrganizationSlug: org.String(), AuthenticationVersion: authVersion,
	})
	return PendingConsent{GrantID: row.GrantID, RedirectURI: row.RedirectUri, Challenge: row.Challenge, ClientState: row.ClientState}, mapError(err)
}

func NarrowUserGrantScopes(ctx context.Context, tx postgres.DBTX, grantID int64, allowed []string) error {
	count, err := dbgen.New(tx).NarrowMCPOAuthUserGrantScopes(ctx, dbgen.NarrowMCPOAuthUserGrantScopesParams{
		GrantID: grantID, AllowedScopeKeys: allowed,
	})
	if err != nil {
		return mapError(err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
