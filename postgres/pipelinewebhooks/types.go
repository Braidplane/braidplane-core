package pipelinewebhooks

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Receiver struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, TriggerSlug, PipelineSlug slug.Slug
	TriggerRevision                                                  int64
	VersionPolicy                                                    model.PipelineTriggerVersionPolicy
	RetryPolicy                                                      model.PipelineTriggerRetryPolicy
	RuntimeInputKeys                                                 []string
	InputMappings                                                    []model.PipelineWebhookInputMapping
}

type AcceptParams struct {
	ReceiverID, DeliveryID, PayloadDigest string
	PayloadSizeBytes                      int64
	KeyVersion                            int
	Receiver                              Receiver
	RunSlug                               slug.Slug
	EncryptedEnvelope                     []byte
	EnvelopeExpiresAt, ReceivedAt         time.Time
}

type AcceptResult struct {
	Inserted      bool
	PayloadDigest string
	RunSlug       slug.Slug
	Status        string
}

type DuplicateResult struct {
	Found, Duplicate bool
	PayloadDigest    string
	RunSlug          slug.Slug
	Status           string
}

type RejectParams struct {
	ReceiverID, DeliveryID, PayloadDigest, DecisionReason string
	PayloadSizeBytes                                      int64
	KeyVersion                                            int
	Receiver                                              Receiver
	RunSlug                                               slug.Slug
	ReceivedAt                                            time.Time
}

// Diagnostic is the safe persistence projection used by operator read surfaces.
type Diagnostic struct {
	ReceiverID, DeliveryID, PayloadDigest, Status string
	TriggerSlug, PipelineSlug                     slug.Slug
	TriggerRevision                               int64
	PayloadSizeBytes                              int64
	MappedInputNames                              []string
	LinkedRunSlug                                 *slug.Slug
	AttemptNumber, DuplicateCount                 int
	FailureKind, DecisionReason                   *string
	NextRetryAt, StartedAt, CompletedAt           *time.Time
	ReceivedAt, UpdatedAt                         time.Time
	LastDuplicateAt                               *time.Time
	EnvelopeRetained                              bool
	EnvelopeExpiresAt                             *time.Time
}

type ReplayRef struct {
	ReceiverID, DeliveryID string
	AttemptNumber          int
}

type Delivery struct {
	Resuming                              bool
	DatabaseID                            int64
	ReceiverID, DeliveryID, PayloadDigest string
	PayloadSizeBytes                      int64
	KeyVersion                            int
	Receiver                              Receiver
	RunSlug                               slug.Slug
	PipelineVersionSpecHash               string
	AttemptNumber                         int
	EncryptedEnvelope                     []byte
	EnvelopeExpiresAt, ReceivedAt         time.Time
}

type CompleteParams struct {
	ReceiverID, DeliveryID              string
	AttemptNumber                       int
	Status, FailureKind, DecisionReason string
	CompletedAt                         time.Time
}

type RecordRetryParams struct {
	ReceiverID, DeliveryID   string
	AttemptNumber            int
	NextAttemptNumber        int
	VersionHash, FailureKind string
	NextRetryAt, UpdatedAt   time.Time
}

type Repository interface {
	GetIntake(context.Context, postgres.DBTX, string, string) (Delivery, error)
	ResetPreflight(context.Context, postgres.DBTX, string, string, int) error
	ResolveReceiver(context.Context, postgres.DBTX, string) (Receiver, error)
	RecordDuplicate(context.Context, postgres.DBTX, string, string, string, time.Time) (DuplicateResult, error)
	Accept(context.Context, postgres.DBTX, AcceptParams) (AcceptResult, error)
	Reject(context.Context, postgres.DBTX, RejectParams) (AcceptResult, error)
	Expire(context.Context, postgres.DBTX, string, string, time.Time) error
	Load(context.Context, postgres.DBTX, string, string, int, time.Time) (Delivery, error)
	MarkRunning(context.Context, postgres.DBTX, string, string, int, time.Time) error
	Complete(context.Context, postgres.DBTX, CompleteParams) error
	RecordRetry(context.Context, postgres.DBTX, RecordRetryParams) error
	ListForTrigger(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) ([]Diagnostic, error)
	GetForTrigger(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (Diagnostic, error)
	Replay(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string, time.Time) (ReplayRef, error)
}
