package memberinvitations

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

// Create persists a pending organization invitation with a hashed acceptance token.
func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateParams,
) (*Invitation, error) {
	row, err := dbgen.New(tx).CreateOrganizationMemberInvitation(
		ctx,
		dbgen.CreateOrganizationMemberInvitationParams{
			OrgSlug:           postgres.SlugToText(params.OrganizationSlug),
			InvitationSlug:    postgres.SlugToText(params.InvitationSlug),
			Email:             postgres.ToText(params.Email),
			RoleSlug:          postgres.SlugToText(params.RoleSlug),
			InvitedByUserSlug: postgres.OptionalSlugToText(params.InvitedByUserSlug),
			TokenHash:         postgres.ToText(params.TokenHash),
			ExpiresAt:         toTimestamp(params.ExpiresAt),
		},
	)
	if err != nil {
		return nil, mapCreateError(err)
	}

	return invitationFromCreateRow(row), nil
}

// Find returns one organization invitation by its organization and invitation slugs.
func (r *repository) Find(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, invitationSlug slug.Slug,
) (*Invitation, error) {
	row, err := dbgen.New(tx).FindOrganizationMemberInvitation(
		ctx,
		dbgen.FindOrganizationMemberInvitationParams{
			OrgSlug:        orgSlug,
			InvitationSlug: invitationSlug,
		},
	)
	if err != nil {
		return nil, mapReadError(err)
	}

	return invitationFromFindRow(row), nil
}

// FindByTokenHash returns one invitation matching a stored acceptance token hash.
func (r *repository) FindByTokenHash(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*Invitation, error) {
	row, err := dbgen.New(tx).FindOrganizationMemberInvitationByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, mapReadError(err)
	}

	return invitationFromTokenRow(row), nil
}

// FindPendingByEmail returns one pending invitation for an organization and normalized email.
func (r *repository) FindPendingByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	email string,
) (*Invitation, error) {
	row, err := dbgen.New(tx).FindPendingOrganizationMemberInvitationByEmail(
		ctx,
		dbgen.FindPendingOrganizationMemberInvitationByEmailParams{
			OrgSlug: orgSlug,
			Email:   email,
		},
	)
	if err != nil {
		return nil, mapReadError(err)
	}

	return invitationFromPendingEmailRow(row), nil
}

// ListByOrganization returns organization invitations using the shared cursor pagination contract.
func (r *repository) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*Invitation, error) {
	cursorSlug, cursorTimestamp, cursorText, err := postgres.BuildCursorValues(
		req.Cursor,
		req.SortBy,
	)
	if err != nil {
		return nil, xerror.Wrapf("build member invitation cursor values: %w", err)
	}

	rows, err := dbgen.New(tx).ListOrganizationMemberInvitations(
		ctx,
		dbgen.ListOrganizationMemberInvitationsParams{
			OrgSlug:         orgSlug,
			CursorSlug:      cursorSlug,
			SortBy:          pgtype.Text{String: req.SortBy, Valid: req.SortBy != ""},
			OrderDir:        pgtype.Text{String: req.Order, Valid: req.Order != ""},
			Status:          pgtype.Text{String: req.Status, Valid: req.Status != ""},
			CursorTimestamp: cursorTimestamp,
			CursorText:      cursorText,
			LimitCount:      int64(req.Limit),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Invitation, 0, len(rows))
	for _, row := range rows {
		result = append(result, invitationFromListRow(row))
	}

	return result, nil
}

// ExpirePendingByOrganization records expiration for all due pending invitations in an organization.
func (r *repository) ExpirePendingByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	expiredAt time.Time,
) ([]*Invitation, error) {
	rows, err := dbgen.New(tx).ExpirePendingOrganizationMemberInvitations(
		ctx,
		dbgen.ExpirePendingOrganizationMemberInvitationsParams{
			ExpiredAt: toTimestamp(expiredAt),
			OrgSlug:   postgres.SlugToText(orgSlug),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Invitation, 0, len(rows))
	for _, row := range rows {
		result = append(result, invitationFromExpirePendingRow(row))
	}

	return result, nil
}

// ListPendingByEmail returns pending, unexpired invitations for a normalized email address.
func (r *repository) ListPendingByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	email string,
	asOf time.Time,
) ([]*Invitation, error) {
	rows, err := dbgen.New(tx).ListPendingOrganizationMemberInvitationsByEmail(
		ctx,
		dbgen.ListPendingOrganizationMemberInvitationsByEmailParams{
			Email: email,
			AsOf:  toTimestamp(asOf),
		},
	)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	result := make([]*Invitation, 0, len(rows))
	for _, row := range rows {
		result = append(result, invitationFromPendingEmailListRow(row))
	}

	return result, nil
}

