package frontend

import "github.com/gofiber/fiber/v3"

// CommunityPresentation is the explicit route presentation for Community builds.
type CommunityPresentation struct{}

func (CommunityPresentation) RegisterPublicRoutes(app *fiber.App, _ ManagedHost) {
	app.Get("/", func(c fiber.Ctx) error {
		return c.Redirect().Status(fiber.StatusSeeOther).To("/auth/login")
	})
	for _, path := range []string{"/privacy", "/pricing", "/solutions", "/solutions/*", "/robots.txt", "/sitemap.xml"} {
		app.All(path, retiredPublicPathNotFound)
	}
	app.All("/orgs/:slug/projects/:projectSlug/settlement-review", retiredPublicPathNotFound)
}

func (CommunityPresentation) RegisterWorkspaceRoutes(fiber.Router, ManagedHost) {}
