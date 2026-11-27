package authorization

import (
	_ "embed"
	"encoding/csv"
	"slices"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// Source is established by authentication, before constructing an authorization input.
type Source string

const (
	SourceSession          Source = "session"
	SourcePAT              Source = "PAT"
	SourceManagingPAT      Source = "management-enabled PAT"
	SourceAPIKey           Source = "API key"
	SourceQueuedMember     Source = "queued member"
	SourceDelegatedUser    Source = "delegated user"
	SourceDelegatedMachine Source = "delegated machine"
)

const (
	OperationRegisterMCPMachineClient   = "registerMCPMachineClient"
	OperationDisconnectMCPMachineClient = "disconnectMCPMachineClient"
)

type operation struct {
	class     string
	sources   []string
	selectors map[string][]string
	route     string
}

//go:embed operations.csv
var operationMatrix string

var operationRoutes, operationRules = func() (map[string]string, map[string]operation) {
	rows, err := csv.NewReader(strings.NewReader(operationMatrix)).ReadAll()
	if err != nil {
		panic(err)
	}
	routes := make(map[string]string, len(rows)-1)
	rules := map[string]operation{
		"instanceUserAdministration":        {class: "global", sources: []string{"session", "PAT"}, selectors: parseSelectors("users:read=none users:update=none")},
		"authorizeQueuedProposalUpdate":     {class: "tenant", sources: []string{"queued member"}, selectors: parseSelectors("pipelines:update=pipelines")},
		"authorizeQueuedProposalCreate":     {class: "tenant", sources: []string{"queued member"}, selectors: parseSelectors("pipelines:create=projects")},
		OperationRegisterMCPMachineClient:   {class: "tenant", sources: []string{string(SourceSession)}, selectors: parseSelectors("api_keys:update=api_keys")},
		OperationDisconnectMCPMachineClient: {class: "tenant", sources: []string{string(SourceSession)}, selectors: parseSelectors("api_keys:delete=api_keys")},
	}
	for _, row := range rows[1:] {
		id, route := row[0], row[1]+" "+row[2]
		if _, duplicate := rules[id]; duplicate || routes[route] != "" {
			panic("duplicate authorization operation " + id)
		}
		routes[route] = id
		selectors := parseSelectors(row[7])
		permissions := strings.Fields(row[5])
		for _, permission := range permissions {
			if len(selectors[permission]) == 0 {
				panic("missing authorization selector: " + id + " " + permission)
			}
		}
		for check := range selectors {
			permission, _, _ := strings.Cut(check, "@")
			if !slices.Contains(permissions, permission) {
				panic("unlisted authorization permission: " + id + " " + permission)
			}
		}
		rules[id] = operation{class: row[3], sources: strings.Split(row[4], ", "), selectors: selectors, route: route}
	}
	return routes, rules
}()

func OperationForRoute(method, path string) string {
	return operationRoutes[method+" "+path]
}

func (source Source) matchesPrincipal(input Input) bool {
	switch source {
	case SourceAPIKey, SourceDelegatedMachine:
		return input.APIKeySlug != "" && input.UserSlug == ""
	case SourceSession, SourcePAT, SourceManagingPAT, SourceQueuedMember, SourceDelegatedUser:
		return input.UserSlug != "" && input.APIKeySlug == ""
	default:
		return false
	}
}

func (op operation) accepts(source Source, permission string, tenant, keyManager bool) bool {
	if tenant != (op.class == "tenant") || len(op.selectors[permission]) == 0 {
		return false
	}
	if source == SourceManagingPAT && slices.Contains(op.sources, string(SourcePAT)) {
		return true
	}
	if source == SourceAPIKey && keyManager && slices.Contains(op.sources, "key-management-enabled API key") {
		return true
	}
	return slices.Contains(op.sources, string(source))
}

func (input Input) keyManagementAllowed(facts model.AuthorizationFacts) bool {
	// The request carries the role used to filter keys or select a child key's role.
	return facts.CanManageAPIKeys && input.APIKeyRoleSlug != "" && input.APIKeyRoleSlug == facts.APIKeyRoleSlug
}

// RecentSessionAuthentication is required to change a profile's MFA settings.
func RecentSessionAuthentication(authenticatedAt time.Time) bool {
	age := time.Since(authenticatedAt)
	return age >= 0 && age <= 5*time.Minute
}

func permitsOperation(input Input, facts model.AuthorizationFacts) bool {
	if !input.validPermission() || !input.Source.matchesPrincipal(input) {
		return false
	}
	if input.Operation == "patchUsersProfile" && input.CheckPurpose == "mfa" &&
		(input.Source != SourceSession || !RecentSessionAuthentication(input.SessionAuthenticatedAt)) {
		return false
	}
	op, ok := operationRules[input.Operation]
	permission := input.Resource.String() + ":" + input.Action.String()
	return ok && op.accepts(input.Source, permission, input.OrgSlug != nil, input.keyManagementAllowed(facts)) && op.permitsSelector(input, permission)
}

func sourceCanUsePermission(input Input, permission string, facts model.AuthorizationFacts) bool {
	// The binding selector changes the meaning of data-source permissions only.
	if !strings.HasPrefix(permission, "datasources:") {
		input.ProjectDataSourceBindings = false
	}
	for _, op := range operationRules {
		if !op.accepts(input.Source, permission, true, input.keyManagementAllowed(facts)) {
			continue
		}
		if input.Target == (model.AuthorizationTarget{}) || op.permitsSelector(input, permission) {
			return true
		}
		// Existing keyed objects may be updated through their PUT upsert mapping.
		if strings.HasPrefix(facts.TargetID, input.Target.Kind+":") {
			update := input
			update.Target.Kind += "_upsert"
			if op.permitsSelector(update, permission) {
				return true
			}
		}
	}
	return false
}
