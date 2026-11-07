package pipelinewebhooksvc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/go-openapi/jsonpointer"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelinewebhooks"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const (
	MaxDeliveryBodyBytes = pipelinerun.MaxEventInputBytes
	deliveryFreshness    = 5 * time.Minute
	envelopeLifetime     = 24 * time.Hour
)

var (
	deliveryIDPattern               = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	receiverIDPattern               = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	canonicalPositiveIntegerPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	signaturePattern                = regexp.MustCompile(`^v1=[0-9a-f]{64}$`)
)

type SecretResolver interface {
	ResolvePipelineWebhookSecret(context.Context, credentialssvc.ResolvePipelineWebhookSecretParams) ([]byte, error)
}

type Producer interface {
	EnqueuePipelineWebhookDelivery(context.Context, postgres.DBTX, jobs.PipelineWebhookDeliveryArgs) (queue.QueuedJob, error)
}

type PipelineExecutor interface {
	FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error
	AcceptQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinerun.RunSourceSummary, time.Time, int64) error
	StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error
	ExecuteVerifiedEvent(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *slug.Slug, model.EventActor, pipelinerun.RunSourceSummary, map[string]any, json.RawMessage) (*pipelinesvc.PipelineRunDetail, error)
	ExecuteVerifiedEventVersion(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string, *slug.Slug, model.EventActor, pipelinerun.RunSourceSummary, map[string]any, json.RawMessage) (*pipelinesvc.PipelineRunDetail, error)
	GetRun(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error)
}

type Service struct {
	notifier notificationsvc.CandidateProducer
	txm      postgres.TransactionManager
	repo     pipelinewebhooks.Repository
	secrets  SecretResolver
	producer Producer
	executor PipelineExecutor
	cipher   crypto.CredentialCipher
	clock    clock.Clock
}

type AcceptRequest struct {
	ReceiverID, KeyVersion, Timestamp, DeliveryID, Signature string
	Body                                                     []byte
}

type Acceptance struct{ Replay bool }

type DeliveryDiagnostic struct {
	ReceiverID, DeliveryID, PayloadDigest, Status string
	TriggerSlug, PipelineSlug                     slug.Slug
	TriggerRevision                               int64
	PayloadSizeBytes                              int64
	MappedInputNames                              []string
	LinkedRunSlug                                 *slug.Slug
	AttemptNumber, DuplicateCount                 int
	FailureKind, FailureReason                    *string
	NextRetryAt, StartedAt, CompletedAt           *time.Time
	ReceivedAt, UpdatedAt                         time.Time
	LastDuplicateAt                               *time.Time
	ReplayAvailable                               bool
	ReplayUnavailableReason                       *string
}

type executionEnvelope struct {
	EventInput   json.RawMessage `json:"event_input"`
	RuntimeInput map[string]any  `json:"runtime_input"`
}

func New(
	txm postgres.TransactionManager,
	repo pipelinewebhooks.Repository,
	secrets SecretResolver,
	producer Producer,
	executor PipelineExecutor,
	cipher crypto.CredentialCipher,
	clk clock.Clock,
	notifier notificationsvc.CandidateProducer,
) (*Service, error) {
	if txm == nil || repo == nil || secrets == nil || producer == nil || executor == nil || cipher == nil || clk == nil || notifier == nil {
		return nil, xerror.Unexpected().Detail("pipeline webhook service dependencies are required")
	}
	return &Service{txm: txm, repo: repo, secrets: secrets, producer: producer, executor: executor, cipher: cipher, clock: clk, notifier: notifier}, nil
}

