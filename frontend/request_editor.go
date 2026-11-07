package frontend

import (
	"context"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/propagation"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
)

func frontendRequestEditor(ctx context.Context, cookieHeader string) api.RequestEditorFn {
	trimmedCookie := strings.TrimSpace(cookieHeader)
	requestID := strings.TrimSpace(requestIDFromContext(ctx))
	csrfToken := strings.TrimSpace(httpsecurity.CSRFTokenFromContext(ctx))
	clientIP := strings.TrimSpace(reqctx.GetClientIP(ctx))
	if requestID == "" {
		requestID = uuid.New().String()
	}

	return func(_ context.Context, req *http.Request) error {
		if trimmedCookie != "" {
			req.Header.Set(fiber.HeaderCookie, trimmedCookie)
		}
		if trimmedCookie != "" && csrfToken != "" && !httpsecurity.SafeMethod(req.Method) {
			req.Header.Set(httpsecurity.CSRFHeader, csrfToken)
		}
		if clientIP != "" {
			// Replace any caller-provided forwarding chain with the frontend's trusted client identity.
			req.Header.Set(fiber.HeaderXForwardedFor, clientIP)
		}

		req.Header.Set(fiber.HeaderXRequestID, requestID)
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		).Inject(ctx, propagation.HeaderCarrier(req.Header))
		if editor, ok := ctx.Value(requestEditorKey{}).(api.RequestEditorFn); ok {
			return editor(ctx, req)
		}
		return nil
	}
}

func requestIDFromContext(ctx context.Context) string {
	requestID := reqctx.GetRequestID(ctx)
	if requestID == nil {
		return ""
	}

	return *requestID
}

func (r *Router) ensureFrontendRequestID(c fiber.Ctx) error {
	requestID := strings.TrimSpace(reqctx.FiberRequestID(c))
	if requestID == "" {
		requestID = strings.TrimSpace(requestIDFromContext(c.Context()))
	}
	if requestID == "" {
		requestID = uuid.New().String()
		c.Locals("requestid", requestID)
	}

	c.SetContext(reqctx.WithRequestID(c.Context(), requestID))
	return c.Next()
}

type requestEditorKey struct{}

// WithRequestEditor adds distribution metadata to frontend API requests.
func WithRequestEditor(ctx context.Context, editor api.RequestEditorFn) context.Context {
	return context.WithValue(ctx, requestEditorKey{}, editor)
}
