package authorization

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
)

func TestCanRejectsMismatchedPrincipalBeforeResolution(t *testing.T) {
	org := slug.Slug("tenant")
	svc, err := New(nil, func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		t.Fatal("a mismatched credential source reached persistence")
		return model.AuthorizationFacts{}, nil
	})
	require.NoError(t, err)
	allowed, err := svc.Can(t.Context(), Input{OrgSlug: &org, Operation: "listAPIKeys", APIKeySlug: "machine", Source: SourceSession, Resource: model.ResourceAPIKeys, Action: model.Read})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestCanRejectsUnverifiedDelegatedSources(t *testing.T) {
	org := slug.Slug("tenant")
	svc, err := New(nil, func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		t.Fatal("an unverified principal reached persistence")
		return model.AuthorizationFacts{}, nil
	})
	require.NoError(t, err)
	for _, input := range []Input{
		{Source: SourceDelegatedUser, UserSlug: "member"},
		{Source: SourceDelegatedMachine, APIKeySlug: "machine"},
	} {
		input.OrgSlug, input.Operation = &org, "listProjects"
		input.Resource, input.Action = model.ResourceProjects, model.Read
		allowed, err := svc.Can(t.Context(), input)
		require.NoError(t, err)
		require.False(t, allowed)
	}
}

func TestCanRequiresDelegatedTenantPrincipalAndMappedOperation(t *testing.T) {
	org, other := slug.Slug("tenant"), slug.Slug("other")
	const principalID = "member:1"
	facts := model.AuthorizationFacts{
		Valid: true, OrganizationID: "1", PrincipalID: principalID, TargetID: "projects:2",
		Bindings: []model.AuthorizationBinding{{Scope: "resource", ScopeID: "projects:2", Permissions: []string{"projects:read"}}},
	}
	svc, err := New(nil, func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		return facts, nil
	})
	require.NoError(t, err)
	principal := DelegatedPrincipal{Organization: org, User: "member", PrincipalID: principalID}
	withScopes := func(keys []string) context.Context {
		return WithDelegatedPrincipal(t.Context(), principal, keys, func(context.Context, postgres.DBTX) error { return nil })
	}
	ctx := withScopes(DelegationScopeKeys(SourceDelegatedUser))
	valid := Input{
		OrgSlug: &org, UserSlug: "member", Source: SourceDelegatedUser, Operation: "getProject", Resource: model.ResourceProjects,
		Action: model.Read, Target: model.AuthorizationTarget{Kind: "projects", Slug: "visible"},
	}
	check := func(input Input, want bool) {
		t.Helper()
		allowed, err := svc.Can(ctx, input)
		require.NoError(t, err)
		require.Equal(t, want, allowed)
	}
	check(valid, true)
	active := ctx
	ctx = withScopes(nil)
	check(valid, false)
	ctx = withScopes([]string{"getProject:outdated"})
	check(valid, false)
	ctx = active
	for _, edit := range []func(*Input){
		func(in *Input) { in.OrgSlug = &other },
		func(in *Input) { in.OrgSlug = nil },
		func(in *Input) { in.UserSlug = "other" },
		func(in *Input) { in.Source = SourceSession },
		func(in *Input) { in.Source, in.UserSlug, in.APIKeySlug = SourceDelegatedMachine, "", "machine" },
		func(in *Input) { in.Operation = "unmapped" },
		func(in *Input) { in.Operation, in.Action = "deleteProject", model.Delete },
		func(in *Input) { in.Operation, in.Action = "updateProject", model.Update },
		func(in *Input) { in.Target = model.AuthorizationTarget{} },
	} {
		invalid := valid
		edit(&invalid)
		check(invalid, false)
	}
	facts.PrincipalID = "member:2"
	check(valid, false)
}

