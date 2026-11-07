// Package instanceadminsvc changes the global pre-tenant instance administrator role.
package instanceadminsvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
)

var (
	ErrUserUnverified = xerror.InvalidInput(xerror.User).Detail("user email must be verified before granting instance administrator access")
	ErrLastAdmin      = xerror.InvalidInput(xerror.User).Detail("cannot revoke the last instance administrator")
)

type Service struct {
	txm  postgres.TransactionManager
	repo instanceadmins.Repository
}

func New(txm postgres.TransactionManager, repo instanceadmins.Repository) (*Service, error) {
	if txm == nil || repo == nil {
		return nil, xerror.Unexpected().Detail("instance administrator dependencies are required")
	}
	return &Service{txm: txm, repo: repo}, nil
}

func (s *Service) Grant(ctx context.Context, userSlug slug.Slug) error {
	return s.change(ctx, userSlug, model.RoleSuperAdmin)
}

func (s *Service) Revoke(ctx context.Context, userSlug slug.Slug) error {
	return s.change(ctx, userSlug, model.RoleUser)
}

func (s *Service) change(ctx context.Context, userSlug slug.Slug, role model.Role) error {
	if userSlug == "" {
		return xerror.InvalidRequired(xerror.User)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.LockRoleChanges(ctx, tx); err != nil {
			return err
		}
		user, err := s.repo.FindUserForUpdate(ctx, tx, userSlug)
		if err != nil {
			return err
		}
		if role == model.RoleSuperAdmin && user.VerifiedAt.IsZero() {
			return ErrUserUnverified
		}
		if user.Role == role {
			return nil
		}
		if role == model.RoleUser {
			count, err := s.repo.CountSuperAdmins(ctx, tx)
			if err != nil {
				return err
			}
			if count <= 1 {
				return ErrLastAdmin
			}
		}
		if err := s.repo.SetUserRole(ctx, tx, userSlug, role); err != nil {
			return err
		}
		return s.repo.CreateAudit(ctx, tx, userSlug, user.Role, role)
	})
}
