package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	serviceName = "braidplane"
	errorValue  = "error"
)

type contextLogAttrsKey struct{}

var allowedContextAttrKeys = map[string]struct{}{
	"organization.slug": {},
	"user.slug":         {},
	"pipeline.slug":     {},
	"pipeline.run_slug": {},
	"data_source.slug":  {},
	"data_source.type":  {},
	"query.slug":        {},
}

// Config contains logging configuration
type Config struct {
	Level            string
	FilePath         string
	RotationInterval time.Duration
	MaxFileSizeMB    int
	Environment      string
	OTLPLogsEndpoint string
}

// WithAttrs attaches allowlisted log attributes to ctx.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}

	existing, _ := ctx.Value(contextLogAttrsKey{}).([]slog.Attr)
	next := make([]slog.Attr, 0, len(existing)+len(attrs))
	next = append(next, existing...)
	for _, attr := range attrs {
		if _, ok := allowedContextAttrKeys[attr.Key]; !ok {
			continue
		}
		next = upsertAttr(next, attr)
	}

	return context.WithValue(ctx, contextLogAttrsKey{}, next)
}

// ContextAttrs returns standard correlation attributes from context.
func ContextAttrs(ctx context.Context) []slog.Attr {
	var attrs []slog.Attr

	if requestID := reqctx.GetRequestID(ctx); requestID != nil {
		attrs = append(attrs, slog.String("request_id", *requestID))
	}

	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		attrs = append(
			attrs,
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}

	if contextAttrs, ok := ctx.Value(contextLogAttrsKey{}).([]slog.Attr); ok {
		attrs = append(attrs, contextAttrs...)
	}

	return attrs
}

func upsertAttr(attrs []slog.Attr, attr slog.Attr) []slog.Attr {
	for i := range attrs {
		if attrs[i].Key == attr.Key {
			attrs[i] = attr
			return attrs
		}
	}
	return append(attrs, attr)
}

// rotatingWriter wraps a file writer with time-based and size-based rotation
type rotatingWriter struct {
	mu               sync.Mutex
	filePath         string
	currentFile      *os.File
	rotationInterval time.Duration
	maxFileSizeMB    int
	nextRotation     time.Time
	bytesWritten     int64
}

// newRotatingWriter creates a new rotating writer
func newRotatingWriter(
	filePath string,
	rotationInterval time.Duration,
	maxFileSizeMB int,
) (*rotatingWriter, error) {
	w := &rotatingWriter{
		filePath:         filePath,
		rotationInterval: rotationInterval,
		maxFileSizeMB:    maxFileSizeMB,
		nextRotation:     time.Now().Add(rotationInterval),
	}

	if err := w.rotate(); err != nil {
		return nil, err
	}

	return w, nil
}

// Write implements io.Writer with rotation logic
func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Check if rotation is needed (time-based or size-based)
	now := time.Now()
	sizeMB := float64(w.bytesWritten) / (1024 * 1024)

	if now.After(w.nextRotation) || (w.maxFileSizeMB > 0 && sizeMB >= float64(w.maxFileSizeMB)) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.currentFile.Write(p)
	w.bytesWritten += int64(n)
	if err != nil {
		return n, xerror.Unexpected().With(err)
	}
	return n, nil
}

// Close closes the current log file
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentFile != nil {
		if err := w.currentFile.Close(); err != nil {
			return xerror.Unexpected().With(err)
		}
	}
	return nil
}

// rotate keeps one previous file; stdout retention belongs to the container runtime.
func (w *rotatingWriter) rotate() error {
	if w.currentFile != nil {
		if err := w.currentFile.Close(); err != nil {
			return xerror.Unexpected().With(err)
		}
		if err := os.Rename(w.filePath, w.filePath+".1"); err != nil {
			return xerror.Unexpected().With(err)
		}
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(w.filePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return xerror.Unexpected().With(err)
	}

	// Open new file (create or append)
	file, err := os.OpenFile(w.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	// Get current file size
	stat, err := file.Stat()
	if err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return xerror.Unexpected().With(err)
		}
		return xerror.Unexpected().With(err)
	}

	w.currentFile = file
	w.bytesWritten = stat.Size()
	w.nextRotation = time.Now().Add(w.rotationInterval)

	return nil
}

