package patssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/pats"
)

var (
	ErrPersonalAccessTokenNotFound      = pats.ErrNotFound
	ErrPersonalAccessTokenAlreadyExists = pats.ErrSlugAlreadyExists
	ErrInvalidInput                     = xerror.ValidationFailed()
	ErrInvalidToken                     = xerror.InvalidCredentials()
	ErrPersonalAccessTokenRevoked       = xerror.InvalidInput(xerror.PersonalAccessToken).
						WithPublicDetail("Personal access token must be active to update.")
	ErrPersonalAccessTokenNotRevoked = xerror.InvalidInput(xerror.PersonalAccessToken).
						WithPublicDetail("Personal access token must be revoked before deletion.")
)