func (s *Service) Accept(ctx context.Context, request AcceptRequest) (Acceptance, error) {
	keyVersion, signature, err := validateDeliveryRequest(request, s.clock.Now().UTC())
	if err != nil {
		return Acceptance{}, err
	}
	receiver, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (pipelinewebhooks.Receiver, error) {
		return s.repo.ResolveReceiver(ctx, db, request.ReceiverID)
	})
	if err != nil {
		return Acceptance{}, err
	}
	secret, err := s.secrets.ResolvePipelineWebhookSecret(ctx, credentialssvc.ResolvePipelineWebhookSecretParams{
		OrgSlug: receiver.OrgSlug, ProjectSlug: receiver.ProjectSlug, EnvironmentSlug: receiver.EnvironmentSlug,
		TriggerSlug: receiver.TriggerSlug, KeyVersion: keyVersion,
	})
	if err != nil {
		if xerror.Is(err, credentialssvc.ErrCredentialNotFound) {
			return Acceptance{}, ErrVerificationFailed
		}
		return Acceptance{}, err
	}
	if !hmac.Equal(signature, deliveryMAC(secret, request.KeyVersion, request.Timestamp, request.DeliveryID, request.Body)) {
		return Acceptance{}, ErrVerificationFailed
	}
	receivedAt := s.clock.Now().UTC()
	digest := sha256.Sum256(request.Body)
	digestText := hex.EncodeToString(digest[:])
	duplicate, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (pipelinewebhooks.DuplicateResult, error) {
		return s.repo.RecordDuplicate(ctx, tx, request.ReceiverID, request.DeliveryID, digestText, receivedAt)
	})
	if err != nil {
		return Acceptance{}, err
	}
	if duplicate.Found {
		if !duplicate.Duplicate || duplicate.PayloadDigest != digestText || duplicate.Status == "rejected" {
			return Acceptance{}, ErrDeliveryConflict
		}
		return Acceptance{Replay: true}, nil
	}

	var document any
	if err := json.Unmarshal(request.Body, &document); err != nil {
		return Acceptance{}, s.rejectVerified(ctx, request, receiver, keyVersion, digestText, receivedAt, "invalid_json", ErrInvalidDelivery.Detail("webhook body must be valid JSON"))
	}
	runtimeInput, err := mapRuntimeInput(document, receiver.InputMappings)
	if err != nil {
		return Acceptance{}, s.rejectVerified(ctx, request, receiver, keyVersion, digestText, receivedAt, "input_mapping_failed", err)
	}
	envelopeJSON, err := json.Marshal(executionEnvelope{EventInput: append(json.RawMessage(nil), request.Body...), RuntimeInput: runtimeInput})
	if err != nil {
		return Acceptance{}, xerror.Unexpected().With(err)
	}
	encryptedEnvelope, err := s.cipher.Seal(envelopeScope(receiver.OrgSlug, request.ReceiverID, request.DeliveryID), envelopeJSON)
	if err != nil {
		return Acceptance{}, xerror.Unexpected().With(err)
	}
	runSlug := deliveryRunSlug(request.ReceiverID, request.DeliveryID)
	var accepted pipelinewebhooks.AcceptResult
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		accepted, err = s.repo.Accept(ctx, tx, pipelinewebhooks.AcceptParams{
			ReceiverID: request.ReceiverID, DeliveryID: request.DeliveryID, PayloadDigest: digestText,
			PayloadSizeBytes: int64(len(request.Body)), KeyVersion: keyVersion, Receiver: receiver, RunSlug: runSlug,
			EncryptedEnvelope: encryptedEnvelope, EnvelopeExpiresAt: receivedAt.Add(envelopeLifetime), ReceivedAt: receivedAt,
		})
		if err != nil {
			return err
		}
		if accepted.PayloadDigest != digestText || accepted.Status == "rejected" {
			return ErrDeliveryConflict
		}
		if !accepted.Inserted {
			return nil
		}
		queued, err := s.producer.EnqueuePipelineWebhookDelivery(ctx, tx, jobs.PipelineWebhookDeliveryArgs{
			ReceiverID: request.ReceiverID, DeliveryID: request.DeliveryID, RunAttempt: 1,
		})
		if err != nil {
			return err
		}
		return s.acceptQueuedDelivery(ctx, tx, request.ReceiverID, request.DeliveryID, 1, receivedAt, queued.ID)
	})
	if err != nil {
		return Acceptance{}, err
	}
	return Acceptance{Replay: !accepted.Inserted}, nil
}

