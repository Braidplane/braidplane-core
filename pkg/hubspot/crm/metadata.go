package crm

import (
	"context"
	"net/http"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	AccountMetadataPath   = "/account-info/v3/details"
	ContactPropertiesPath = "/crm/properties/" + supportedAPIVersion + "/contacts"
)

// ReadContactMetadata uses the caller's restricted HTTP client and fixed
// provider endpoint. Account identity is checked before reading properties.
func ReadContactMetadata(ctx context.Context, transport *http.Client, accessToken string, accountID, maxBytes int64) ([]Property, error) {
	if transport == nil || transport.Transport == nil || strings.TrimSpace(accessToken) == "" || accountID <= 0 {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("HubSpot metadata requires a client, credential, and exact account")
	}
	c := &Client{baseURL: defaultBaseURL, token: accessToken, client: transport}
	account, err := c.AccountDetails(ctx)
	if err != nil {
		return nil, err
	}
	if account.AccountID != accountID {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("HubSpot metadata account does not match")
	}
	return c.ListPropertiesBounded(ctx, "contacts", maxBytes)
}
