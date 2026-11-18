package userssvc

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

const localPasswordUnavailableDetail = "local password sign-in is not enabled for this account"

// Re-exported from repository layer — single source of truth.
var (
	ErrUserNotFound         = users.ErrUserNotFound
	ErrEmailAlreadyExists   = users.ErrEmailAlreadyExists
	ErrSlugAlreadyExists    = users.ErrSlugAlreadyExists
	ErrMFAChallengeConsumed = users.ErrMFAChallengeConsumed
)

// Service-only errors.
var (
	ErrInvalidUserSlug           = xerror.InvalidInput(xerror.User)
	ErrInvalidInput              = xerror.ValidationFailed()
	ErrInvalidUsernameOrPassword = xerror.InvalidCredentials()
	ErrEmailNotVerified          = xerror.EmailNotVerified()
	ErrAlreadyVerified           = xerror.AlreadyVerified()
	ErrInvalidToken              = xerror.InvalidToken()
	ErrLocalPasswordUnavailable  = xerror.ValidationFailed().WithPublicDetail(localPasswordUnavailableDetail).Detail(localPasswordUnavailableDetail)
	ErrMFARequired               = xerror.ValidationFailed().Detail("mfa verification required")
	ErrMFAChallengeExpired       = xerror.InvalidToken().Detail("mfa challenge expired")
	ErrMFAChallengeLocked        = xerror.InvalidToken().Detail("mfa challenge attempt limit exceeded")
	errMFAEmailRateLimited       = xerror.New(xerror.KindUnavailable, "MFA_EMAIL_RATE_LIMITED", "MFA email delivery rate limited")
)

// MFAEmailRateLimitError carries the delivery window to the sign-in handler.
type MFAEmailRateLimitError struct {
	ResetAt time.Time
}

func (*MFAEmailRateLimitError) Error() string { return "MFA email delivery budget exhausted" }