func (s *Service) rejectVerified(ctx context.Context, request AcceptRequest, receiver pipelinewebhooks.Receiver, keyVersion int, digest string, receivedAt time.Time, reason string, rejection error) error {
	result, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (pipelinewebhooks.AcceptResult, error) {
		return s.repo.Reject(ctx, tx, pipelinewebhooks.RejectParams{
			ReceiverID: request.ReceiverID, DeliveryID: request.DeliveryID, PayloadDigest: digest,
			PayloadSizeBytes: int64(len(request.Body)), KeyVersion: keyVersion, Receiver: receiver,
			RunSlug: deliveryRunSlug(request.ReceiverID, request.DeliveryID), DecisionReason: reason, ReceivedAt: receivedAt,
		})
	})
	if err != nil {
		return err
	}
	if result.PayloadDigest != digest {
		return ErrDeliveryConflict
	}
	return rejection
}

func (s *Service) ListDeliveries(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, req model.ListRequest) (model.ListResponse[DeliveryDiagnostic], error) {
	normalized, _ := model.PrepareListQuery(req, model.ListSortCreatedAt)
	rows, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]pipelinewebhooks.Diagnostic, error) {
		return s.repo.ListForTrigger(ctx, db, orgSlug, projectSlug, environmentSlug, triggerSlug, normalized)
	})
	if err != nil {
		return model.ListResponse[DeliveryDiagnostic]{}, err
	}
	items := make([]DeliveryDiagnostic, 0, len(rows))
	for _, row := range rows {
		items = append(items, deliveryDiagnostic(row, s.clock.Now().UTC()))
	}
	items, hasMore := model.FinalizePage(items, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(items, normalized, hasMore, func(item DeliveryDiagnostic, _ string) (string, string) {
		return item.ReceivedAt.Format(time.RFC3339Nano), item.DeliveryID
	})
	return model.ListResponse[DeliveryDiagnostic]{Items: items, HasMore: hasMore, NextCursor: nextCursor, PrevCursor: prevCursor}, nil
}

func (s *Service) GetDelivery(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, deliveryID string) (DeliveryDiagnostic, error) {
	if !deliveryIDPattern.MatchString(deliveryID) {
		return DeliveryDiagnostic{}, ErrInvalidDelivery.Detail("webhook delivery ID is invalid")
	}
	row, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (pipelinewebhooks.Diagnostic, error) {
		return s.repo.GetForTrigger(ctx, db, orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
	})
	if err != nil {
		return DeliveryDiagnostic{}, err
	}
	return deliveryDiagnostic(row, s.clock.Now().UTC()), nil
}

func (s *Service) ReplayDelivery(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, deliveryID string) (DeliveryDiagnostic, error) {
	delivery, err := s.GetDelivery(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
	if err != nil {
		return DeliveryDiagnostic{}, err
	}
	if !delivery.ReplayAvailable {
		return DeliveryDiagnostic{}, ErrReplayUnavailable.Detail(*delivery.ReplayUnavailableReason)
	}
	now := s.clock.Now().UTC()
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		replay, err := s.repo.Replay(ctx, tx, orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID, now)
		if err != nil {
			return err
		}
		queued, err := s.producer.EnqueuePipelineWebhookDelivery(ctx, tx, jobs.PipelineWebhookDeliveryArgs{
			ReceiverID: replay.ReceiverID, DeliveryID: replay.DeliveryID, RunAttempt: replay.AttemptNumber,
		})
		if err != nil {
			return err
		}
		return s.acceptQueuedDelivery(ctx, tx, replay.ReceiverID, replay.DeliveryID, replay.AttemptNumber, now, queued.ID)
	})
	if xerror.Is(err, pipelinewebhooks.ErrDeliveryNotFound) {
		return DeliveryDiagnostic{}, ErrReplayUnavailable.With(err)
	}
	if err != nil {
		return DeliveryDiagnostic{}, err
	}
	return s.GetDelivery(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
}

