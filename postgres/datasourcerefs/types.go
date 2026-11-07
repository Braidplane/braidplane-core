package datasourcerefs

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateDataSourceRefParams struct {
	Slug                 slug.Slug
	Name                 string
	DataSourceType       model.DataSourceType
	RequiredCapabilities []model.DataSourceRefCapability
}

type UpdateDataSourceRefParams struct {
	Name                 string
	DataSourceType       model.DataSourceType
	RequiredCapabilities []model.DataSourceRefCapability
}

type UpsertEnvironmentBindingParams struct {
	DataSourceSlug slug.Slug
}

type DataSourceRef struct {
	PipelineSlug         slug.Slug
	Slug                 slug.Slug
	Name                 string
	DataSourceType       model.DataSourceType
	RequiredCapabilities []model.DataSourceRefCapability
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type DataSourceBinding struct {
	PipelineSlug         slug.Slug
	DataSourceRefSlug    slug.Slug
	DataSourceRefName    string
	EnvironmentSlug      slug.Slug
	DataSourceSlug       slug.Slug
	DataSourceName       string
	DataSourceType       model.DataSourceType
	DataSourceActive     bool
	DataSourceReadOnly   bool
	RequiredCapabilities []model.DataSourceRefCapability
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
