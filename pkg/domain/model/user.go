//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type UserLogin struct {
	Email    string `validate:"required,email"`
	Password string `validate:"required,min=8,max=1024"`
}

type SessionLoginResult struct {
	User         *User
	MFARequired  bool
	MFAChallenge *MFAChallenge
}

type MFAChallenge struct {
	ChallengeToken string
	ExpiresAt      time.Time
}

type NewUser struct {
	FullName string `validate:"required,min=1,max=100"`
	Email    string `validate:"required,email"`
	Password string `validate:"required,min=8,max=1024"`
	Role     Role   `validate:"required,role"`
	Locale   string
}

type OIDCUserInfo struct {
	Provider          string // e.g., "google", "github", etc.
	ProviderID        string // Provider-specific user ID (e.g., Google sub, GitHub ID, etc.)
	Email             string
	Name              string
	Picture           string
	Locale            string
	PreferredUsername string // OIDC preferred_username claim
}

type UpdateUserProfile struct {
	FullName        *string `validate:"omitempty,min=1,max=100"`
	Locale          *string `validate:"omitempty,bcp47_language_tag"`
	AvatarURL       *string `validate:"omitempty,max=2048"`
	PhoneNumber     *string `validate:"omitempty,max=32"`
	JobTitle        *string `validate:"omitempty,max=120"`
	Timezone        *string `validate:"omitempty,max=64"`
	MFAEmailEnabled *bool
}

type User struct {
	AuthenticationVersion int64
	Slug                  slug.Slug
	Email                 string
	FullName              string
	Role                  Role
	Locale                string
	AvatarURL             string
	PhoneNumber           string
	JobTitle              string
	Timezone              string
	MFAEmailEnabled       bool
	HasLocalPassword      bool
	VerifiedAt            time.Time
	Timestamps
}

// UserWithPassword is a user with a password.
// Separate type has been created to make it
// harder to leak password hashes by accident.
type UserWithPassword struct {
	User
	Password string
}