func deliveryDiagnostic(row pipelinewebhooks.Diagnostic, now time.Time) DeliveryDiagnostic {
	status := row.Status
	if status == "accepted" {
		status = "queued"
	}
	reason := replayUnavailableReason(row, now)
	return DeliveryDiagnostic{
		ReceiverID: row.ReceiverID, DeliveryID: row.DeliveryID, PayloadDigest: row.PayloadDigest, Status: status,
		TriggerSlug: row.TriggerSlug, TriggerRevision: row.TriggerRevision, PipelineSlug: row.PipelineSlug,
		PayloadSizeBytes: row.PayloadSizeBytes, MappedInputNames: append([]string(nil), row.MappedInputNames...),
		LinkedRunSlug: row.LinkedRunSlug, AttemptNumber: row.AttemptNumber, DuplicateCount: row.DuplicateCount,
		FailureKind: safeFailureKind(row.FailureKind), FailureReason: safeDecisionReason(row.DecisionReason),
		NextRetryAt: row.NextRetryAt, StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
		ReceivedAt: row.ReceivedAt, UpdatedAt: row.UpdatedAt, LastDuplicateAt: row.LastDuplicateAt,
		ReplayAvailable: reason == nil, ReplayUnavailableReason: reason,
	}
}

func replayUnavailableReason(row pipelinewebhooks.Diagnostic, now time.Time) *string {
	reason := ""
	switch {
	case row.Status == "accepted" || row.Status == "running":
		reason = "delivery_not_terminal"
	case row.Status == "succeeded":
		reason = "delivery_succeeded"
	case row.Status == "rejected":
		reason = "delivery_rejected"
	case row.Status != "failed":
		reason = "delivery_state_unsupported"
	case row.LinkedRunSlug != nil:
		reason = "linked_run_requires_run_replay"
	case !row.EnvelopeRetained || row.EnvelopeExpiresAt == nil:
		reason = "execution_input_not_retained"
	case !row.EnvelopeExpiresAt.After(now):
		reason = "execution_input_expired"
	case row.AttemptNumber >= 10:
		reason = "attempt_limit_reached"
	}
	if reason == "" {
		return nil
	}
	return &reason
}

func safeFailureKind(value *string) *string {
	if value == nil || (*value != "data_source" && *value != "platform") {
		return nil
	}
	return value
}

func safeDecisionReason(value *string) *string {
	if value == nil {
		return nil
	}
	switch *value {
	case "invalid_json", "input_mapping_failed", "envelope_decryption_failed", "execution_envelope_invalid",
		"invalid_version_policy", "execution_rejected", "execution_returned_no_run", "non_retryable_failure_kind",
		"retryable_failure_kind", "retry_policy_disabled", "max_attempts_exhausted", "max_elapsed_exhausted",
		"execution_envelope_expired", "operator_replay":
		return value
	default:
		fallback := "delivery_failed"
		return &fallback
	}
}

