package views

import "context"

type googleSignInKey struct{}

func WithGoogleSignIn(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, googleSignInKey{}, enabled)
}

func WithGoogleSignInLookup(ctx context.Context, lookup func(context.Context) bool) context.Context {
	return context.WithValue(ctx, googleSignInKey{}, lookup)
}

func googleSignInEnabled(ctx context.Context) bool {
	switch value := ctx.Value(googleSignInKey{}).(type) {
	case bool:
		return value
	case func(context.Context) bool:
		return value(ctx)
	default:
		return false
	}
}
