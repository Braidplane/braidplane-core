package mcpauth

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type MachinePrincipal struct {
	APIKey, Organization slug.Slug
	APIKeyID             int64
	ClientID             string
	ScopeKeys            []string
}

func RegisterMachineClient(ctx context.Context, tx postgres.DBTX, issuance Issuance, org, key slug.Slug, hash string, lifetimeSeconds int32, scopeKeys []string) (time.Time, error) {
	expiry, err := dbgen.New(tx).RegisterMCPOAuthMachineClient(ctx, dbgen.RegisterMCPOAuthMachineClientParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID,
		OrganizationSlug: org, ApiKeySlug: key, ClientSecretHash: hash, LifetimeSeconds: lifetimeSeconds,
		OperationScopeKeys: scopeKeys,
	})
	return expiry.Time, mapError(err)
}

func ReadMachineClientSecretHash(ctx context.Context, db postgres.DBTX, issuance Issuance) (string, error) {
	hash, err := dbgen.New(db).ReadMCPOAuthMachineClientSecretHash(ctx, dbgen.ReadMCPOAuthMachineClientSecretHashParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID,
	})
	return hash, mapError(err)
}

func CreateMachineGrant(ctx context.Context, tx postgres.DBTX, issuance Issuance, lifetimeSeconds int32) (int64, error) {
	id, err := dbgen.New(tx).CreateMCPOAuthMachineGrant(ctx, dbgen.CreateMCPOAuthMachineGrantParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID, LifetimeSeconds: lifetimeSeconds,
	})
	return id, mapError(err)
}

func AuthenticateMachineAccessToken(ctx context.Context, db postgres.DBTX, issuance Issuance, org slug.Slug, hash string) (MachinePrincipal, error) {
	row, err := dbgen.New(db).AuthenticateMCPOAuthMachineAccessToken(ctx, dbgen.AuthenticateMCPOAuthMachineAccessTokenParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, OrganizationSlug: org.String(), CredentialHash: hash,
	})
	if err != nil {
		return MachinePrincipal{}, mapError(err)
	}
	return MachinePrincipal{
		APIKey: slug.Slug(row.ApiKeySlug), Organization: slug.Slug(row.OrganizationSlug), APIKeyID: row.ApiKeyID, ClientID: row.ClientID,
		ScopeKeys: row.OperationScopeKeys,
	}, nil
}

func DisconnectMachineClient(ctx context.Context, tx postgres.DBTX, issuance Issuance, org, key slug.Slug) error {
	count, err := dbgen.New(tx).DisconnectMCPOAuthMachineClient(ctx, dbgen.DisconnectMCPOAuthMachineClientParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID, OrganizationSlug: org, ApiKeySlug: key,
	})
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return mapError(err)
}

func CheckMachineAdminSession(ctx context.Context, tx postgres.DBTX, org, user slug.Slug, authenticationVersion int64) error {
	_, err := dbgen.New(tx).CheckMCPOAuthMachineAdminSession(ctx, dbgen.CheckMCPOAuthMachineAdminSessionParams{
		OrganizationSlug: org, UserSlug: user, AuthenticationVersion: authenticationVersion,
	})
	return mapError(err)
}