func (s *Service) ExecuteDelivery(ctx context.Context, args jobs.PipelineWebhookDeliveryArgs) error {
	if args.RunAttempt < 1 {
		return xerror.InvalidInput(xerror.PipelineRun).Detail("pipeline webhook run attempt must be positive")
	}
	now := s.clock.Now().UTC()
	if args.Cleanup {
		return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			return s.repo.Expire(ctx, tx, args.ReceiverID, args.DeliveryID, now)
		})
	}
	delivery, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (pipelinewebhooks.Delivery, error) {
		if err := s.repo.Expire(ctx, db, args.ReceiverID, args.DeliveryID, now); err != nil {
			return pipelinewebhooks.Delivery{}, err
		}
		return s.repo.Load(ctx, db, args.ReceiverID, args.DeliveryID, args.RunAttempt, now)
	})
	if err != nil {
		if xerror.Is(err, pipelinewebhooks.ErrDeliveryNotFound) {
			return nil
		}
		return err
	}
	if args.FinalizeOnly && !delivery.Resuming {
		return nil
	}
	runSlug, err := webhookAttemptRunSlug(delivery.RunSlug, args.RunAttempt)
	if err != nil {
		return xerror.Wrapf("derive pipeline webhook attempt run slug: %w", err)
	}
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.MarkRunning(ctx, tx, args.ReceiverID, args.DeliveryID, args.RunAttempt, now); err != nil {
			return err
		}
		if !delivery.Resuming {
			return s.executor.StartQueuedRun(ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug, runSlug, now)
		}
		return nil
	}); err != nil {
		if xerror.Is(err, pipelinewebhooks.ErrDeliveryNotFound) {
			return nil
		}
		return err
	}
	var run *pipelinesvc.PipelineRunDetail
	if delivery.Resuming {
		run, err = s.executor.GetRun(ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug, runSlug.String())
		if err != nil {
			return xerror.Wrapf("load webhook attempt for finalization: %w", err)
		}
		if run == nil || (run.Timeline.Failure != nil && run.Timeline.Failure.OutcomeUncertain) || (run.Metadata.Status != "succeeded" && run.Metadata.Status != "failed" && run.Metadata.Status != "canceled") {
			return xerror.Unexpected().Detail("webhook finalization is awaiting a terminal attempt")
		}
	} else {
		plaintext, decryptErr := s.cipher.Open(envelopeScope(delivery.Receiver.OrgSlug, delivery.ReceiverID, delivery.DeliveryID), delivery.EncryptedEnvelope)
		if decryptErr != nil {
			return s.complete(ctx, args, "failed", "platform", "envelope_decryption_failed", now, delivery.EnvelopeExpiresAt)
		}
		var envelope executionEnvelope
		if err := json.Unmarshal(plaintext, &envelope); err != nil || len(envelope.EventInput) == 0 {
			return s.complete(ctx, args, "failed", "platform", "execution_envelope_invalid", now, delivery.EnvelopeExpiresAt)
		}
		source, sourceErr := webhookRunSource(delivery, args.RunAttempt)
		if sourceErr != nil {
			return sourceErr
		}
		switch {
		case args.RunAttempt > 1 && delivery.PipelineVersionSpecHash != "":
			run, err = s.executor.ExecuteVerifiedEventVersion(
				ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug,
				delivery.Receiver.PipelineSlug, delivery.PipelineVersionSpecHash, &runSlug,
				model.ExternalEventActor(), source, envelope.RuntimeInput, envelope.EventInput,
			)
		case delivery.Receiver.VersionPolicy.Mode == model.PipelineTriggerVersionPolicyModeLatestDeployed:
			run, err = s.executor.ExecuteVerifiedEvent(
				ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug,
				delivery.Receiver.PipelineSlug, &runSlug, model.ExternalEventActor(), source, envelope.RuntimeInput, envelope.EventInput,
			)
		case delivery.Receiver.VersionPolicy.Mode == model.PipelineTriggerVersionPolicyModePinned:
			if delivery.Receiver.VersionPolicy.PinnedVersionHash == nil {
				return s.complete(ctx, args, "failed", "platform", "invalid_version_policy", now, delivery.EnvelopeExpiresAt)
			}
			run, err = s.executor.ExecuteVerifiedEventVersion(
				ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug,
				delivery.Receiver.PipelineSlug, *delivery.Receiver.VersionPolicy.PinnedVersionHash, &runSlug,
				model.ExternalEventActor(), source, envelope.RuntimeInput, envelope.EventInput,
			)
		default:
			return s.complete(ctx, args, "failed", "platform", "invalid_version_policy", now, delivery.EnvelopeExpiresAt)
		}
		if xerror.Is(err, pipelineruns.ErrRunAlreadyExists) {
			run, err = s.executor.GetRun(ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug, runSlug.String())
		}

	}
	if err != nil && run == nil {
		if permanentExecutionError(err) {
			return s.complete(ctx, args, "failed", "platform", "execution_rejected", s.clock.Now().UTC(), delivery.EnvelopeExpiresAt)
		}
		if xerror.Is(err, pipelinesvc.ErrPipelineExecutionNotStarted) {
			if resetErr := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
				return s.repo.ResetPreflight(ctx, tx, args.ReceiverID, args.DeliveryID, args.RunAttempt)
			}); resetErr != nil {
				return xerror.Join(err, resetErr)
			}
		}
		return err
	}
	if run == nil {
		return s.complete(ctx, args, "failed", "platform", "execution_returned_no_run", s.clock.Now().UTC(), delivery.EnvelopeExpiresAt)
	}
	if run.Metadata.Status != "succeeded" && run.Metadata.Status != "failed" && run.Metadata.Status != "canceled" {
		return xerror.Unexpected().Detail("webhook execution is awaiting a terminal attempt")
	}
	if run.Metadata.Status == "succeeded" && err == nil {
		return s.complete(ctx, args, "succeeded", "", "", s.clock.Now().UTC(), delivery.EnvelopeExpiresAt)
	}
	failureKind := runFailureKind(run)
	scheduledAt, decisionReason, shouldRetry := retryDecision(
		delivery, failureKind, args.RunAttempt, run.Metadata.StartedAt, s.clock.Now().UTC(),
	)
	if run.Timeline.Failure != nil && run.Timeline.Failure.AutomaticRetryProhibited {
		shouldRetry = false
		decisionReason = "execution_response_limit_exceeded"
	}
	if shouldRetry {
		return s.recordRetry(ctx, args, run.Metadata.PipelineVersionSpecHash, failureKind, scheduledAt)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.complete(ctx, args, "failed", failureKind, decisionReason, s.clock.Now().UTC(), delivery.EnvelopeExpiresAt); err != nil {
			return err
		}
		if run.Metadata.Status != "failed" {
			return nil
		}
		candidate := notificationsvc.RunFailedCandidate(delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug,
			delivery.Receiver.EnvironmentSlug, delivery.Receiver.PipelineSlug, runSlug, delivery.RunSlug, s.clock.Now().UTC())
		candidate.SourceRefs["trigger_slug"] = delivery.Receiver.TriggerSlug.String()
		candidate.SourceRefs["trigger_revision"] = strconv.FormatInt(delivery.Receiver.TriggerRevision, 10)
		return s.notifier.EnqueueCandidate(ctx, tx, candidate)
	})
}

