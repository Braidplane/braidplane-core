package mcpauthsvc

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/mcpauth"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

const (
	machineClientPrefix   = "mcp-machine"
	machineSecretKind     = "client"
	machineClientLifetime = 90 * 24 * 60 * 60
)

type MachineClientCredentials struct {
	ClientID, ClientSecret string
}

type MachineRegistration struct {
	MachineClientCredentials
	ExpiresAt time.Time
}

// RegisterMachineClient requires an authorized administrator's approval of the exact organization key.
func (s *TokenService) RegisterMachineClient(ctx context.Context, organization, key slug.Slug) (MachineRegistration, error) {
	if organization == "" || key == "" {
		return MachineRegistration{}, ErrInvalidRequest
	}
	id, err := slug.WithRandomSuffix(machineClientPrefix, 32)
	if err != nil {
		return MachineRegistration{}, err
	}
	secret, err := newCredential(machineSecretKind)
	if err != nil {
		return MachineRegistration{}, err
	}
	credentials := MachineClientCredentials{ClientID: id.String(), ClientSecret: secret}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (MachineRegistration, error) {
		expiresAt, err := mcpauth.RegisterMachineClient(ctx, tx, s.issuance(credentials.ClientID),
			organization, key, oauthflow.HashState(secret), machineClientLifetime, authorization.DelegationScopeKeys(authorization.SourceDelegatedMachine))
		if err != nil {
			return MachineRegistration{}, invalidGrantIfMissing(err)
		}
		return MachineRegistration{MachineClientCredentials: credentials, ExpiresAt: expiresAt}, nil
	})
}

// IssueMachineTokens authenticates a pre-registered confidential client scoped to one API key.
func (s *TokenService) IssueMachineTokens(ctx context.Context, credentials MachineClientCredentials, resource string) (TokenPair, error) {
	issuance, err := s.authenticateMachineClient(ctx, credentials, resource)
	if err != nil {
		return TokenPair{}, err
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (TokenPair, error) {
		id, err := mcpauth.CreateMachineGrant(ctx, tx, issuance, grantLifetime)
		if err != nil {
			return TokenPair{}, invalidGrantIfMissing(err)
		}
		tokens, err := issueTokenPair(ctx, tx, id)
		return tokens, invalidGrantIfMissing(err)
	})
}

func (s *TokenService) RotateMachineRefreshToken(ctx context.Context, credentials MachineClientCredentials, token, resource string) (TokenPair, error) {
	issuance, err := s.authenticateMachineClient(ctx, credentials, resource)
	if err != nil {
		return TokenPair{}, err
	}
	return s.exchange(ctx, token, refreshCredential, resource, issuance, nil)
}

func (s *TokenService) AuthenticateMachineAccessToken(ctx context.Context, raw, resource string, organization slug.Slug) (mcpauth.MachinePrincipal, error) {
	hash, ok := credentialHash(raw, accessCredential)
	if !ok || organization == "" || !s.deployment.AcceptsResource(resource) {
		return mcpauth.MachinePrincipal{}, ErrInvalidAccessToken
	}
	principal, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (mcpauth.MachinePrincipal, error) {
		return mcpauth.AuthenticateMachineAccessToken(ctx, db, s.issuance(""), organization, hash)
	})
	if xerror.Is(err, mcpauth.ErrNotFound) {
		return mcpauth.MachinePrincipal{}, ErrInvalidAccessToken
	}
	return principal, err
}

func (s *TokenService) RevokeMachineToken(ctx context.Context, credentials MachineClientCredentials, raw, resource string) error {
	issuance, err := s.authenticateMachineClient(ctx, credentials, resource)
	if err != nil {
		return err
	}
	return s.revokeToken(ctx, raw, resource, issuance)
}

// DisconnectMachineClient requires authorization to administer the selected organization.
func (s *TokenService) DisconnectMachineClient(ctx context.Context, organization, key slug.Slug, clientID string) error {
	if organization == "" || key == "" || !isMachineClientID(clientID) {
		return ErrInvalidRequest
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		err := mcpauth.DisconnectMachineClient(ctx, tx, s.issuance(clientID), organization, key)
		if xerror.Is(err, mcpauth.ErrNotFound) {
			return ErrInvalidClient
		}
		return err
	})
}

func (s *TokenService) authenticateMachineClient(ctx context.Context, credentials MachineClientCredentials, resource string) (mcpauth.Issuance, error) {
	if !s.deployment.AcceptsResource(resource) {
		return mcpauth.Issuance{}, ErrInvalidTarget
	}
	hash, ok := credentialHash(credentials.ClientSecret, machineSecretKind)
	if !ok || !isMachineClientID(credentials.ClientID) {
		return mcpauth.Issuance{}, ErrInvalidClient
	}
	issuance := s.issuance(credentials.ClientID)
	registered, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (string, error) {
		return mcpauth.ReadMachineClientSecretHash(ctx, db, issuance)
	})
	if xerror.Is(err, mcpauth.ErrNotFound) ||
		(err == nil && subtle.ConstantTimeCompare([]byte(hash), []byte(registered)) != 1) {
		return mcpauth.Issuance{}, ErrInvalidClient
	}
	if err != nil {
		return mcpauth.Issuance{}, err
	}
	issuance.Machine = true
	return issuance, nil
}

func isMachineClientID(clientID string) bool {
	return strings.HasPrefix(clientID, machineClientPrefix+"-")
}
