//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=ProofClaimFamily,ProofEvidenceLevel,ProofVerdict,ProofEvidenceReferenceKind,ProofLifecycleLinkKind -json -text -linecomment -output=proof_enumer.go
package model

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	// internal/redaction is a runtime-neutral shared helper permitted by internal_boundary.
	"github.com/Braidplane/braidplane-core/pkg/redaction"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const ProofReceiptSchemaVersion = 1

// ProofClaimFamily is the bounded vocabulary of promises Braidplane can evaluate.
type ProofClaimFamily int

const (
	ProofClaimFamilyUnknown             ProofClaimFamily = iota // unknown
	ProofClaimFamilySourceIdentity                              // source_identity
	ProofClaimFamilyDestinationIdentity                         // destination_identity
	ProofClaimFamilyAccessCapability                            // access_capability
	ProofClaimFamilyFieldsTypes                                 // fields_types
	ProofClaimFamilyCursorCheckpoint                            // cursor_checkpoint
	ProofClaimFamilyDeleteBehavior                              // delete_behavior
	ProofClaimFamilyWriteMode                                   // write_mode
	ProofClaimFamilyCoercion                                    // coercion
	ProofClaimFamilyIdempotency                                 // idempotency
	ProofClaimFamilyReplay                                      // replay
	ProofClaimFamilyDriftImpact                                 // drift_impact
)

// ProofEvidenceLevel states how directly a claim was evaluated.
type ProofEvidenceLevel int

const (
	ProofEvidenceLevelUnknown          ProofEvidenceLevel = iota // unknown
	ProofEvidenceLevelStatic                                     // static
	ProofEvidenceLevelCachedObserved                             // cached_observed
	ProofEvidenceLevelLiveReadOnly                               // live_read_only
	ProofEvidenceLevelBoundedExecution                           // bounded_execution
	ProofEvidenceLevelOperational                                // operational
)

// ProofVerdict is a clause or aggregate result; superseded is a derived read state.
type ProofVerdict int

const (
	ProofVerdictUnknown      ProofVerdict = iota // unknown
	ProofVerdictPassed                           // passed
	ProofVerdictFailed                           // failed
	ProofVerdictUnverified                       // unverified
	ProofVerdictInconclusive                     // inconclusive
	ProofVerdictStale                            // stale
	ProofVerdictSuperseded                       // superseded
)

// ProofEvidenceReferenceKind identifies sanitized evidence without storing provider payloads.
type ProofEvidenceReferenceKind int

const (
	ProofEvidenceReferenceKindUnknown       ProofEvidenceReferenceKind = iota // unknown
	ProofEvidenceReferenceKindValidation                                      // validation
	ProofEvidenceReferenceKindIntrospection                                   // introspection
	ProofEvidenceReferenceKindExecution                                       // execution
)

// ProofLifecycleLinkKind links later immutable deployment and run evidence.
type ProofLifecycleLinkKind int

const (
	ProofLifecycleLinkKindUnknown    ProofLifecycleLinkKind = iota // unknown
	ProofLifecycleLinkKindDeployment                               // deployment
	ProofLifecycleLinkKindRun                                      // run
)

// ProofCandidateIdentity binds a receipt to one tenant, target, Git candidate, and compiler input.
type ProofCandidateIdentity struct {
	OrganizationSlug       slug.Slug `json:"organization_slug"`
	ProjectSlug            slug.Slug `json:"project_slug"`
	EnvironmentSlug        slug.Slug `json:"environment_slug"`
	ProviderConnectionSlug slug.Slug `json:"provider_connection_slug"`
	RepositoryID           string    `json:"repository_id"`
	ChangeRequestID        string    `json:"change_request_id"`
	HeadSHA                string    `json:"head_sha"`
	TargetRef              string    `json:"target_ref"`
	PipelinePath           string    `json:"pipeline_path"`
	SpecHash               string    `json:"spec_hash"`
	BindingFingerprint     string    `json:"binding_fingerprint"`
	CapabilityFingerprint  string    `json:"capability_fingerprint"`
	CompilerVersion        string    `json:"compiler_version"`
	PolicyVersion          string    `json:"policy_version"`
}

