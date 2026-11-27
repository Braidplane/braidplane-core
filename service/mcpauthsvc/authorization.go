// Package mcpauthsvc owns Core's deployment-local MCP OAuth decisions.
package mcpauthsvc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrInvalidClient  = xerror.New(xerror.KindInvalidInput, "MCP_OAUTH_INVALID_CLIENT", "OAuth client was rejected")
	ErrInvalidTarget  = xerror.New(xerror.KindInvalidInput, "MCP_OAUTH_INVALID_TARGET", "OAuth resource was rejected")
	ErrInvalidRequest = xerror.New(xerror.KindInvalidInput, "MCP_OAUTH_INVALID_REQUEST", "OAuth request was rejected")
	ErrInvalidGrant   = xerror.New(xerror.KindInvalidInput, "MCP_OAUTH_INVALID_GRANT", "OAuth grant was rejected")
)

type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
}

type Deployment struct {
	issuer, resource *url.URL
	clients          map[string]Client
	metadataHTTP     *http.Client
}

// New uses deployment configuration for issuer and audience. Both URLs require HTTPS.
func New(issuer, resource string, clients []Client) (*Deployment, error) {
	issuerURL, ok := deploymentURL(issuer)
	if !ok {
		return nil, ErrInvalidRequest.Detail("invalid issuer URL")
	}
	resourceURL, ok := deploymentURL(resource)
	if !ok {
		return nil, ErrInvalidTarget
	}
	registered := make(map[string]Client, len(clients))
	for _, client := range clients {
		if _, found := registered[client.ID]; found || isMachineClientID(client.ID) || !validClient(client) {
			return nil, ErrInvalidClient.Detail("invalid or duplicate pre-registered client")
		}
		client.RedirectURIs = slices.Clone(client.RedirectURIs)
		registered[client.ID] = client
	}
	return &Deployment{
		issuer: issuerURL, resource: resourceURL, clients: registered, metadataHTTP: newMetadataHTTPClient(),
	}, nil
}

type AuthorizationRequest struct {
	ClientID, RedirectURI, Resource, ResponseType, Challenge, ChallengeMethod string
}

// CodeBinding is the validated protocol state to store with an approved code.
type CodeBinding struct {
	issuer, clientID, redirectURI, resource, challenge string
}

// ValidateAuthorization checks protocol inputs before consent or code issuance.
func (d *Deployment) ValidateAuthorization(ctx context.Context, request AuthorizationRequest) (Client, CodeBinding, error) {
	if !d.AcceptsResource(request.Resource) {
		return Client{}, CodeBinding{}, ErrInvalidTarget
	}
	if request.ResponseType != "code" || request.ChallengeMethod != oauthflow.PKCES256 || !validBase64URL32(request.Challenge) {
		return Client{}, CodeBinding{}, ErrInvalidRequest
	}
	client, err := d.resolveClient(ctx, request.ClientID)
	if err != nil {
		return Client{}, CodeBinding{}, err
	}
	if !slices.Contains(client.RedirectURIs, request.RedirectURI) {
		return Client{}, CodeBinding{}, ErrInvalidRequest.Detail("redirect URI is not registered")
	}
	return client, CodeBinding{
		issuer: d.issuer.String(), clientID: client.ID, redirectURI: request.RedirectURI,
		resource: d.resource.String(), challenge: request.Challenge,
	}, nil
}

// ValidateCodeExchange checks the binding loaded from trusted code storage.
// The caller must check expiry and consume that code atomically with issuance.
func (d *Deployment) ValidateCodeExchange(binding CodeBinding, clientID, redirectURI, resource, verifier string) error {
	if !d.AcceptsResource(resource) {
		return ErrInvalidTarget
	}
	if binding.issuer != d.issuer.String() || binding.resource != d.resource.String() ||
		binding.clientID == "" || binding.clientID != clientID || binding.redirectURI == "" || binding.redirectURI != redirectURI ||
		!oauthflow.ValidPKCEVerifier(verifier) ||
		subtle.ConstantTimeCompare([]byte(binding.challenge), []byte(oauthflow.S256Challenge(verifier))) != 1 {
		return ErrInvalidGrant
	}
	return nil
}

// AcceptsResource compares the full deployment resource, folding scheme and host only.
func (d *Deployment) AcceptsResource(raw string) bool {
	resource, ok := deploymentURL(raw)
	return ok && resource.String() == d.resource.String()
}

func deploymentURL(raw string) (*url.URL, bool) {
	u, ok := webURL(raw)
	if !ok || u.Scheme != "https" || u.ForceQuery || u.RawQuery != "" || hasDotSegment(u.Path) {
		return nil, false
	}
	u.Host = strings.ToLower(u.Host)
	return u, true
}

func webURL(raw string) (*url.URL, bool) {
	if len(raw) > 2048 || strings.ContainsAny(raw, "#\\") || strings.ContainsFunc(raw, func(r rune) bool {
		return r <= ' ' || r >= 127
	}) {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.Contains(u.Hostname(), "%") {
		return nil, false
	}
	return u, true
}

func validClient(client Client) bool {
	if client.ID == "" || len(client.ID) > 2048 || strings.TrimSpace(client.Name) == "" || len(client.RedirectURIs) == 0 {
		return false
	}
	for _, redirect := range client.RedirectURIs {
		u, ok := webURL(redirect)
		if !ok || (u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname()))) {
			return false
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Has("code") || query.Has("state") || query.Has("error") ||
			query.Has("error_description") || query.Has("error_uri") || query.Has("iss") {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func hasDotSegment(path string) bool {
	segments := strings.Split(path, "/")
	return slices.Contains(segments, ".") || slices.Contains(segments, "..")
}

func validBase64URL32(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
