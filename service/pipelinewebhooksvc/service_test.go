package pipelinewebhooksvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinewebhooks"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestAcceptVerifiesBeforeMappingAndDeduplicates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	secret := []byte("exact-secret-bytes")
	repo := &fakeWebhookRepository{receiver: webhookReceiver(), inserted: true}
	producer := &fakeWebhookProducer{}
	var resolved credentialssvc.ResolvePipelineWebhookSecretParams
	executor := &fakePipelineExecutor{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{secret: secret, resolved: &resolved}, producer, executor)

	request := signedRequest(now, secret, []byte(`{"event":{"customer_id":"c-17"}}`))
	accepted, err := svc.Accept(t.Context(), request)
	require.NoError(t, err)
	require.False(t, accepted.Replay)
	require.Equal(t, 1, producer.calls)
	require.Equal(t, 1, producer.jobs[0].RunAttempt)
	require.Equal(t, slug.FromString("acme"), resolved.OrgSlug)
	require.Equal(t, slug.FromString("etl"), resolved.ProjectSlug)
	require.Equal(t, slug.FromString("prod"), resolved.EnvironmentSlug)
	require.Equal(t, slug.FromString("customer-hook"), resolved.TriggerSlug)
	require.Equal(t, 1, resolved.KeyVersion)
	require.Equal(t, "receiver-opaque", repo.accepted.ReceiverID)
	require.Equal(t, int64(len(request.Body)), repo.accepted.PayloadSizeBytes)
	require.Len(t, repo.accepted.PayloadDigest, sha256.Size*2)
	require.Len(t, executor.queued, 1)
	require.Zero(t, executor.latestCalls)
	require.Equal(t, now, executor.queuedAt)
	require.EqualValues(t, 42, executor.queuedJobID)
	require.EqualValues(t, 42, executor.queued[0].Webhook.DeliveryRecordID)
	require.Equal(t, repo.accepted.RunSlug, executor.queued[0].RootRunSlug)
	queuedJSON, err := json.Marshal(executor.queued[0])
	require.NoError(t, err)
	require.NotContains(t, string(queuedJSON), "c-17")
	require.NotContains(t, string(queuedJSON), string(secret))

	plaintext, err := svc.cipher.Open(envelopeScope(repo.accepted.Receiver.OrgSlug, repo.accepted.ReceiverID, repo.accepted.DeliveryID), repo.accepted.EncryptedEnvelope)
	require.NoError(t, err)
	var envelope executionEnvelope
	require.NoError(t, json.Unmarshal(plaintext, &envelope))
	require.JSONEq(t, string(request.Body), string(envelope.EventInput))
	require.Equal(t, map[string]any{"customer": "c-17"}, envelope.RuntimeInput)

	repo.inserted = false
	replayed, err := svc.Accept(t.Context(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replay)
	require.Equal(t, 1, producer.calls)

	conflict := signedRequest(now, secret, []byte(`{"event":{"customer_id":"different"}}`))
	_, err = svc.Accept(t.Context(), conflict)
	require.ErrorIs(t, err, ErrDeliveryConflict)
	require.Equal(t, 1, producer.calls)

	badSignature := signedRequest(now, secret, []byte(`not-json`))
	badSignature.Signature = "v1=" + strings.Repeat("0", 64)
	_, err = svc.Accept(t.Context(), badSignature)
	require.ErrorIs(t, err, ErrVerificationFailed)

	tamperedBody := request
	tamperedBody.Body = []byte(`{"event":{"customer_id":"tampered"}}`)
	_, err = svc.Accept(t.Context(), tamperedBody)
	require.ErrorIs(t, err, ErrVerificationFailed)

	stale := signedRequest(now.Add(-deliveryFreshness-time.Second), secret, request.Body)
	_, err = svc.Accept(t.Context(), stale)
	require.ErrorIs(t, err, ErrVerificationFailed)
	require.Equal(t, 1, repo.acceptCalls)
}

