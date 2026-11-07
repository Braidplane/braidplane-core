package pipelinewebhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type repository struct{}

func New() Repository { return &repository{} }

func (r *repository) ResolveReceiver(ctx context.Context, db postgres.DBTX, receiverID string) (Receiver, error) {
	row, err := dbgen.New(db).ResolvePipelineWebhookReceiver(ctx, pgtype.Text{String: receiverID, Valid: true})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return Receiver{}, ErrReceiverNotFound.With(err)
		}
		return Receiver{}, xerror.Unexpected().With(err)
	}
	policy, err := versionPolicy(row.VersionPolicy, row.PinnedVersionHash)
	if err != nil {
		return Receiver{}, err
	}
	var mappings []model.PipelineWebhookInputMapping
	if err := json.Unmarshal(row.WebhookInputMappings, &mappings); err != nil {
		return Receiver{}, xerror.Unexpected().With(err)
	}
	retryPolicy, err := pipelineRetryPolicy(row.RetryEnabled, row.RetryMaxAttempts, row.RetryBackoffStrategy, row.RetryInitialDelaySeconds, row.RetryMaxDelaySeconds, row.RetryJitter, row.RetryableFailureKinds, row.RetryMaxElapsedSeconds)
	if err != nil {
		return Receiver{}, err
	}
	return Receiver{
		OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug,
		EnvironmentSlug: row.EnvironmentSlug, TriggerSlug: row.TriggerSlug,
		PipelineSlug: row.PipelineSlug, TriggerRevision: row.TriggerRevision,
		VersionPolicy: policy, RetryPolicy: retryPolicy, RuntimeInputKeys: row.RuntimeInputKeys, InputMappings: mappings,
	}, nil
}

func (r *repository) Accept(ctx context.Context, tx postgres.DBTX, params AcceptParams) (AcceptResult, error) {
	if params.KeyVersion <= 0 || params.KeyVersion > math.MaxInt32 {
		return AcceptResult{}, xerror.InvalidInput(xerror.PipelineTrigger).Detail("webhook key version is outside the supported range")
	}
	row, err := dbgen.New(tx).AcceptPipelineWebhookDelivery(ctx, dbgen.AcceptPipelineWebhookDeliveryParams{
		ReceiverID: params.ReceiverID, DeliveryID: params.DeliveryID, PayloadDigest: params.PayloadDigest,
		PayloadSizeBytes: params.PayloadSizeBytes,
		KeyVersion:       int32(params.KeyVersion),
		TriggerRevision:  params.Receiver.TriggerRevision, PipelineSlug: params.Receiver.PipelineSlug.String(),
		VersionPolicy:     params.Receiver.VersionPolicy.Mode.String(),
		PinnedVersionHash: postgres.ToNullableText(params.Receiver.VersionPolicy.PinnedVersionHash),
		RuntimeInputKeys:  params.Receiver.RuntimeInputKeys,
		RetryEnabled:      params.Receiver.RetryPolicy.Enabled,
		// Trigger validation and database constraints bound these values well inside int32.
		RetryMaxAttempts:         int32(params.Receiver.RetryPolicy.MaxAttempts), //nolint:gosec
		RetryBackoffStrategy:     params.Receiver.RetryPolicy.BackoffStrategy.String(),
		RetryInitialDelaySeconds: int32(params.Receiver.RetryPolicy.InitialDelaySeconds), //nolint:gosec
		RetryMaxDelaySeconds:     int32(params.Receiver.RetryPolicy.MaxDelaySeconds),     //nolint:gosec // trigger validation bounds this value
		RetryJitter:              params.Receiver.RetryPolicy.Jitter,
		RetryableFailureKinds:    retryFailureKindStrings(params.Receiver.RetryPolicy.RetryableFailureKinds),
		RetryMaxElapsedSeconds:   optionalInt4(params.Receiver.RetryPolicy.MaxElapsedSeconds), RunSlug: params.RunSlug.String(),
		EncryptedEnvelope: params.EncryptedEnvelope, EnvelopeExpiresAt: postgres.ToTimestamptz(params.EnvelopeExpiresAt),
		ReceivedAt: postgres.ToTimestamptz(params.ReceivedAt),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return AcceptResult{}, ErrReceiverNotFound.With(err)
		}
		return AcceptResult{}, xerror.Unexpected().With(err)
	}
	return AcceptResult{Inserted: row.Inserted, PayloadDigest: row.PayloadDigest, RunSlug: slug.FromString(row.RunSlug), Status: row.Status}, nil
}

