package executiondelivery

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ErrAccountUnavailable is terminal for queued work; retry cannot restore the
// initiating identity or make an invalid account binding authoritative.
var ErrAccountUnavailable = xerror.New(
	xerror.KindUnauthorized,
	"EXECUTION_ACCOUNT_UNAVAILABLE",
	"execution request account is unavailable",
)

type initiatingUserKey struct{}

// WithInitiatingUser carries an already-authorized account across execution
// adapters. It is attribution, not a substitute for resource authorization.
func WithInitiatingUser(ctx context.Context, user slug.Slug) context.Context {
	return context.WithValue(ctx, initiatingUserKey{}, user)
}

func InitiatingUser(ctx context.Context) slug.Slug {
	user, _ := ctx.Value(initiatingUserKey{}).(slug.Slug)
	return user
}
