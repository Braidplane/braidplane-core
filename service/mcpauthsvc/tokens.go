package mcpauthsvc

import (
	"context"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/mcpauth"
)

const (
	codeLifetime    = 5 * 60
	accessLifetime  = 10 * 60
	refreshLifetime = 7 * 24 * 60 * 60
	grantLifetime   = 30 * 24 * 60 * 60

	codeCredential    = "code"
	accessCredential  = "access"
	refreshCredential = "refresh"
)

var ErrInvalidAccessToken = xerror.New(xerror.KindUnauthorized, "MCP_OAUTH_INVALID_TOKEN", "OAuth access token was rejected")

type TokenService struct {
	deployment *Deployment
	txm        postgres.TransactionManager
}

func NewTokenService(deployment *Deployment, txm postgres.TransactionManager) (*TokenService, error) {
	if deployment == nil || deployment.issuer == nil || deployment.resource == nil || txm == nil {
		return nil, xerror.Unexpected().Detail("MCP token service requires a configured deployment and transaction manager")
	}
	return &TokenService{deployment: deployment, txm: txm}, nil
}

func (s *TokenService) Deployment() *Deployment {
	return s.deployment
}

type CodeRedemption struct {
	Code, ClientID, RedirectURI, Resource, Verifier string
}

type TokenPair struct {
	AccessToken, RefreshToken string
	AccessExpiresAt           time.Time
}

func (s *TokenService) RedeemUserCode(ctx context.Context, request CodeRedemption) (TokenPair, error) {
	issuance := s.issuance(request.ClientID)
	return s.exchange(ctx, request.Code, codeCredential, request.Resource, issuance, func(state mcpauth.CredentialState) error {
		binding := CodeBinding{
			issuer: issuance.Issuer, resource: issuance.Audience, clientID: issuance.ClientID,
			redirectURI: state.RedirectURI, challenge: state.Challenge,
		}
		return s.deployment.ValidateCodeExchange(binding, request.ClientID, request.RedirectURI, request.Resource, request.Verifier)
	})
}

func (s *TokenService) RotateUserRefreshToken(ctx context.Context, token, clientID, resource string) (TokenPair, error) {
	return s.exchange(ctx, token, refreshCredential, resource, s.issuance(clientID), nil)
}

func (s *TokenService) exchange(
	ctx context.Context, raw, kind, resource string, issuance mcpauth.Issuance, validate func(mcpauth.CredentialState) error,
) (TokenPair, error) {
	if !s.deployment.AcceptsResource(resource) {
		return TokenPair{}, ErrInvalidTarget
	}
	hash, ok := credentialHash(raw, kind)
	if !ok || issuance.ClientID == "" {
		return TokenPair{}, ErrInvalidGrant
	}
	type outcome struct {
		tokens TokenPair
		replay bool
	}
	// Own the transaction so the replay response cannot roll back family revocation.
	result, err := postgres.InTx(postgres.WithoutTransaction(ctx), s.txm, func(ctx context.Context, tx postgres.DBTX) (outcome, error) {
		id, err := mcpauth.LockGrant(ctx, tx, issuance, hash, kind)
		if err != nil {
			return outcome{}, err
		}
		if err := mcpauth.CheckActiveGrant(ctx, tx, id); err != nil {
			return outcome{}, err
		}
		// Read after taking the family lock so a waiting exchange sees the latest consumption.
		state, err := mcpauth.ReadCredential(ctx, tx, id, hash)
		if err != nil {
			return outcome{}, err
		}
		if validate != nil {
			if err := validate(state); err != nil {
				return outcome{}, err
			}
		}
		if state.Used {
			return outcome{replay: true}, mcpauth.RevokeGrant(ctx, tx, id)
		}
		if !state.Unexpired {
			return outcome{}, ErrInvalidGrant
		}
		if err := mcpauth.ConsumeCredential(ctx, tx, id, hash); err != nil {
			return outcome{}, err
		}
		tokens, err := issueTokenPair(ctx, tx, id)
		return outcome{tokens: tokens}, err
	})
	if err != nil {
		return TokenPair{}, invalidGrantIfMissing(err)
	}
	if result.replay {
		return TokenPair{}, ErrInvalidGrant
	}
	return result.tokens, nil
}

