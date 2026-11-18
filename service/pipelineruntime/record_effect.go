package pipelineruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// One delivery can update many records. Its replay identity stays fixed during repair
// and changes for a new evaluation, even when the remote record is the same.
func (s *sinkRuntime) executeRecordEffect(ctx context.Context, input *SinkRuntimeInput, recordID string, write func(context.Context) error, confirm func(context.Context) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return xerror.Unexpected().With(err)
	}
	if s.effectLedger == nil || input.RunSlug == "" || input.ReplayIdentity == "" {
		return errSinkNotConfigured.Detail("record updates require a durable effect ledger and run identity")
	}
	record := *input
	digest := sha256.Sum256([]byte(input.ReplayIdentity + "\x00" + input.SinkStepSlug.String() + "\x00" + recordID))
	record.IdempotencyKey = "record:v1:" + hex.EncodeToString(digest[:])
	record.ExclusiveEffectClaim = true
	record.EffectState = pipelinerun.SinkEffectStateUnknown
	defer func() {
		input.ReplayDecision = record.ReplayDecision
		input.EffectState = record.EffectState
		input.DeliveryOutcome = record.DeliveryOutcome
	}()

	skip, err := s.beginEffect(ctx, &record)
	if skip {
		return nil
	}
	switch {
	case err == nil:
		record.DeliveryOutcome = "unknown"
		if err := write(ctx); err != nil {
			return err
		}
	case xerror.Is(err, writegate.ErrOutcomeUnknown) && record.EffectState == pipelinerun.SinkEffectStateInProgress:
		record.ReplayDecision = "reconcile_provider_effect"
		matches, err := confirm(ctx)
		if err != nil {
			return err
		}
		if !matches {
			return writegate.ErrOutcomeUnknown
		}
	default:
		return err
	}
	if err := s.markEffectSucceeded(ctx, &record); err != nil {
		return err
	}
	record.DeliveryOutcome = "confirmed"
	return nil
}
