package authorization

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
)

var delegatedScopeKeys = func() map[Source]map[string]string {
	scopes := make(map[Source]map[string]string)
	for _, source := range []Source{SourceDelegatedUser, SourceDelegatedMachine} {
		scopes[source] = make(map[string]string)
		for id, op := range operationRules {
			if op.class == "tenant" && slices.Contains(op.sources, string(source)) {
				scopes[source][id] = delegationScopeKey(source, id, op)
			}
		}
	}
	return scopes
}()

func delegationScopeKey(source Source, id string, op operation) string {
	var value strings.Builder
	value.WriteString(strings.Join([]string{string(source), id, op.class, op.route}, "\n"))
	for _, permission := range slices.Sorted(maps.Keys(op.selectors)) {
		value.WriteByte('\n')
		value.WriteString(permission)
		value.WriteByte('=')
		value.WriteString(strings.Join(slices.Sorted(slices.Values(op.selectors[permission])), "|"))
	}
	digest := sha256.Sum256([]byte(value.String()))
	return id + ":" + hex.EncodeToString(digest[:])
}

// DelegationScopeKeys captures the current operation mappings for a newly authorized grant.
func DelegationScopeKeys(source Source) []string {
	return slices.Sorted(maps.Values(delegatedScopeKeys[source]))
}

// DelegationScopeDescriptions describes the operation mappings offered during consent.
func DelegationScopeDescriptions(keys []string) []string {
	var descriptions []string
	for _, key := range keys {
		operation, _, _ := strings.Cut(key, ":")
		switch operation {
		case "getOrganization":
			operation = "View the organization"
		case "listProjects":
			operation = "List projects"
		case "getProject":
			operation = "View projects"
		case "updateProject":
			operation = "Rename projects"
		}
		descriptions = append(descriptions, operation)
	}
	return descriptions
}