// Setup creates a logger with file rotation and stdout output
func Setup(cfg Config, component string) (*slog.Logger, io.Closer, error) {
	levelName := strings.ToLower(strings.TrimSpace(cfg.Level))
	component = strings.TrimSpace(component)
	environment := strings.TrimSpace(cfg.Environment)
	if environment == "" {
		environment = "local"
	}

	resourceAttrs := []slog.Attr{
		slog.String("service.name", serviceName),
		slog.String("deployment.environment", environment),
		slog.String("braidplane.component", component),
	}

	if levelName == "none" {
		return slog.New(
			slog.DiscardHandler,
		).With(attrsToAny(resourceAttrs)...), &multiCloser{}, nil
	}

	// Parse log level
	var level slog.Level
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case errorValue:
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		AddSource: true,
		Level:     level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.LevelKey {
				attr.Value = slog.StringValue(strings.ToLower(attr.Value.String()))
			}
			return attr
		},
	}

	var writers []io.Writer
	var closers []io.Closer
	var handlers []slog.Handler

	// Always write to stdout
	writers = append(writers, os.Stdout)

	// Add file writer if path is specified
	if cfg.FilePath != "" {
		fileWriter, err := newRotatingWriter(cfg.FilePath, cfg.RotationInterval, cfg.MaxFileSizeMB)
		if err != nil {
			return nil, nil, xerror.Wrapf("failed to create rotating writer: %w", err)
		}
		writers = append(writers, fileWriter)
		closers = append(closers, fileWriter)
	}

	// Create multi-writer
	multiWriter := io.MultiWriter(writers...)

	handlers = append(handlers, slog.NewJSONHandler(multiWriter, opts).WithAttrs(resourceAttrs))

	if endpoint := strings.TrimSpace(cfg.OTLPLogsEndpoint); endpoint != "" {
		handler, closer, err := newOTLPHandler(endpoint, component, level, resourceAttrs)
		if err != nil {
			return nil, nil, err
		}
		handlers = append(handlers, handler)
		closers = append(closers, closer)
	}

	handler := newFanoutHandler(handlers...)
	logger := slog.New(handler)

	// Return logger and a closer that closes all file writers
	return logger, &multiCloser{closers: closers}, nil
}

func newOTLPHandler(
	endpoint string,
	component string,
	level slog.Level,
	resourceAttrs []slog.Attr,
) (slog.Handler, io.Closer, error) {
	exporter, err := otlploghttp.New(
		context.Background(),
		otlploghttp.WithEndpointURL(endpoint),
		otlploghttp.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, nil, xerror.Wrapf("create OTLP log exporter: %w", err)
	}

	res, err := sdkresource.New(
		context.Background(),
		sdkresource.WithAttributes(slogAttrsToOTELAttributes(resourceAttrs)...),
	)
	if err != nil {
		return nil, nil, xerror.Wrapf("create OTLP log resource: %w", err)
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)
	handler := otelslog.NewHandler(
		serviceName,
		otelslog.WithLoggerProvider(provider),
		otelslog.WithSource(true),
	)

	return levelHandler{handler: handler, level: level}, closeFunc(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			return xerror.Wrapf("shutdown OTLP log provider: %w", err)
		}
		return nil
	}), nil
}

func slogAttrsToOTELAttributes(attrs []slog.Attr) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if attr.Value.Kind() != slog.KindString {
			continue
		}
		result = append(result, attribute.String(attr.Key, attr.Value.String()))
	}
	return result
}

func attrsToAny(attrs []slog.Attr) []any {
	result := make([]any, len(attrs))
	for i := range attrs {
		result[i] = attrs[i]
	}
	return result
}

type fanoutHandler struct {
	handlers []slog.Handler
}

func newFanoutHandler(handlers ...slog.Handler) slog.Handler {
	return fanoutHandler{handlers: handlers}
}

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return fanoutHandler{handlers: handlers}
}

func (h fanoutHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return fanoutHandler{handlers: handlers}
}

type levelHandler struct {
	handler slog.Handler
	level   slog.Level
}

func (h levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level && h.handler.Enabled(ctx, level)
}

func (h levelHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.handler.Handle(ctx, record)
}

func (h levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelHandler{handler: h.handler.WithAttrs(attrs), level: h.level}
}

func (h levelHandler) WithGroup(name string) slog.Handler {
	return levelHandler{handler: h.handler.WithGroup(name), level: h.level}
}

// multiCloser implements io.Closer for multiple closers
type multiCloser struct {
	closers []io.Closer
}

type closeFunc func() error

func (fn closeFunc) Close() error {
	return fn()
}

func (mc *multiCloser) Close() error {
	for _, c := range mc.closers {
		if err := c.Close(); err != nil {
			return xerror.Wrapf("close logger writer: %w", err)
		}
	}
	return nil
}
