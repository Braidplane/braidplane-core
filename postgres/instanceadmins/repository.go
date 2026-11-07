package instanceadmins

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type User struct {
	Slug       slug.Slug
	Role       model.Role
	VerifiedAt time.Time
}

type Repository interface {
	LockRoleChanges(context.Context, postgres.DBTX) error
	FindUserForUpdate(context.Context, postgres.DBTX, slug.Slug) (*User, error)
	CountSuperAdmins(context.Context, postgres.DBTX) (int64, error)
	SetUserRole(context.Context, postgres.DBTX, slug.Slug, model.Role) error
	CreateAudit(context.Context, postgres.DBTX, slug.Slug, model.Role, model.Role) error
}
