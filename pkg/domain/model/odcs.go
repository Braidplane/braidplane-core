package model

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type NewODCSContract struct {
	Slug   slug.Slug
	Name   string `validate:"required,min=1,max=255"`
	ODCSID string `validate:"required,min=1,max=255"`
}

type UpdateODCSContract struct {
	Name   string `validate:"required,min=1,max=255"`
	Status string `validate:"oneof=active archived"`
}

type ODCSContract struct {
	Slug   slug.Slug
	Name   string
	ODCSID string
	Status string
	Timestamps
}

type ODCSSchemaObject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ODCSRevision struct {
	Slug            slug.Slug
	Digest          string
	ODCSVersion     string
	DocumentVersion string
	DeclaredStatus  string
	Status          string
	SchemaObjects   []ODCSSchemaObject
	Document        json.RawMessage
	CreatedAt       time.Time
}

// Paths identify declarations within the exact immutable document digest.
// Enforced means compiled into a runtime check, never that a check has passed.
type ODCSDeclarationCoverage struct {
	Path          string `json:"path"`
	State         string `json:"state"`
	Reason        string `json:"reason"`
	Applicability string `json:"applicability"`
	RuntimeScope  string `json:"runtime_scope,omitempty"`
}

// PinnedODCSPort retains the executable shape and provenance independently of catalog reads.
type PinnedODCSPort struct {
	StepSlug       slug.Slug                 `json:"step_slug"`
	Port           string                    `json:"port"`
	ContractSlug   slug.Slug                 `json:"contract_slug"`
	RevisionSlug   slug.Slug                 `json:"revision_slug"`
	SchemaObjectID string                    `json:"schema_object_id"`
	Digest         string                    `json:"digest"`
	Shape          *payload.Contract         `json:"shape"`
	CompilerID     string                    `json:"compiler_id,omitempty"`
	Coverage       []ODCSDeclarationCoverage `json:"coverage,omitempty"`
}

// ODCSArtifact retains original bytes independently of semantic revision identity.
type ODCSArtifact struct {
	ContractSlug   slug.Slug `json:"contract_slug"`
	RevisionSlug   slug.Slug `json:"revision_slug"`
	Path           string    `json:"path"`
	Format         string    `json:"format"`
	Digest         string    `json:"digest"`
	ArtifactDigest string    `json:"artifact_digest"`
	Original       []byte    `json:"original"`
	SourceRepo     string    `json:"source_repo"`
	Commit         string    `json:"commit"`
}

type ODCSArtifactImport struct {
	Commit    string         `json:"commit"`
	Applied   bool           `json:"applied"`
	Artifacts []ODCSArtifact `json:"artifacts"`
}