// Validate rejects an incomplete or non-canonical Proof candidate identity.
func (c ProofCandidateIdentity) Validate() error {
	for _, value := range []slug.Slug{
		c.OrganizationSlug, c.ProjectSlug, c.EnvironmentSlug, c.ProviderConnectionSlug,
	} {
		if _, err := slug.New(value.String()); err != nil {
			return proofValidationError("proof candidate identity is incomplete")
		}
	}
	for _, value := range []string{
		c.RepositoryID, c.ChangeRequestID, c.TargetRef, c.PipelinePath,
		c.CompilerVersion, c.PolicyVersion,
	} {
		if strings.TrimSpace(value) == "" {
			return proofValidationError("proof candidate identity is incomplete")
		}
	}
	if !isGitObjectID(c.HeadSHA) || !isSHA256Digest(c.SpecHash) ||
		!isSHA256Digest(c.BindingFingerprint) || !isSHA256Digest(c.CapabilityFingerprint) {
		return proofValidationError("proof candidate hashes and fingerprints are invalid")
	}
	return nil
}

// ProofClaimIdentity is stable within an immutable pipeline version.
type ProofClaimIdentity struct {
	Family              ProofClaimFamily `json:"family"`
	PipelineVersionHash string           `json:"pipeline_version_hash"`
	SemanticPath        string           `json:"semantic_path"`
}

// ProofEvidenceReference points to sanitized, access-controlled evidence by digest.
type ProofEvidenceReference struct {
	Kind   ProofEvidenceReferenceKind `json:"kind"`
	Digest string                     `json:"digest"`
}

// ProofDiagnostic contains only sanitized operator-facing text.
type ProofDiagnostic struct {
	Code        string `json:"code"`
	Summary     string `json:"summary"`
	Remediation string `json:"remediation"`
	Retryable   bool   `json:"retryable"`
}

// ProofClaim records one clause-level result without raw evidence payloads.
type ProofClaim struct {
	Identity              ProofClaimIdentity       `json:"identity"`
	Description           string                   `json:"description"`
	RequiredEvidenceLevel ProofEvidenceLevel       `json:"required_evidence_level"`
	ActualEvidenceLevel   *ProofEvidenceLevel      `json:"actual_evidence_level"`
	ObservedAt            *time.Time               `json:"observed_at"`
	ExpiresAt             *time.Time               `json:"expires_at"`
	Verdict               ProofVerdict             `json:"verdict"`
	Evidence              []ProofEvidenceReference `json:"evidence"`
	Diagnostics           []ProofDiagnostic        `json:"diagnostics"`
}

// ProofLifecycleLink points forward from a receipt without changing earlier evidence.
type ProofLifecycleLink struct {
	Kind       ProofLifecycleLinkKind `json:"kind"`
	ReceiptID  slug.Slug              `json:"receipt_id"`
	Identifier string                 `json:"identifier"`
	LinkedAt   time.Time              `json:"linked_at"`
}

// ProofReceipt is the immutable, tenant-scoped product receipt contract.
type ProofReceipt struct {
	Operational         *ProofOperationalCapture `json:"operational,omitempty"`
	SchemaVersion       int                      `json:"schema_version"`
	ID                  slug.Slug                `json:"id"`
	Candidate           ProofCandidateIdentity   `json:"candidate"`
	Claims              []ProofClaim             `json:"claims"`
	AggregateVerdict    ProofVerdict             `json:"aggregate_verdict"`
	SupersedesReceiptID *slug.Slug               `json:"supersedes_receipt_id"`
	CreatedAt           time.Time                `json:"created_at"`
	RetainUntil         time.Time                `json:"retain_until"`
}

// NewProofReceipt redacts every string before validating a persistence-ready receipt.
func NewProofReceipt(draft ProofReceipt, redact func(string) string) (ProofReceipt, error) {
	if redact == nil {
		return ProofReceipt{}, xerror.InvalidInput(xerror.Pipeline).Detail("proof receipt redactor is required")
	}
	if draft.SchemaVersion != ProofReceiptSchemaVersion {
		return ProofReceipt{}, proofValidationError("unsupported proof receipt schema version")
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return ProofReceipt{}, xerror.Wrapf("marshal proof receipt draft: %w", err)
	}
	var receipt ProofReceipt
	if err := json.Unmarshal(redaction.RawJSON(raw, redact), &receipt); err != nil {
		return ProofReceipt{}, xerror.Wrapf("unmarshal redacted proof receipt: %w", err)
	}
	receipt.SchemaVersion = ProofReceiptSchemaVersion
	receipt.AggregateVerdict = AggregateProofVerdict(receipt.Claims)
	if err := receipt.Validate(); err != nil {
		return ProofReceipt{}, err
	}
	return receipt, nil
}

