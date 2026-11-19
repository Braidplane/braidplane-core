package run

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const DefaultIncrementalCheckpointNamespace = "normal"

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=IncrementalCheckpointStatus -json -text -linecomment -output=incremental_checkpoint_status_enumer.go
//go:generate enumer -type=IncrementalCheckpointTransitionStatus -json -text -linecomment -output=incremental_checkpoint_transition_status_enumer.go
type IncrementalCheckpointStatus int

const (
	IncrementalCheckpointStatusUnknown    IncrementalCheckpointStatus = iota // unknown
	IncrementalCheckpointStatusCommitted                                     // committed
	IncrementalCheckpointStatusBlocked                                       // blocked
	IncrementalCheckpointStatusSuperseded                                    // superseded
)

type IncrementalCheckpointTransitionStatus int

const (
	IncrementalCheckpointTransitionUnknown   IncrementalCheckpointTransitionStatus = iota // unknown
	IncrementalCheckpointTransitionCommitted                                              // committed
	IncrementalCheckpointTransitionBlocked                                                // blocked
)

type IncrementalCheckpointLookup struct {
	PipelineSlug         slug.Slug
	SourceStepSlug       slug.Slug
	CheckpointNamespace  string
	CheckpointKind       string
	DataSourceSlug       slug.Slug
	OperationType        string
	OperationRef         slug.Slug
	OperationInlineHash  string
	OperationIdentity    string
	PipelineVersionID    int64
	IncrementalField     string
	IncrementalCursor    string
	IncrementalOrdering  []string
	IncrementalDedupeKey string
	IncrementalLookback  string
}

type IncrementalCheckpoint struct {
	PipelineSlug        slug.Slug
	PipelineVersionID   int64
	SourceStepSlug      slug.Slug
	CheckpointNamespace string
	CheckpointKind      string
	DataSourceSlug      slug.Slug
	OperationType       string
	OperationRef        slug.Slug
	OperationInlineHash string
	OperationIdentity   string
	ObservedLowerBound  json.RawMessage
	ObservedUpperBound  json.RawMessage
	DisplayMetadata     json.RawMessage
	CommitStatus        IncrementalCheckpointStatus
	ObservedRunSlug     string
	CommittingRunSlug   string
	BlockedReason       string
	ObservedAt          time.Time
	CommittedAt         *time.Time
	SupersededAt        *time.Time
	BlockedAt           *time.Time
	ExpiredAt           *time.Time
	CreatedAt           time.Time
}

type IncrementalCheckpointInput struct {
	Lookup            IncrementalCheckpointLookup
	LowerBound        json.RawMessage
	UpperBound        json.RawMessage
	CurrentCheckpoint *IncrementalCheckpoint
	Window            *IncrementalSourceWindow
}

type IncrementalSourceWindow struct {
	// RecoveryPredecessor permits a retained normal-run window to advance only from its original checkpoint.
	RecoveryPredecessor *string         `json:"recovery_predecessor,omitempty"`
	LowerBound          json.RawMessage `json:"lower_bound,omitempty"`
	UpperBound          json.RawMessage `json:"upper_bound,omitempty"`
	DisplayMetadata     json.RawMessage `json:"display_metadata,omitempty"`
}

type IncrementalRunOptions struct {
	CheckpointNamespace  string
	SourceWindows        map[slug.Slug]IncrementalSourceWindow
	SkipCheckpointCommit bool
	PromoteCheckpoint    bool
	BackfillSeriesSlug   slug.Slug
	BackfillChunkIndex   int
	BackfillChunkCount   int
	TriggerSource        string
}

type IncrementalCheckpointObservation struct {
	SourceStepSlug      slug.Slug
	CheckpointNamespace string
	CheckpointKind      string
	ObservedLowerBound  json.RawMessage
	ObservedUpperBound  json.RawMessage
	DisplayMetadata     json.RawMessage
	ObservedAt          time.Time
}

type IncrementalCheckpointTransition struct {
	RecoveryPredecessor *string
	Status              IncrementalCheckpointTransitionStatus
	Lookup              IncrementalCheckpointLookup
	SourceStepSlug      slug.Slug
	CheckpointNamespace string
	CheckpointKind      string
	DataSourceSlug      slug.Slug
	OperationType       string
	OperationRef        slug.Slug
	OperationInlineHash string
	OperationIdentity   string
	ObservedLowerBound  json.RawMessage
	ObservedUpperBound  json.RawMessage
	DisplayMetadata     json.RawMessage
	ObservedRunSlug     string
	CommittingRunSlug   string
	BlockedByStep       slug.Slug
	BlockedReason       string
	ObservedAt          time.Time
}

// RecoveryPredecessor identifies a normal commit or an operator bootstrap in the scoped cursor history.
func (c *IncrementalCheckpoint) RecoveryPredecessor() string {
	if c == nil {
		return ""
	}
	if c.CommittingRunSlug == "" {
		return "bootstrap:" + c.ObservedRunSlug
	}
	return c.CommittingRunSlug
}

// CheckRecoveryPredecessor prevents retained windows from superseding newer commits.
func CheckRecoveryPredecessor(expected string, current *IncrementalCheckpoint) error {
	if current.RecoveryPredecessor() == expected {
		return nil
	}
	return ErrRecoveryCheckpointChanged
}

var ErrRecoveryCheckpointChanged = xerror.New(xerror.KindInvalidInput, "RECOVERY_CHECKPOINT_CHANGED", "the source checkpoint changed after the original evaluation")
