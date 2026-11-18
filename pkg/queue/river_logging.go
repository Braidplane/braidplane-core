package queue

import (
	"context"
	"log/slog"

	"github.com/Braidplane/braidplane-core/pkg/logging"
)

// River also logs infrastructure errors that occur outside the work middleware.
type riverLogHandler struct{ slog.Handler }

func (h riverLogHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		safe.AddAttrs(riverLogAttr(attr))
		return true
	})
	return h.Handler.Handle(ctx, safe)
}

func (h riverLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		safe = append(safe, riverLogAttr(attr))
	}
	return riverLogHandler{h.Handler.WithAttrs(safe)}
}

func (h riverLogHandler) WithGroup(name string) slog.Handler {
	return riverLogHandler{h.Handler.WithGroup(name)}
}

func riverLogAttr(attr slog.Attr) slog.Attr {
	switch attr.Key {
	case "error", "err":
		if err, ok := attr.Value.Any().(error); ok {
			return logging.ErrorAttr(err)
		}
		return slog.String("error.type", "error")
	case "panic_val":
		return slog.String("error.type", "WORKER_PANIC")
	default:
		return attr
	}
}
