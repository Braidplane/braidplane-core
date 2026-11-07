package model

import (
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type Owner struct {
	Slug  slug.Slug
	Email string
	Timestamps
}
