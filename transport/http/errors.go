package http

import (
	"log/slog"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// MapServiceError converts a service-layer error into an appropriate API error.
// It extracts the kind, code, and message from the xerror.Error in the error
// chain to build the response. Non-xerror errors are logged and mapped to 500.
func MapServiceError(err error) error {
	var xe *xerror.Error
	if !xerror.As(err, &xe) {
		slog.Error("unexpected service error", "error", err)
		return apierror.InternalServerError()
	}

	apiErr := apierror.New(xe.Kind().HTTPStatus(), xe.Code(), xe.Message())
	detailed := xerror.PublicDetail(err)
	if detailed == "" {
		return apiErr
	}

	return apiErr.WithDetailedError(detailed)
}

func mapServiceError(err error) error { return MapServiceError(err) }

func serviceErrorHTTPStatus(err error) int {
	var xe *xerror.Error
	if !xerror.As(err, &xe) {
		return apierror.InternalServerError().HTTPStatus
	}
	return xe.Kind().HTTPStatus()
}
