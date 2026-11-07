package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// CheckWindow links the attempt's source window without copying cursor values.
type StructuralCheckWindow struct {
	SourceSteps        []slug.Slug `json:"source_steps"`
	Kind               string      `json:"kind"`
	HasLowerBound      bool        `json:"has_lower_bound"`
	HasUpperBound      bool        `json:"has_upper_bound"`
	BackfillSeriesSlug string      `json:"backfill_series_slug,omitempty"`
	BackfillChunkIndex int         `json:"backfill_chunk_index,omitempty"`
}

// OperationalSelection identifies an exact retained check. A spec hash alone
// cannot authorize substituting another compilation, deployment or attempt.
type ProofOperationalSelection struct {
	CandidateSlug              slug.Slug              `json:"candidate_slug"`
	Candidate                  ProofCandidateIdentity `json:"candidate"`
	Pipeline                   slug.Slug              `json:"pipeline"`
	VersionCommit              string                 `json:"version_commit"`
	CompiledPlanDigest         string                 `json:"compiled_plan_digest"`
	DeploymentResolutionDigest string                 `json:"deployment_resolution_digest"`
	Run                        slug.Slug              `json:"run"`
	Attempt                    int                    `json:"attempt"`
	CheckID                    string                 `json:"check_id"`
	Step                       slug.Slug              `json:"step"`
	Input                      string                 `json:"input"`
	Origin                     string                 `json:"origin"`
	Phase                      string                 `json:"phase"`
	Declaration                string                 `json:"declaration"`
	ProjectionDigest           string                 `json:"projection_digest"`
	Target                     slug.Slug              `json:"target"`
	Window                     StructuralCheckWindow  `json:"window"`
	Evaluator                  string                 `json:"evaluator"`
	EvaluatorPolicy            string                 `json:"evaluator_policy"`
	StartedAt                  *time.Time             `json:"started_at"`
	CompletedAt                *time.Time             `json:"completed_at"`
	EvidenceDigest             string                 `json:"evidence_digest"`
}

// ProofOperationalCapture links a new receipt to retained evidence without
// copying run payloads or extending the source record's retention.
type ProofOperationalCapture struct {
	SourceReceiptID slug.Slug                 `json:"source_receipt_id"`
	PolicyVersion   string                    `json:"policy_version"`
	Selection       ProofOperationalSelection `json:"selection"`
	RecordDigest    string                    `json:"record_digest"`
	ExpiresAt       time.Time                 `json:"expires_at"`
}

func (selection ProofOperationalSelection) Validate() error {
	denied := proofValidationError("operational Proof selection is invalid")
	if selection.Candidate.Validate() != nil || selection.VersionCommit != selection.Candidate.HeadSHA || selection.Attempt < 1 {
		return denied
	}
	for _, id := range []slug.Slug{selection.CandidateSlug, selection.Pipeline, selection.Run, selection.Step} {
		if _, err := slug.New(id.String()); err != nil {
			return denied
		}
	}
	if selection.Target != "" {
		if _, err := slug.New(selection.Target.String()); err != nil {
			return denied
		}
	}
	for _, digest := range []string{selection.CompiledPlanDigest, selection.DeploymentResolutionDigest} {
		if !strings.HasPrefix(digest, "sha256:") {
			return denied
		}
	}
	for _, digest := range []string{strings.TrimPrefix(selection.CompiledPlanDigest, "sha256:"), strings.TrimPrefix(selection.DeploymentResolutionDigest, "sha256:"), selection.CheckID, selection.ProjectionDigest} {
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size || digest != strings.ToLower(digest) {
			return denied
		}
	}
	if selection.Origin != "step" && selection.Origin != "operation" || selection.Declaration == "" {
		return denied
	}
	switch selection.Phase {
	case "normalized_input":
		if selection.Input == "" {
			return denied
		}
	case "event_admission", "producer_output", "pre_sink":
		if selection.Input != "" {
			return denied
		}
	default:
		return denied
	}
	window := selection.Window
	if window.Kind != "full" && window.Kind != "incremental" && window.Kind != "backfill" || window.BackfillChunkIndex < 0 {
		return denied
	}
	if (window.Kind == "backfill") != (window.BackfillSeriesSlug != "") || window.Kind != "backfill" && window.BackfillChunkIndex != 0 {
		return denied
	}
	if window.BackfillSeriesSlug != "" {
		if _, err := slug.New(window.BackfillSeriesSlug); err != nil {
			return denied
		}
	}
	previous := ""
	for _, source := range window.SourceSteps {
		if _, err := slug.New(source.String()); err != nil || source.String() <= previous {
			return denied
		}
		previous = source.String()
	}
	return nil
}

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=ProofOperationalEvidenceStatus -json -text -linecomment -output=proof_operational_enumer.go

// ProofOperationalEvidenceStatus describes current support for a historical capture.
type ProofOperationalEvidenceStatus int

const (
	ProofOperationalEvidenceStatusUnknown   ProofOperationalEvidenceStatus = iota // unknown
	ProofOperationalEvidenceStatusAvailable                                       // available
	ProofOperationalEvidenceStatusMissing                                         // missing
	ProofOperationalEvidenceStatusExpired                                         // expired
	ProofOperationalEvidenceStatusChanged                                         // changed
)
