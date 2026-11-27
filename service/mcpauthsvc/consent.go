package mcpauthsvc

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/mcpauth"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

const consentLifetime = 5 * 60

type BrowserIdentity struct {
	User, Organization    slug.Slug
	SessionID             string
	AuthenticationVersion int64
	AuthenticatedAt       time.Time
}

func (identity BrowserIdentity) valid() bool {
	return identity.User != "" && identity.SessionID != "" && identity.AuthenticationVersion > 0
}

// BeginUserConsent pins the verified identity before returning a one-use browser prompt.
func (s *TokenService) BeginUserConsent(ctx context.Context, request AuthorizationRequest, state string, browser BrowserIdentity) (model.MCPConsentPrompt, error) {
	if !browser.valid() {
		return model.MCPConsentPrompt{}, ErrInvalidGrant
	}
	if len(state) > 2048 || strings.ContainsFunc(state, func(r rune) bool { return r < ' ' || r > '~' }) {
		return model.MCPConsentPrompt{}, ErrInvalidRequest
	}
	client, binding, err := s.deployment.ValidateAuthorization(ctx, request)
	if err != nil {
		return model.MCPConsentPrompt{}, err
	}
	redirect, err := url.Parse(binding.redirectURI)
	if err != nil {
		return model.MCPConsentPrompt{}, xerror.Unexpected().Detail("invalid verified redirect URI")
	}
	nonce, err := oauthflow.RandomState()
	if err != nil {
		return model.MCPConsentPrompt{}, err
	}
	var scopeKeys []string
	if browser.Organization != "" {
		scopeKeys = authorization.DelegationScopeKeys(authorization.SourceDelegatedUser)
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (model.MCPConsentPrompt, error) {
		id, err := mcpauth.CreateUserGrant(ctx, tx, s.issuance(binding.clientID),
			browser.User, browser.Organization, browser.AuthenticationVersion, grantLifetime, scopeKeys)
		if err != nil {
			return model.MCPConsentPrompt{}, invalidGrantIfMissing(err)
		}
		if err := mcpauth.CreatePendingConsent(ctx, tx, mcpauth.PendingConsent{
			GrantID: id, RedirectURI: binding.redirectURI, Challenge: binding.challenge, ClientState: state,
		}, oauthflow.HashState(nonce), oauthflow.HashState(browser.SessionID), consentLifetime); err != nil {
			return model.MCPConsentPrompt{}, err
		}
		return model.MCPConsentPrompt{
			ID: nonce, ClientID: client.ID, ClientName: client.Name, RedirectOrigin: redirect.Scheme + "://" + redirect.Host,
			Resource: binding.resource, User: browser.User, Organization: browser.Organization,
			Operations: authorization.DelegationScopeDescriptions(scopeKeys),
		}, nil
	})
}

// CompleteUserConsent consumes the prompt and issues a code in the same transaction.
// The browser boundary must validate CSRF before calling it.
func (s *TokenService) CompleteUserConsent(ctx context.Context, nonce string, browser BrowserIdentity, approve bool) (string, error) {
	if !browser.valid() || !validBase64URL32(nonce) {
		return "", ErrInvalidGrant
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (string, error) {
		issuance := s.issuance("")
		pending, err := mcpauth.ConsumePendingConsent(ctx, tx, issuance, oauthflow.HashState(nonce), oauthflow.HashState(browser.SessionID),
			browser.User, browser.Organization, browser.AuthenticationVersion)
		if err != nil {
			return "", invalidGrantIfMissing(err)
		}
		response := url.Values{"iss": {issuance.Issuer}}
		if pending.ClientState != "" {
			response.Set("state", pending.ClientState)
		}
		if approve {
			code, err := issueConsentCode(ctx, tx, pending)
			if err != nil {
				return "", invalidGrantIfMissing(err)
			}
			response.Set("code", code)
		} else {
			if err := mcpauth.RevokeGrant(ctx, tx, pending.GrantID); err != nil {
				return "", err
			}
			response.Set("error", "access_denied")
		}
		callback, err := url.Parse(pending.RedirectURI)
		if err != nil {
			return "", xerror.Unexpected().Detail("invalid stored redirect URI")
		}
		if callback.RawQuery != "" {
			callback.RawQuery += "&"
		}
		callback.RawQuery += response.Encode()
		return callback.String(), nil
	})
}

func issueConsentCode(ctx context.Context, tx postgres.DBTX, pending mcpauth.PendingConsent) (string, error) {
	code, err := newCredential(codeCredential)
	if err != nil {
		return "", err
	}
	_, err = mcpauth.CreateCredential(ctx, tx, mcpauth.Credential{
		Hash: oauthflow.HashState(code), GrantID: pending.GrantID, Kind: codeCredential, LifetimeSeconds: codeLifetime,
		RedirectURI: pending.RedirectURI, Challenge: pending.Challenge,
	})
	if err != nil {
		return "", err
	}
	return code, nil
}
