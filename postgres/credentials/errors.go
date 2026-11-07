package credentials

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrNotificationConnectionSlugTaken = xerror.AlreadyExists(xerror.Slug)
	ErrCredentialNotFound              = xerror.New(xerror.KindNotFound, "CREDENTIAL_NOT_FOUND", "credential not found")
	ErrOAuthStateInvalid               = xerror.New(xerror.KindInvalidInput, "OAUTH_STATE_INVALID", "oauth state invalid")
	ErrOAuthStateConsumed              = xerror.New(xerror.KindInvalidInput, "OAUTH_STATE_CONSUMED", "oauth state consumed")
	ErrOAuthStateExpired               = xerror.New(xerror.KindInvalidInput, "OAUTH_STATE_EXPIRED", "oauth state expired")
	ErrOAuthIntentConflict             = xerror.New(xerror.KindInvalidInput, "OAUTH_INTENT_CONFLICT", "oauth install no longer matches credential state")
	ErrOAuthGrantConflict              = xerror.New(xerror.KindAlreadyExists, "OAUTH_GRANT_CONFLICT", "oauth grant changed during provider operation")
)
