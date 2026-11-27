package mcpauth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var ErrNotFound = xerror.New(xerror.KindNotFound, "MCP_OAUTH_GRANT_MISSING", "OAuth grant is unavailable")

type Issuance struct {
	Issuer   string
	Audience string
	ClientID string
	Machine  bool
}

type UserPrincipal struct {
	User, Organization slug.Slug
	UserID, MemberID   int32
	ClientID           string
	ScopeKeys          []string
}

type Credential struct {
	Hash, Kind, RedirectURI, Challenge string
	GrantID                            int64
	LifetimeSeconds                    int32
}

type CredentialState struct {
	RedirectURI, Challenge string
	Used, Unexpired        bool
}

func CreateUserGrant(ctx context.Context, tx postgres.DBTX, issuance Issuance, user, org slug.Slug, authVersion int64, lifetimeSeconds int32, scopeKeys []string) (int64, error) {
	id, err := dbgen.New(tx).CreateMCPOAuthUserGrant(ctx, dbgen.CreateMCPOAuthUserGrantParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID,
		UserSlug: user.String(), OrganizationSlug: org.String(), AuthenticationVersion: authVersion, LifetimeSeconds: lifetimeSeconds,
		OperationScopeKeys: scopeKeys,
	})
	return id, mapError(err)
}

func CreateCredential(ctx context.Context, tx postgres.DBTX, credential Credential) (time.Time, error) {
	expiry, err := dbgen.New(tx).CreateMCPOAuthCredential(ctx, dbgen.CreateMCPOAuthCredentialParams{
		GrantID: credential.GrantID, Kind: credential.Kind, CredentialHash: credential.Hash, RedirectUri: credential.RedirectURI,
		Challenge: credential.Challenge, LifetimeSeconds: credential.LifetimeSeconds,
	})
	return expiry.Time, mapError(err)
}

func LockGrant(ctx context.Context, tx postgres.DBTX, issuance Issuance, hash, kind string) (int64, error) {
	id, err := dbgen.New(tx).LockMCPOAuthGrant(ctx, dbgen.LockMCPOAuthGrantParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID, CredentialHash: hash, Kind: kind,
		Machine: issuance.Machine,
	})
	return id, mapError(err)
}

func CheckActiveGrant(ctx context.Context, tx postgres.DBTX, id int64) error {
	_, err := dbgen.New(tx).CheckActiveMCPOAuthGrant(ctx, id)
	return mapError(err)
}

func ReadCredential(ctx context.Context, tx postgres.DBTX, id int64, hash string) (CredentialState, error) {
	row, err := dbgen.New(tx).ReadMCPOAuthCredential(ctx, dbgen.ReadMCPOAuthCredentialParams{GrantID: id, CredentialHash: hash})
	return CredentialState{RedirectURI: row.RedirectUri, Challenge: row.Challenge, Used: row.Used.Bool, Unexpired: row.Unexpired}, mapError(err)
}

func ConsumeCredential(ctx context.Context, tx postgres.DBTX, id int64, hash string) error {
	count, err := dbgen.New(tx).ConsumeMCPOAuthCredential(ctx, dbgen.ConsumeMCPOAuthCredentialParams{GrantID: id, CredentialHash: hash})
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return mapError(err)
}

func AuthenticateAccessToken(ctx context.Context, db postgres.DBTX, issuer, audience, hash string) (UserPrincipal, error) {
	row, err := dbgen.New(db).AuthenticateMCPOAuthAccessToken(ctx, dbgen.AuthenticateMCPOAuthAccessTokenParams{
		Issuer: issuer, Audience: audience, CredentialHash: hash,
	})
	if err != nil {
		return UserPrincipal{}, mapError(err)
	}
	return UserPrincipal{
		User: slug.Slug(row.UserSlug), Organization: slug.Slug(row.OrganizationSlug), ClientID: row.ClientID,
		UserID: row.UserID, MemberID: row.MemberID,
		ScopeKeys: row.OperationScopeKeys,
	}, nil
}

func RevokeGrant(ctx context.Context, tx postgres.DBTX, id int64) error {
	return mapError(dbgen.New(tx).RevokeMCPOAuthGrant(ctx, id))
}

func DisconnectUserClient(ctx context.Context, tx postgres.DBTX, issuance Issuance, user, org slug.Slug) error {
	return mapError(dbgen.New(tx).DisconnectMCPOAuthUserClient(ctx, dbgen.DisconnectMCPOAuthUserClientParams{
		Issuer: issuance.Issuer, Audience: issuance.Audience, ClientID: issuance.ClientID, UserSlug: user.String(), OrganizationSlug: org.String(),
	}))
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return xerror.Unexpected().With(err)
}
