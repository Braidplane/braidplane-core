package model

import "github.com/Braidplane/braidplane-core/pkg/slug"

// AuthorizationTarget contains lookup keys. Only the persistence resolver establishes ownership.
type AuthorizationTarget struct {
	Kind            string
	Slug            slug.Slug
	ProjectSlug     slug.Slug
	EnvironmentSlug slug.Slug
	DataSourceSlug  slug.Slug
	ParentSlug      slug.Slug
}

// AuthorizationFacts are resolved together for a single principal, tenant, and target.
type AuthorizationFacts struct {
	APIKeyRoleSlug   slug.Slug              `json:"api_key_role_slug,omitempty"`
	PrincipalID      string                 `json:"principal_id"`
	CanManageAPIKeys bool                   `json:"can_manage_api_keys"`
	AncestorIDs      []string               `json:"ancestor_ids"`
	Valid            bool                   `json:"valid"`
	OrganizationID   string                 `json:"organization_id"`
	TargetID         string                 `json:"target_id"`
	ProjectID        string                 `json:"project_id"`
	EnvironmentID    string                 `json:"environment_id"`
	OwnerSlug        slug.Slug              `json:"owner_slug"`
	Bindings         []AuthorizationBinding `json:"bindings"`
}

type AuthorizationBinding struct {
	Scope       string   `json:"scope"`
	ScopeID     string   `json:"scope_id"`
	Permissions []string `json:"permissions"`
}
