package views

import (
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type RoleGrantsPageData struct {
	Organization  api.Organization
	Grants        []api.RoleGrant
	Roles         []api.OrgRole
	Form          api.NewRoleGrant
	FormError     string
	ListError     string
	SubjectKind   string
	SubjectSlug   string
	ListAvailable bool
	CanManage     bool
}

func RoleGrantsHref(org, subjectKind, subjectSlug string) string {
	base := "/organizations/" + org + "/grants"
	if subjectSlug == "" {
		return base
	}
	return base + "?" + url.Values{"subject_kind": {subjectKind}, "subject_slug": {subjectSlug}}.Encode()
}

func roleGrantRevokeHref(page RoleGrantsPageData, grant api.RoleGrant) string {
	path, query, _ := strings.Cut(RoleGrantsHref(page.Organization.Slug, page.SubjectKind, page.SubjectSlug), "?")
	if query != "" {
		query = "?" + query
	}
	return path + "/" + grant.Slug + "/revoke" + query
}

func roleGrantSubjectLabel(kind string) string {
	if kind == "api_key" {
		return "API key"
	}
	return "Member"
}

func roleGrantScopeLabel(scope api.RoleGrantScope) string {
	switch scope {
	case api.RoleGrantScopeOrganization:
		return "Organization"
	case api.RoleGrantScopeProject:
		return "Project"
	case api.RoleGrantScopeEnvironment:
		return "Environment"
	default:
		return "Resource"
	}
}

func roleGrantTargetLabel(grant api.RoleGrant) string {
	if grant.TargetSlug == nil {
		return "Target unavailable"
	}
	if grant.Scope != api.RoleGrantScopeResource {
		return *grant.TargetSlug
	}
	return strings.TrimSpace(valueOrEmpty(grant.TargetKind) + " / " + *grant.TargetSlug)
}