func (s *Service) complete(ctx context.Context, args jobs.PipelineWebhookDeliveryArgs, status, failureKind, decisionReason string, at, envelopeExpiresAt time.Time) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if status == "failed" {
			delivery, err := s.repo.GetIntake(ctx, tx, args.ReceiverID, args.DeliveryID)
			if err != nil {
				return err
			}
			run, err := webhookAttemptRunSlug(delivery.RunSlug, args.RunAttempt)
			if err != nil {
				return err
			}
			if err := s.executor.FailUnadmittedRun(ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug, run); err != nil {
				return err
			}
		}
		if err := s.repo.Complete(ctx, tx, pipelinewebhooks.CompleteParams{
			ReceiverID: args.ReceiverID, DeliveryID: args.DeliveryID, AttemptNumber: args.RunAttempt,
			Status: status, FailureKind: failureKind, DecisionReason: decisionReason, CompletedAt: at,
		}); err != nil {
			return err
		}
		if status != "failed" {
			return nil
		}
		_, err := s.producer.EnqueuePipelineWebhookDelivery(ctx, tx, jobs.PipelineWebhookDeliveryArgs{
			ReceiverID: args.ReceiverID, DeliveryID: args.DeliveryID, RunAttempt: args.RunAttempt,
			Cleanup: true, ScheduledAt: envelopeExpiresAt,
		})
		return err
	})
}

func (s *Service) recordRetry(ctx context.Context, args jobs.PipelineWebhookDeliveryArgs, versionHash, failureKind string, scheduledAt time.Time) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		nextAttempt := args.RunAttempt + 1
		if err := s.repo.RecordRetry(ctx, tx, pipelinewebhooks.RecordRetryParams{
			ReceiverID: args.ReceiverID, DeliveryID: args.DeliveryID,
			AttemptNumber: args.RunAttempt, NextAttemptNumber: nextAttempt,
			VersionHash: versionHash, FailureKind: failureKind,
			NextRetryAt: scheduledAt, UpdatedAt: s.clock.Now().UTC(),
		}); err != nil {
			return err
		}
		queued, err := s.producer.EnqueuePipelineWebhookDelivery(ctx, tx, jobs.PipelineWebhookDeliveryArgs{
			ReceiverID: args.ReceiverID, DeliveryID: args.DeliveryID,
			RunAttempt: nextAttempt, ScheduledAt: scheduledAt,
		})
		if err != nil {
			return err
		}
		return s.acceptQueuedDelivery(ctx, tx, args.ReceiverID, args.DeliveryID, nextAttempt, s.clock.Now().UTC(), queued.ID)
	})
}

