package authorization

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

type Service interface {
	Can(ctx context.Context, input Input) (bool, error)
}

type Input struct {
	UserSlug          slug.Slug    // The user performing the action
	APIKeySlug        slug.Slug    // Optional API key service principal
	APIKeyRoleSlug    slug.Slug    // Optional API key role in the organization
	OrgSlug           *slug.Slug   // Optional: for organization-scoped authorization
	Action            model.Action // The action being performed
	Resource          fmt.Stringer // The typed resource, including consumer-owned resources
	ResourceOwnerSlug *slug.Slug   // Optional: for ownership checks
}

type service struct {
	query        rego.PreparedEvalQuery
	usersService userssvc.UsersService
	rolesService rolessvc.Service
}

type opaUserInput struct {
	Slug        string   `json:"slug"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

type opaAuthorizationInput struct {
	User              opaUserInput `json:"user"`
	OrgScoped         bool         `json:"org_scoped"`
	Action            string       `json:"action"`
	Resource          string       `json:"resource"`
	ResourceOwnerSlug *string      `json:"resource_owner_slug,omitempty"`
}

//go:embed authz.rego
var authzModule string

func New(usersService userssvc.UsersService, rolesService rolessvc.Service) (Service, error) {
	query, err := preparedPolicy()
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &service{
		query:        query,
		usersService: usersService,
		rolesService: rolesService,
	}, nil
}

func (s *service) Can(ctx context.Context, input Input) (bool, error) {
	if input.Resource == nil {
		return false, nil
	}
	var actorSlug slug.Slug
	var actorRole string
	permissions := []string{}

	if input.APIKeySlug != "" {
		if input.OrgSlug == nil || input.APIKeyRoleSlug == "" {
			return false, nil
		}

		if _, err := s.rolesService.GetRole(ctx, *input.OrgSlug, input.APIKeyRoleSlug); err != nil {
			if xerror.Is(err, rolessvc.ErrRoleNotFound) {
				return false, nil
			}
			return false, xerror.Wrapf("failed to fetch api key role: %w", err)
		}

		rolePerms, err := s.rolesService.ListRolePermissions(ctx, *input.OrgSlug, input.APIKeyRoleSlug)
		if err != nil {
			return false, xerror.Wrapf("failed to fetch api key role permissions: %w", err)
		}

		for _, perm := range rolePerms {
			permissions = append(permissions, fmt.Sprintf("%s:%s", perm.Resource, perm.Action))
		}

		actorSlug = input.APIKeySlug
		actorRole = input.APIKeyRoleSlug.String()
	} else {
		user, err := s.usersService.GetUserBySlug(ctx, input.UserSlug)
		if err != nil {
			if xerror.Is(err, userssvc.ErrUserNotFound) {
				return false, nil
			}
			return false, xerror.Wrapf("failed to fetch user: %w", err)
		}

		if user.VerifiedAt.IsZero() {
			return false, nil
		}

		if input.OrgSlug != nil {
			rolePerms, err := s.rolesService.GetUserPermissionsInOrg(
				ctx,
				*input.OrgSlug,
				input.UserSlug,
			)
			if err != nil {
				if xerror.Is(err, rolessvc.ErrUserNotMember) {
					return false, nil
				}
				return false, xerror.Wrapf("failed to fetch role permissions: %w", err)
			}

			for _, perm := range rolePerms {
				permissions = append(permissions, fmt.Sprintf("%s:%s", perm.Resource, perm.Action))
			}
		}

		actorSlug = input.UserSlug
		actorRole = user.Role.String()
	}
	// For system-level authorization (no org context), pass empty permissions.
	// OPA will use role_permissions map defined in authz.rego instead.

	var resourceOwnerSlug *string
	if input.ResourceOwnerSlug != nil {
		owner := string(*input.ResourceOwnerSlug)
		resourceOwnerSlug = &owner
	}

	// Build the input for OPA.
	opaInput := opaAuthorizationInput{
		User: opaUserInput{
			Slug:        string(actorSlug),
			Role:        actorRole,
			Permissions: permissions,
		},
		OrgScoped:         input.OrgSlug != nil,
		Action:            input.Action.String(),
		Resource:          input.Resource.String(),
		ResourceOwnerSlug: resourceOwnerSlug,
	}

	return evaluate(ctx, s.query, opaInput)
}

var preparedPolicy = sync.OnceValues(func() (rego.PreparedEvalQuery, error) {
	return rego.New(rego.Query("data.braidplane.authz.allow"), rego.Module("authz.rego", authzModule)).PrepareForEval(context.Background())
})

// CanCapturedCompletionAuthority evaluates database-captured permission facts
// with the same policy as Can. Callers must never supply request-owned facts.
func CanCapturedCompletionAuthority(ctx context.Context, snapshot model.CompletionAuthoritySnapshot) (bool, error) {
	if !snapshot.Valid {
		return false, nil
	}
	query, err := preparedPolicy()
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	for _, requirement := range []struct {
		action   model.Action
		resource model.Resource
	}{
		{model.Read, model.ResourcePipelines},
		{model.Update, model.ResourcePipelineTriggers},
		{model.Execute, model.ResourcePipelines},
	} {
		allowed, err := evaluate(ctx, query, opaAuthorizationInput{
			User: opaUserInput{Permissions: snapshot.Permissions}, OrgScoped: true,
			Action: requirement.action.String(), Resource: requirement.resource.String(),
		})
		if err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}

func evaluate(ctx context.Context, query rego.PreparedEvalQuery, opaInput opaAuthorizationInput) (bool, error) {
	results, err := query.Eval(ctx, rego.EvalInput(opaInput))
	if err != nil {
		return false, xerror.Wrapf("failed to evaluate policy: %w", err)
	}

	// Check if access is allowed
	if len(results) == 0 {
		return false, nil
	}

	allowed, ok := results[0].Expressions[0].Value.(bool)
	if !ok {
		return false, xerror.Unexpected().Detail("unexpected policy result type")
	}

	return allowed, nil
}
