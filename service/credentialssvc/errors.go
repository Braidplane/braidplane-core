package credentialssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
)

var (
	ErrInvalidInput          = xerror.ValidationFailed()
	ErrCredentialNotFound    = repocredentials.ErrCredentialNotFound
	ErrOAuthStateInvalid     = repocredentials.ErrOAuthStateInvalid
	ErrOAuthStateConsumed    = repocredentials.ErrOAuthStateConsumed
	ErrOAuthStateExpired     = repocredentials.ErrOAuthStateExpired
	ErrOAuthIntentConflict   = repocredentials.ErrOAuthIntentConflict
	ErrOAuthGrantConflict    = repocredentials.ErrOAuthGrantConflict
	ErrProviderNotConfigured = xerror.New(xerror.KindInvalidInput, "PROVIDER_NOT_CONFIGURED", "provider not configured")
	ErrProviderInvalidGrant  = xerror.New(xerror.KindInvalidInput, "PROVIDER_INVALID_GRANT", "provider invalid grant")
	ErrProviderGrantRevoked  = xerror.New(xerror.KindInvalidInput, "PROVIDER_GRANT_REVOKED", "provider grant revoked")
	ErrProviderUnavailable   = xerror.Unavailable(xerror.DataSource)
)