func validateDeliveryRequest(request AcceptRequest, now time.Time) (int, []byte, error) {
	if !receiverIDPattern.MatchString(request.ReceiverID) || len(request.Body) == 0 {
		return 0, nil, ErrInvalidDelivery.Detail("webhook receiver and body are required")
	}
	if len(request.Body) > MaxDeliveryBodyBytes {
		return 0, nil, ErrInvalidDelivery.Detail("webhook body exceeds the delivery limit")
	}
	if !canonicalPositiveIntegerPattern.MatchString(request.KeyVersion) || !canonicalPositiveIntegerPattern.MatchString(request.Timestamp) ||
		!deliveryIDPattern.MatchString(request.DeliveryID) || !signaturePattern.MatchString(request.Signature) {
		return 0, nil, ErrInvalidDelivery.Detail("webhook headers are invalid")
	}
	keyVersion64, err := strconv.ParseInt(request.KeyVersion, 10, 32)
	if err != nil {
		return 0, nil, ErrInvalidDelivery.With(err).Detail("webhook key version is invalid")
	}
	timestamp, err := strconv.ParseInt(request.Timestamp, 10, 64)
	if err != nil {
		return 0, nil, ErrInvalidDelivery.With(err).Detail("webhook timestamp is invalid")
	}
	signedAt := time.Unix(timestamp, 0).UTC()
	if signedAt.Before(now.Add(-deliveryFreshness)) || signedAt.After(now.Add(deliveryFreshness)) {
		return 0, nil, ErrVerificationFailed
	}
	signature, err := hex.DecodeString(request.Signature[len("v1="):])
	if err != nil || len(signature) != sha256.Size {
		return 0, nil, ErrInvalidDelivery.With(err).Detail("webhook signature is invalid")
	}
	return int(keyVersion64), signature, nil
}

