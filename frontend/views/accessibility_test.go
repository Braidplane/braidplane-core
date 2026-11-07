package views

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	htmlparser "golang.org/x/net/html"
)

func TestErrorPopupIsAnnouncedAsAlert(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, ErrorPopup("test-error", "Save failed", "").Render(context.Background(), &out))

	require.Contains(t, out.String(), `role="alert"`)
	require.Contains(t, out.String(), `aria-live="assertive"`)
}

func TestSuccessPopupAutoDismisses(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, SuccessPopup("test-success", "Saved").Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `data-success-popup`)
	require.Contains(t, html, `data-auto-dismiss-ms="5000"`)
	require.NotContains(t, html, `window.setTimeout`)
}

func TestAuthenticatedShellOrganizationSwitcherHasAccessibleNameAndStatusRegion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	data := AuthenticatedShellData{
		Title:                  "Dashboard",
		CurrentPath:            "/dashboard",
		ActiveOrganizationName: "Acme HQ",
		ActiveOrganizationSlug: "acme-hq",
		Organizations: []ShellOrganizationOption{
			{Slug: "acme-hq", Name: "Acme HQ", IsActive: true},
			{Slug: "beta-labs", Name: "Beta Labs"},
		},
	}
	require.NoError(t, AuthenticatedShell(data, templText("content")).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `data-org-switcher`)
	require.Contains(t, html, `<span class="sr-only">Switch organization: </span>`)
	require.Contains(t, html, `>Organization settings<`)
	require.Contains(t, html, `href="/organizations/acme-hq"`)
	require.Contains(t, html, `href="/organizations"`)
	require.Contains(t, html, `href="/organizations/new"`)
	require.Contains(t, html, `action="/organizations/beta-labs/switch?return_to=%2Fdashboard"`)
	require.Contains(t, html, `data-org-switcher-status`)
	require.Contains(t, html, `aria-live="polite"`)
}

func TestAuthenticatedShellUsesControlRoomThemeAndCurrentNavState(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	data := AuthenticatedShellData{
		Title:       "Dashboard",
		CurrentPath: "/dashboard",
		NavItems: []ShellNavItem{
			{Label: "Dashboard", Href: "/dashboard", Icon: "home", IsActive: true, Enabled: true},
			{Label: "Runs", Href: "/runs", Icon: "document", Enabled: true},
		},
	}
	require.NoError(t, AuthenticatedShell(data, templText("content")).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `data-theme="control-room"`)
	require.Contains(t, html, `/assets/icons/app/logo-1024-transparent.png?v=`)
	require.NotContains(t, html, `>B</span>`)
	require.Contains(t, html, `aria-current="page"`)
	require.NotContains(t, html, `aria-label="Alpha safety notice"`)
	require.Contains(t, html, `aria-label="Product stage">Alpha</span>`)
	tokenizer := htmlparser.NewTokenizer(strings.NewReader(html))
	var headingText strings.Builder
	inHeading := false
	for token := tokenizer.Next(); token != htmlparser.ErrorToken; token = tokenizer.Next() {
		switch token {
		case htmlparser.StartTagToken, htmlparser.EndTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == "h1" {
				inHeading = token == htmlparser.StartTagToken
			}
		case htmlparser.TextToken:
			if inHeading {
				headingText.Write(tokenizer.Text())
			}
		case htmlparser.ErrorToken, htmlparser.SelfClosingTagToken, htmlparser.CommentToken, htmlparser.DoctypeToken:
		}
	}
	require.Equal(t, "Overview", headingText.String(), "stage branding must not change the page heading's accessible name")

	require.NotContains(t, html, `Use test data only.`)
}

func TestAuthenticatedShellKeepsBuilderAlignedWithProductPages(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	data := AuthenticatedShellData{
		Title:       "Builder",
		CurrentPath: "/builder",
	}
	require.NoError(t, AuthenticatedShell(data, templText("content")).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `id="main-content"`)
	require.Contains(t, html, `lg:p-8`)
	require.Equal(t, ShellContentClass("/organizations/acme/projects"), ShellContentClass("/builder"))
}

