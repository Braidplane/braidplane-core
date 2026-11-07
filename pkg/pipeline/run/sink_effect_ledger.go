package run

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=SinkEffectState -json -text -linecomment -output=sink_effect_state_enumer.go

// SinkEffectState records the durable lifecycle of an outbound sink effect.
type SinkEffectState int

const (
	SinkEffectStateUnknown    SinkEffectState = iota // unknown
	SinkEffectStateInProgress                        // in_progress
	SinkEffectStateSucceeded                         // succeeded
)

// SinkEffectAttempt is the safe, durable identity for one sink delivery. It
// deliberately excludes payload values, credentials, and the idempotency key
// from run diagnostics; the key is used only as the repository identity.
type SinkEffectAttempt struct {
	ReplayIdentity    string
	SinkStepSlug      slug.Slug
	IdempotencyKey    string
	IdempotencyClass  string
	ReplayMethod      string
	ReplayDecision    string
	DiagnosticCode    string
	DiagnosticSummary string
}

// SinkEffect stores the persisted state needed to decide a retry safely.
type SinkEffect struct {
	Claimed   bool
	State     SinkEffectState
	StartedAt time.Time
}

// SinkEffectLedger creates a durable pre-effect attempt and records a returned
// success. It is intentionally transaction-neutral: a side effect can happen
// before the terminal pipeline-run row is persisted.
type SinkEffectLedger interface {
	// BeginOrGet claims an effect before dispatching it, or returns the previous
	// claim for the same replay identity. A returned succeeded effect is skipped;
	// an existing in-progress effect is replay-ambiguous and callers must apply
	// its replay policy instead of blindly dispatching the sink again.
	BeginOrGet(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		attempt SinkEffectAttempt,
	) (*SinkEffect, error)
	// MarkSucceeded records that a claimed effect completed so later retries and
	// replays of the same identity can skip its already-applied side effect.
	MarkSucceeded(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		attempt SinkEffectAttempt,
	) error
}
