package authorization

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// Resolve reads the principal and target in the same database snapshot.
func Resolve(ctx context.Context, tx postgres.DBTX, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
	data, err := dbgen.New(tx).ResolveAuthorizationFacts(ctx, dbgen.ResolveAuthorizationFactsParams{
		OrgSlug: org.String(), UserSlug: user.String(), KeySlug: key.String(), Kind: target.Kind,
		ResourceSlug: target.Slug.String(), ProjectSlug: target.ProjectSlug.String(),
		EnvironmentSlug: target.EnvironmentSlug.String(), DataSourceSlug: target.DataSourceSlug.String(), ParentSlug: target.ParentSlug.String(),
	})
	if err != nil {
		return model.AuthorizationFacts{}, xerror.Unexpected().With(err)
	}
	var facts model.AuthorizationFacts
	if err := json.Unmarshal(data, &facts); err != nil {
		return model.AuthorizationFacts{}, xerror.Unexpected().With(err)
	}
	return facts, nil
}