// Validate rejects incomplete identities, invalid evidence, and hidden aggregate gaps.
func (r ProofReceipt) Validate() error {
	if r.SchemaVersion != ProofReceiptSchemaVersion {
		return proofValidationError("unsupported proof receipt schema version")
	}
	if _, err := slug.New(r.ID.String()); err != nil {
		return proofValidationError("proof receipt ID is required")
	}
	if err := r.Candidate.Validate(); err != nil {
		return err
	}
	if len(r.Claims) == 0 {
		return proofValidationError("proof receipt requires at least one claim")
	}
	seen := make(map[ProofClaimIdentity]struct{}, len(r.Claims))
	for _, claim := range r.Claims {
		if !claim.Identity.Family.IsAProofClaimFamily() || claim.Identity.Family == ProofClaimFamilyUnknown ||
			claim.Identity.PipelineVersionHash != r.Candidate.SpecHash || !strings.HasPrefix(claim.Identity.SemanticPath, "/") {
			return proofValidationError("proof claim identity is incomplete")
		}
		if _, ok := seen[claim.Identity]; ok {
			return proofValidationError("proof claim identity is duplicated")
		}
		seen[claim.Identity] = struct{}{}
		if strings.TrimSpace(claim.Description) == "" || !claim.RequiredEvidenceLevel.IsAProofEvidenceLevel() || claim.RequiredEvidenceLevel == ProofEvidenceLevelUnknown {
			return proofValidationError("proof claim description and required evidence level are required")
		}
		if claim.ActualEvidenceLevel != nil && (!claim.ActualEvidenceLevel.IsAProofEvidenceLevel() || *claim.ActualEvidenceLevel == ProofEvidenceLevelUnknown) {
			return proofValidationError("proof claim actual evidence level is invalid")
		}
		if !claim.Verdict.IsAProofVerdict() || claim.Verdict == ProofVerdictUnknown || claim.Verdict == ProofVerdictSuperseded {
			return proofValidationError("proof claim verdict is invalid")
		}
		if claim.Verdict == ProofVerdictPassed && (claim.ActualEvidenceLevel == nil || !ProofEvidenceLevelSatisfies(*claim.ActualEvidenceLevel, claim.RequiredEvidenceLevel)) {
			return proofValidationError("passed proof claim does not meet its required evidence level")
		}
		if claim.Verdict == ProofVerdictFailed && claim.ActualEvidenceLevel == nil {
			return proofValidationError("failed proof claim requires an actual evidence level")
		}
		if claim.Verdict == ProofVerdictStale && (claim.ActualEvidenceLevel == nil || claim.ExpiresAt == nil) {
			return proofValidationError("stale proof claim requires expired observed evidence")
		}
		if claim.Verdict == ProofVerdictPassed && claim.ExpiresAt != nil && !claim.ExpiresAt.After(r.CreatedAt) {
			return proofValidationError("passed proof claim evidence is stale")
		}
		if claim.Verdict == ProofVerdictStale && claim.ExpiresAt.After(r.CreatedAt) {
			return proofValidationError("stale proof claim evidence has not expired")
		}
		if claim.ActualEvidenceLevel != nil && claim.ObservedAt == nil {
			return proofValidationError("observed proof evidence requires an observation time")
		}
		if claim.ExpiresAt != nil && (claim.ObservedAt == nil || claim.ExpiresAt.Before(*claim.ObservedAt)) {
			return proofValidationError("proof evidence freshness window is invalid")
		}
		if (claim.Verdict == ProofVerdictPassed || claim.Verdict == ProofVerdictFailed) && len(claim.Evidence) == 0 {
			return proofValidationError("passed or failed proof claim requires evidence")
		}
		for _, evidence := range claim.Evidence {
			if !evidence.Kind.IsAProofEvidenceReferenceKind() || evidence.Kind == ProofEvidenceReferenceKindUnknown || !isSHA256Digest(evidence.Digest) {
				return proofValidationError("proof evidence reference is invalid")
			}
		}
		for _, diagnostic := range claim.Diagnostics {
			if strings.TrimSpace(diagnostic.Code) == "" || strings.TrimSpace(diagnostic.Summary) == "" || strings.TrimSpace(diagnostic.Remediation) == "" {
				return proofValidationError("proof diagnostic code, summary, and remediation are required")
			}
		}
	}
	if r.AggregateVerdict == ProofVerdictSuperseded || r.AggregateVerdict == ProofVerdictUnknown {
		return proofValidationError("stored proof aggregate verdict is invalid")
	}
	if r.AggregateVerdict != AggregateProofVerdict(r.Claims) {
		return proofValidationError("proof aggregate verdict does not match clause verdicts")
	}
	if r.SupersedesReceiptID != nil {
		if _, err := slug.New(r.SupersedesReceiptID.String()); err != nil {
			return proofValidationError("superseded proof receipt ID is invalid")
		}
		if *r.SupersedesReceiptID == r.ID {
			return proofValidationError("proof receipt cannot supersede itself")
		}
	}
	if r.CreatedAt.IsZero() || r.RetainUntil.Before(r.CreatedAt) {
		return proofValidationError("proof receipt retention window is invalid")
	}
	if r.Operational != nil {
		capture := r.Operational
		if capture.Selection.Validate() != nil || capture.Selection.Candidate != r.Candidate || capture.PolicyVersion == "" || capture.ExpiresAt.IsZero() || r.SupersedesReceiptID != nil || r.RetainUntil.After(r.CreatedAt.Add(7*24*time.Hour)) {
			return proofValidationError("operational Proof capture identity or retention is invalid")
		}
		if _, err := slug.New(capture.SourceReceiptID.String()); err != nil {
			return proofValidationError("operational Proof source receipt is invalid")
		}
		if !isSHA256Digest(capture.RecordDigest) {
			return proofValidationError("operational Proof record digest is invalid")
		}
		for _, claim := range r.Claims {
			if claim.Identity.Family != ProofClaimFamilyFieldsTypes || claim.Identity.SemanticPath != "/operational/checks/"+capture.Selection.CheckID {
				return proofValidationError("operational Proof claim selection is invalid")
			}
			for _, reference := range claim.Evidence {
				if reference.Kind != ProofEvidenceReferenceKindExecution || reference.Digest != capture.RecordDigest {
					return proofValidationError("operational Proof claim evidence is invalid")
				}
			}
			if claim.RequiredEvidenceLevel != ProofEvidenceLevelOperational || claim.ActualEvidenceLevel != nil && *claim.ActualEvidenceLevel != ProofEvidenceLevelOperational {
				return proofValidationError("operational Proof cannot inherit another evidence level")
			}
		}
	} else {
		for _, claim := range r.Claims {
			if claim.RequiredEvidenceLevel == ProofEvidenceLevelOperational || claim.ActualEvidenceLevel != nil && *claim.ActualEvidenceLevel == ProofEvidenceLevelOperational {
				return proofValidationError("operational Proof requires a capture")
			}
		}
	}

	return nil
}

