package logging

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// MethodCompletion describes one completed decorated interface method call.
type MethodCompletion struct {
	Layer       string
	Package     string
	Interface   string
	Method      string
	StartedAt   time.Time
	CompletedAt time.Time
	Err         error
}

// LogMethodCompletion logs one method completion event for generated decorators.
func LogMethodCompletion(ctx context.Context, logger *slog.Logger, completion MethodCompletion) {
	if logger == nil {
		return
	}

	level := slog.LevelDebug
	outcome := "success"
	if completion.Err != nil {
		level = slog.LevelWarn
		outcome = errorValue
	}
	if !logger.Enabled(ctx, level) {
		return
	}

	completedAt := completion.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	duration := completedAt.Sub(completion.StartedAt)
	if completion.StartedAt.IsZero() || duration < 0 {
		duration = 0
	}

	layer := strings.TrimSpace(completion.Layer)
	attrs := []slog.Attr{
		slog.String("braidplane.event", layer+".method.completed"),
		slog.String("braidplane.layer", layer),
		slog.String("braidplane.method", completionMethodName(completion)),
		slog.String("outcome", outcome),
		slog.Int64("duration_ms", duration.Milliseconds()),
	}
	attrs = append(attrs, ContextAttrs(ctx)...)

	if completion.Err != nil {
		attrs = append(attrs, errorAttrs(completion.Err)...)
	}

	logger.LogAttrs(ctx, level, "interface method completed", attrs...)
}

func completionMethodName(completion MethodCompletion) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{completion.Package, completion.Interface, completion.Method} {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "method"
	}
	return strings.Join(parts, ".")
}