func TestAuthenticatedShellSkipsDuplicatePreAlphaNoticeOnInvitationPage(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	data := AuthenticatedShellData{
		Title:       "Member invitation",
		CurrentPath: "/member-invitations/accept",
	}
	require.NoError(t, AuthenticatedShell(data, templText("content")).Render(context.Background(), &out))

	require.NotContains(t, out.String(), `aria-label="Alpha safety notice"`)
}

func TestAuthenticatedShellRendersTopbarRuntimeAndSidebarUserContext(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	data := AuthenticatedShellData{
		Title:                      "Pipelines",
		CurrentPath:                "/orgs/acme-hq/projects/data-migration/pipelines",
		ActiveOrganizationName:     "Acme HQ",
		ActiveOrganizationSlug:     "acme-hq",
		ActiveProjectName:          "Data Migration",
		ActiveProjectSlug:          "data-migration",
		ActiveEnvironmentName:      "Production",
		ActiveEnvironmentSlug:      "prod",
		ActiveEnvironmentColor:     "red",
		ActiveEnvironmentProtected: true,
		User: ShellUserSummary{
			DisplayName: "Jordan Lee",
		},
		Organizations: []ShellOrganizationOption{
			{Slug: "acme-hq", Name: "Acme HQ", IsActive: true},
		},
		Projects: []ShellProjectOption{
			{Slug: "data-migration", Name: "Data Migration", IsActive: true},
		},
		Environments: []ShellEnvironmentOption{
			{Slug: "prod", Name: "Production", Color: "red", Protected: true, Href: "?environment_slug=prod", IsActive: true},
		},
		NavItems: []ShellNavItem{
			{Label: "Pipelines", Href: "/orgs/acme-hq/projects/data-migration/pipelines", Icon: "document", IsActive: true, Enabled: true},
		},
		Breadcrumbs: []BreadcrumbItem{
			{Label: "Acme HQ", Href: "/organizations/acme-hq"},
			{Label: "Data Migration"},
		},
	}
	require.NoError(t, AuthenticatedShell(data, templText("content")).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `Workspace navigation`)
	require.NotContains(t, html, `Collapsed active context`)
	require.Contains(t, html, `Acme HQ`)
	require.Contains(t, html, `Data Migration`)
	require.Contains(t, html, `Protected runtime`)
	require.Contains(t, html, `badge-error`)
	require.Contains(t, html, `aria-label="Switch project runtime target"`)
	require.Contains(t, html, `Target: Production`)
	require.Contains(t, html, `Jordan Lee`)
	require.Contains(t, html, `href="/account"`)
	require.NotContains(t, html, `id="project-switcher"`)
	require.Contains(t, html, `id="environment-switcher"`)
}

func TestShellEnvironmentBadgeClassUsesStoredColor(t *testing.T) {
	t.Parallel()

	require.Contains(t, ShellEnvironmentBadgeClass("red"), "badge-error")
	require.Contains(t, ShellEnvironmentBadgeClass("amber"), "badge-warning")
	require.Contains(t, ShellEnvironmentBadgeClass("blue"), "badge-info")
	require.Contains(t, ShellEnvironmentBadgeClass("gray"), "badge-ghost")
	require.Equal(t, "Protected runtime", ShellEnvironmentRiskLabel("sandbox", true))
	require.Equal(t, "Protected runtime", ShellEnvironmentRiskLabel("prod", false))
	require.Equal(t, "Pre-production target", ShellEnvironmentRiskLabel("stg", false))
	require.Equal(t, "Runtime target", ShellEnvironmentRiskLabel("sandbox", false))
}

type templText string

func (t templText) Render(_ context.Context, w io.Writer) error {
	_, err := w.Write([]byte(t))
	return err
}
