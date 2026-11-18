package http

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// MapServiceError converts a service-layer error into an appropriate API error.
// It extracts the kind, code, and message from the xerror.Error in the error
// chain and takes public detail from that error or its causes. Non-xerror
// errors are logged and mapped to 500.
func MapServiceError(err error) error {
	var xe *xerror.Error
	if !xerror.As(err, &xe) || xe == nil {
		logging.LogError(context.Background(), "unexpected service error", err)
		return apierror.InternalServerError()
	}

	apiErr := apierror.New(xe.Kind().HTTPStatus(), xe.Code(), xe.Message())
	detailed := xerror.PublicDetail(xe)
	if detailed == "" {
		return apiErr
	}

	return apiErr.WithDetailedError(detailed)
}

func mapServiceError(err error) error { return MapServiceError(err) }

func serviceErrorHTTPStatus(err error) int {
	var xe *xerror.Error
	if !xerror.As(err, &xe) || xe == nil {
		return apierror.InternalServerError().HTTPStatus
	}
	return xe.Kind().HTTPStatus()
}