func TestValidateDeliveryRequestRejectsNonCanonicalHeaders(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	valid := signedRequest(now, []byte("secret"), []byte(`{"event":{}}`))
	tests := []struct {
		name   string
		mutate func(*AcceptRequest)
	}{
		{name: "leading zero key version", mutate: func(request *AcceptRequest) { request.KeyVersion = "01" }},
		{name: "leading zero timestamp", mutate: func(request *AcceptRequest) { request.Timestamp = "0" + request.Timestamp }},
		{name: "invalid delivery identity", mutate: func(request *AcceptRequest) { request.DeliveryID = "delivery id" }},
		{name: "unsupported signature version", mutate: func(request *AcceptRequest) { request.Signature = "v2=" + strings.Repeat("0", 64) }},
		{name: "uppercase signature", mutate: func(request *AcceptRequest) { request.Signature = "v1=" + strings.Repeat("A", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := valid
			tt.mutate(&request)
			_, _, err := validateDeliveryRequest(request, now)
			require.Error(t, err)
		})
	}
}

func TestAcceptFailsWhenDurableHandoffFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	handoffErr := xerror.Unexpected().Detail("enqueue failed")
	repo := &fakeWebhookRepository{receiver: webhookReceiver(), inserted: true}
	producer := &fakeWebhookProducer{err: handoffErr}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{secret: []byte("secret")}, producer, &fakePipelineExecutor{})

	_, err := svc.Accept(t.Context(), signedRequest(now, []byte("secret"), []byte(`{"event":{"customer_id":"c-17"}}`)))
	require.ErrorIs(t, err, handoffErr)
	require.Equal(t, 1, producer.calls)
}

func TestAcceptPersistsVerifiedSafeRejection(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	secret := []byte("secret")
	repo := &fakeWebhookRepository{receiver: webhookReceiver()}
	producer := &fakeWebhookProducer{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{secret: secret}, producer, &fakePipelineExecutor{})

	_, err := svc.Accept(t.Context(), signedRequest(now, secret, []byte(`not-json`)))

	require.ErrorIs(t, err, ErrInvalidDelivery)
	require.Equal(t, "invalid_json", repo.rejected.DecisionReason)
	require.Len(t, repo.rejected.PayloadDigest, sha256.Size*2)
	require.Zero(t, producer.calls)
}

func TestExecuteDeliveryUsesVerifiedEventAndCompletesDelivery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	body := json.RawMessage(`{"event":{"customer_id":"c-17"}}`)
	envelope, err := json.Marshal(executionEnvelope{EventInput: body, RuntimeInput: map[string]any{"customer": "c-17"}})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(envelope)
	require.NoError(t, err)
	repo := &fakeWebhookRepository{delivery: pipelinewebhooks.Delivery{
		DatabaseID: 42,
		ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", PayloadDigest: digestHex(body),
		PayloadSizeBytes: int64(len(body)), RunSlug: slug.FromString("webhook-run"),
		EncryptedEnvelope: encrypted, EnvelopeExpiresAt: now.Add(time.Hour), ReceivedAt: now,
		Receiver: webhookReceiver(),
	}}
	executor := &fakePipelineExecutor{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{secret: []byte("unused")}, &fakeWebhookProducer{}, executor)

	err = svc.ExecuteDelivery(t.Context(), jobs.PipelineWebhookDeliveryArgs{ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunAttempt: 1})
	require.NoError(t, err)
	require.Equal(t, 1, executor.latestCalls)
	require.JSONEq(t, string(body), string(executor.eventInput))
	require.Equal(t, map[string]any{"customer": "c-17"}, executor.runtimeInput)
	require.Equal(t, model.EventActorTypeExternal, executor.actor.Type)
	require.Equal(t, model.PipelineRunSourceTypeWebhook, executor.source.Type)
	require.True(t, repo.loadAfterExpire)
	require.Equal(t, int64(42), executor.source.Webhook.DeliveryRecordID)
	require.Equal(t, "verified", executor.source.Webhook.VerificationStatus)
	safeSource, err := json.Marshal(executor.source)
	require.NoError(t, err)
	require.NotContains(t, string(safeSource), "customer_id")
	require.NotContains(t, string(safeSource), "c-17")
	require.Equal(t, pipelinewebhooks.CompleteParams{
		ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", AttemptNumber: 1,
		Status: "succeeded", CompletedAt: now,
	}, repo.completed)
}

func TestExecuteDeliveryStopsWhenExpiryFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	expireErr := xerror.Unexpected().Detail("expire delivery")
	repo := &fakeWebhookRepository{expireErr: expireErr}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{secret: []byte("unused")}, &fakeWebhookProducer{}, &fakePipelineExecutor{})

	err := svc.ExecuteDelivery(t.Context(), jobs.PipelineWebhookDeliveryArgs{
		ReceiverID: "receiver-opaque",
		DeliveryID: "delivery-1",
		RunAttempt: 1,
	})

	require.ErrorIs(t, err, expireErr)
	require.Zero(t, repo.loadCalls)
	require.Zero(t, repo.markRunningCalls)
}

func TestFailedDeliverySchedulesEnvelopeCleanup(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	repo := &fakeWebhookRepository{delivery: pipelinewebhooks.Delivery{Receiver: webhookReceiver(), RunSlug: "webhook-rejected"}}
	producer := &fakeWebhookProducer{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{}, producer, &fakePipelineExecutor{})
	args := jobs.PipelineWebhookDeliveryArgs{ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunAttempt: 1}

	require.NoError(t, svc.complete(t.Context(), args, "failed", "platform", "execution_rejected", now, expiresAt))
	require.Equal(t, 1, producer.calls)
	require.True(t, producer.jobs[0].Cleanup)
	require.Equal(t, expiresAt, producer.jobs[0].ScheduledAt)

	require.NoError(t, svc.ExecuteDelivery(t.Context(), producer.jobs[0]))
	require.True(t, repo.expired)
	require.Zero(t, repo.loadCalls)
}

func TestAcceptHidesMissingWebhookSecretAsVerificationFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	repo := &fakeWebhookRepository{receiver: webhookReceiver(), inserted: true}
	svc := newWebhookService(
		t, now, repo, fakeSecretResolver{err: credentialssvc.ErrCredentialNotFound},
		&fakeWebhookProducer{}, &fakePipelineExecutor{},
	)

	_, err := svc.Accept(t.Context(), signedRequest(now, []byte("unknown"), []byte(`{"event":{}}`)))

	require.ErrorIs(t, err, ErrVerificationFailed)
}

func TestExecuteDeliveryKeepsEnvelopeForRetryableFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	envelope, err := json.Marshal(executionEnvelope{EventInput: json.RawMessage(`{"event":{}}`), RuntimeInput: map[string]any{}})
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(envelope)
	require.NoError(t, err)
	receiver := webhookReceiver()
	receiver.RetryPolicy = model.PipelineTriggerRetryPolicy{
		Enabled: true, MaxAttempts: 3, BackoffStrategy: model.PipelineTriggerBackoffStrategyFixed,
		InitialDelaySeconds: 30, RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
	}
	repo := &fakeWebhookRepository{delivery: pipelinewebhooks.Delivery{
		DatabaseID: 42,
		ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", PayloadDigest: strings.Repeat("a", 64),
		PayloadSizeBytes: 12, RunSlug: slug.FromString("webhook-run"), EncryptedEnvelope: encrypted,
		EnvelopeExpiresAt: now.Add(time.Hour), ReceivedAt: now, Receiver: receiver,
	}}
	failureKind := model.PipelineTriggerRetryFailureKindDataSource.String()
	executor := &retryingPipelineExecutor{run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{
		Status: "failed", FailureKind: &failureKind, PipelineVersionSpecHash: strings.Repeat("b", 64),
	}}}
	producer := &fakeWebhookProducer{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{secret: []byte("unused")}, producer, executor)

	err = svc.ExecuteDelivery(t.Context(), jobs.PipelineWebhookDeliveryArgs{ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunAttempt: 1})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("b", 64), repo.retryVersionHash)
	require.Equal(t, 2, repo.retryAttempt)
	require.Equal(t, now.Add(30*time.Second), repo.nextRetryAt)
	require.Equal(t, 1, producer.calls)
	require.Equal(t, 2, producer.jobs[0].RunAttempt)
	require.Equal(t, now.Add(30*time.Second), producer.jobs[0].ScheduledAt)
	require.Empty(t, repo.completed.Status)
}

