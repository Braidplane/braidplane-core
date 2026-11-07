package memberssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

const adminRoleSlug = slug.Slug("admin")

// ListMembers returns all members of the specified organization.
func (s *service) ListMembers(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.Member], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.Member

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		ms, err := s.membersRepo.ListByOrganization(ctx, tx, orgSlug, queryReq)
		if err != nil {
			return xerror.Wrapf("list members: %w", err)
		}

		ret = toMembers(ms)
		return nil
	})
	if err != nil {
		return model.ListResponse[model.Member]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.Member, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
			case model.ListSortEmail:
				cursorValue = item.Email
			}
			return cursorValue, item.UserSlug.String()
		},
	)

	return model.ListResponse[model.Member]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// CreateMember adds a new member to the organization.
func (s *service) CreateMember(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newMember model.NewMember,
) (*model.Member, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.Member, error) {
		m, err := s.createMember(ctx, tx, orgSlug, newMember)
		if err != nil {
			return nil, xerror.Wrapf("create member in tx: %w", err)
		}
		if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberCreatedEvent{
			member: *m,
		}); err != nil {
			return nil, err
		}
		return m, nil
	})
}

// CreateBootstrapMember adds the initial member seeded during organization creation.
func (s *service) CreateBootstrapMember(
	ctx context.Context,
	orgSlug slug.Slug,
	newMember model.NewMember,
) (*model.Member, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.Member, error) {
		m, err := s.createMember(ctx, tx, orgSlug, newMember)
		if err != nil {
			return nil, xerror.Wrapf("create bootstrap member in tx: %w", err)
		}
		return m, nil
	})
}

func (s *service) createMember(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	newMember model.NewMember,
) (*model.Member, error) {
	if err := validate.Default().Struct(newMember); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	// Ensure user is not already a member of this organization.
	if _, err := s.membersRepo.Find(ctx, tx, orgSlug, newMember.UserSlug); err == nil {
		return nil, ErrMemberAlreadyExists
	} else if !xerror.Is(err, members.ErrNotFound) {
		return nil, xerror.Wrapf("find member: %w", err)
	}

	// Ensure invited user exists.
	if _, err := s.usersService.GetUserBySlug(ctx, newMember.UserSlug); err != nil {
		if xerror.Is(err, userssvc.ErrUserNotFound) {
			return nil, ErrInvalidUserSlug.With(err)
		}
		return nil, xerror.Wrapf("get user by slug: %w", err)
	}

	// Validate role exists in organization (using the same transaction)
	if _, err := s.rolesService.GetRole(ctx, orgSlug, newMember.Role); err != nil {
		return nil, ErrInvalidRole.With(err)
	}

	m, err := s.membersRepo.Create(ctx, tx, &members.CreateParams{
		OrganizationSlug: orgSlug,
		UserSlug:         newMember.UserSlug,
		RoleSlug:         newMember.Role,
	})
	if err != nil {
		return nil, xerror.Wrapf("create member: %w", err)
	}

	return toMember(m), nil
}

// UpdateMember updates the role of an existing member.
func (s *service) UpdateMember(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateMember,
) (*model.Member, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	// Validate role exists in organization
	if _, err := s.rolesService.GetRole(ctx, orgSlug, update.Role); err != nil {
		return nil, ErrInvalidRole.With(err)
	}

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Ownership changes share this lock with member removal. Counting admins
		// without it lets concurrent demotions/removals leave no active owner.
		if _, err := s.orgsRepo.FindBySlugForUpdate(ctx, tx, orgSlug); err != nil {
			return err
		}
		if update.Role != adminRoleSlug {
			member, err := s.membersRepo.Find(ctx, tx, orgSlug, userSlug)
			if err != nil {
				return err
			}
			if member.RoleSlug == adminRoleSlug {
				if err := s.requireOtherAdmin(ctx, tx, orgSlug); err != nil {
					return err
				}
			}
		}
		if err := s.membersRepo.Update(ctx, tx, orgSlug, userSlug, update.Role); err != nil {
			return err
		}
		if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberUpdatedEvent{
			memberSlug: userSlug,
			roleSlug:   update.Role,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, xerror.Wrapf("update member: %w", err)
	}

	return &model.Member{
		UserSlug: userSlug,
		Role:     update.Role,
	}, nil
}

// RemoveMember removes a member from the organization.
func (s *service) RemoveMember(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.orgsRepo.FindBySlugForUpdate(ctx, tx, orgSlug); err != nil {
			return err
		}
		member, err := s.membersRepo.Find(ctx, tx, orgSlug, userSlug)
		if err != nil {
			return xerror.Wrapf("find member: %w", err)
		}

		memberCount, err := s.membersRepo.CountByOrganization(ctx, tx, orgSlug)
		if err != nil {
			return xerror.Wrapf("count members: %w", err)
		}
		if memberCount <= 1 {
			return ErrCannotRemoveLastAdmin
		}

		if member.RoleSlug == adminRoleSlug {
			if err := s.requireOtherAdmin(ctx, tx, orgSlug); err != nil {
				return err
			}
		}

		err = s.membersRepo.Remove(ctx, tx, orgSlug, userSlug)
		if err != nil {
			return xerror.Wrapf("remove member: %w", err)
		}

		if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberDeletedEvent{
			memberSlug: userSlug,
			roleSlug:   member.RoleSlug,
		}); err != nil {
			return err
		}

		return nil
	})
}

func (s *service) requireOtherAdmin(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) error {
	adminCount, err := s.membersRepo.CountByRole(ctx, tx, orgSlug, adminRoleSlug)
	if err != nil {
		return xerror.Wrapf("count admins: %w", err)
	}
	if adminCount <= 1 {
		return ErrCannotRemoveLastAdmin
	}
	return nil
}
