package braidplane.authz

default allow := false

# New Core operations require an explicit permission mapping.
tenant_actions := {
    "organizations": {"read", "update", "delete"},
    "members": {"create", "read", "update", "delete"},
    "roles": {"create", "read", "update", "delete", "restore"},
    "api_keys": {"create", "read", "update", "delete"},
    "projects": {"create", "read", "update", "delete"},
    "environments": {"create", "read", "update", "delete"},
    "datasources": {"create", "read", "update", "delete", "restore"},
    "queries": {"create", "read", "update", "delete", "restore", "purge", "execute"},
    "pipelines": {"create", "read", "update", "execute"},
    "pipeline_triggers": {"create", "read", "update", "delete"},
    "events": {"read"},
    "notifications": {"read", "update"},
    "alert_policies": {"create", "read", "update", "delete"},
    "alert_destinations": {"create", "read", "update", "delete"},
}

scope_ids := {
    "organization": input.facts.organization_id,
    "project": object.get(input.facts, "project_id", ""),
    "environment": object.get(input.facts, "environment_id", ""),
    "resource": object.get(input.facts, "target_id", ""),
}

allow if {
    input.org_scoped
    input.facts.valid
    input.facts.organization_id != ""
    target_accepts_resource
    some binding in input.facts.bindings
    scope_permits_action(binding.scope)
    binding.scope_id != ""
    binding_matches(binding)
    some permission in binding.permissions
    grants_action(permission)
}

binding_matches(binding) if binding.scope_id == scope_ids[binding.scope]

binding_matches(binding) if {
    binding.scope == "resource"
    binding.scope_id in object.get(input.facts, "ancestor_ids", [])
}

# Permission families sometimes cover children or collections with a different storage kind.
target_resources := {
    "projects": {"projects", "pipelines", "pipeline_triggers", "environments", "alert_policies"},
    "project_environments": {"projects", "pipelines", "pipeline_triggers", "environments", "alert_policies"},

    "datasources": {"datasources", "queries"},
    "contracts": {"projects"},
    "query_executions": {"queries"},
    "webhook_deliveries": {"pipeline_triggers"},
    "environment_secrets": {"environments"},
    "environment_runtime_variables": {"environments"},
    "runtime_variable_definitions": {"environments"},
    "project_repository_setups": {"projects"},
    "pipeline_creations": {"pipelines"},
    "runs": {"pipelines"},
    "pipeline_validation_results": {"pipelines"},
    "drift_impact_reports": {"pipelines"},
    "proof_receipts": {"pipelines"},
    "proof_observations": {"pipelines"},
    "member_invitations": {"members"},
    "notification_connections": {"alert_destinations"},
}

target_accepts_resource if object.get(input.facts, "target_id", "") == ""

target_accepts_resource if {
    kind := split(input.facts.target_id, ":")[0]
    kind == input.resource
}

target_accepts_resource if {
    kind := split(input.facts.target_id, ":")[0]
    input.resource in target_resources[kind]
}

target_accepts_resource if {
    input.project_datasource_bindings
    input.resource == "datasources"
    split(input.facts.target_id, ":")[0] in {"projects", "project_environments", "pipelines", "data_source_refs", "data_source_bindings"}
}

grants_action(permission) if {
    input.action in tenant_actions[input.resource]
    permission in {"*:*", concat(":", [input.resource, "*"]), concat(":", [input.resource, input.action])}
}

# Consumer-owned types require an exact permission registered by the distribution.
grants_action(permission) if {
    input.consumer_resource
    permission == concat(":", [input.resource, input.action])
}

allow if {
    not input.org_scoped
    input.user.role == "super_admin"
    input.resource == "users"
    input.action in {"read", "update"}
}

allow if {
    not input.org_scoped
    input.user.role in {"user", "super_admin"}
    input.resource == "organizations"
    input.action in {"read", "create"}
}

allow if {
    not input.org_scoped
    input.resource == "profile"
    input.action in {"read", "update", "delete"}
    input.resource_owner_slug == input.user.slug
}

delegation_permissions := {"roles:create", "roles:update", "roles:restore", "api_keys:create", "api_keys:update", "members:create", "members:update"}

scope_permits_action(scope) if scope == "organization"

scope_permits_action(scope) if {
    scope != "organization"
    not concat(":", [input.resource, input.action]) in delegation_permissions
}

effective contains [resource, action] if {
    some resource, actions in tenant_actions
    some action in actions
    allow with input.resource as resource with input.action as action
}