// RefreshToken rotates the stored token hash and sent/expiration timestamps for a pending invitation.
func (r *repository) RefreshToken(
	ctx context.Context,
	tx postgres.DBTX,
	params RefreshTokenParams,
) (*Invitation, error) {
	row, err := dbgen.New(tx).RefreshOrganizationMemberInvitationToken(
		ctx,
		dbgen.RefreshOrganizationMemberInvitationTokenParams{
			TokenHash:      postgres.ToText(params.TokenHash),
			ExpiresAt:      toTimestamp(params.ExpiresAt),
			LastSentAt:     toTimestamp(params.LastSentAt),
			OrgSlug:        postgres.SlugToText(params.OrganizationSlug),
			InvitationSlug: postgres.SlugToText(params.InvitationSlug),
		},
	)
	if err != nil {
		return nil, mapMutationError(err)
	}

	return invitationFromRefreshRow(row), nil
}

// MarkAccepted records that a pending invitation created the accepted user's organization membership.
func (r *repository) MarkAccepted(
	ctx context.Context,
	tx postgres.DBTX,
	params MarkAcceptedParams,
) (*Invitation, error) {
	row, err := dbgen.New(tx).AcceptOrganizationMemberInvitation(
		ctx,
		dbgen.AcceptOrganizationMemberInvitationParams{
			AcceptedUserSlug: postgres.SlugToText(params.AcceptedUserSlug),
			AcceptedAt:       toTimestamp(params.AcceptedAt),
			OrgSlug:          postgres.SlugToText(params.OrganizationSlug),
			InvitationSlug:   postgres.SlugToText(params.InvitationSlug),
		},
	)
	if err != nil {
		return nil, mapMutationError(err)
	}

	return invitationFromAcceptRow(row), nil
}

// MarkDeclined records that an invitee declined a pending invitation.
func (r *repository) MarkDeclined(
	ctx context.Context,
	tx postgres.DBTX,
	params MarkDeclinedParams,
) (*Invitation, error) {
	row, err := dbgen.New(tx).DeclineOrganizationMemberInvitation(
		ctx,
		dbgen.DeclineOrganizationMemberInvitationParams{
			DeclinedAt:     toTimestamp(params.DeclinedAt),
			OrgSlug:        postgres.SlugToText(params.OrganizationSlug),
			InvitationSlug: postgres.SlugToText(params.InvitationSlug),
		},
	)
	if err != nil {
		return nil, mapMutationError(err)
	}

	return invitationFromDeclineRow(row), nil
}

// MarkRevoked records that an admin revoked a pending invitation.
func (r *repository) MarkRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	params MarkRevokedParams,
) (*Invitation, error) {
	row, err := dbgen.New(tx).RevokeOrganizationMemberInvitation(
		ctx,
		dbgen.RevokeOrganizationMemberInvitationParams{
			RevokedAt:      toTimestamp(params.RevokedAt),
			OrgSlug:        postgres.SlugToText(params.OrganizationSlug),
			InvitationSlug: postgres.SlugToText(params.InvitationSlug),
		},
	)
	if err != nil {
		return nil, mapMutationError(err)
	}

	return invitationFromRevokeRow(row), nil
}

// MarkExpired records lazy expiration for a pending invitation once its expiration time has passed.
func (r *repository) MarkExpired(
	ctx context.Context,
	tx postgres.DBTX,
	params MarkExpiredParams,
) (*Invitation, error) {
	row, err := dbgen.New(tx).ExpireOrganizationMemberInvitation(
		ctx,
		dbgen.ExpireOrganizationMemberInvitationParams{
			ExpiredAt:      toTimestamp(params.ExpiredAt),
			OrgSlug:        postgres.SlugToText(params.OrganizationSlug),
			InvitationSlug: postgres.SlugToText(params.InvitationSlug),
		},
	)
	if err != nil {
		return nil, mapMutationError(err)
	}

	return invitationFromExpireRow(row), nil
}

func mapCreateError(err error) error {
	switch {
	case postgres.IsUniqueViolation(err, "idx_organization_member_invitations_pending_email"):
		return ErrAlreadyPending
	case postgres.IsUniqueViolation(err, "idx_organization_member_invitations_token_hash"):
		return ErrTokenConflict
	case postgres.IsUniqueViolation(err, "uq_organization_member_invitation_slug"):
		return ErrAlreadyPending
	default:
		return xerror.Unexpected().With(err)
	}
}

