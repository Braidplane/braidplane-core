package oidc

import (
	"context"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type Service interface {
	Configured() bool
	ExchangeCodeForToken(ctx context.Context, code, redirectURI string) (*User, error)
	GetAuthURL(ctx context.Context, state, redirectURI string) (string, error)
}

func (s *service) Configured() bool {
	return strings.TrimSpace(s.oauth2Config.ClientID) != ""
}

type User struct {
	ProviderID        string // Provider-specific user ID (sub claim)
	Email             string
	EmailVerified     bool
	Name              string
	Picture           string
	GivenName         string
	FamilyName        string
	Locale            string // BCP 47 language tag
	PreferredUsername string // OIDC preferred_username claim
}

type service struct {
	discoveryGate chan struct{}
	issuerURL     string
	oauth2Config  oauth2.Config
	verifier      *oidc.IDTokenVerifier
}

// New defers provider discovery until authentication is requested.
func New(issuerURL, clientID, clientSecret, redirectURI string) (Service, error) {
	if (strings.TrimSpace(clientID) == "") != (strings.TrimSpace(clientSecret) == "") {
		return nil, xerror.ValidationFailed().Detail("OIDC client ID and secret must be configured together")
	}
	return &service{
		issuerURL:     issuerURL,
		discoveryGate: make(chan struct{}, 1),
		oauth2Config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
}

func (s *service) discover(ctx context.Context) error {
	select {
	case s.discoveryGate <- struct{}{}:
		defer func() { <-s.discoveryGate }()
	case <-ctx.Done():
		return xerror.Unexpected().With(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return xerror.Unexpected().With(err)
	}
	if s.verifier != nil {
		return nil
	}
	if strings.TrimSpace(s.oauth2Config.ClientID) == "" {
		return xerror.ValidationFailed().Detail("OIDC authentication is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, s.issuerURL)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	s.oauth2Config.Endpoint = provider.Endpoint()
	s.verifier = provider.Verifier(&oidc.Config{ClientID: s.oauth2Config.ClientID})
	return nil
}

// ExchangeCodeForToken exchanges an authorization code for user information using OpenID Connect
func (s *service) ExchangeCodeForToken(
	ctx context.Context,
	code, redirectURI string,
) (*User, error) {
	if err := s.discover(ctx); err != nil {
		return nil, err
	}
	oauth2Config := s.oauth2Config
	oauth2Config.RedirectURL = redirectURI

	// Exchange authorization code for token (includes ID token)
	oauth2Token, err := oauth2Config.Exchange(ctx, code)
	if err != nil {
		var providerErr *oauth2.RetrieveError
		if xerror.As(err, &providerErr) {
			err = oauthflow.SafeProviderError(providerErr.ErrorCode, nil)
		}
		return nil, xerror.Unexpected().With(err)
	}

	// Extract ID token from OAuth2 token
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		return nil, xerror.Unexpected().Detail("no id_token in token response")
	}

	// Verify ID token signature and claims
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Extract claims from ID token
	var claims struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		Picture           string `json:"picture"`
		GivenName         string `json:"given_name"`
		FamilyName        string `json:"family_name"`
		Locale            string `json:"locale"`
		PreferredUsername string `json:"preferred_username"`
	}

	if err := idToken.Claims(&claims); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &User{
		ProviderID:        claims.Sub,
		Email:             claims.Email,
		EmailVerified:     claims.EmailVerified,
		Name:              claims.Name,
		Picture:           claims.Picture,
		GivenName:         claims.GivenName,
		FamilyName:        claims.FamilyName,
		Locale:            claims.Locale,
		PreferredUsername: claims.PreferredUsername,
	}, nil
}

// GetAuthURL generates the OpenID Connect authorization URL
func (s *service) GetAuthURL(ctx context.Context, state, redirectURI string) (string, error) {
	if err := s.discover(ctx); err != nil {
		return "", err
	}
	oauth2Config := s.oauth2Config
	oauth2Config.RedirectURL = redirectURI

	return oauth2Config.AuthCodeURL(state, oauth2.AccessTypeOffline), nil
}
