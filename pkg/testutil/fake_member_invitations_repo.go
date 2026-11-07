package testutil

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
)

var errFakeMemberInvitationsRepoFnNotSet = xerror.Unexpected().Detail(
	"fake member invitations repo function is not set",
)

// FakeMemberInvitationsRepo is a test double for memberinvitations.Repository.
type FakeMemberInvitationsRepo struct {
	CreateFn                      func(ctx context.Context, tx postgres.DBTX, params memberinvitations.CreateParams) (*memberinvitations.Invitation, error)
	FindFn                        func(ctx context.Context, tx postgres.DBTX, orgSlug, invitationSlug slug.Slug) (*memberinvitations.Invitation, error)
	FindByTokenHashFn             func(ctx context.Context, tx postgres.DBTX, tokenHash string) (*memberinvitations.Invitation, error)
	FindPendingByEmailFn          func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, email string) (*memberinvitations.Invitation, error)
	ListByOrganizationFn          func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListRequest) ([]*memberinvitations.Invitation, error)
	ExpirePendingByOrganizationFn func(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		expiredAt time.Time,
	) ([]*memberinvitations.Invitation, error)
	ListPendingByEmailFn func(ctx context.Context, tx postgres.DBTX, email string, asOf time.Time) ([]*memberinvitations.Invitation, error)
	RefreshTokenFn       func(ctx context.Context, tx postgres.DBTX, params memberinvitations.RefreshTokenParams) (*memberinvitations.Invitation, error)
	MarkAcceptedFn       func(ctx context.Context, tx postgres.DBTX, params memberinvitations.MarkAcceptedParams) (*memberinvitations.Invitation, error)
	MarkDeclinedFn       func(ctx context.Context, tx postgres.DBTX, params memberinvitations.MarkDeclinedParams) (*memberinvitations.Invitation, error)
	MarkRevokedFn        func(ctx context.Context, tx postgres.DBTX, params memberinvitations.MarkRevokedParams) (*memberinvitations.Invitation, error)
	MarkExpiredFn        func(ctx context.Context, tx postgres.DBTX, params memberinvitations.MarkExpiredParams) (*memberinvitations.Invitation, error)
}

func (f *FakeMemberInvitationsRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params memberinvitations.CreateParams,
) (*memberinvitations.Invitation, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, tx, params)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) Find(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, invitationSlug slug.Slug,
) (*memberinvitations.Invitation, error) {
	if f.FindFn != nil {
		return f.FindFn(ctx, tx, orgSlug, invitationSlug)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) FindByTokenHash(
	ctx context.Context,
	tx postgres.DBTX,
	tokenHash string,
) (*memberinvitations.Invitation, error) {
	if f.FindByTokenHashFn != nil {
		return f.FindByTokenHashFn(ctx, tx, tokenHash)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) FindPendingByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	email string,
) (*memberinvitations.Invitation, error) {
	if f.FindPendingByEmailFn != nil {
		return f.FindPendingByEmailFn(ctx, tx, orgSlug, email)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*memberinvitations.Invitation, error) {
	if f.ListByOrganizationFn != nil {
		return f.ListByOrganizationFn(ctx, tx, orgSlug, req)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) ExpirePendingByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	expiredAt time.Time,
) ([]*memberinvitations.Invitation, error) {
	if f.ExpirePendingByOrganizationFn != nil {
		return f.ExpirePendingByOrganizationFn(ctx, tx, orgSlug, expiredAt)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) ListPendingByEmail(
	ctx context.Context,
	tx postgres.DBTX,
	email string,
	asOf time.Time,
) ([]*memberinvitations.Invitation, error) {
	if f.ListPendingByEmailFn != nil {
		return f.ListPendingByEmailFn(ctx, tx, email, asOf)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) RefreshToken(
	ctx context.Context,
	tx postgres.DBTX,
	params memberinvitations.RefreshTokenParams,
) (*memberinvitations.Invitation, error) {
	if f.RefreshTokenFn != nil {
		return f.RefreshTokenFn(ctx, tx, params)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) MarkAccepted(
	ctx context.Context,
	tx postgres.DBTX,
	params memberinvitations.MarkAcceptedParams,
) (*memberinvitations.Invitation, error) {
	if f.MarkAcceptedFn != nil {
		return f.MarkAcceptedFn(ctx, tx, params)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) MarkDeclined(
	ctx context.Context,
	tx postgres.DBTX,
	params memberinvitations.MarkDeclinedParams,
) (*memberinvitations.Invitation, error) {
	if f.MarkDeclinedFn != nil {
		return f.MarkDeclinedFn(ctx, tx, params)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) MarkRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	params memberinvitations.MarkRevokedParams,
) (*memberinvitations.Invitation, error) {
	if f.MarkRevokedFn != nil {
		return f.MarkRevokedFn(ctx, tx, params)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}

func (f *FakeMemberInvitationsRepo) MarkExpired(
	ctx context.Context,
	tx postgres.DBTX,
	params memberinvitations.MarkExpiredParams,
) (*memberinvitations.Invitation, error) {
	if f.MarkExpiredFn != nil {
		return f.MarkExpiredFn(ctx, tx, params)
	}
	return nil, errFakeMemberInvitationsRepoFnNotSet
}