func (r *repository) RecordDuplicate(ctx context.Context, tx postgres.DBTX, receiverID, deliveryID, digest string, at time.Time) (DuplicateResult, error) {
	row, err := dbgen.New(tx).RecordPipelineWebhookDuplicate(ctx, dbgen.RecordPipelineWebhookDuplicateParams{
		ReceiverID: optionalText(receiverID), DeliveryID: optionalText(deliveryID),
		PayloadDigest: optionalText(digest), ReceivedAt: postgres.ToTimestamptz(at),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return DuplicateResult{}, nil
		}
		return DuplicateResult{}, xerror.Unexpected().With(err)
	}
	return DuplicateResult{Found: true, Duplicate: row.Duplicate.Bool, PayloadDigest: row.PayloadDigest, RunSlug: slug.FromString(row.RunSlug), Status: row.Status}, nil
}

func (r *repository) Reject(ctx context.Context, tx postgres.DBTX, params RejectParams) (AcceptResult, error) {
	row, err := dbgen.New(tx).RejectPipelineWebhookDelivery(ctx, dbgen.RejectPipelineWebhookDeliveryParams{
		ReceiverID: params.ReceiverID, DeliveryID: params.DeliveryID, PayloadDigest: params.PayloadDigest,
		PayloadSizeBytes: params.PayloadSizeBytes, KeyVersion: int32(params.KeyVersion), //nolint:gosec // validated as int32 during request parsing
		TriggerRevision: params.Receiver.TriggerRevision, PipelineSlug: params.Receiver.PipelineSlug.String(),
		VersionPolicy:     params.Receiver.VersionPolicy.Mode.String(),
		PinnedVersionHash: postgres.ToNullableText(params.Receiver.VersionPolicy.PinnedVersionHash),
		RuntimeInputKeys:  params.Receiver.RuntimeInputKeys, RetryEnabled: params.Receiver.RetryPolicy.Enabled,
		RetryMaxAttempts:         int32(params.Receiver.RetryPolicy.MaxAttempts), //nolint:gosec // trigger validation bounds this value
		RetryBackoffStrategy:     params.Receiver.RetryPolicy.BackoffStrategy.String(),
		RetryInitialDelaySeconds: int32(params.Receiver.RetryPolicy.InitialDelaySeconds), //nolint:gosec // trigger validation bounds this value
		RetryMaxDelaySeconds:     int32(params.Receiver.RetryPolicy.MaxDelaySeconds),     //nolint:gosec // trigger validation bounds this value
		RetryJitter:              params.Receiver.RetryPolicy.Jitter,
		RetryableFailureKinds:    retryFailureKindStrings(params.Receiver.RetryPolicy.RetryableFailureKinds),
		RetryMaxElapsedSeconds:   optionalInt4(params.Receiver.RetryPolicy.MaxElapsedSeconds),
		RunSlug:                  params.RunSlug.String(), DecisionReason: optionalText(params.DecisionReason), ReceivedAt: postgres.ToTimestamptz(params.ReceivedAt),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return AcceptResult{}, ErrReceiverNotFound.With(err)
		}
		return AcceptResult{}, xerror.Unexpected().With(err)
	}
	return AcceptResult{Inserted: row.Inserted, PayloadDigest: row.PayloadDigest, RunSlug: slug.FromString(row.RunSlug), Status: row.Status}, nil
}