func TestRetryDecisionDoesNotScheduleBeforeFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	receiver := webhookReceiver()
	receiver.RetryPolicy = model.PipelineTriggerRetryPolicy{
		Enabled: true, MaxAttempts: 3, BackoffStrategy: model.PipelineTriggerBackoffStrategyFixed,
		InitialDelaySeconds: 30, MaxDelaySeconds: 30,
		RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
	}

	scheduledAt, _, retry := retryDecision(
		pipelinewebhooks.Delivery{ReceivedAt: now.Add(-5 * time.Minute), Receiver: receiver},
		model.PipelineTriggerRetryFailureKindDataSource.String(), 1, now.Add(-2*time.Minute), now,
	)

	require.True(t, retry)
	require.Equal(t, now, scheduledAt)
}

func TestDeliveryDiagnosticsFailClosedAndReplayExplicitly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	unsafeFailure := "provider response contained secret material"
	unsafeKind := "provider_internal"
	repo := &fakeWebhookRepository{diagnostic: pipelinewebhooks.Diagnostic{
		ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", PayloadDigest: strings.Repeat("a", 64),
		Status: "failed", TriggerSlug: slug.FromString("customer-hook"), TriggerRevision: 3,
		PipelineSlug: slug.FromString("customer-sync"), AttemptNumber: 1, ReceivedAt: now.Add(-time.Minute), UpdatedAt: now,
		FailureKind: &unsafeKind, DecisionReason: &unsafeFailure, EnvelopeRetained: true, EnvelopeExpiresAt: timePtr(now.Add(time.Hour)),
	}, replay: pipelinewebhooks.ReplayRef{ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", AttemptNumber: 2}, delivery: pipelinewebhooks.Delivery{DatabaseID: 42, Receiver: webhookReceiver(), RunSlug: "webhook-replay"}}
	producer := &fakeWebhookProducer{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{}, producer, &fakePipelineExecutor{})

	delivery, err := svc.GetDelivery(t.Context(), slug.FromString("acme"), slug.FromString("etl"), slug.FromString("prod"), slug.FromString("customer-hook"), "delivery-1")
	require.NoError(t, err)
	require.True(t, delivery.ReplayAvailable)
	require.Nil(t, delivery.FailureKind)
	require.Equal(t, "delivery_failed", *delivery.FailureReason)

	replayed, err := svc.ReplayDelivery(t.Context(), slug.FromString("acme"), slug.FromString("etl"), slug.FromString("prod"), slug.FromString("customer-hook"), "delivery-1")
	require.NoError(t, err)
	require.Equal(t, "queued", replayed.Status)
	require.Equal(t, 1, producer.calls)
	require.Equal(t, 2, producer.jobs[0].RunAttempt)
}

func TestListDeliveriesBuildsNextCursorFromTheLastReturnedDelivery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	repo := &fakeWebhookRepository{diagnostics: []pipelinewebhooks.Diagnostic{
		{DeliveryID: "delivery-3", Status: "succeeded", ReceivedAt: now.Add(-time.Minute), UpdatedAt: now},
		{DeliveryID: "delivery-2", Status: "succeeded", ReceivedAt: now.Add(-2 * time.Minute), UpdatedAt: now},
		{DeliveryID: "delivery-1", Status: "succeeded", ReceivedAt: now.Add(-3 * time.Minute), UpdatedAt: now},
	}}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{}, &fakeWebhookProducer{}, &fakePipelineExecutor{})

	page, err := svc.ListDeliveries(t.Context(), slug.FromString("acme"), slug.FromString("etl"), slug.FromString("prod"), slug.FromString("customer-hook"), model.ListRequest{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.True(t, page.HasMore)
	require.Equal(t, model.EncodeCursor(now.Add(-2*time.Minute).Format(time.RFC3339Nano), "delivery-2"), page.NextCursor)
	require.Nil(t, page.PrevCursor)
	require.Equal(t, 2, repo.listRequest.Limit)
}

func TestDeliveryReplayRequiresRunReplayWhenLinked(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	linkedRun := slug.FromString("run-linked")
	repo := &fakeWebhookRepository{diagnostic: pipelinewebhooks.Diagnostic{
		DeliveryID: "delivery-1", Status: "failed", LinkedRunSlug: &linkedRun,
		AttemptNumber: 1, ReceivedAt: now, UpdatedAt: now, EnvelopeRetained: true, EnvelopeExpiresAt: timePtr(now.Add(time.Hour)),
	}}
	producer := &fakeWebhookProducer{}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{}, producer, &fakePipelineExecutor{})

	_, err := svc.ReplayDelivery(t.Context(), slug.FromString("acme"), slug.FromString("etl"), slug.FromString("prod"), slug.FromString("customer-hook"), "delivery-1")
	require.ErrorIs(t, err, ErrReplayUnavailable)
	require.Contains(t, err.Error(), "linked_run_requires_run_replay")
	require.Zero(t, producer.calls)
}