func deliveryMAC(secret []byte, keyVersion, timestamp, deliveryID string, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(bytes.Join([][]byte{[]byte("v1"), []byte(keyVersion), []byte(timestamp), []byte(deliveryID)}, []byte{0}))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

func mapRuntimeInput(document any, mappings []model.PipelineWebhookInputMapping) (map[string]any, error) {
	result := make(map[string]any, len(mappings))
	for _, mapping := range mappings {
		pointer, err := jsonpointer.New(mapping.JSONPointer)
		if err != nil {
			return nil, ErrInvalidDelivery.With(err).Detail("webhook input mapping is invalid")
		}
		value, _, err := pointer.Get(document)
		if err != nil {
			return nil, ErrInvalidDelivery.With(err).Detail("webhook input mapping did not resolve")
		}
		result[mapping.RuntimeInputKey] = value
	}
	return result, nil
}

func deliveryRunSlug(receiverID, deliveryID string) slug.Slug {
	digest := sha256.Sum256([]byte(receiverID + "\x00" + deliveryID))
	return slug.FromString("webhook-" + hex.EncodeToString(digest[:12]))
}

func webhookAttemptRunSlug(root slug.Slug, attempt int) (slug.Slug, error) {
	if attempt <= 1 {
		if root == "" {
			return "", slug.ErrInvalidSlug
		}
		return root, nil
	}
	return slug.FromName(fmt.Sprintf("%s-retry-%d", root, attempt))
}

func retryDecision(
	delivery pipelinewebhooks.Delivery,
	failureKind string,
	attempt int,
	startedAt, now time.Time,
) (time.Time, string, bool) {
	policy := delivery.Receiver.RetryPolicy
	if !policy.Enabled || policy.MaxAttempts <= 1 {
		return time.Time{}, "retry_policy_disabled", false
	}
	if attempt >= policy.MaxAttempts {
		return time.Time{}, "max_attempts_exhausted", false
	}
	retryable := false
	for _, allowed := range policy.RetryableFailureKinds {
		if allowed.String() == failureKind {
			retryable = true
			break
		}
	}
	if !retryable {
		return time.Time{}, "non_retryable_failure_kind", false
	}
	if startedAt.IsZero() {
		startedAt = now
	}
	scheduledAt := startedAt.Add(retryDelay(policy, attempt+1))
	if scheduledAt.Before(now) {
		scheduledAt = now
	}
	if policy.MaxElapsedSeconds != nil &&
		scheduledAt.Sub(delivery.ReceivedAt) > time.Duration(*policy.MaxElapsedSeconds)*time.Second {
		return time.Time{}, "max_elapsed_exhausted", false
	}
	return scheduledAt, "retryable_failure_kind", true
}

func retryDelay(policy model.PipelineTriggerRetryPolicy, attempt int) time.Duration {
	delay := policy.InitialDelaySeconds
	if policy.BackoffStrategy == model.PipelineTriggerBackoffStrategyExponential {
		for i := 2; i < attempt; i++ {
			delay *= 2
			if policy.MaxDelaySeconds > 0 && delay >= policy.MaxDelaySeconds {
				delay = policy.MaxDelaySeconds
				break
			}
		}
	}
	return time.Duration(delay) * time.Second
}

func runFailureKind(run *pipelinesvc.PipelineRunDetail) string {
	if run != nil && run.Metadata.FailureKind != nil {
		return *run.Metadata.FailureKind
	}
	return "unknown"
}

func permanentExecutionError(err error) bool {
	var typed *xerror.Error
	if !xerror.As(err, &typed) {
		return false
	}
	switch typed.Kind() {
	case xerror.KindInvalidInput, xerror.KindNotFound, xerror.KindForbidden, xerror.KindUnauthorized, xerror.KindUnsupported:
		return true
	default:
		return false
	}
}

func webhookRunSource(delivery pipelinewebhooks.Delivery, attempt int) (pipelinerun.RunSourceSummary, error) {
	source := pipelinerun.RunSourceSummary{
		Type: model.PipelineRunSourceTypeWebhook, Summary: fmt.Sprintf("Webhook run from trigger %s", delivery.Receiver.TriggerSlug),
		EnvironmentSlug: delivery.Receiver.EnvironmentSlug, Entrypoint: model.PipelineRunSourceEntrypointWebhook,
		ActorType: model.EventActorTypeExternal, ParameterKeys: append([]string(nil), delivery.Receiver.RuntimeInputKeys...),
		AttemptNumber: attempt, RootRunSlug: delivery.RunSlug,
		Webhook: &pipelinerun.RunSourceWebhookSummary{
			DeliveryRecordID: delivery.DatabaseID,
			TriggerSlug:      delivery.Receiver.TriggerSlug, TriggerRevision: delivery.Receiver.TriggerRevision,
			DeliveryID: delivery.DeliveryID, PayloadDigest: delivery.PayloadDigest,
			PayloadSizeBytes: delivery.PayloadSizeBytes, ReceivedAt: delivery.ReceivedAt, VerificationStatus: "verified",
		},
	}
	if attempt > 1 {
		retryOf, err := webhookAttemptRunSlug(delivery.RunSlug, attempt-1)
		if err != nil {
			return pipelinerun.RunSourceSummary{}, xerror.Wrapf("derive previous pipeline webhook attempt run slug: %w", err)
		}
		source.RetryOfRunSlug = &retryOf
	}
	return source, nil
}

func (s *Service) acceptQueuedDelivery(ctx context.Context, tx postgres.DBTX, receiverID, deliveryID string, attempt int, at time.Time, jobID int64) error {
	delivery, err := s.repo.GetIntake(ctx, tx, receiverID, deliveryID)
	if err != nil {
		return err
	}
	source, err := webhookRunSource(delivery, attempt)
	if err != nil {
		return err
	}
	runSlug, err := webhookAttemptRunSlug(delivery.RunSlug, attempt)
	if err != nil {
		return err
	}
	return s.executor.AcceptQueuedRun(ctx, delivery.Receiver.OrgSlug, delivery.Receiver.ProjectSlug, delivery.Receiver.EnvironmentSlug,
		delivery.Receiver.PipelineSlug, runSlug, source, at, jobID)
}

func envelopeScope(org slug.Slug, receiver, delivery string) crypto.CredentialScope {
	identity, _ := json.Marshal([2]string{receiver, delivery}) //nolint:errchkjson // A fixed string array has no unsupported values or cycles.
	return crypto.CredentialScope{Tenant: org.String(), Resource: string(identity), Purpose: "pipeline-webhook-envelope", SchemaVersion: 1}
}
