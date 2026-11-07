// Package snowflakewif validates exact Snowflake workload identity claims.
package snowflakewif

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var accountPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-.][a-z0-9]+)*$`)

// Identity contains only the public claims needed to establish Snowflake trust.
type Identity struct {
	Issuer   string
	Subject  spiffeid.ID
	Audience string
}

// NewIdentity validates public claims; it does not authorize token issuance.
func NewIdentity(issuerURL string, organization, dataSource slug.Slug, account string) (Identity, error) {
	issuer, err := url.Parse(issuerURL)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil ||
		issuer.Port() != "" || issuer.Path != "" || issuer.RawQuery != "" || issuer.Fragment != "" {
		return Identity{}, invalidIdentity()
	}
	trustDomain, err := spiffeid.TrustDomainFromString(issuer.Host)
	if err != nil {
		return Identity{}, invalidIdentity()
	}
	_, orgErr := slug.New(organization.String())
	_, sourceErr := slug.New(dataSource.String())
	if orgErr != nil || sourceErr != nil || !accountPattern.MatchString(account) || strings.HasSuffix(account, ".snowflakecomputing.com") {
		return Identity{}, invalidIdentity()
	}
	subject, err := spiffeid.FromPath(trustDomain, "/tenant/"+organization.String()+"/snowflake/"+account+"/connection/"+dataSource.String())
	if err != nil {
		return Identity{}, invalidIdentity()
	}
	return Identity{Issuer: issuerURL, Subject: subject, Audience: "https://" + account + ".snowflakecomputing.com"}, nil
}

// Token obtains and verifies a fresh JWT-SVID through the local attested Workload
// API. Neither JWTs nor upstream errors containing claims escape as diagnostics.
func Token(ctx context.Context, socketPath string, identity Identity) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := workloadapi.New(ctx, workloadapi.WithAddr("unix://"+socketPath))
	if err != nil {
		return "", identityUnavailable()
	}
	defer func() { _ = client.Close() }()
	svid, err := client.FetchJWTSVID(ctx, jwtsvid.Params{Subject: identity.Subject, Audience: identity.Audience})
	if err != nil {
		return "", identityUnavailable()
	}
	bundles, err := client.FetchJWTBundles(ctx)
	if err != nil {
		return "", identityUnavailable()
	}
	return validateToken(svid.Marshal(), bundles, identity)
}

func validateToken(token string, bundles jwtbundle.Source, identity Identity) (string, error) {
	svid, err := jwtsvid.ParseAndValidate(token, bundles, []string{identity.Audience})
	if err != nil {
		return "", identityUnavailable()
	}
	iat, ok := svid.Claims["iat"].(float64)
	now := time.Now()
	issued := time.Unix(int64(iat), 0)
	if !ok || svid.ID != identity.Subject || svid.Claims["iss"] != identity.Issuer ||
		len(svid.Audience) != 1 || svid.Audience[0] != identity.Audience ||
		issued.After(now.Add(30*time.Second)) || !svid.Expiry.After(now.Add(10*time.Second)) ||
		!svid.Expiry.After(issued) || svid.Expiry.Sub(issued) > 5*time.Minute {
		return "", identityUnavailable()
	}
	return token, nil
}

func unavailable(detail string) error {
	return xerror.Unavailable(xerror.DataSource).WithPublicDetail(detail).Detail(detail)
}

func invalidIdentity() error {
	return unavailable("Snowflake workload identity claims are invalid. Check the issuer and exact saved connection account.")
}

func identityUnavailable() error {
	return unavailable("Snowflake runtime identity is unavailable or invalid. Ask your operator to check the connection registration and issuer health.")
}
