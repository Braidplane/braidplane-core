package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type EnvironmentSecret struct {
	EnvironmentSlug slug.Slug
	Key             string
	Description     *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type UpsertEnvironmentSecret struct {
	Key                              string  `validate:"required,min=1,max=100"`
	Value                            string  `validate:"required,max=20000"`
	Description                      *string `validate:"omitempty,max=2000"`
	ProtectedEnvironmentConfirmation *slug.Slug
}
