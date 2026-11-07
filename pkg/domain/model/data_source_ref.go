//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=DataSourceRefCapability -transform=snake -output=data_source_ref_capability_string.go -text -linecomment
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type DataSourceRefCapability int

const (
	DataSourceRefCapabilityUnknown        DataSourceRefCapability = iota // unknown
	DataSourceRefCapabilityPipelineSource                                // pipeline_source
	DataSourceRefCapabilityPipelineSink                                  // pipeline_sink
)

type NewDataSourceRef struct {
	Name                 string `validate:"required,min=1,max=255"`
	Slug                 slug.Slug
	DataSourceType       DataSourceType            `validate:"required"`
	RequiredCapabilities []DataSourceRefCapability `validate:"required,min=1,dive,required"`
}

type UpdateDataSourceRef struct {
	Name                             string                    `validate:"required,min=1,max=255"`
	DataSourceType                   DataSourceType            `validate:"required"`
	RequiredCapabilities             []DataSourceRefCapability `validate:"required,min=1,dive,required"`
	ProtectedEnvironmentConfirmation *slug.Slug
}

type DataSourceRef struct {
	PipelineSlug         slug.Slug
	Slug                 slug.Slug
	Name                 string
	DataSourceType       DataSourceType
	RequiredCapabilities []DataSourceRefCapability
	Timestamps
}

type UpsertDataSourceBinding struct {
	DataSourceSlug                   slug.Slug `validate:"required"`
	ProtectedEnvironmentConfirmation *slug.Slug
}

type DataSourceBinding struct {
	PipelineSlug         slug.Slug
	DataSourceRefSlug    slug.Slug
	DataSourceRefName    string
	EnvironmentSlug      slug.Slug
	DataSourceSlug       slug.Slug
	DataSourceName       string
	DataSourceType       DataSourceType
	DataSourceActive     bool
	DataSourceReadOnly   bool
	RequiredCapabilities []DataSourceRefCapability
	BoundAt              time.Time
	UpdatedAt            time.Time
}

type ResolvedPipelineDataSourceRef struct {
	BindingRevision string `json:"-"` // Deployment provenance must not change historical Proof fingerprints.
	PipelineSlug    slug.Slug
	RefSlug         slug.Slug
	DataSourceSlug  slug.Slug
	DataSourceType  DataSourceType
}
