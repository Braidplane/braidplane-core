package executionrequests

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateAmbiguous State = "ambiguous"
)

type Request struct {
	ClaimToken        uuid.UUID
	ObservedAt        time.Time
	ObservedLocallyAt time.Time
	ID                uuid.UUID
	OrganizationSlug  string
	DataSourceSlug    string
	Kind              string
	State             State
	RequestCiphertext []byte
	ResultCiphertext  []byte
	DeadlineAt        time.Time
	CancelRequestedAt *time.Time
	LeaseExpiresAt    *time.Time
	QueueDeadlineAt   *time.Time
	ConcurrencyLimit  int
	QueueCapacity     int
}

type DeliverySnapshot struct {
	Request    Request
	CreatedAt  time.Time
	ObservedAt time.Time
}

type Metrics struct {
	PendingCount            int64
	RunningCount            int64
	OldestPendingAgeSeconds float64
	ConnectorActiveCount    int64
	ConnectorQueuedCount    int64
}

type Repository struct{}

func New() Repository { return Repository{} }

func (Repository) LockInitiatingAccount(ctx context.Context, tx postgres.DBTX, user slug.Slug) error {
	active, err := dbgen.New(tx).LockExecutionInitiatingAccount(ctx, user)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if !active {
		return executiondelivery.ErrAccountUnavailable
	}
	return nil
}

