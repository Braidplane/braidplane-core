package authorization

import (
	"maps"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// RolePermissionOption describes a grantable permission and its display labels.
type RolePermissionOption struct {
	model.RolePermission
	ResourceLabel string
	ActionLabel   string
}

var rolePermissionOptions = func() map[string]RolePermissionOption {
	resourceLabels := map[string]string{
		"*": "All resources", "organizations": "Organizations", "members": "Members",
		"roles": "Roles", "api_keys": "API keys", "projects": "Projects",
		"environments": "Environments", "datasources": "Data sources", "queries": "Queries",
		"pipelines": "Pipelines", "pipeline_triggers": "Pipeline triggers", "events": "Events",
		"notifications": "Notifications", "alert_policies": "Alert policies",
		"alert_destinations": "Alert destinations",
	}
	actionLabels := map[string]string{
		"*": "All actions", "create": "Create", "read": "Read", "update": "Update",
		"delete": "Delete", "restore": "Restore", "purge": "Purge", "execute": "Execute",
	}
	options := make(map[string]RolePermissionOption)
	add := func(resource, action string) {
		options[resource+":"+action] = RolePermissionOption{
			RolePermission: model.RolePermission{Resource: resource, Action: action},
			ResourceLabel:  resourceLabels[resource],
			ActionLabel:    actionLabels[action],
		}
	}
	add("*", "*")
	for _, rule := range operationRules {
		if rule.class != "tenant" {
			continue
		}
		for check := range rule.selectors {
			permission, _, _ := strings.Cut(check, "@")
			resource, action, _ := strings.Cut(permission, ":")
			add(resource, action)
			add(resource, "*")
		}
	}
	return options
}()

// RolePermissionCatalog returns the permissions supported on organization roles.
func RolePermissionCatalog() []RolePermissionOption {
	catalog := make([]RolePermissionOption, 0, len(rolePermissionOptions))
	for _, key := range slices.Sorted(maps.Keys(rolePermissionOptions)) {
		catalog = append(catalog, rolePermissionOptions[key])
	}
	return catalog
}

func IsGrantableRolePermission(permission model.RolePermission) bool {
	option, found := rolePermissionOptions[permission.Resource+":"+permission.Action]
	return found && option.RolePermission == permission
}
