package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type NewRole struct {
	Name      string `validate:"required,min=1,max=100"`
	Slug      slug.Slug
	IsDefault bool
}

type UpdateRole struct {
	Name string `validate:"required,min=1,max=100"`
}

type OrgRole struct {
	Slug      slug.Slug
	Name      string
	IsDefault bool
	DeletedAt *time.Time
	Timestamps
}