func timePtr(value time.Time) *time.Time { return &value }

func newWebhookService(
	t *testing.T,
	now time.Time,
	repo *fakeWebhookRepository,
	secrets SecretResolver,
	producer Producer,
	executor PipelineExecutor,
) *Service {
	t.Helper()
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": make([]byte, 32)}, make([]byte, 32))
	require.NoError(t, err)
	svc, err := New(testutil.FakeTransactionManager{}, repo, secrets, producer, executor, cipher, testutil.FakeClock{NowFn: func() time.Time { return now }}, testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc
}

func webhookReceiver() pipelinewebhooks.Receiver {
	return pipelinewebhooks.Receiver{
		OrgSlug: slug.FromString("acme"), ProjectSlug: slug.FromString("etl"), EnvironmentSlug: slug.FromString("prod"),
		TriggerSlug: slug.FromString("customer-hook"), PipelineSlug: slug.FromString("customer-sync"), TriggerRevision: 3,
		VersionPolicy:    model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
		RetryPolicy:      model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone},
		RuntimeInputKeys: []string{"customer"},
		InputMappings:    []model.PipelineWebhookInputMapping{{JSONPointer: "/event/customer_id", RuntimeInputKey: "customer"}},
	}
}

func signedRequest(now time.Time, secret, body []byte) AcceptRequest {
	request := AcceptRequest{
		ReceiverID: "receiver-opaque", KeyVersion: "1", Timestamp: "1787313600", DeliveryID: "delivery-1", Body: body,
	}
	request.Timestamp = strconv.FormatInt(now.Unix(), 10)
	request.Signature = "v1=" + hex.EncodeToString(deliveryMAC(secret, request.KeyVersion, request.Timestamp, request.DeliveryID, body))
	return request
}

func digestHex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

type fakeSecretResolver struct {
	secret   []byte
	resolved *credentialssvc.ResolvePipelineWebhookSecretParams
	err      error
}

func (f fakeSecretResolver) ResolvePipelineWebhookSecret(_ context.Context, params credentialssvc.ResolvePipelineWebhookSecretParams) ([]byte, error) {
	if f.resolved != nil {
		*f.resolved = params
	}
	if f.err != nil {
		return nil, f.err
	}
	return append([]byte(nil), f.secret...), nil
}

type fakeWebhookProducer struct {
	calls int
	jobs  []jobs.PipelineWebhookDeliveryArgs
	err   error
}

func (f *fakeWebhookProducer) EnqueuePipelineWebhookDelivery(_ context.Context, _ postgres.DBTX, args jobs.PipelineWebhookDeliveryArgs) (queue.QueuedJob, error) {
	f.calls++
	f.jobs = append(f.jobs, args)
	return queue.QueuedJob{ID: 42}, f.err
}

type fakeWebhookRepository struct {
	preflightResets  int
	receiver         pipelinewebhooks.Receiver
	delivery         pipelinewebhooks.Delivery
	accepted         pipelinewebhooks.AcceptParams
	rejected         pipelinewebhooks.RejectParams
	inserted         bool
	acceptCalls      int
	payloadDigest    string
	completed        pipelinewebhooks.CompleteParams
	retryVersionHash string
	retryAttempt     int
	nextRetryAt      time.Time
	expired          bool
	loadAfterExpire  bool
	expireErr        error
	loadCalls        int
	markRunningCalls int
	diagnostic       pipelinewebhooks.Diagnostic
	diagnostics      []pipelinewebhooks.Diagnostic
	listRequest      model.ListRequest
	replay           pipelinewebhooks.ReplayRef
}

func (f *fakeWebhookRepository) ResolveReceiver(context.Context, postgres.DBTX, string) (pipelinewebhooks.Receiver, error) {
	return f.receiver, nil
}

