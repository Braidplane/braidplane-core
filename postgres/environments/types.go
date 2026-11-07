package environments

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	Slug        slug.Slug
	Name        string
	Description *string
	Color       model.EnvironmentColor
	Protected   bool
}

type UpdateParams struct {
	Name        string
	Description *string
	Color       model.EnvironmentColor
	Protected   bool
}

type DeleteParams struct {
	Reason *string
}

type EnableProjectParams struct {
	IsDefault bool
}

type Environment struct {
	Slug         slug.Slug
	Name         string
	Description  *string
	Color        model.EnvironmentColor
	Protected    bool
	DeletedAt    *time.Time
	DeleteReason *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ProjectEnvironment struct {
	Environment Environment
	IsDefault   bool
	EnabledAt   time.Time
	UpdatedAt   time.Time
}
