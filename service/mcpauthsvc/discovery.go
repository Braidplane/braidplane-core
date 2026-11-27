package mcpauthsvc

import (
	"net/url"

	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
)

type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	BearerMethods        []string `json:"bearer_methods_supported"`
}

type IssuerMetadata struct {
	Issuer                               string   `json:"issuer"`
	AuthorizationEndpoint                string   `json:"authorization_endpoint"`
	TokenEndpoint                        string   `json:"token_endpoint"`
	RevocationEndpoint                   string   `json:"revocation_endpoint"`
	ResponseTypes                        []string `json:"response_types_supported"`
	GrantTypes                           []string `json:"grant_types_supported"`
	TokenEndpointAuthMethods             []string `json:"token_endpoint_auth_methods_supported"`
	RevocationAuthMethods                []string `json:"revocation_endpoint_auth_methods_supported"`
	CodeChallengeMethods                 []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataSupported            bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseIssuerSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

func (d *Deployment) ProtectedResourceDiscovery() (string, ProtectedResourceMetadata) {
	return wellKnownURL(d.resource, "oauth-protected-resource"), ProtectedResourceMetadata{
		Resource: d.resource.String(), AuthorizationServers: []string{d.issuer.String()},
		BearerMethods: []string{"header"},
	}
}

func (d *Deployment) IssuerDiscovery() (string, IssuerMetadata) {
	return wellKnownURL(d.issuer, "oauth-authorization-server"), IssuerMetadata{
		Issuer: d.issuer.String(), AuthorizationEndpoint: d.issuer.JoinPath("authorize").String(),
		TokenEndpoint: d.issuer.JoinPath("token").String(), RevocationEndpoint: d.issuer.JoinPath("revoke").String(),
		ResponseTypes: []string{"code"}, GrantTypes: []string{"authorization_code", "refresh_token", "client_credentials"},
		TokenEndpointAuthMethods: []string{"none", "client_secret_basic"},
		RevocationAuthMethods:    []string{"none", "client_secret_basic"},
		CodeChallengeMethods:     []string{oauthflow.PKCES256}, ClientIDMetadataSupported: true,
		AuthorizationResponseIssuerSupported: true,
	}
}

func wellKnownURL(base *url.URL, suffix string) string {
	u := *base
	prefix := "/.well-known/" + suffix
	if u.Path == "/" {
		u.Path, u.RawPath = prefix, ""
	} else {
		u.Path = prefix + u.Path
		if u.RawPath != "" {
			u.RawPath = prefix + u.RawPath
		}
	}
	return u.String()
}
