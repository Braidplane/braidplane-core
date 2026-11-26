package roles

import (
	"context"
	"database/sql"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (r *repository) CreateRoleGrant(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, grant model.NewRoleGrant) (*model.RoleGrant, error) {
	id, err := dbgen.New(tx).CreateRoleGrant(ctx, dbgen.CreateRoleGrantParams{
		OrgSlug:         orgSlug,
		RoleSlug:        grant.RoleSlug,
		Scope:           grant.Scope,
		SubjectKind:     grant.SubjectKind,
		SubjectSlug:     grant.SubjectSlug.String(),
		TargetKind:      grant.Target.Kind,
		TargetSlug:      grant.Target.Slug.String(),
		ProjectSlug:     grant.Target.ProjectSlug.String(),
		EnvironmentSlug: grant.Target.EnvironmentSlug.String(),
		DataSourceSlug:  grant.Target.DataSourceSlug.String(),
		ParentSlug:      grant.Target.ParentSlug.String(),
	})
	if xerror.Is(err, sql.ErrNoRows) {
		return nil, xerror.NotFound(xerror.RoleGrant).Detail("role, subject or unique target not found in this organization")
	}
	if postgres.IsUniqueViolation(err) {
		return nil, xerror.AlreadyExists(xerror.RoleGrant).With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &model.RoleGrant{Slug: id, NewRoleGrant: grant}, nil
}

func (r *repository) ListRoleGrants(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) ([]model.RoleGrant, error) {
	rows, err := dbgen.New(tx).ListRoleGrants(ctx, orgSlug)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	grants := make([]model.RoleGrant, 0, len(rows))
	for _, row := range rows {
		grants = append(grants, model.RoleGrant{
			Slug: row.Slug,
			NewRoleGrant: model.NewRoleGrant{
				Scope:       row.Scope,
				RoleSlug:    row.RoleSlug,
				SubjectKind: row.SubjectKind,
				SubjectSlug: slug.Slug(row.SubjectSlug),
				Target: model.AuthorizationTarget{
					Kind:            row.TargetKind,
					Slug:            slug.Slug(row.TargetSlug),
					ProjectSlug:     slug.Slug(row.ProjectSlug),
					EnvironmentSlug: slug.Slug(row.EnvironmentSlug),
					DataSourceSlug:  slug.Slug(row.DataSourceSlug),
					ParentSlug:      slug.Slug(row.ParentSlug),
				},
			},
		})
	}
	return grants, nil
}

func (r *repository) RevokeRoleGrant(ctx context.Context, tx postgres.DBTX, orgSlug, grantSlug slug.Slug) (slug.Slug, error) {
	role, err := dbgen.New(tx).RevokeRoleGrant(ctx, dbgen.RevokeRoleGrantParams{OrgSlug: orgSlug, GrantSlug: grantSlug})
	if xerror.Is(err, sql.ErrNoRows) {
		return "", xerror.NotFound(xerror.RoleGrant).With(err)
	}
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return slug.Slug(role), nil
}
