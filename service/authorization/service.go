package authorization

import (
	"context"
	_ "embed"
	"sync"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

type Service interface {
	Can(ctx context.Context, input Input) (bool, error)
	EffectivePermissions(ctx context.Context, input Input) ([]model.RolePermission, error)
}

type Input struct {
	SessionAuthenticatedAt    time.Time      // Trusted session authentication time for sensitive self-service changes.
	Operation                 string         // Registered action chosen by the server or trusted adapter.
	CheckPurpose              string         // Named secondary check when the same permission protects multiple targets.
	Source                    Source         // Authenticated credential type.
	UserSlug                  slug.Slug      // The user performing the action
	APIKeySlug                slug.Slug      // Optional API key service principal
	APIKeyRoleSlug            slug.Slug      // Authenticated role used by key management. The resolver verifies it against the current role.
	OrgSlug                   *slug.Slug     // Optional: for organization-scoped authorization
	Action                    model.Action   // The action being performed
	Resource                  model.Resource // The Core permission family for this check.
	ResourceOwnerSlug         *slug.Slug     // Global profile subject.
	Target                    model.AuthorizationTarget
	ProjectDataSourceBindings bool // The operation acts on project-owned references or bindings.
}

func (input Input) validPermission() bool {
	return input.Resource != model.ResourceUnknown && input.Resource.IsAResource() &&
		input.Action != model.Unknown && input.Action.IsAAction()
}

type ResolveFacts func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error)

type service struct {
	query        rego.PreparedEvalQuery
	usersService userssvc.UsersService
	resolve      ResolveFacts
}

type opaUserInput struct {
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type opaAuthorizationInput struct {
	ProjectDataSourceBindings bool                     `json:"project_datasource_bindings"`
	Facts                     model.AuthorizationFacts `json:"facts"`
	User                      opaUserInput             `json:"user"`
	OrgScoped                 bool                     `json:"org_scoped"`
	Action                    string                   `json:"action"`
	Resource                  string                   `json:"resource"`
	ResourceOwnerSlug         *string                  `json:"resource_owner_slug,omitempty"`
}

//go:embed authz.rego
var authzModule string

func New(usersService userssvc.UsersService, resolve ResolveFacts) (Service, error) {
	query, err := preparedPolicy()
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &service{
		query:        query,
		usersService: usersService,
		resolve:      resolve,
	}, nil
}

func (s *service) Can(ctx context.Context, input Input) (bool, error) {
	if _, registered := operationRules[input.Operation]; !registered || !input.validPermission() ||
		!input.Source.matchesPrincipal(input) {
		return false, nil
	}
	if input.OrgSlug != nil {
		if s.resolve == nil || *input.OrgSlug == "" || input.ResourceOwnerSlug != nil {
			return false, nil
		}
		facts, err := s.resolve(ctx, *input.OrgSlug, input.UserSlug, input.APIKeySlug, input.Target)
		if err != nil {
			return false, xerror.Wrapf("resolve authorization facts: %w", err)
		}
		if !permitsOperation(input, facts) {
			return false, nil
		}
		allowed, err := evaluateTenant(ctx, input, facts)
		if err == nil && allowed {
			bindDecision(ctx, input, facts)
		}
		return allowed, err
	}
	if !permitsOperation(input, model.AuthorizationFacts{}) || s.usersService == nil || input.APIKeySlug != "" || input.Target != (model.AuthorizationTarget{}) {
		return false, nil
	}
	user, err := s.usersService.GetUserBySlug(ctx, input.UserSlug)
	if err != nil {
		if xerror.Is(err, userssvc.ErrUserNotFound) {
			return false, nil
		}
		return false, xerror.Wrapf("fetch authorization user: %w", err)
	}
	if user == nil || user.Slug != input.UserSlug || user.VerifiedAt.IsZero() {
		return false, nil
	}
	var owner *string
	if input.ResourceOwnerSlug != nil {
		value := input.ResourceOwnerSlug.String()
		owner = &value
	}
	return evaluate(ctx, s.query, opaAuthorizationInput{
		User:   opaUserInput{Slug: user.Slug.String(), Role: user.Role.String()},
		Action: input.Action.String(), Resource: input.Resource.String(), ResourceOwnerSlug: owner,
	})
}

// CanWithResolvedFacts evaluates tenant facts captured by a trusted persistence reader.
func CanWithResolvedFacts(ctx context.Context, action model.Action, resource model.Resource, facts model.AuthorizationFacts) (bool, error) {
	return evaluateTenant(ctx, Input{Resource: resource, Action: action}, facts)
}

func evaluateTenant(ctx context.Context, input Input, facts model.AuthorizationFacts) (bool, error) {
	if !facts.Valid || !input.validPermission() {
		return false, nil
	}
	query, err := preparedPolicy()
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	// Older deliveries may predate the current pipeline's ownership of this trigger.
	if input.Operation == "listPipelineWebhookDeliveries" && input.CheckPurpose == "delivery_history" {
		facts.AncestorIDs = nil
	}
	return evaluate(ctx, query, opaAuthorizationInput{
		OrgScoped: true, Facts: facts, Resource: input.Resource.String(),
		Action: input.Action.String(), ProjectDataSourceBindings: input.ProjectDataSourceBindings,
	})
}

var preparedPolicy = sync.OnceValues(func() (rego.PreparedEvalQuery, error) {
	return rego.New(rego.Query("data.braidplane.authz.allow"), rego.Module("authz.rego", authzModule)).PrepareForEval(context.Background())
})

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
