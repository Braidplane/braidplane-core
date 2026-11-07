package frontend

import (
	"context"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type routerManagedHost struct {
	router *Router
}

func (h routerManagedHost) ProjectPage(c fiber.Ctx, title string, organizationSlug, projectSlug slug.Slug) ProjectPage {
	cookieHeader := c.Get(fiber.HeaderCookie)
	permissions := h.router.resolveOrganizationPermissions(c.Context(), cookieHeader, organizationSlug.String())
	shell := h.router.buildShellDataForProjectPage(
		c,
		title,
		organizationSlug.String(),
		projectSlug.String(),
		"",
		"",
		cookieHeader,
	)
	return ProjectPage{
		Shell:             shell,
		CanUpdateProjects: permissions.Can("projects", "update"),
	}
}

func (h routerManagedHost) OrganizationPage(c fiber.Ctx, title string, organizationSlug slug.Slug) OrganizationPage {
	cookie := c.Get(fiber.HeaderCookie)
	permissions := h.router.resolveOrganizationPermissions(c.Context(), cookie, organizationSlug.String())
	return OrganizationPage{
		Shell:     h.router.buildShellDataForOrganizationPage(c, title, organizationSlug.String(), "", "", cookie),
		CanDelete: permissions.Can("organizations", "delete"),
	}
}

func (h routerManagedHost) RequestEditor(c fiber.Ctx) func(context.Context, *http.Request) error {
	return frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie))
}

func (h routerManagedHost) Render(c fiber.Ctx, page templ.Component) error {
	return renderTempl(c, page)
}

func (h routerManagedHost) RedirectToLogin(c fiber.Ctx, returnTo string) error {
	return redirectToLogin(c, returnTo)
}

func (h routerManagedHost) PublicPresentation(c fiber.Ctx) PublicPresentation {
	return PublicPresentation{
		GA4MeasurementID:    h.router.publicAnalyticsMeasurementID(c),
		PublicOrigin:        h.router.publicOrigin(c),
		DocumentationURL:    h.router.documentationURL,
		ApplicationURL:      h.router.applicationURL,
		RegistrationEnabled: h.router.selfServiceRegistrationEnabled,
	}
}

func (h routerManagedHost) APIBaseURL() string {
	return h.router.apiBaseURL
}

func (r *Router) publicAnalyticsMeasurementID(c fiber.Ctx) string {
	requestOrigin := c.Scheme() + "://" + c.Host()
	for _, origin := range r.publicOrigins {
		if strings.EqualFold(requestOrigin, origin) {
			return r.ga4Destinations[strings.ToLower(requestOrigin)]
		}
	}
	return ""
}

func (r *Router) publicOrigin(c fiber.Ctx) string {
	requestOrigin := c.Scheme() + "://" + c.Host()
	for _, publicOrigin := range r.publicOrigins {
		if strings.EqualFold(requestOrigin, publicOrigin) {
			return publicOrigin
		}
	}
	if len(r.publicOrigins) == 0 {
		return ""
	}
	return r.publicOrigins[0]
}