func issueTokenPair(ctx context.Context, tx postgres.DBTX, id int64) (TokenPair, error) {
	access, err := newCredential(accessCredential)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, err := newCredential(refreshCredential)
	if err != nil {
		return TokenPair{}, err
	}
	expiresAt, err := mcpauth.CreateCredential(ctx, tx, mcpauth.Credential{
		Hash: oauthflow.HashState(access), GrantID: id, Kind: accessCredential, LifetimeSeconds: accessLifetime,
	})
	if err != nil {
		return TokenPair{}, err
	}
	_, err = mcpauth.CreateCredential(ctx, tx, mcpauth.Credential{
		Hash: oauthflow.HashState(refresh), GrantID: id, Kind: refreshCredential, LifetimeSeconds: refreshLifetime,
	})
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: refresh, AccessExpiresAt: expiresAt}, nil
}

// AuthenticateUserAccessToken returns an identity for the exact operation tenant.
// The caller still needs a scoped authorization decision for the operation.
func (s *TokenService) AuthenticateUserAccessToken(ctx context.Context, raw, resource string, organization slug.Slug) (mcpauth.UserPrincipal, error) {
	hash, ok := credentialHash(raw, accessCredential)
	if !ok || !s.deployment.AcceptsResource(resource) {
		return mcpauth.UserPrincipal{}, ErrInvalidAccessToken
	}
	issuance := s.issuance("")
	principal, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (mcpauth.UserPrincipal, error) {
		return mcpauth.AuthenticateAccessToken(ctx, db, issuance.Issuer, issuance.Audience, hash)
	})
	if xerror.Is(err, mcpauth.ErrNotFound) || (err == nil && principal.Organization != organization) {
		return mcpauth.UserPrincipal{}, ErrInvalidAccessToken
	}
	return principal, err
}

// RevokeUserToken accepts an access or refresh credential and revokes its entire grant.
func (s *TokenService) RevokeUserToken(ctx context.Context, raw, clientID, resource string) error {
	return s.revokeToken(ctx, raw, resource, s.issuance(clientID))
}

func (s *TokenService) revokeToken(ctx context.Context, raw, resource string, issuance mcpauth.Issuance) error {
	if !s.deployment.AcceptsResource(resource) {
		return ErrInvalidTarget
	}
	if issuance.ClientID == "" {
		return ErrInvalidClient
	}
	kind := accessCredential
	hash, ok := credentialHash(raw, kind)
	if !ok {
		kind = refreshCredential
		hash, ok = credentialHash(raw, kind)
	}
	if !ok {
		return nil
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		id, err := mcpauth.LockGrant(ctx, tx, issuance, hash, kind)
		if xerror.Is(err, mcpauth.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return mcpauth.RevokeGrant(ctx, tx, id)
	})
}

// DisconnectUserClient revokes grants for the authenticated user's specified tenant and client.
func (s *TokenService) DisconnectUserClient(ctx context.Context, user, organization slug.Slug, clientID string) error {
	if user == "" || clientID == "" {
		return ErrInvalidRequest
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return mcpauth.DisconnectUserClient(ctx, tx, s.issuance(clientID), user, organization)
	})
}

func (s *TokenService) issuance(clientID string) mcpauth.Issuance {
	return mcpauth.Issuance{Issuer: s.deployment.issuer.String(), Audience: s.deployment.resource.String(), ClientID: clientID}
}

func newCredential(kind string) (string, error) {
	value, err := oauthflow.RandomState()
	if err != nil {
		return "", err
	}
	return "bpmcp_" + kind + "_" + value, nil
}

func credentialHash(raw, kind string) (string, bool) {
	value, found := strings.CutPrefix(raw, "bpmcp_"+kind+"_")
	if !found || !validBase64URL32(value) {
		return "", false
	}
	return oauthflow.HashState(raw), true
}

func invalidGrantIfMissing(err error) error {
	if xerror.Is(err, mcpauth.ErrNotFound) {
		return ErrInvalidGrant
	}
	return err
}
