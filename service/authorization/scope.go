package authorization

import (
	"context"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	authstore "github.com/Braidplane/braidplane-core/postgres/authorization"
)

// WithDecisionScope carries allowed object identities into subsequent database units of work.
func WithDecisionScope(ctx context.Context) context.Context {
	return postgres.WithTransactionChecks(ctx)
}

func bindDecision(ctx context.Context, input Input, original model.AuthorizationFacts) {
	org := *input.OrgSlug
	postgres.AddTransactionCheck(ctx, func(ctx context.Context, db postgres.DBTX) error {
		current, err := authstore.Resolve(ctx, db, org, input.UserSlug, input.APIKeySlug, input.Target)
		if err != nil {
			return err
		}
		if !current.Valid || current.OrganizationID != original.OrganizationID || current.TargetID != original.TargetID ||
			current.ProjectID != original.ProjectID || current.EnvironmentID != original.EnvironmentID ||
			current.PrincipalID != original.PrincipalID || !slices.Equal(current.AncestorIDs, original.AncestorIDs) {
			return xerror.New(xerror.KindForbidden, "AUTHORIZATION_TARGET_CHANGED", "The authorized principal or target changed")
		}
		if permitsOperation(input, current) {
			allowed, err := evaluateTenant(ctx, input, current)
			if err != nil {
				return err
			}
			if allowed {
				return nil
			}
		}
		return xerror.New(xerror.KindForbidden, "AUTHORIZATION_REVOKED", "Permission was revoked before the operation")
	})
}
