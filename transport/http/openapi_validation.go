package http

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// NewOpenAPIRequestValidator validates requests before generated handlers bind them.
func NewOpenAPIRequestValidator(schema *openapi3.T) (fiber.Handler, error) {
	router, err := gorillamux.NewRouter(schema)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return func(c fiber.Ctx) error {
		request, err := adaptor.ConvertRequest(c, false)
		if err != nil {
			return openAPIValidationFailure(c, err.Error())
		}
		route, params, err := router.FindRoute(request)
		if err != nil {
			return openAPIValidationFailure(c, err.Error())
		}
		err = openapi3filter.ValidateRequest(c.Context(), &openapi3filter.RequestValidationInput{
			Request: request, PathParams: params, Route: route,
			// Principal authentication and permissions belong to route middleware and handlers.
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		})
		if err != nil {
			message := err.Error()
			var multiError openapi3.MultiError
			var requestErr *openapi3filter.RequestError
			var securityErr *openapi3filter.SecurityRequirementsError
			switch {
			case xerror.As(err, &multiError):
				messages := make([]string, 0, len(multiError))
				for _, failure := range multiError {
					// Schema/value dumps can contain submitted credentials. Keep only the diagnostic.
					messages = append(messages, strings.SplitN(failure.Error(), "\n", 2)[0])
				}
				message = "multiple errors encountered: " + strings.Join(messages, " | ")
			case xerror.As(err, &requestErr):
				message = "error in openapi3filter.RequestError: " + strings.SplitN(message, "\n", 2)[0]
			case xerror.As(err, &securityErr):
				message = "error in openapi3filter.SecurityRequirementsError: " + message
			}
			return openAPIValidationFailure(c, message)
		}
		return c.Next()
	}, nil
}

func openAPIValidationFailure(c fiber.Ctx, message string) error {
	response := apierror.OapiValidationFailed(message, reqctx.FiberRequestID(c), fiber.StatusBadRequest)
	return c.Status(response.Error.HTTPStatus).JSON(response)
}