func (r *repository) Load(ctx context.Context, db postgres.DBTX, receiverID, deliveryID string, attempt int, now time.Time) (Delivery, error) {
	row, err := dbgen.New(db).LoadPipelineWebhookDelivery(ctx, dbgen.LoadPipelineWebhookDeliveryParams{
		ReceiverID: receiverID, DeliveryID: deliveryID, AttemptNumber: int32(attempt), //nolint:gosec // attempts are bounded to 1..10
		Now: postgres.ToTimestamptz(now),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return Delivery{}, ErrDeliveryNotFound.With(err)
		}
		return Delivery{}, xerror.Unexpected().With(err)
	}
	policy, err := versionPolicy(row.VersionPolicy, row.PinnedVersionHash)
	if err != nil {
		return Delivery{}, err
	}
	retryPolicy, err := pipelineRetryPolicy(row.RetryEnabled, row.RetryMaxAttempts, row.RetryBackoffStrategy, row.RetryInitialDelaySeconds, row.RetryMaxDelaySeconds, row.RetryJitter, row.RetryableFailureKinds, row.RetryMaxElapsedSeconds)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{
		Resuming:   row.Status == "running",
		DatabaseID: int64(row.ID),
		ReceiverID: row.ReceiverID, DeliveryID: row.DeliveryID, PayloadDigest: row.PayloadDigest,
		PayloadSizeBytes: row.PayloadSizeBytes, KeyVersion: int(row.KeyVersion), RunSlug: slug.FromString(row.RunSlug),
		PipelineVersionSpecHash: row.PipelineVersionSpecHash.String,
		AttemptNumber:           int(row.AttemptNumber),
		EncryptedEnvelope:       append([]byte(nil), row.EncryptedEnvelope...), EnvelopeExpiresAt: row.EnvelopeExpiresAt.Time,
		ReceivedAt: row.ReceivedAt.Time,
		Receiver: Receiver{
			OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug,
			EnvironmentSlug: row.EnvironmentSlug, TriggerSlug: row.TriggerSlug,
			PipelineSlug: slug.FromString(row.PipelineSlug), TriggerRevision: row.TriggerRevision,
			VersionPolicy: policy, RetryPolicy: retryPolicy, RuntimeInputKeys: row.RuntimeInputKeys,
		},
	}, nil
}

