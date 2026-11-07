package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// TableMetadataPath is also used by the Proof transport's exact route allowlist.
func TableMetadataPath(req GetTableRequest) string {
	return "/bigquery/v2/projects/" + url.PathEscape(req.ProjectID) +
		"/datasets/" + url.PathEscape(req.DatasetID) + "/tables/" + url.PathEscape(req.TableID)
}

// ReadTableMetadata uses fixed provider/authentication endpoints and the caller's
// restricted client. Service-account token_uri cannot widen metadata egress.
func ReadTableMetadata(ctx context.Context, transport *http.Client, creds Credentials, req GetTableRequest) (*TableMetadata, error) {
	if transport == nil || transport.Transport == nil {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("BigQuery metadata requires a restricted client")
	}
	account, err := parseCredentials(creds)
	if err != nil {
		return nil, err
	}
	c := &Client{baseURL: defaultBaseURL, tokenURL: defaultTokenURL, scope: "https://www.googleapis.com/auth/bigquery.readonly", serviceAccount: account, accessTokenOnly: creds.AccessToken, client: transport}
	if err := c.initSDK(ctx); err != nil {
		return nil, err
	}
	return c.GetTable(ctx, req)
}