// BindUser serializes admission with account removal. The service keeps this
// lock and the new request in the same transaction until dispatch is committed.
func (Repository) BindUser(ctx context.Context, tx postgres.DBTX, id uuid.UUID, org string, user slug.Slug) error {
	count, err := dbgen.New(tx).BindExecutionRequestUser(ctx, dbgen.BindExecutionRequestUserParams{
		RequestID: pgtype.Text{String: id.String(), Valid: true},
		OrgSlug:   pgtype.Text{String: org, Valid: true},
		UserSlug:  pgtype.Text{String: user.String(), Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if count != 1 {
		return executiondelivery.ErrAccountUnavailable
	}
	return nil
}

// Preflight verifies the executor's complete capability set without claiming
// or mutating work. Missing migration/functions or grants fail startup closed.
func (Repository) Preflight(ctx context.Context, db postgres.DBTX) error {
	var allowed bool
	err := db.QueryRow(ctx, `
		SELECT
			to_regprocedure('public.claim_next_execution_request()') IS NOT NULL
			AND to_regprocedure('public.heartbeat_execution_request(uuid,uuid)') IS NOT NULL
 AND to_regprocedure('public.check_connector_dispatch(uuid,uuid,text,text,text,jsonb,text,timestamp with time zone,boolean)') IS NOT NULL
 AND has_function_privilege(current_user,'public.check_connector_dispatch(uuid,uuid,text,text,text,jsonb,text,timestamp with time zone,boolean)','EXECUTE')
				AND to_regprocedure('public.complete_execution_request(uuid,uuid,bytea,boolean)') IS NOT NULL
				AND to_regprocedure('public.execution_request_metrics()') IS NOT NULL
				AND to_regprocedure('public.connector_execution_admission_metrics()') IS NOT NULL
				AND has_function_privilege(current_user, 'public.claim_next_execution_request()', 'EXECUTE')
				AND has_function_privilege(current_user, 'public.heartbeat_execution_request(uuid,uuid)', 'EXECUTE')
				AND has_function_privilege(current_user, 'public.complete_execution_request(uuid,uuid,bytea,boolean)', 'EXECUTE')
				AND has_function_privilege(current_user, 'public.execution_request_metrics()', 'EXECUTE')
				AND has_function_privilege(
					current_user,
					'public.connector_execution_admission_metrics()',
					'EXECUTE'
				)
	`).Scan(&allowed)
	if err != nil {
		return xerror.Wrapf("preflight execution request capabilities: %w", err)
	}
	if !allowed {
		return xerror.Unexpected().Detail("execution request capabilities are unavailable")
	}
	return nil
}

// PreflightDelivery verifies the app worker can claim one opaque River
// delivery and then heartbeat and complete the resulting request.
func (Repository) PreflightDelivery(ctx context.Context, db postgres.DBTX) error {
	var allowed bool
	err := db.QueryRow(ctx, `
		SELECT
			to_regprocedure('public.claim_execution_request(uuid)') IS NOT NULL
			AND to_regprocedure('public.heartbeat_execution_request(uuid,uuid)') IS NOT NULL
 AND to_regprocedure('public.check_connector_dispatch(uuid,uuid,text,text,text,jsonb,text,timestamp with time zone,boolean)') IS NOT NULL
 AND has_function_privilege(current_user,'public.check_connector_dispatch(uuid,uuid,text,text,text,jsonb,text,timestamp with time zone,boolean)','EXECUTE')
			AND to_regprocedure('public.complete_execution_request(uuid,uuid,bytea,boolean)') IS NOT NULL
			AND has_function_privilege(current_user, 'public.claim_execution_request(uuid)', 'EXECUTE')
			AND has_function_privilege(current_user, 'public.heartbeat_execution_request(uuid,uuid)', 'EXECUTE')
			AND has_function_privilege(current_user, 'public.complete_execution_request(uuid,uuid,bytea,boolean)', 'EXECUTE')
			AND has_table_privilege(current_user, 'public.execution_requests', 'SELECT')
			AND has_table_privilege(current_user, 'public.execution_requests', 'UPDATE')
			AND has_table_privilege(current_user, 'public.organizations', 'SELECT')
	`).Scan(&allowed)
	if err != nil {
		return xerror.Wrapf("preflight River execution delivery capabilities: %w", err)
	}
	if !allowed {
		return xerror.Unexpected().Detail("River execution delivery capabilities are unavailable")
	}
	return nil
}

// Metrics reads only fixed queue aggregates through the executor capability.
func (Repository) Metrics(ctx context.Context, db postgres.DBTX) (Metrics, error) {
	var result Metrics
	err := db.QueryRow(ctx, `
		SELECT
			request.pending_count,
			request.running_count,
			request.oldest_pending_age_seconds,
			admission.active_count,
			admission.queued_count
		FROM execution_request_metrics() AS request
		CROSS JOIN connector_execution_admission_metrics() AS admission
	`).Scan(
		&result.PendingCount,
		&result.RunningCount,
		&result.OldestPendingAgeSeconds,
		&result.ConnectorActiveCount,
		&result.ConnectorQueuedCount,
	)
	if err != nil {
		return Metrics{}, xerror.Wrapf("read execution request metrics: %w", err)
	}
	return result, nil
}

func (Repository) Create(ctx context.Context, tx postgres.DBTX, request Request, expiresAt time.Time) error {
	if request.DataSourceSlug != "" {
		if request.ConcurrencyLimit < 1 ||
			request.QueueCapacity < 1 {
			return xerror.Unexpected().Detail("execution admission limits must be positive")
		}
		if request.QueueDeadlineAt == nil {
			return xerror.Unexpected().Detail("execution admission queue deadline is required")
		}

		var outcome string
		err := tx.QueryRow(ctx, `
			SELECT create_admitted_execution_request(
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
			)
		`,
			request.ID,
			request.OrganizationSlug,
			request.DataSourceSlug,
			request.Kind,
			request.RequestCiphertext,
			request.DeadlineAt,
			request.QueueDeadlineAt,
			expiresAt,
			request.ConcurrencyLimit,
			request.QueueCapacity,
		).Scan(&outcome)
		if err != nil {
			return xerror.Wrapf("create admitted execution request: %w", err)
		}
		switch outcome {
		case "created":
			return nil
		case "queue_full":
			return xerror.ExecutionConcurrencyLimitExceeded().Detailf(
				"data source %q execution queue is full",
				request.DataSourceSlug,
			)
		case "not_found":
			return xerror.Unexpected().Detail("execution request data source not found")
		default:
			return xerror.Unexpected().Detail("invalid execution admission outcome")
		}
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO execution_requests (
			id, organization_id, kind, request_ciphertext, deadline_at, expires_at
		)
		SELECT $1, id, $3, $4,
		       CASE WHEN $3 = 'proof_metadata' THEN LEAST($5::timestamptz, clock_timestamp() + $7::bigint * interval '1 microsecond') ELSE $5::timestamptz END, $6
		FROM organizations
		WHERE slug = $2
	`, request.ID, request.OrganizationSlug, request.Kind, request.RequestCiphertext, request.DeadlineAt, expiresAt, time.Until(request.DeadlineAt).Microseconds())
	if err != nil {
		return xerror.Wrapf("create execution request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return xerror.Unexpected().Detail("execution request organization not found")
	}
	return nil
}

func (Repository) BindSource(ctx context.Context, db postgres.DBTX, id uuid.UUID, org string, source slug.Slug) error {
	n, err := dbgen.New(db).BindExecutionRequestSource(ctx, dbgen.BindExecutionRequestSourceParams{RequestID: pgtype.UUID{Bytes: id.UUID, Valid: true}, OrgSlug: slug.FromString(org), SourceSlug: source})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if n != 1 {
		return xerror.Unexpected().Detail("execution source binding is unavailable")
	}
	return nil
}

func (Repository) Find(ctx context.Context, db postgres.DBTX, id uuid.UUID, organizationSlug string) (Request, error) {
	var request Request
	err := db.QueryRow(ctx, `
		SELECT r.id, o.slug, r.kind, r.state, r.request_ciphertext,
		       r.result_ciphertext, r.deadline_at, r.cancel_requested_at,
		       r.queue_deadline_at
		FROM execution_requests r
		JOIN organizations o ON o.id = r.organization_id
		WHERE r.id = $1 AND o.slug = $2
	`, id, organizationSlug).Scan(
		&request.ID,
		&request.OrganizationSlug,
		&request.Kind,
		&request.State,
		&request.RequestCiphertext,
		&request.ResultCiphertext,
		&request.DeadlineAt,
		&request.CancelRequestedAt,
		&request.QueueDeadlineAt,
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return Request{}, xerror.Unexpected().With(err).Detail("execution request not found")
		}
		return Request{}, xerror.Wrapf("find execution request: %w", err)
	}
	return request, nil
}

func (Repository) Claim(ctx context.Context, db postgres.DBTX, id uuid.UUID) (Request, bool, error) {
	request := Request{ObservedLocallyAt: time.Now()}
	err := db.QueryRow(ctx, `SELECT request.*, clock_timestamp() FROM claim_execution_request($1) AS request`, id).Scan(
		&request.ID,
		&request.OrganizationSlug,
		&request.Kind,
		&request.State,
		&request.RequestCiphertext,
		&request.ResultCiphertext,
		&request.DeadlineAt,
		&request.CancelRequestedAt,
		&request.ClaimToken,
		&request.ObservedAt,
	)
	if xerror.Is(err, pgx.ErrNoRows) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, xerror.Wrapf("claim execution request: %w", err)
	}
	return request, true, nil
}

// FindForDelivery locks one opaque River delivery so the service can decide
// its disposition without racing another claimant.
func (Repository) FindForDelivery(
	ctx context.Context,
	db postgres.DBTX,
	id uuid.UUID,
) (DeliverySnapshot, bool, error) {
	var snapshot DeliverySnapshot
	request := &snapshot.Request
	err := db.QueryRow(ctx, `
		-- Force the volatile clock read above the row-locking plan node so a
		-- waiter cannot report an observation time from before it owned the row.
		WITH locked AS MATERIALIZED (
			SELECT r.id, o.slug, r.kind, r.state, r.request_ciphertext,
			       r.result_ciphertext, r.deadline_at, r.cancel_requested_at,
			       r.lease_expires_at, r.queue_deadline_at, r.created_at
			FROM execution_requests r
			JOIN organizations o ON o.id = r.organization_id
			WHERE r.id = $1
			FOR UPDATE OF r
		)
		SELECT id, slug, kind, state, request_ciphertext, result_ciphertext,
		       deadline_at, cancel_requested_at, lease_expires_at,
		       queue_deadline_at, created_at, clock_timestamp()
		FROM locked
	`, id).Scan(
		&request.ID,
		&request.OrganizationSlug,
		&request.Kind,
		&request.State,
		&request.RequestCiphertext,
		&request.ResultCiphertext,
		&request.DeadlineAt,
		&request.CancelRequestedAt,
		&request.LeaseExpiresAt,
		&request.QueueDeadlineAt,
		&snapshot.CreatedAt,
		&snapshot.ObservedAt,
	)
	if xerror.Is(err, pgx.ErrNoRows) {
		return DeliverySnapshot{}, false, nil
	}
	if err != nil {
		return DeliverySnapshot{}, false, xerror.Wrapf("find execution request delivery: %w", err)
	}
	return snapshot, true, nil
}

// ClaimNext obtains the next opaque execution capability through a
// SECURITY DEFINER function. The executor role has no direct table access.
func (Repository) ClaimNext(ctx context.Context, db postgres.DBTX) (Request, bool, error) {
	request := Request{ObservedLocallyAt: time.Now()}
	err := db.QueryRow(ctx, `SELECT request.*, clock_timestamp() FROM claim_next_execution_request() AS request`).Scan(
		&request.ID,
		&request.OrganizationSlug,
		&request.Kind,
		&request.State,
		&request.RequestCiphertext,
		&request.ResultCiphertext,
		&request.DeadlineAt,
		&request.CancelRequestedAt,
		&request.ClaimToken,
		&request.ObservedAt,
	)
	if xerror.Is(err, pgx.ErrNoRows) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, xerror.Wrapf("claim next execution request: %w", err)
	}
	return request, true, nil
}

// Heartbeat renews the scoped capability and reports cancellation/deadline.
func (Repository) Heartbeat(ctx context.Context, db postgres.DBTX, id, claimToken uuid.UUID) (bool, error) {
	var cancelled bool
	err := db.QueryRow(ctx, `SELECT heartbeat_execution_request($1, $2)`, id, claimToken).Scan(&cancelled)
	if xerror.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, xerror.Wrapf("heartbeat execution request: %w", err)
	}
	return cancelled, nil
}

func (Repository) Complete(ctx context.Context, db postgres.DBTX, id, claimToken uuid.UUID, result []byte, succeeded bool) error {
	var completed bool
	err := db.QueryRow(ctx, `SELECT COALESCE(complete_execution_request($1, $2, $3, $4), FALSE)`, id, claimToken, result, succeeded).Scan(&completed)
	if err != nil {
		return xerror.Wrapf("complete execution request: %w", err)
	}
	if !completed {
		return xerror.Unexpected().Detail("execution request was not running")
	}
	return nil
}

func (Repository) Cancel(ctx context.Context, db postgres.DBTX, id uuid.UUID) (bool, error) {
	var cancelledBeforeExecution bool
	err := db.QueryRow(ctx, `
		WITH cancellation AS (
			SELECT clock_timestamp() AS cancelled_at
		),
		updated AS (
			UPDATE execution_requests
			SET cancel_requested_at = COALESCE(cancel_requested_at, cancellation.cancelled_at),
			    state = CASE WHEN state = 'pending' THEN 'cancelled' ELSE state END,
			    completed_at = CASE
			        WHEN state = 'pending' THEN cancellation.cancelled_at
			        ELSE completed_at
			    END,
			    updated_at = cancellation.cancelled_at
			FROM cancellation
			WHERE id = $1 AND state IN ('pending', 'running')
			RETURNING execution_requests.state
		)
		SELECT COALESCE((SELECT state = 'cancelled' FROM updated), FALSE)
	`, id).Scan(&cancelledBeforeExecution)
	if err != nil {
		return false, xerror.Wrapf("cancel execution request: %w", err)
	}
	return cancelledBeforeExecution, nil
}

// DeleteExpired expires encrypted envelopes while retaining unresolved processing
// metadata. Its count includes only deleted rows, not scrubbed payloads. It runs
// on the control-plane connection; executors have no table DELETE privilege.
func (Repository) DeleteExpired(ctx context.Context, db postgres.DBTX) (int64, error) {
	count, err := dbgen.New(db).ExpireExecutionRequests(ctx)
	if err != nil {
		return 0, xerror.Unexpected().With(err).Detail("expire execution request payloads")
	}
	return count, nil
}

type DispatchAuthority struct {
	ClaimToken                     uuid.UUID
	RequestID                      uuid.UUID
	Organization, DataSource, Type string
	Config                         []byte
	CredentialSlug                 *string
	CredentialUpdatedAt            *time.Time
	ReadOnly                       bool
}

// CheckDispatch exposes only authority for the claimed UUID, never source data.
func (Repository) CheckDispatch(ctx context.Context, db postgres.DBTX, expected DispatchAuthority) (bool, error) {
	at := pgtype.Timestamptz{}
	if expected.CredentialUpdatedAt != nil {
		at = pgtype.Timestamptz{Time: *expected.CredentialUpdatedAt, Valid: true}
	}
	allowed, err := dbgen.New(db).CheckConnectorDispatch(ctx, dbgen.CheckConnectorDispatchParams{
		RequestID:          pgtype.UUID{Bytes: expected.RequestID.UUID, Valid: true},
		ExpectedClaimToken: pgtype.UUID{Bytes: expected.ClaimToken.UUID, Valid: true},
		OrganizationSlug:   expected.Organization, DataSourceSlug: expected.DataSource, DataSourceType: expected.Type,
		ExpectedConfig: expected.Config, CredentialSlug: postgres.ToNullableText(expected.CredentialSlug), CredentialUpdatedAt: at, ReadOnly: expected.ReadOnly,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return allowed, nil
}