func (f *fakeWebhookRepository) RecordDuplicate(_ context.Context, _ postgres.DBTX, _, _, digest string, _ time.Time) (pipelinewebhooks.DuplicateResult, error) {
	if f.acceptCalls == 0 {
		return pipelinewebhooks.DuplicateResult{}, nil
	}
	return pipelinewebhooks.DuplicateResult{
		Found: true, Duplicate: digest == f.payloadDigest, PayloadDigest: f.payloadDigest,
		RunSlug: f.accepted.RunSlug, Status: "accepted",
	}, nil
}

func (f *fakeWebhookRepository) Accept(_ context.Context, _ postgres.DBTX, params pipelinewebhooks.AcceptParams) (pipelinewebhooks.AcceptResult, error) {
	f.acceptCalls++
	f.accepted = params
	f.delivery = pipelinewebhooks.Delivery{DatabaseID: 42, Receiver: params.Receiver, ReceiverID: params.ReceiverID, DeliveryID: params.DeliveryID, RunSlug: params.RunSlug, PayloadDigest: params.PayloadDigest, PayloadSizeBytes: params.PayloadSizeBytes, ReceivedAt: params.ReceivedAt}
	if f.payloadDigest == "" {
		f.payloadDigest = params.PayloadDigest
	}
	return pipelinewebhooks.AcceptResult{Inserted: f.inserted, PayloadDigest: f.payloadDigest, RunSlug: params.RunSlug, Status: "accepted"}, nil
}

func (f *fakeWebhookRepository) Reject(_ context.Context, _ postgres.DBTX, params pipelinewebhooks.RejectParams) (pipelinewebhooks.AcceptResult, error) {
	f.rejected = params
	return pipelinewebhooks.AcceptResult{Inserted: true, PayloadDigest: params.PayloadDigest, RunSlug: params.RunSlug, Status: "rejected"}, nil
}

func (f *fakeWebhookRepository) Expire(context.Context, postgres.DBTX, string, string, time.Time) error {
	f.expired = true
	return f.expireErr
}

func (f *fakeWebhookRepository) Load(context.Context, postgres.DBTX, string, string, int, time.Time) (pipelinewebhooks.Delivery, error) {
	f.loadCalls++
	f.loadAfterExpire = f.expired
	return f.delivery, nil
}

func (f *fakeWebhookRepository) MarkRunning(context.Context, postgres.DBTX, string, string, int, time.Time) error {
	f.markRunningCalls++
	return nil
}

func (f *fakeWebhookRepository) Complete(_ context.Context, _ postgres.DBTX, params pipelinewebhooks.CompleteParams) error {
	f.completed = params
	return nil
}

func (f *fakeWebhookRepository) RecordRetry(_ context.Context, _ postgres.DBTX, params pipelinewebhooks.RecordRetryParams) error {
	f.retryVersionHash = params.VersionHash
	f.retryAttempt = params.NextAttemptNumber
	f.nextRetryAt = params.NextRetryAt
	return nil
}

func (f *fakeWebhookRepository) ListForTrigger(_ context.Context, _ postgres.DBTX, _, _, _, _ slug.Slug, req model.ListRequest) ([]pipelinewebhooks.Diagnostic, error) {
	f.listRequest = req
	return f.diagnostics, nil
}

func (f *fakeWebhookRepository) GetForTrigger(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooks.Diagnostic, error) {
	if f.diagnostic.DeliveryID == "" {
		return pipelinewebhooks.Diagnostic{}, pipelinewebhooks.ErrDeliveryNotFound
	}
	return f.diagnostic, nil
}

func (f *fakeWebhookRepository) Replay(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string, time.Time) (pipelinewebhooks.ReplayRef, error) {
	if f.replay.DeliveryID == "" {
		return pipelinewebhooks.ReplayRef{}, pipelinewebhooks.ErrDeliveryNotFound
	}
	f.diagnostic.Status = "accepted"
	f.diagnostic.AttemptNumber = f.replay.AttemptNumber
	return f.replay, nil
}

