package instanceadminsvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

// Bootstrapper is local operator provisioning, never an HTTP registration path.
type Bootstrapper struct {
	txm    postgres.TransactionManager
	admins instanceadmins.Repository
	users  users.Repository
	hasher crypto.Hasher
}

func NewBootstrapper(txm postgres.TransactionManager, admins instanceadmins.Repository, userRepo users.Repository, hasher crypto.Hasher) (*Bootstrapper, error) {
	if txm == nil || admins == nil || userRepo == nil || hasher == nil {
		return nil, xerror.Unexpected().Detail("bootstrap dependencies are required")
	}
	return &Bootstrapper{txm: txm, admins: admins, users: userRepo, hasher: hasher}, nil
}

// Bootstrap requires the database operator to have independently verified email ownership.
func (s *Bootstrapper) Bootstrap(ctx context.Context, userSlug slug.Slug, name, email, password string) error {
	if _, err := slug.New(userSlug.String()); err != nil {
		return err
	}
	input := model.NewUser{FullName: name, Email: email, Password: password, Role: model.RoleUser}
	if err := validate.Default().Struct(input); err != nil {
		return xerror.ValidationFailed().Detail("invalid bootstrap account name, email or password")
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.admins.LockRoleChanges(ctx, tx); err != nil {
			return err
		}
		count, err := s.admins.CountSuperAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if count != 0 {
			return xerror.ValidationFailed().Detail("instance administrator already exists; bootstrap is closed")
		}
		if _, err := s.users.CreateUser(ctx, tx, &users.CreateParams{Slug: userSlug, FullName: name, Email: email, Password: hash, Role: model.RoleUser, Locale: "en"}); err != nil {
			return err
		}
		if err := s.users.MarkEmailAsVerified(ctx, tx, userSlug, time.Now()); err != nil {
			return err
		}
		if err := s.admins.SetUserRole(ctx, tx, userSlug, model.RoleSuperAdmin); err != nil {
			return err
		}
		return s.admins.CreateAudit(ctx, tx, userSlug, model.RoleUser, model.RoleSuperAdmin)
	})
}
