package credentialssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func credentialScope(tenant, record slug.Slug, purpose string) crypto.CredentialScope {
	return crypto.CredentialScope{Tenant: tenant.String(), Resource: record.String(), Purpose: purpose, SchemaVersion: 1}
}

// Creating a grant activates its existing pending record; rotation inserts a new one.
func initialGrantSlug(install CredentialInstall) slug.Slug {
	if install.Intent == OAuthInstallIntentCreate {
		return install.ExpectedCredentialSlug
	}
	return ""
}
