package authorization

import (
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// Each entry associates a permission with its reviewed target kinds. The optional
// @purpose distinguishes secondary checks from the primary operation.
func parseSelectors(value string) map[string][]string {
	rules := make(map[string][]string)
	for _, field := range strings.Fields(value) {
		key, kinds, ok := strings.Cut(field, "=")
		if !ok || key == "" || kinds == "" || rules[key] != nil {
			panic("invalid authorization selector mapping: " + field)
		}
		rules[key] = strings.Split(kinds, "|")
	}
	return rules
}

func (op operation) permitsSelector(input Input, permission string) bool {
	kind := input.Target.Kind
	switch {
	case kind == "":
		if input.Target != (model.AuthorizationTarget{}) {
			return false
		}
		kind = "organization"
		if input.OrgSlug == nil {
			kind = "none"
		}
	case kind == "organization" || kind == "none" || strings.Contains(kind, "."):
		return false
	case kind == "project_environments":
		if input.Target.ProjectSlug == "" || input.Target.EnvironmentSlug == "" {
			return false
		}
	case input.Target.Slug == "":
		return false
	}
	if input.ProjectDataSourceBindings {
		kind = "binding." + kind
	}
	if input.CheckPurpose != "" {
		permission += "@" + input.CheckPurpose
	}
	return slices.Contains(op.selectors[permission], kind)
}