func mapReadError(err error) error {
	if xerror.Is(err, sql.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return xerror.Unexpected().With(err)
}

func mapMutationError(err error) error {
	if xerror.Is(err, sql.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	if postgres.IsUniqueViolation(err, "idx_organization_member_invitations_token_hash") {
		return ErrTokenConflict.With(err)
	}
	return xerror.Unexpected().With(err)
}

func toTimestamp(t time.Time) pgtype.Timestamp {
	return pgtype.Timestamp{Time: t, Valid: true}
}

func nullableTime(t pgtype.Timestamp) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func nullableSlug(s pgtype.Text) *slug.Slug {
	if !s.Valid || s.String == "" {
		return nil
	}
	value := slug.Slug(s.String)
	return &value
}

type dbInvitation struct {
	Slug       slug.Slug
	Email      string
	Status     string
	ExpiresAt  pgtype.Timestamp
	LastSentAt pgtype.Timestamp
	AcceptedAt pgtype.Timestamp
	DeclinedAt pgtype.Timestamp
	RevokedAt  pgtype.Timestamp
	ExpiredAt  pgtype.Timestamp
	CreatedAt  pgtype.Timestamp
	UpdatedAt  pgtype.Timestamp
}

func invitationFromFields(
	row dbInvitation,
	organizationSlug slug.Slug,
	roleSlug slug.Slug,
	invitedByUserSlug pgtype.Text,
	acceptedUserSlug pgtype.Text,
) *Invitation {
	return &Invitation{
		Slug:              row.Slug,
		OrganizationSlug:  organizationSlug,
		Email:             row.Email,
		RoleSlug:          roleSlug,
		Status:            memberInvitationStatusFromDB(row.Status),
		InvitedByUserSlug: nullableSlug(invitedByUserSlug),
		AcceptedUserSlug:  nullableSlug(acceptedUserSlug),
		ExpiresAt:         row.ExpiresAt.Time,
		LastSentAt:        row.LastSentAt.Time,
		AcceptedAt:        nullableTime(row.AcceptedAt),
		DeclinedAt:        nullableTime(row.DeclinedAt),
		RevokedAt:         nullableTime(row.RevokedAt),
		ExpiredAt:         nullableTime(row.ExpiredAt),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}
}

func memberInvitationStatusFromDB(value string) model.MemberInvitationStatus {
	status, err := model.MemberInvitationStatusString(value)
	if err != nil {
		return model.MemberInvitationStatusUnknown
	}
	return status
}

func invitationFromCreateRow(row dbgen.CreateOrganizationMemberInvitationRow) *Invitation {
	return invitationFromFields(dbInvitationFromCreateRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromFindRow(row dbgen.FindOrganizationMemberInvitationRow) *Invitation {
	return invitationFromFields(dbInvitationFromFindRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromTokenRow(row dbgen.FindOrganizationMemberInvitationByTokenHashRow) *Invitation {
	return invitationFromFields(dbInvitationFromTokenRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromPendingEmailRow(row dbgen.FindPendingOrganizationMemberInvitationByEmailRow) *Invitation {
	return invitationFromFields(dbInvitationFromPendingEmailRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromListRow(row dbgen.ListOrganizationMemberInvitationsRow) *Invitation {
	return invitationFromFields(dbInvitationFromListRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromPendingEmailListRow(row dbgen.ListPendingOrganizationMemberInvitationsByEmailRow) *Invitation {
	return invitationFromFields(dbInvitationFromPendingEmailListRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromExpirePendingRow(row dbgen.ExpirePendingOrganizationMemberInvitationsRow) *Invitation {
	return invitationFromFields(dbInvitationFromExpirePendingRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromRefreshRow(row dbgen.RefreshOrganizationMemberInvitationTokenRow) *Invitation {
	return invitationFromFields(dbInvitationFromRefreshRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromAcceptRow(row dbgen.AcceptOrganizationMemberInvitationRow) *Invitation {
	return invitationFromFields(dbInvitationFromAcceptRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromDeclineRow(row dbgen.DeclineOrganizationMemberInvitationRow) *Invitation {
	return invitationFromFields(dbInvitationFromDeclineRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromRevokeRow(row dbgen.RevokeOrganizationMemberInvitationRow) *Invitation {
	return invitationFromFields(dbInvitationFromRevokeRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func invitationFromExpireRow(row dbgen.ExpireOrganizationMemberInvitationRow) *Invitation {
	return invitationFromFields(dbInvitationFromExpireRow(row), row.OrganizationSlug, row.RoleSlug, row.InvitedByUserSlug, row.AcceptedUserSlug)
}

func dbInvitationFromCreateRow(row dbgen.CreateOrganizationMemberInvitationRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromFindRow(row dbgen.FindOrganizationMemberInvitationRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromTokenRow(row dbgen.FindOrganizationMemberInvitationByTokenHashRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromPendingEmailRow(row dbgen.FindPendingOrganizationMemberInvitationByEmailRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromListRow(row dbgen.ListOrganizationMemberInvitationsRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromPendingEmailListRow(row dbgen.ListPendingOrganizationMemberInvitationsByEmailRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromExpirePendingRow(row dbgen.ExpirePendingOrganizationMemberInvitationsRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromRefreshRow(row dbgen.RefreshOrganizationMemberInvitationTokenRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromAcceptRow(row dbgen.AcceptOrganizationMemberInvitationRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromDeclineRow(row dbgen.DeclineOrganizationMemberInvitationRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromRevokeRow(row dbgen.RevokeOrganizationMemberInvitationRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func dbInvitationFromExpireRow(row dbgen.ExpireOrganizationMemberInvitationRow) dbInvitation {
	return dbInvitation{
		Slug:       row.Slug,
		Email:      row.Email,
		Status:     row.Status,
		ExpiresAt:  row.ExpiresAt,
		LastSentAt: row.LastSentAt,
		AcceptedAt: row.AcceptedAt,
		DeclinedAt: row.DeclinedAt,
		RevokedAt:  row.RevokedAt,
		ExpiredAt:  row.ExpiredAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}
