package googleclient

import (
	"context"
	"net/http"

	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"google.golang.org/api/googleapi"
)

// LogError retains provider diagnostics without dumping response bodies or detail payloads.
func LogError(ctx context.Context, err error, sensitiveValues ...string) {
	var provider *googleapi.Error
	if xerror.As(err, &provider) {
		diagnostic := *provider
		diagnostic.Body, diagnostic.Details, diagnostic.Errors = "", nil, nil
		if diagnostic.Message == "" {
			diagnostic.Message = http.StatusText(diagnostic.Code)
		}
		err = &diagnostic
	}
	logging.LogError(ctx, "Google provider request failed", err, sensitiveValues...)
}
