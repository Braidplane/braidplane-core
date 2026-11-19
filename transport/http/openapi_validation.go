package http

import (
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
			return openAPIValidationFailure(c, err)
		}
		route, params, err := router.FindRoute(request)
		if err != nil {
			return openAPIValidationFailure(c, err)
		}
		err = openapi3filter.ValidateRequest(c.Context(), &openapi3filter.RequestValidationInput{
			Request: request, PathParams: params, Route: route,
			// Principal authentication and permissions belong to route middleware and handlers.
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, KeepValidatedValue: true},
		})
		if err != nil {
			return openAPIValidationFailure(c, err)
		}
		// The generated strict Fiber handler binds this validated JSON value.
		if body, ok := request.Body.(interface{ ValidatedValue() any }); ok {
			c.Locals("x-oapi-codegen-validated-body", body.ValidatedValue())
		}
		return c.Next()
	}, nil
}

func openAPIValidationFailure(c fiber.Ctx, err error) error {
	response := apierror.OapiValidationFailed(err, reqctx.FiberRequestID(c))
	return c.Status(response.Error.HTTPStatus).JSON(response)
}
