package users

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	Slug     slug.Slug
	Email    string
	FullName string
	Password string
	Role     model.Role
	Locale   string
}

type CreateOIDCUserParams struct {
	Slug           slug.Slug
	Email          string
	FullName       string
	OIDCProvider   string
	OIDCProviderID string
	AvatarURL      string
	Role           model.Role
	Locale         string
}

type UpdateProfileParams struct {
	Slug            slug.Slug
	FullName        *string
	Locale          *string
	AvatarURL       *string
	PhoneNumber     *string
	JobTitle        *string
	Timezone        *string
	MFAEmailEnabled *bool
}

type CreateEmailVerificationTokenParams struct {
	Slug                            slug.Slug
	EmailVerificationTokenHash      string
	EmailVerificationTokenExpiresAt time.Time
}

type CreatePasswordResetTokenParams struct {
	Slug                        slug.Slug
	PasswordResetTokenHash      string
	PasswordResetTokenExpiresAt time.Time
}

type UserPublic struct {
	AuthenticationVersion int64
	Slug                  slug.Slug
	Email                 string
	FullName              string
	Role                  model.Role
	Locale                string
	AvatarURL             string
	PhoneNumber           string
	JobTitle              string
	Timezone              string
	MFAEmailEnabled       bool
	HasLocalPassword      bool
	VerifiedAt            time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// UserWithPassword is a user with a password.
// Separate type has been created to make it
// harder to leak password hashes by accident.
type UserWithPassword struct {
	Slug            slug.Slug
	Email           string
	FullName        string
	Password        string
	MFAEmailEnabled bool
}

type MFAChallenge struct {
	ID           int
	User         *UserPublic
	CodeHash     string
	ExpiresAt    time.Time
	ConsumedAt   time.Time
	AttemptCount int
}
