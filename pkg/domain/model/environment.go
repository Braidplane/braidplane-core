//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=EnvironmentColor -json -text -linecomment -output=environment_color_enumer.go
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const (
	DefaultDevelopmentEnvironmentSlug slug.Slug = "dev"
	DefaultStagingEnvironmentSlug     slug.Slug = "stg"
	DefaultProductionEnvironmentSlug  slug.Slug = "prod"
)

// EnvironmentColor is the controlled visual palette for runtime environment identity.
type EnvironmentColor int

const (
	EnvironmentColorUnknown EnvironmentColor = iota // unknown
	EnvironmentColorBlue                            // blue
	EnvironmentColorAmber                           // amber
	EnvironmentColorRed                             // red
	EnvironmentColorGreen                           // green
	EnvironmentColorPurple                          // purple
	EnvironmentColorGray                            // gray
)

func DefaultEnvironmentDefinitions() []NewEnvironment {
	developmentDescription := "Default development target for local and in-progress work."
	stagingDescription := "Default staging target for pre-production validation."
	productionDescription := "Default production target for live operations."

	return []NewEnvironment{
		{
			Slug:        DefaultDevelopmentEnvironmentSlug,
			Name:        "Development",
			Description: &developmentDescription,
			Color:       EnvironmentColorBlue,
		},
		{
			Slug:        DefaultStagingEnvironmentSlug,
			Name:        "Staging",
			Description: &stagingDescription,
			Color:       EnvironmentColorAmber,
		},
		{
			Slug:        DefaultProductionEnvironmentSlug,
			Name:        "Production",
			Description: &productionDescription,
			Protected:   true,
			Color:       EnvironmentColorRed,
		},
	}
}

type NewEnvironment struct {
	Name        string `validate:"required,min=1,max=255"`
	Slug        slug.Slug
	Description *string `validate:"omitempty,max=2000"`
	Protected   bool
	Color       EnvironmentColor `validate:"required,environmentcolor"`
}

type UpdateEnvironment struct {
	Name                             string  `validate:"required,min=1,max=255"`
	Description                      *string `validate:"omitempty,max=2000"`
	Protected                        bool
	Color                            EnvironmentColor `validate:"required,environmentcolor"`
	ProtectedEnvironmentConfirmation *slug.Slug
}

type DeleteEnvironment struct {
	Reason                           *string `validate:"omitempty,max=2000"`
	ProtectedEnvironmentConfirmation *slug.Slug
}

type ListEnvironmentsRequest struct {
	ListRequest
	IncludeDeleted bool
}

type Environment struct {
	Slug         slug.Slug
	Name         string
	Description  *string
	Protected    bool
	Color        EnvironmentColor
	DeletedAt    *time.Time
	DeleteReason *string
	Timestamps
}

type EnableProjectEnvironment struct {
	IsDefault                        bool
	ProtectedEnvironmentConfirmation *slug.Slug
}

type ProjectEnvironment struct {
	Environment Environment
	IsDefault   bool
	EnabledAt   time.Time
	UpdatedAt   time.Time
}

type ProjectEnvironmentProtection struct {
	EnvironmentSlug slug.Slug
	Protected       bool
}
