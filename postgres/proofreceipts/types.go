package proofreceipts

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CandidateTarget struct {
	OrganizationID         int32
	ProjectID              int64
	ProjectEnvironmentID   int64
	MappingID              int64
	CandidateID            int64
	SupersedesCandidateID  *int64
	CandidateSlug          slug.Slug
	OrganizationSlug       slug.Slug
	ProjectSlug            slug.Slug
	EnvironmentSlug        slug.Slug
	ProviderConnectionSlug slug.Slug
	RepositoryID           string
	ChangeRequestID        string
	HeadSHA                string
	TargetRef              string
	PipelinePath           string
	SpecHash               string
	CanonicalSpec          json.RawMessage
	CreatedAt              time.Time
}

type Evidence struct {
	ID         int64
	Digest     string
	ObservedAt time.Time
	ExpiresAt  *time.Time
}

type EvidenceParams struct {
	Kind                  string
	Digest                string
	SourceIdentifier      string
	BindingFingerprint    string
	CapabilityFingerprint string
	ObservedAt            time.Time
	ExpiresAt             *time.Time
}

type LatestReceipt struct {
	ID   int64
	Slug slug.Slug
}

type CreateReceiptParams struct {
	EvidenceID int64
	ReceiptKey string
	Receipt    model.ProofReceipt
	Supersedes *LatestReceipt
}

type Receipt struct {
	Value      model.ProofReceipt
	Superseded bool
}

type LifecycleLink struct {
	Kind                  model.ProofLifecycleLinkKind
	ReceiptSlug           slug.Slug
	Identifier            string
	SpecHash              string
	BindingFingerprint    string
	CapabilityFingerprint string
	LinkedAt              time.Time
	RetainUntil           time.Time
	Superseded            bool
	PipelineSlug          *slug.Slug
	RunSlug               *slug.Slug
	CurrentSpecHash       string
	CurrentIdentifier     string
}

type LinkDeploymentParams struct {
	ReceiptSlug slug.Slug
	Identifier  string
	SpecHash    string
	LinkedAt    time.Time
}
