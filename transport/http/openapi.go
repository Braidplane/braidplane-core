package http

import (
	"github.com/Braidplane/braidplane-core/pkg/publicschema"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
)

// PublicOpenAPIDocument omits internal paths and types without changing server validation.
func PublicOpenAPIDocument(schema *openapi3.T) ([]byte, error) {
	return publicschema.Project(schema)
}

// RegisterOpenAPIRoute publishes the already-validated API document at a
// stable unauthenticated endpoint for programmatic consumers and API docs.
func RegisterOpenAPIRoute(app *fiber.App, document []byte) {
	app.Get("/openapi.json", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		return c.Send(document)
	})
}
