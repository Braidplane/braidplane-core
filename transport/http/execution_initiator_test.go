package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestAuthorizedExecutionAccountAttribution(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"user", "api-key", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s := &server{authorizationService: &fakeAuthService{
				CanFn: func(context.Context, authorization.Input) (bool, error) { return scenario != "denied", nil },
			}}
			var user slug.Slug
			called := false
			app := fiber.New()
			app.Get("/test", func(c fiber.Ctx) error {
				input := authorization.Input{UserSlug: "alice", Action: model.Read, Resource: model.ResourceProfile}
				if scenario == "api-key" {
					input.UserSlug, input.APIKeySlug = "", "automation"
					c.SetContext(executiondelivery.WithInitiatingUser(c.Context(), "stale-user"))
				}
				if err := s.canWithInput(c, input); err != nil {
					return err
				}
				called = true
				user = executiondelivery.InitiatingUser(c.Context())
				return c.SendStatus(fiber.StatusNoContent)
			})
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil))
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if scenario == "denied" {
				require.False(t, called)
			} else {
				require.True(t, called)
				require.Equal(t, fiber.StatusNoContent, response.StatusCode)
				if scenario == "user" {
					require.Equal(t, slug.Slug("alice"), user)
				} else {
					require.Empty(t, user)
				}
			}
		})
	}
}
