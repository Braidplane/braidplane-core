package authorization

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/open-policy-agent/opa/v1/rego"
)

var effectivePolicy = sync.OnceValues(func() (rego.PreparedEvalQuery, error) {
	return rego.New(rego.Query("some pair in data.braidplane.authz.effective; resource := pair[0]; action := pair[1]"), rego.Module("authz.rego", authzModule)).PrepareForEval(context.Background())
})

// EffectivePermissions enumerates the same Core rules used by Can in a single database snapshot.
func (s *service) EffectivePermissions(ctx context.Context, input Input) ([]model.RolePermission, error) {
	permissions := []model.RolePermission{}
	if !input.Source.matchesPrincipal(input) || input.Source == SourceQueuedMember || s.resolve == nil || input.OrgSlug == nil || *input.OrgSlug == "" || (input.UserSlug == "") == (input.APIKeySlug == "") {
		return permissions, nil
	}
	facts, err := s.resolve(ctx, *input.OrgSlug, input.UserSlug, input.APIKeySlug, input.Target)
	if err != nil {
		return nil, xerror.Wrapf("resolve authorization facts: %w", err)
	}
	if !facts.Valid {
		return permissions, nil
	}
	query, err := effectivePolicy()
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	results, err := query.Eval(ctx, rego.EvalInput(opaAuthorizationInput{OrgScoped: true, Facts: facts, ProjectDataSourceBindings: input.ProjectDataSourceBindings}))
	if err != nil {
		return nil, xerror.Wrapf("evaluate effective permissions: %w", err)
	}
	for _, row := range results {
		resource, okResource := row.Bindings["resource"].(string)
		action, okAction := row.Bindings["action"].(string)
		if !okResource || !okAction {
			return nil, xerror.Unexpected().Detail("invalid permission result")
		}
		if sourceCanUsePermission(input, resource+":"+action, facts) {
			permissions = append(permissions, model.RolePermission{Resource: resource, Action: action})
		}
	}
	slices.SortFunc(permissions, func(a, b model.RolePermission) int {
		return cmp.Or(cmp.Compare(a.Resource, b.Resource), cmp.Compare(a.Action, b.Action))
	})
	return permissions, nil
}
