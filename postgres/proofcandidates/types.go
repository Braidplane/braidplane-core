package proofcandidates

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type Mapping struct {
	ID                     int64
	OrganizationSlug       slug.Slug
	ProjectSlug            slug.Slug
	EnvironmentSlug        slug.Slug
	ProviderConnectionSlug slug.Slug
	Provider               string
	ProviderConnectionID   string
	RepositoryID           string
	TargetRef              string
	PipelinePath           string
}

type MappingLookup struct {
	MappingID            int64
	OrganizationSlug     slug.Slug
	Provider             string
	ProviderConnectionID string
	RepositoryID         string
	TargetRef            string
	PipelinePath         string
}

type Candidate struct {
	ID                    int64
	MappingID             int64
	Slug                  slug.Slug
	CandidateKey          string
	ChangeRequestID       string
	SourceRef             string
	HeadSHA               string
	SourceSHA256          string
	SpecHash              string
	CanonicalSpec         json.RawMessage
	SupersedesCandidateID *int64
	ClosedAt              *time.Time
	CreatedAt             time.Time
}

type WebhookMappingLookup struct {
	Provider             string
	ProviderConnectionID string
	RepositoryID         string
	TargetRef            string
}

type CreateCandidateParams struct {
	MappingID             int64
	Slug                  slug.Slug
	CandidateKey          string
	ChangeRequestID       string
	SourceRef             string
	HeadSHA               string
	SourceSHA256          string
	SpecHash              string
	CanonicalSpec         json.RawMessage
	SupersedesCandidateID *int64
}

type CreateMappingParams struct {
	OrganizationSlug       slug.Slug
	ProjectSlug            slug.Slug
	EnvironmentSlug        slug.Slug
	ProviderConnectionSlug slug.Slug
	Provider               string
	ProviderConnectionID   string
	RepositoryID           string
	TargetRef              string
	PipelinePath           string
}