func (r *repository) Expire(ctx context.Context, db postgres.DBTX, receiverID, deliveryID string, now time.Time) error {
	_, err := dbgen.New(db).ExpirePipelineWebhookDelivery(ctx, dbgen.ExpirePipelineWebhookDeliveryParams{
		ReceiverID: receiverID,
		DeliveryID: deliveryID,
		Now:        postgres.ToTimestamptz(now),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (r *repository) MarkRunning(ctx context.Context, tx postgres.DBTX, receiverID, deliveryID string, attempt int, startedAt time.Time) error {
	rows, err := dbgen.New(tx).MarkPipelineWebhookDeliveryRunning(ctx, dbgen.MarkPipelineWebhookDeliveryRunningParams{
		StartedAt: postgres.ToTimestamptz(startedAt), ReceiverID: receiverID, DeliveryID: deliveryID,
		AttemptNumber: int32(attempt), //nolint:gosec // attempts are bounded to 1..10
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrDeliveryNotFound
	}
	return nil
}

func (r *repository) Complete(ctx context.Context, tx postgres.DBTX, params CompleteParams) error {
	rows, err := dbgen.New(tx).CompletePipelineWebhookDelivery(ctx, dbgen.CompletePipelineWebhookDeliveryParams{
		Status: params.Status, FailureKind: optionalText(params.FailureKind), DecisionReason: optionalText(params.DecisionReason),
		CompletedAt: postgres.ToTimestamptz(params.CompletedAt), ReceiverID: params.ReceiverID, DeliveryID: params.DeliveryID,
		AttemptNumber: int32(params.AttemptNumber), //nolint:gosec // attempts are bounded to 1..10
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrDeliveryNotFound
	}
	return nil
}

func (r *repository) RecordRetry(ctx context.Context, tx postgres.DBTX, params RecordRetryParams) error {
	rows, err := dbgen.New(tx).RecordPipelineWebhookDeliveryRetry(ctx, dbgen.RecordPipelineWebhookDeliveryRetryParams{
		PipelineVersionSpecHash: optionalText(params.VersionHash), FailureKind: optionalText(params.FailureKind),
		NextRetryAt: postgres.ToTimestamptz(params.NextRetryAt), UpdatedAt: postgres.ToTimestamptz(params.UpdatedAt),
		ReceiverID: params.ReceiverID, DeliveryID: params.DeliveryID,
		AttemptNumber: int32(params.AttemptNumber), NextAttemptNumber: int32(params.NextAttemptNumber), //nolint:gosec // attempts are bounded to 1..10
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrDeliveryNotFound
	}
	return nil
}

func (r *repository) ListForTrigger(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, req model.ListRequest) ([]Diagnostic, error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(queryReq.Cursor, normalized.SortBy)
	if err != nil {
		return nil, xerror.Wrapf("build pipeline webhook delivery cursor values: %w", err)
	}
	rows, err := dbgen.New(db).ListPipelineWebhookDeliveriesForTrigger(ctx, dbgen.ListPipelineWebhookDeliveriesForTriggerParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, TriggerSlug: triggerSlug,
		CursorSlug: cursorSlug, CursorTimestamp: cursorTimestamp,
		OrderDir: pgtype.Text{String: queryReq.Order, Valid: queryReq.Order != ""}, LimitCount: int64(queryReq.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]Diagnostic, 0, len(rows))
	for _, row := range rows {
		result = append(result, diagnosticFromRow(dbgen.GetPipelineWebhookDeliveryForTriggerRow(row)))
	}
	return result, nil
}

func (r *repository) GetForTrigger(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, deliveryID string) (Diagnostic, error) {
	row, err := dbgen.New(db).GetPipelineWebhookDeliveryForTrigger(ctx, dbgen.GetPipelineWebhookDeliveryForTriggerParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, TriggerSlug: triggerSlug, DeliveryID: deliveryID,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return Diagnostic{}, ErrDeliveryNotFound.With(err)
		}
		return Diagnostic{}, xerror.Unexpected().With(err)
	}
	return diagnosticFromRow(row), nil
}

func (r *repository) Replay(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, deliveryID string, at time.Time) (ReplayRef, error) {
	row, err := dbgen.New(tx).ReplayPipelineWebhookDelivery(ctx, dbgen.ReplayPipelineWebhookDeliveryParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, TriggerSlug: triggerSlug,
		DeliveryID: deliveryID, ReplayedAt: postgres.ToTimestamptz(at),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ReplayRef{}, ErrDeliveryNotFound.With(err)
		}
		return ReplayRef{}, xerror.Unexpected().With(err)
	}
	return ReplayRef{ReceiverID: row.ReceiverID, DeliveryID: row.DeliveryID, AttemptNumber: int(row.AttemptNumber)}, nil
}

func diagnosticFromRow(row dbgen.GetPipelineWebhookDeliveryForTriggerRow) Diagnostic {
	return Diagnostic{
		ReceiverID: row.ReceiverID, DeliveryID: row.DeliveryID, PayloadDigest: row.PayloadDigest, Status: row.Status,
		TriggerSlug: row.TriggerSlug, TriggerRevision: row.TriggerRevision, PipelineSlug: slug.FromString(row.PipelineSlug),
		PayloadSizeBytes: row.PayloadSizeBytes, MappedInputNames: append([]string(nil), row.RuntimeInputKeys...),
		LinkedRunSlug: optionalStringSlug(row.LinkedRunSlug), AttemptNumber: int(row.AttemptNumber), DuplicateCount: int(row.DuplicateCount),
		FailureKind: optionalTextString(row.FailureKind), DecisionReason: optionalTextString(row.DecisionReason),
		NextRetryAt: optionalTimestamp(row.NextRetryAt), StartedAt: optionalTimestamp(row.StartedAt), CompletedAt: optionalTimestamp(row.CompletedAt),
		ReceivedAt: row.ReceivedAt.Time, UpdatedAt: row.UpdatedAt.Time, LastDuplicateAt: optionalTimestamp(row.LastDuplicateAt),
		EnvelopeRetained: len(row.EncryptedEnvelope) > 0, EnvelopeExpiresAt: optionalTimestamp(row.EnvelopeExpiresAt),
	}
}

func optionalTextString(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func optionalTimestamp(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func optionalStringSlug(value string) *slug.Slug {
	if value == "" {
		return nil
	}
	parsed := slug.FromString(value)
	return &parsed
}

func versionPolicy(mode string, pinned pgtype.Text) (model.PipelineTriggerVersionPolicy, error) {
	parsed, err := model.PipelineTriggerVersionPolicyModeString(mode)
	if err != nil {
		return model.PipelineTriggerVersionPolicy{}, xerror.Unexpected().With(err)
	}
	result := model.PipelineTriggerVersionPolicy{Mode: parsed}
	if pinned.Valid {
		result.PinnedVersionHash = &pinned.String
	}
	return result, nil
}

func pipelineRetryPolicy(enabled bool, maxAttempts int32, strategy string, initialDelay, maxDelay int32, jitter bool, failureKinds []string, maxElapsed pgtype.Int4) (model.PipelineTriggerRetryPolicy, error) {
	parsedStrategy, err := model.PipelineTriggerBackoffStrategyString(strategy)
	if err != nil {
		return model.PipelineTriggerRetryPolicy{}, xerror.Unexpected().With(err)
	}
	parsedKinds := make([]model.PipelineTriggerRetryFailureKind, len(failureKinds))
	for i, value := range failureKinds {
		parsedKinds[i], err = model.PipelineTriggerRetryFailureKindString(value)
		if err != nil {
			return model.PipelineTriggerRetryPolicy{}, xerror.Unexpected().With(err)
		}
	}
	policy := model.PipelineTriggerRetryPolicy{Enabled: enabled, MaxAttempts: int(maxAttempts), BackoffStrategy: parsedStrategy, InitialDelaySeconds: int(initialDelay), MaxDelaySeconds: int(maxDelay), Jitter: jitter, RetryableFailureKinds: parsedKinds}
	if maxElapsed.Valid {
		value := int(maxElapsed.Int32)
		policy.MaxElapsedSeconds = &value
	}
	return policy, nil
}

func retryFailureKindStrings(kinds []model.PipelineTriggerRetryFailureKind) []string {
	result := make([]string, len(kinds))
	for i, kind := range kinds {
		result[i] = kind.String()
	}
	return result
}

func optionalInt4(value *int) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*value), Valid: true} //nolint:gosec // trigger validation bounds this value
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func (r *repository) ResetPreflight(ctx context.Context, tx postgres.DBTX, receiver, delivery string, attempt int) error {
	rows, err := dbgen.New(tx).ResetPipelineWebhookPreflight(ctx, dbgen.ResetPipelineWebhookPreflightParams{ReceiverID: receiver, DeliveryID: delivery, AttemptNumber: int32(attempt)}) //nolint:gosec // source attempts are bounded to 1..10
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrDeliveryNotFound
	}
	return nil
}

func (r *repository) GetIntake(ctx context.Context, db postgres.DBTX, receiverID, deliveryID string) (Delivery, error) {
	row, err := dbgen.New(db).GetPipelineWebhookIntake(ctx, dbgen.GetPipelineWebhookIntakeParams{ReceiverID: receiverID, DeliveryID: deliveryID})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return Delivery{}, ErrDeliveryNotFound.With(err)
		}
		return Delivery{}, xerror.Unexpected().With(err)
	}
	return Delivery{
		DatabaseID: int64(row.ID), ReceiverID: row.ReceiverID, DeliveryID: row.DeliveryID, PayloadDigest: row.PayloadDigest,
		PayloadSizeBytes: row.PayloadSizeBytes, RunSlug: slug.FromString(row.RunSlug), ReceivedAt: row.ReceivedAt.Time,
		Receiver: Receiver{
			OrgSlug: row.OrgSlug, ProjectSlug: row.ProjectSlug, EnvironmentSlug: row.EnvironmentSlug, TriggerSlug: row.TriggerSlug,
			PipelineSlug: slug.FromString(row.PipelineSlug), TriggerRevision: row.TriggerRevision, RuntimeInputKeys: row.RuntimeInputKeys,
		},
	}, nil
}