// AggregateProofVerdict deterministically preserves the most actionable clause-level gap.
func AggregateProofVerdict(claims []ProofClaim) ProofVerdict {
	result := ProofVerdictPassed
	for _, claim := range claims {
		switch claim.Verdict {
		case ProofVerdictUnknown, ProofVerdictSuperseded:
			return ProofVerdictUnknown
		case ProofVerdictFailed:
			return ProofVerdictFailed
		case ProofVerdictStale:
			result = ProofVerdictStale
		case ProofVerdictInconclusive:
			if result != ProofVerdictStale {
				result = ProofVerdictInconclusive
			}
		case ProofVerdictUnverified:
			if result == ProofVerdictPassed {
				result = ProofVerdictUnverified
			}
		case ProofVerdictPassed:
			continue
		}
	}
	return result
}

// GitSummary returns fixed annotation text without tenant, target, evidence, or diagnostic content.
func (r ProofReceipt) GitSummary() string {
	counts := map[ProofVerdict]int{}
	for _, claim := range r.Claims {
		counts[claim.Verdict]++
	}
	return "Braidplane Proof (informational): " + r.AggregateVerdict.String() +
		"; passed=" + strconv.Itoa(counts[ProofVerdictPassed]) +
		", failed=" + strconv.Itoa(counts[ProofVerdictFailed]) +
		", unverified=" + strconv.Itoa(counts[ProofVerdictUnverified]) +
		", inconclusive=" + strconv.Itoa(counts[ProofVerdictInconclusive]) +
		", stale=" + strconv.Itoa(counts[ProofVerdictStale])
}

func proofValidationError(detail string) error {
	return xerror.InvalidInput(xerror.Pipeline).Detail(detail)
}

func isGitObjectID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value) && (len(decoded) == 20 || len(decoded) == 32)
}

func isSHA256Digest(value string) bool {
	encoded, ok := strings.CutPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(encoded)
	return ok && err == nil && encoded == strings.ToLower(encoded) && len(decoded) == 32
}

// ProofEvidenceLevelSatisfies keeps operational results outside the no-write
// evidence ladder; an ordinary run cannot satisfy bounded_execution.
func ProofEvidenceLevelSatisfies(actual, required ProofEvidenceLevel) bool {
	if actual == ProofEvidenceLevelOperational || required == ProofEvidenceLevelOperational {
		return actual == required
	}
	return actual.IsAProofEvidenceLevel() && required.IsAProofEvidenceLevel() && actual != ProofEvidenceLevelUnknown && required != ProofEvidenceLevelUnknown && actual >= required
}
