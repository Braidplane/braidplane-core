package apikeyssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
)

var (
	ErrAPIKeyNotFound      = apikeys.ErrNotFound
	ErrAPIKeyAlreadyExists = apikeys.ErrSlugAlreadyExists
	ErrInvalidInput        = xerror.ValidationFailed()
	ErrInvalidToken        = xerror.InvalidCredentials()
	ErrAPIKeyNotRevoked    = xerror.InvalidInput(xerror.APIKey).
				WithPublicDetail("API key must be revoked before deletion.")
)
