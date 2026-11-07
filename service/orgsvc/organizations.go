package orgsvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

const adminRole = "admin"

func (s *service) DeleteOrganization(ctx context.Context, orgSlug slug.Slug) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Pin the tenant against concurrent child creation until deletion commits.
		if _, err := s.orgsRepo.FindBySlugForUpdate(ctx, tx, orgSlug); err != nil {
			return xerror.Wrapf("lock organization for deletion: %w", err)
		}
		blocked, err := s.orgsRepo.DeletionBlocked(ctx, tx, orgSlug)
		if err != nil {
			return xerror.Wrapf("check organization deletion: %w", err)
		}
		if blocked {
			return ErrDeletionBlocked
		}
		if s.beforeDelete != nil {
			if err := s.beforeDelete(ctx, tx, orgSlug); err != nil {
				return err
			}
		}
		return s.orgsRepo.Delete(ctx, tx, orgSlug)
	})
}

// CreateOrganization creates a new organization and adds the caller as an admin member.
func (s *service) CreateOrganization(
	ctx context.Context,
	userSlug slug.Slug,
	actor model.EventActor,
	newOrg model.NewOrganization,
) (*model.Organization, error) {
	if err := validate.Default().Struct(newOrg); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	orgSlug, err := newOrg.Slug.ValidateOrGenerate(newOrg.Name)
	if err != nil {
		return nil, ErrInvalidOrgSlug.With(err)
	}

	// Check if slug exists and append suffix once if it does (single attempt)
	var ret *model.Organization
	txErr := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Pre-check if slug exists
		if existingOrg, err := s.orgsRepo.FindBySlug(
			ctx,
			tx,
			orgSlug,
		); err == nil &&
			existingOrg != nil {
			// Slug exists, append suffix once
			nextSlug, genErr := slug.WithRandomSuffix(orgSlug, 6)
			if genErr != nil {
				return xerror.Unexpected().With(genErr)
			}
			orgSlug = nextSlug
		} else if err != nil && !xerror.Is(err, organizations.ErrNotFound) {
			return xerror.Wrapf("check organization slug collision: %w", err)
		}

		org, err := s.orgsRepo.Create(ctx, tx, &organizations.CreateParams{
			Name: newOrg.Name,
			Slug: orgSlug,
		})
		if err != nil {
			return xerror.Wrapf("create organization: %w", err)
		}
		if err := s.admission.InitializeOrganization(ctx, orgSlug); err != nil {
			return xerror.Wrapf("create organization admission: %w", err)
		}

		if err := s.environmentsService.CreateDefaultEnvironments(ctx, orgSlug); err != nil {
			return xerror.Wrapf("seed default environments: %w", err)
		}

		// Seed default roles for the new organization
		if err := s.rolesService.CreateDefaultRoles(ctx, orgSlug); err != nil {
			return xerror.Wrapf("create default roles: %w", err)
		}

		_, err = s.membersService.CreateBootstrapMember(
			ctx,
			orgSlug,
			model.NewMember{
				UserSlug: userSlug,
				Role:     adminRole,
			},
		)
		if err != nil {
			return xerror.Wrapf("create admin member: %w", err)
		}

		ret = s.toOrganization(org)
		if err := s.enqueueRequiredOrganizationAuditEvent(ctx, tx, orgSlug, actor, organizationCreatedEvent{
			orgSlug: orgSlug,
		}); err != nil {
			return err
		}
		return nil
	})

	if txErr != nil {
		return nil, txErr
	}

	return ret, nil
}

// ListOrganizations returns all organizations the user is a member of.
func (s *service) ListOrganizations(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.OrganizationWithRole], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.OrganizationWithRole

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		orgs, err := s.orgsRepo.ListByUser(ctx, tx, userSlug, queryReq)
		if err != nil {
			return xerror.Wrapf("list organizations: %w", err)
		}

		ret = make([]model.OrganizationWithRole, 0, len(orgs))
		for _, org := range orgs {
			ret = append(ret, s.toOrganizationWithRole(org))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.OrganizationWithRole]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.OrganizationWithRole, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
			case model.ListSortName:
				cursorValue = item.Name
			}
			return cursorValue, item.Slug.String()
		},
	)

	return model.ListResponse[model.OrganizationWithRole]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// GetOrganization retrieves a specific organization.
func (s *service) GetOrganization(
	ctx context.Context,
	orgSlug slug.Slug,
) (*model.Organization, error) {
	var ret *model.Organization

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		org, err := s.orgsRepo.FindBySlug(ctx, tx, orgSlug)
		if err != nil {
			return xerror.Wrapf("get organization: %w", err)
		}

		ret = s.toOrganization(org)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// UpdateOrganization updates an organization.
func (s *service) UpdateOrganization(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateOrganization,
) (*model.Organization, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	var ret *model.Organization

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		err := s.orgsRepo.Update(ctx, tx, &organizations.UpdateParams{
			Slug: orgSlug,
			Name: update.Name,
		})
		if err != nil {
			return xerror.Wrapf("update organization: %w", err)
		}

		org, err := s.orgsRepo.FindBySlug(ctx, tx, orgSlug)
		if err != nil {
			return xerror.Wrapf("fetch organization: %w", err)
		}

		ret = s.toOrganization(org)
		if err := s.enqueueRequiredOrganizationAuditEvent(ctx, tx, orgSlug, actor, organizationUpdatedEvent{
			orgSlug: orgSlug,
		}); err != nil {
			return err
		}
		return nil
	})

	return ret, err
}