type fakePipelineExecutor struct {
	queued       []pipelinerun.RunSourceSummary
	queuedAt     time.Time
	queuedJobID  int64
	latestCalls  int
	actor        model.EventActor
	source       pipelinerun.RunSourceSummary
	runtimeInput map[string]any
	eventInput   json.RawMessage
}

func (f *fakePipelineExecutor) ExecuteVerifiedEvent(
	_ context.Context, _, _, _, _ slug.Slug, _ *slug.Slug, actor model.EventActor,
	source pipelinerun.RunSourceSummary, runtimeInput map[string]any, eventInput json.RawMessage,
) (*pipelinesvc.PipelineRunDetail, error) {
	f.latestCalls++
	f.actor = actor
	f.source, f.runtimeInput, f.eventInput = source, runtimeInput, append(json.RawMessage(nil), eventInput...)
	return &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{Status: "succeeded"}}, nil
}

func (f *fakePipelineExecutor) ExecuteVerifiedEventVersion(
	context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string, *slug.Slug, model.EventActor,
	pipelinerun.RunSourceSummary, map[string]any, json.RawMessage,
) (*pipelinesvc.PipelineRunDetail, error) {
	return nil, xerror.Unexpected().Detail("unexpected pinned execution")
}

func (f *fakePipelineExecutor) GetRun(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error) {
	return nil, xerror.Unexpected().Detail("unexpected existing run lookup")
}

type retryingPipelineExecutor struct {
	executions   int
	executionErr error
	run          *pipelinesvc.PipelineRunDetail
}

func (f *retryingPipelineExecutor) ExecuteVerifiedEvent(
	context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *slug.Slug, model.EventActor,
	pipelinerun.RunSourceSummary, map[string]any, json.RawMessage,
) (*pipelinesvc.PipelineRunDetail, error) {
	f.executions++
	if f.executionErr != nil {
		return nil, f.executionErr
	}
	return f.run, xerror.Unexpected().Detail("pipeline failed")
}

func (f *retryingPipelineExecutor) ExecuteVerifiedEventVersion(
	context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string, *slug.Slug, model.EventActor,
	pipelinerun.RunSourceSummary, map[string]any, json.RawMessage,
) (*pipelinesvc.PipelineRunDetail, error) {
	f.executions++
	if f.executionErr != nil {
		return nil, f.executionErr
	}
	return f.run, xerror.Unexpected().Detail("pipeline failed")
}

func (f *retryingPipelineExecutor) GetRun(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error) {
	return f.run, nil
}

func TestRecoveredWebhookFinalizesWithoutDecryptingOrExecuting(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	receiver := webhookReceiver()
	repo := &fakeWebhookRepository{delivery: pipelinewebhooks.Delivery{Resuming: true, Receiver: receiver, ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunSlug: slug.FromString("webhook-recovery"), EncryptedEnvelope: []byte("not-an-envelope"), EnvelopeExpiresAt: now.Add(time.Hour)}}
	executor := &retryingPipelineExecutor{run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{RunSlug: "webhook-recovery", Status: "succeeded"}}}
	svc := newWebhookService(t, now, repo, fakeSecretResolver{}, &fakeWebhookProducer{}, executor)
	require.NoError(t, svc.ExecuteDelivery(t.Context(), jobs.PipelineWebhookDeliveryArgs{ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunAttempt: 1, FinalizeOnly: true}))
	require.Equal(t, "succeeded", repo.completed.Status)
	require.Zero(t, executor.executions)
}

func (f *fakeWebhookRepository) ResetPreflight(context.Context, postgres.DBTX, string, string, int) error {
	f.preflightResets++
	return nil
}

