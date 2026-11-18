package googleclient

import (
	"context"
	"log/slog"

	"google.golang.org/api/googleapi"

	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// LogError records a provider status and bounded classification.
func LogError(ctx context.Context, err error) {
	var provider *googleapi.Error
	if xerror.As(err, &provider) && provider != nil && provider.Code >= 100 && provider.Code <= 599 {
		attrs := append(logging.ContextAttrs(ctx), logging.ErrorAttrs(err)...)
		attrs = append(attrs, slog.Int("upstream.status_code", provider.Code))
		slog.Default().LogAttrs(ctx, slog.LevelError, "Google provider request failed", attrs...)
		return
	}
	logging.LogError(ctx, "Google provider request failed", err)
}