func TestDelegationScopeKeyIsStableAcrossSelectorOrderAndChangesWithAuthority(t *testing.T) {
	const id = "exampleAction"
	base := operation{
		class: "tenant", route: "POST /organizations/:slug/projects/:project_slug/example",
		selectors: parseSelectors("projects:update=projects|project_environments projects:read=projects"),
	}
	key := delegationScopeKey(SourceDelegatedUser, id, base)
	reordered := base
	reordered.selectors = parseSelectors("projects:read=projects projects:update=project_environments|projects")
	require.Equal(t, key, delegationScopeKey(SourceDelegatedUser, id, reordered))
	reordered.selectors = parseSelectors("projects:read=projects projects:update=project_environments|projects|organization")
	require.NotEqual(t, key, delegationScopeKey(SourceDelegatedUser, id, reordered))
	reordered = base
	reordered.route = "PUT /organizations/:slug/projects/:project_slug/example"
	require.NotEqual(t, key, delegationScopeKey(SourceDelegatedUser, id, reordered))
	require.NotEqual(t, key, delegationScopeKey(SourceDelegatedMachine, id, base))
}

func TestCanRequiresMappedSourceAndPermission(t *testing.T) {
	org := slug.Slug("tenant")
	facts := model.AuthorizationFacts{Valid: true, OrganizationID: "1", Bindings: []model.AuthorizationBinding{
		{Scope: "organization", ScopeID: "1", Permissions: []string{"*:*"}},
	}}
	svc, err := New(&fakes.UsersService{GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
		return &model.User{Slug: "actor", Role: model.RoleUser, VerifiedAt: time.Now()}, nil
	}}, func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		return facts, nil
	})
	require.NoError(t, err)
	input := Input{APIKeySlug: "key", OrgSlug: &org, Source: SourceAPIKey, Resource: model.ResourceAPIKeys, Action: model.Read, Operation: "listAPIKeys"}
	check := func(want bool) {
		t.Helper()
		got, err := svc.Can(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	keyRead := model.RolePermission{Resource: "api_keys", Action: "read"}
	permissions := func() []model.RolePermission {
		t.Helper()
		result, err := svc.EffectivePermissions(t.Context(), input)
		require.NoError(t, err)
		return result
	}
	check(false)
	require.NotContains(t, permissions(), keyRead)
	facts.CanManageAPIKeys = true
	facts.APIKeyRoleSlug = "manager"
	check(false)
	input.APIKeyRoleSlug = "obsolete"
	check(false)
	require.NotContains(t, permissions(), keyRead)
	input.APIKeyRoleSlug = "manager"
	check(true)
	require.Contains(t, permissions(), keyRead)
	input.Operation = "getProject"
	check(false)
	input.Operation = ""
	check(false)
	input.Operation = "listAPIKeys"
	input.Source = SourceSession
	check(false)
	input.APIKeySlug, input.UserSlug = "", "actor"
	input.Source = SourcePAT
	check(false)
	require.NotContains(t, permissions(), keyRead)
	input.Source = SourceSession
	check(true)
	input.OrgSlug = nil
	input.Operation = "listPersonalAccessTokens"
	input.Resource = model.ResourceProfile
	input.ResourceOwnerSlug = &input.UserSlug
	input.Source = SourcePAT
	check(false)
	input.Source = SourceManagingPAT
	check(true)
	input.Operation = "updateUserPassword"
	input.Action = model.Update
	check(false)
	input.Source = SourceSession
	check(true)
	input.Operation = "patchUsersProfile"
	input.CheckPurpose = "mfa"
	check(false)
	input.SessionAuthenticatedAt = time.Now().Add(-6 * time.Minute)
	check(false)
	input.SessionAuthenticatedAt = time.Now().Add(time.Minute)
	check(false)
	input.SessionAuthenticatedAt = time.Now().Add(-time.Minute)
	input.Source = SourceManagingPAT
	check(false)
	input.Source = SourceSession
	check(true)
}

func TestEveryCoreOperationHasAnAuthorizationMapping(t *testing.T) {
	schema, err := openapi3.NewLoader().LoadFromFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	parameter := regexp.MustCompile(`\{([^}]+)\}`)

	expected := make(map[string]string)
	for path, item := range schema.Paths.Map() {
		path = parameter.ReplaceAllString(strings.ReplaceAll(path, ":", `\:`), ":$1")
		for method, op := range item.Operations() {
			expected[method+" "+path] = op.OperationID
		}
	}
	require.Equal(t, expected, operationRoutes)
}

func TestCanRequiresOperationSelector(t *testing.T) {
	org := slug.Slug("tenant")
	svc, err := New(nil, func(_ context.Context, _, _, _ slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		facts := model.AuthorizationFacts{Valid: true, OrganizationID: "1", Bindings: []model.AuthorizationBinding{
			{Scope: "organization", ScopeID: "1", Permissions: []string{"*:*"}},
		}}
		if target.Kind != "" {
			facts.TargetID = target.Kind + ":2"
			facts.ProjectID = "3"
		}
		return facts, nil
	})
	require.NoError(t, err)
	project := model.AuthorizationTarget{Kind: "projects", Slug: "one"}
	pipeline := model.AuthorizationTarget{Kind: "pipelines", Slug: "example", ProjectSlug: "one"}
	source := model.AuthorizationTarget{Kind: "datasources", Slug: "shared"}
	empty := model.AuthorizationTarget{}
	for _, tc := range []struct {
		operation string
		resource  model.Resource
		target    model.AuthorizationTarget
		binding   bool
		purpose   string
		want      bool
	}{
		{"getPipeline", model.ResourcePipelines, pipeline, false, "", true},
		{"getPipeline", model.ResourcePipelines, project, false, "", false},
		{"getPipeline", model.ResourcePipelines, empty, false, "", false},
		{"listPipelines", model.ResourcePipelines, pipeline, false, "", false},
		{"listPipelines", model.ResourcePipelines, project, false, "", true},
		{"getDataSource", model.ResourceDataSources, source, false, "", true},
		{"getDataSource", model.ResourceDataSources, project, true, "", false},
		{"getDataSource", model.ResourceDataSources, source, true, "", false},
		{"listDataSourceRefs", model.ResourceDataSources, project, true, "", true},
		{"listDataSourceRefs", model.ResourceDataSources, project, false, "", false},
		{"listDataSourceRefs", model.ResourceDataSources, source, false, "", false},
		{"getPipelineRunCompletion", model.ResourcePipelines, pipeline, false, "", false},
		{"getPipelineRunCompletion", model.ResourcePipelines, pipeline, false, "related_pipeline", true},
	} {
		allowed, err := svc.Can(t.Context(), Input{Operation: tc.operation, Source: SourceSession, OrgSlug: &org, UserSlug: "member", Action: model.Read, Resource: tc.resource, Target: tc.target, ProjectDataSourceBindings: tc.binding, CheckPurpose: tc.purpose})
		require.NoError(t, err)
		require.Equal(t, tc.want, allowed, "%s %v binding=%v purpose=%q", tc.operation, tc.target, tc.binding, tc.purpose)
	}
}

func TestEffectivePermissionRequiresMappedSelector(t *testing.T) {
	org := slug.Slug("tenant")
	input := Input{OrgSlug: &org, Source: SourceSession, UserSlug: "member"}
	facts := model.AuthorizationFacts{Valid: true, OrganizationID: "1", Bindings: []model.AuthorizationBinding{{Scope: "organization", ScopeID: "1", Permissions: []string{"*:*"}}}}
	svc, err := New(nil, func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		return facts, nil
	})
	require.NoError(t, err)
	perms := func() []model.RolePermission {
		t.Helper()
		p, err := svc.EffectivePermissions(t.Context(), input)
		require.NoError(t, err)
		return p
	}
	execute := model.RolePermission{Resource: "pipelines", Action: "execute"}
	require.Contains(t, perms(), execute)
	input.Target = model.AuthorizationTarget{Kind: "projects", Slug: "proj"}
	facts.TargetID = "projects:2"
	require.NotContains(t, perms(), execute)
	input.ProjectDataSourceBindings = true
	require.Contains(t, perms(), model.RolePermission{Resource: "pipelines", Action: "read"})
	require.Contains(t, perms(), model.RolePermission{Resource: "datasources", Action: "read"})
	input.ProjectDataSourceBindings = false
	input.Target = model.AuthorizationTarget{Kind: "deployed_pipelines", Slug: "pipe", ProjectSlug: "proj", EnvironmentSlug: "env"}
	facts.TargetID = "pipelines:3"
	require.Contains(t, perms(), execute)
	input.Target = model.AuthorizationTarget{Kind: "environment_secrets", Slug: "EXAMPLE", EnvironmentSlug: "env"}
	facts.TargetID = "environment_secrets:5"
	require.Contains(t, perms(), model.RolePermission{Resource: "environments", Action: "update"})
	require.NotContains(t, perms(), model.RolePermission{Resource: "environments", Action: "create"})
}