func TestWebhookTransientPreflightFailureRemainsRetryable(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt([]byte(`{"event_input":{"event":{}},"runtime_input":{}}`))
	require.NoError(t, err)
	for _, safe := range []bool{false, true} {
		t.Run(strconv.FormatBool(safe), func(t *testing.T) {
			repo := &fakeWebhookRepository{delivery: pipelinewebhooks.Delivery{Receiver: webhookReceiver(), RunSlug: slug.FromString("preflight"), EncryptedEnvelope: encrypted, EnvelopeExpiresAt: now.Add(time.Hour)}}
			transient := xerror.Unexpected().Detail("temporary deployment lookup failure")
			var failure error = transient
			if safe {
				failure = xerror.Join(transient, pipelinesvc.ErrPipelineExecutionNotStarted)
			}
			executor := &retryingPipelineExecutor{executionErr: failure}
			svc := newWebhookService(t, now, repo, fakeSecretResolver{}, &fakeWebhookProducer{}, executor)
			err := svc.ExecuteDelivery(t.Context(), jobs.PipelineWebhookDeliveryArgs{ReceiverID: "receiver", DeliveryID: "delivery", RunAttempt: 1})
			require.ErrorIs(t, err, transient)
			expected := 0
			if safe {
				expected = 1
			}
			require.Equal(t, expected, repo.preflightResets)
			require.Empty(t, repo.completed.Status)
		})
	}
}

func TestWebhookRunFailureWaitsForTerminalRetryDecision(t *testing.T) {
	t.Parallel()
	for _, retry := range []bool{false, true} {
		t.Run(strconv.FormatBool(retry), func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			receiver := webhookReceiver()
			receiver.RetryPolicy = model.PipelineTriggerRetryPolicy{Enabled: retry, MaxAttempts: 3, BackoffStrategy: model.PipelineTriggerBackoffStrategyFixed, InitialDelaySeconds: 30, RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource}}
			repo := &fakeWebhookRepository{delivery: pipelinewebhooks.Delivery{Resuming: true, Receiver: receiver, RunSlug: "webhook-root", EnvelopeExpiresAt: now.Add(time.Hour)}}
			kind := model.PipelineTriggerRetryFailureKindDataSource.String()
			executor := &retryingPipelineExecutor{run: &pipelinesvc.PipelineRunDetail{Metadata: pipelinesvc.PipelineRunMetadata{RunSlug: "webhook-root", Status: "failed", FailureKind: &kind, PipelineVersionSpecHash: strings.Repeat("b", 64)}}}
			svc := newWebhookService(t, now, repo, fakeSecretResolver{}, &fakeWebhookProducer{}, executor)
			var candidates []model.NotificationCandidate
			svc.notifier = testutil.FakeRunSucceededNotifier{CandidateFn: func(_ context.Context, _ postgres.DBTX, candidate model.NotificationCandidate) error {
				candidates = append(candidates, candidate)
				return nil
			}}
			args := jobs.PipelineWebhookDeliveryArgs{ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunAttempt: 1, FinalizeOnly: true}
			require.NoError(t, svc.ExecuteDelivery(t.Context(), args))
			if retry {
				require.Empty(t, candidates)
				require.Equal(t, 2, repo.retryAttempt)
				return
			}
			require.Len(t, candidates, 1)
			require.Equal(t, model.NotificationTypePipelineRunFailed, candidates[0].NotificationType)
			require.Equal(t, "webhook-root", candidates[0].SourceRefs["root_run_slug"])
			require.Equal(t, receiver.TriggerSlug.String(), candidates[0].SourceRefs["trigger_slug"])
			require.Equal(t, "failed", repo.completed.Status)
			queueErr := xerror.Unexpected().Detail("queue unavailable")
			svc.notifier = testutil.FakeRunSucceededNotifier{CandidateFn: func(context.Context, postgres.DBTX, model.NotificationCandidate) error { return queueErr }}
			require.ErrorIs(t, svc.ExecuteDelivery(t.Context(), args), queueErr)
		})
	}
}

func (f *fakeWebhookRepository) GetIntake(context.Context, postgres.DBTX, string, string) (pipelinewebhooks.Delivery, error) {
	return f.delivery, nil
}

func (f *fakePipelineExecutor) AcceptQueuedRun(_ context.Context, _, _, _, _, _ slug.Slug, source pipelinerun.RunSourceSummary, at time.Time, jobID int64) error {
	f.queued = append(f.queued, source)
	f.queuedAt = at
	f.queuedJobID = jobID
	return nil
}

func (f *fakePipelineExecutor) StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error {
	return nil
}

func (f *retryingPipelineExecutor) AcceptQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinerun.RunSourceSummary, time.Time, int64) error {
	return nil
}

func (f *retryingPipelineExecutor) StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error {
	return nil
}

func (*fakePipelineExecutor) FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return nil
}

func (*retryingPipelineExecutor) FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return nil
}
