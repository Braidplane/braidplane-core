package frontend

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func projectSlugForAPI(projectSlug string) string {
	return strings.TrimSpace(projectSlug)
}

func optionalProjectSlug(c fiber.Ctx) (string, error) {
	raw := strings.TrimSpace(c.Params("projectSlug"))
	if raw == "" {
		return "", nil
	}

	projectSlug, err := slug.New(raw)
	if err != nil {
		return "", err
	}
	return projectSlug.String(), nil
}

func requiredProjectSlug(c fiber.Ctx) (string, error) {
	raw := strings.TrimSpace(c.Params("projectSlug"))
	projectSlug, err := slug.New(raw)
	if err != nil {
		return "", err
	}
	return projectSlug.String(), nil
}
