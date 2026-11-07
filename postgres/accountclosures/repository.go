package accountclosures

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var ErrNotFound = xerror.New(xerror.KindNotFound, "ACCOUNT_CLOSURE_NOT_FOUND", "account closure subject or request not found")

type Repository struct{}

type Subject struct {
	ID      int32
	Slug    slug.Slug
	Email   string
	Deleted bool
}

type Membership struct {
	OrganizationSlug slug.Slug
	RoleSlug         slug.Slug
}

type Request struct {
	ID          int
	Slug        slug.Slug
	UserID      int32
	State       string
	RequestedAt time.Time
	CompletedAt *time.Time
}

func (Repository) LockSubject(ctx context.Context, tx postgres.DBTX, user slug.Slug) (Subject, error) {
	row, err := dbgen.New(tx).LockAccountForDeletion(ctx, user)
	return Subject{ID: int32(row.ID), Slug: row.Slug, Email: row.Email, Deleted: row.DeletedAt.Valid}, queryError(err) //nolint:gosec // users.id is PostgreSQL SERIAL (int32); SQLC projects IDs as int.
}

func (Repository) FindRequest(ctx context.Context, tx postgres.DBTX, userID int32) (Request, error) {
	row, err := dbgen.New(tx).FindAccountDeletionRequest(ctx, pgtype.Int4{Int32: userID, Valid: true})
	return toRequest(row), queryError(err)
}

func (Repository) CreateRequest(ctx context.Context, tx postgres.DBTX, userID int32, request slug.Slug) (Request, error) {
	row, err := dbgen.New(tx).CreateAccountDeletionRequest(ctx, dbgen.CreateAccountDeletionRequestParams{RequestSlug: request, UserID: pgtype.Int4{Int32: userID, Valid: true}})
	return toRequest(row), queryError(err)
}

func (Repository) LockOrganizations(ctx context.Context, tx postgres.DBTX, userID int32) ([]Membership, error) {
	rows, err := dbgen.New(tx).LockAccountDeletionOrganizations(ctx, userID)
	if err != nil {
		return nil, queryError(err)
	}
	result := make([]Membership, 0, len(rows))
	for _, row := range rows {
		result = append(result, Membership{OrganizationSlug: row.Slug, RoleSlug: row.RoleSlug})
	}
	return result, nil
}

func (Repository) HasOtherAdmin(ctx context.Context, tx postgres.DBTX, org slug.Slug, userID int32) (bool, error) {
	other, err := dbgen.New(tx).HasOtherAccountDeletionAdmin(ctx, dbgen.HasOtherAccountDeletionAdminParams{OrgSlug: org, UserID: int(userID)})
	return other, queryError(err)
}

func (Repository) HasOtherMember(ctx context.Context, tx postgres.DBTX, org slug.Slug, userID int32) (bool, error) {
	other, err := dbgen.New(tx).HasOtherAccountDeletionMember(ctx, dbgen.HasOtherAccountDeletionMemberParams{OrgSlug: org, UserID: int(userID)})
	return other, queryError(err)
}

func (Repository) DeletePATs(ctx context.Context, tx postgres.DBTX, userID int32) error {
	return queryError(dbgen.New(tx).DeleteAccountPATs(ctx, userID))
}

func (Repository) DeleteMFAChallenges(ctx context.Context, tx postgres.DBTX, userID int32) error {
	return queryError(dbgen.New(tx).DeleteAccountMFAChallenges(ctx, userID))
}

func (Repository) DeleteMemberships(ctx context.Context, tx postgres.DBTX, userID int32) error {
	return queryError(dbgen.New(tx).DeleteAccountMemberships(ctx, userID))
}

func (Repository) RevokePendingInvitations(ctx context.Context, tx postgres.DBTX, email string) error {
	return queryError(dbgen.New(tx).RevokeAccountPendingInvitations(ctx, email))
}

func (Repository) CancelExecutions(ctx context.Context, tx postgres.DBTX, userID int32) error {
	return queryError(dbgen.New(tx).CancelAccountExecutionRequests(ctx, pgtype.Int4{Int32: userID, Valid: true}))
}

func (Repository) LockJobs(ctx context.Context, tx postgres.DBTX, user slug.Slug) ([]int, error) {
	ids, err := dbgen.New(tx).LockAccountJobs(ctx, user.String())
	return ids, queryError(err)
}

func toRequest(row dbgen.AccountDeletionRequest) Request {
	result := Request{ID: row.ID, Slug: row.Slug, UserID: row.UserID.Int32, State: row.State, RequestedAt: row.RequestedAt.Time}
	if row.CompletedAt.Valid {
		result.CompletedAt = &row.CompletedAt.Time
	}
	return result
}

func (Repository) LockRequest(ctx context.Context, tx postgres.DBTX, request slug.Slug) (Request, error) {
	row, err := dbgen.New(tx).LockAccountDeletionRequest(ctx, request)
	return toRequest(row), queryError(err)
}

func (Repository) LockSubjectByID(ctx context.Context, tx postgres.DBTX, userID int32) (Subject, error) {
	row, err := dbgen.New(tx).LockDeletionSubjectByID(ctx, int(userID))
	return Subject{ID: userID, Slug: row.Slug, Email: row.Email, Deleted: row.DeletedAt.Valid}, queryError(err)
}

func (Repository) Revoke(ctx context.Context, tx postgres.DBTX, userID int32) error {
	return queryError(dbgen.New(tx).RevokeAccount(ctx, int(userID)))
}

func (Repository) ProcessingPending(ctx context.Context, tx postgres.DBTX, subject Subject) (bool, error) {
	pending, err := dbgen.New(tx).AccountDeletionProcessingPending(ctx, dbgen.AccountDeletionProcessingPendingParams{
		UserID: pgtype.Int4{Int32: subject.ID, Valid: true}, UserSlug: subject.Slug.String(),
	})
	return pending.Bool, queryError(err)
}

func (Repository) LegacyProcessingPending(ctx context.Context, tx postgres.DBTX, userID int32) (bool, error) {
	pending, err := dbgen.New(tx).AccountDeletionLegacyProcessingPending(ctx, userID)
	return pending.Bool, queryError(err)
}

func (Repository) HardDelete(ctx context.Context, tx postgres.DBTX, subject Subject) error {
	count, err := dbgen.New(tx).HardDeleteAccount(ctx, dbgen.HardDeleteAccountParams{
		UserID: pgtype.Int4{Int32: subject.ID, Valid: true}, UserSlug: pgtype.Text{String: subject.Slug.String(), Valid: true},
	})
	if err != nil {
		return queryError(err)
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (Repository) Complete(ctx context.Context, tx postgres.DBTX, requestID int) (Request, error) {
	row, err := dbgen.New(tx).CompleteAccountDeletionRequest(ctx, requestID)
	return toRequest(row), queryError(err)
}

func (Repository) Fail(ctx context.Context, tx postgres.DBTX, request slug.Slug) error {
	return queryError(dbgen.New(tx).FailAccountDeletionRequest(ctx, request))
}

func queryError(err error) error {
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
