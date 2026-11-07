package memberssvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

const (
	invitationTokenLength = 32
	invitationExpiry      = 7 * 24 * time.Hour
)

// ListMemberInvitations returns paginated invitations for one organization.
func (s *service) ListMemberInvitations(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.MemberInvitation], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.MemberInvitation
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		expired, err := s.memberInvitationsRepo.ExpirePendingByOrganization(ctx, tx, orgSlug, s.clock.Now().UTC())
		if err != nil {
			return xerror.Wrapf("expire pending member invitations before list: %w", err)
		}
		for _, invitation := range expired {
			if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, model.SystemEventActor(), memberInvitationExpiredEvent{
				invitation: *toMemberInvitation(invitation),
			}); err != nil {
				return err
			}
		}

		invitations, err := s.memberInvitationsRepo.ListByOrganization(ctx, tx, orgSlug, queryReq)
		if err != nil {
			return xerror.Wrapf("list member invitations: %w", err)
		}

		ret = toMemberInvitations(invitations)
		return nil
	})
	if err != nil {
		return model.ListResponse[model.MemberInvitation]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		memberInvitationCursorParts,
	)

	return model.ListResponse[model.MemberInvitation]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// sortAndPageMemberInvitations applies the shared cursor semantics to an in-memory invitation set.
func sortAndPageMemberInvitations(
	invitations []model.MemberInvitation,
	req model.ListRequest,
) ([]model.MemberInvitation, error) {
	sort.SliceStable(invitations, func(i, j int) bool {
		cmp := compareMemberInvitations(invitations[i], invitations[j], req.SortBy)
		if req.Order == model.ListOrderAsc {
			return cmp < 0
		}
		return cmp > 0
	})

	if req.Cursor != "" {
		var err error
		invitations, err = filterMemberInvitationsAfterCursor(invitations, req)
		if err != nil {
			return nil, err
		}
	}
	if len(invitations) > req.Limit {
		return invitations[:req.Limit], nil
	}
	return invitations, nil
}

// filterMemberInvitationsAfterCursor keeps rows on the requested side of a shared list cursor.
func filterMemberInvitationsAfterCursor(
	invitations []model.MemberInvitation,
	req model.ListRequest,
) ([]model.MemberInvitation, error) {
	cursorValue, cursorSlug, err := parseMemberInvitationCursor(req.Cursor, req.SortBy)
	if err != nil {
		return nil, err
	}

	filtered := make([]model.MemberInvitation, 0, len(invitations))
	for _, invitation := range invitations {
		cmp := compareMemberInvitationToCursor(invitation, cursorValue, req.SortBy)
		if cmp == 0 {
			cmp = strings.Compare(invitation.Slug.String(), cursorSlug)
		}
		if req.Order == model.ListOrderAsc && cmp > 0 {
			filtered = append(filtered, invitation)
		}
		if req.Order == model.ListOrderDesc && cmp < 0 {
			filtered = append(filtered, invitation)
		}
	}

	return filtered, nil
}

type parsedMemberInvitationCursor struct {
	text      string
	timestamp time.Time
}

// parseMemberInvitationCursor separates and validates the shared "value_slug" cursor shape.
func parseMemberInvitationCursor(cursor, sortBy string) (parsedMemberInvitationCursor, string, error) {
	idx := strings.LastIndex(cursor, "_")
	if idx <= 0 || idx == len(cursor)-1 {
		return parsedMemberInvitationCursor{}, "", xerror.InvalidInput(xerror.Cursor).
			Detail("cursor must contain both a value and slug")
	}
	value := cursor[:idx]
	cursorSlug := cursor[idx+1:]
	parsed := parsedMemberInvitationCursor{text: value}
	switch sortBy {
	case model.ListSortCreatedAt, model.ListSortUpdatedAt, model.ListSortExpiresAt:
		timestamp, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return parsedMemberInvitationCursor{}, "", xerror.InvalidInput(xerror.Cursor).
				Detailf("cursor timestamp is invalid: %w", err)
		}
		parsed.timestamp = timestamp
	}

	return parsed, cursorSlug, nil
}

// compareMemberInvitationToCursor compares one invitation with a typed cursor value.
func compareMemberInvitationToCursor(
	invitation model.MemberInvitation,
	cursor parsedMemberInvitationCursor,
	sortBy string,
) int {
	switch sortBy {
	case model.ListSortUpdatedAt:
		return compareTimes(invitation.UpdatedAt, cursor.timestamp)
	case model.ListSortEmail:
		return strings.Compare(invitation.Email, cursor.text)
	case model.ListSortExpiresAt:
		return compareTimes(invitation.ExpiresAt, cursor.timestamp)
	default:
		return compareTimes(invitation.CreatedAt, cursor.timestamp)
	}
}

// compareMemberInvitations orders invitations by the requested list field and slug tie-breaker.
func compareMemberInvitations(a, b model.MemberInvitation, sortBy string) int {
	switch sortBy {
	case model.ListSortUpdatedAt:
		if cmp := compareTimes(a.UpdatedAt, b.UpdatedAt); cmp != 0 {
			return cmp
		}
	case model.ListSortEmail:
		if cmp := strings.Compare(a.Email, b.Email); cmp != 0 {
			return cmp
		}
	case model.ListSortExpiresAt:
		if cmp := compareTimes(a.ExpiresAt, b.ExpiresAt); cmp != 0 {
			return cmp
		}
	default:
		if cmp := compareTimes(a.CreatedAt, b.CreatedAt); cmp != 0 {
			return cmp
		}
	}
	return strings.Compare(a.Slug.String(), b.Slug.String())
}

// compareTimes returns standard integer ordering for two timestamps.
func compareTimes(a, b time.Time) int {
	if a.Before(b) {
		return -1
	}
	if a.After(b) {
		return 1
	}
	return 0
}

// memberInvitationCursorParts builds stable cursor values for invitation list pagination.
func memberInvitationCursorParts(item model.MemberInvitation, sortBy string) (string, string) {
	cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
	switch sortBy {
	case model.ListSortUpdatedAt:
		cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
	case model.ListSortEmail:
		cursorValue = item.Email
	case model.ListSortExpiresAt:
		cursorValue = item.ExpiresAt.Format(time.RFC3339Nano)
	}
	return cursorValue, item.Slug.String()
}

// ListMyMemberInvitations returns pending invitations for the authenticated user's verified email.
func (s *service) ListMyMemberInvitations(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.MemberInvitation], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.MemberInvitation
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		user, err := s.usersService.GetUserBySlug(ctx, userSlug)
		if err != nil {
			return xerror.Wrapf("get invitation user: %w", err)
		}
		if user.VerifiedAt.IsZero() {
			return xerror.EmailNotVerified()
		}

		invitations, err := s.memberInvitationsRepo.ListPendingByEmail(
			ctx,
			tx,
			normalizeInvitationEmail(user.Email),
			s.clock.Now().UTC(),
		)
		if err != nil {
			return xerror.Wrapf("list my member invitations: %w", err)
		}

		ret, err = sortAndPageMemberInvitations(toMemberInvitations(invitations), queryReq)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.MemberInvitation]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		memberInvitationCursorParts,
	)

	return model.ListResponse[model.MemberInvitation]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// GetMemberInvitation returns one organization invitation without exposing token material.
func (s *service) GetMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
) (*model.MemberInvitation, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitation, error) {
		invitation, err := s.memberInvitationsRepo.Find(ctx, tx, orgSlug, invitationSlug)
		if err != nil {
			return nil, xerror.Wrapf("find member invitation: %w", err)
		}

		invitation, err = s.expireInvitationIfNeeded(ctx, tx, invitation)
		if err != nil {
			return nil, err
		}

		return toMemberInvitation(invitation), nil
	})
}

// CreateMemberInvitation stores a pending invitation and queues its acceptance email atomically.
func (s *service) CreateMemberInvitation(
	ctx context.Context,
	orgSlug slug.Slug,
	inviterUserSlug slug.Slug,
	actor model.EventActor,
	newInvitation model.NewMemberInvitation,
) (*model.MemberInvitation, error) {
	normalizedEmail := normalizeInvitationEmail(newInvitation.Email)
	newInvitation.Email = normalizedEmail
	if err := validate.Default().Struct(newInvitation); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitation, error) {
		now := s.clock.Now().UTC()

		org, err := s.orgsRepo.FindBySlug(ctx, tx, orgSlug)
		if err != nil {
			return nil, xerror.Wrapf("find organization: %w", err)
		}

		var inviter *model.User
		var invitedByUserSlug *slug.Slug
		if inviterUserSlug != "" {
			loadedInviter, err := s.usersService.GetUserBySlug(ctx, inviterUserSlug)
			if err != nil {
				return nil, xerror.Wrapf("get inviter user: %w", err)
			}
			inviter = loadedInviter
			invitedByUserSlug = &inviterUserSlug
		}

		if _, err := s.rolesService.GetRole(ctx, orgSlug, newInvitation.Role); err != nil {
			return nil, ErrInvalidRole.With(err)
		}

		if _, err := s.membersRepo.FindByEmail(ctx, tx, orgSlug, normalizedEmail); err == nil {
			return nil, ErrMemberAlreadyExists
		} else if !xerror.Is(err, members.ErrNotFound) {
			return nil, xerror.Wrapf("find member by email: %w", err)
		}

		if err := s.expireExistingPendingEmailInvitation(ctx, tx, orgSlug, normalizedEmail, actor, now); err != nil {
			return nil, err
		}

		invitationSlug, err := generateInvitationSlug(normalizedEmail)
		if err != nil {
			return nil, err
		}
		token, tokenHash, err := generateInvitationToken()
		if err != nil {
			return nil, err
		}
		expiresAt := now.Add(invitationExpiry)

		invitation, err := s.memberInvitationsRepo.Create(ctx, tx, memberinvitations.CreateParams{
			OrganizationSlug:  orgSlug,
			InvitationSlug:    invitationSlug,
			Email:             normalizedEmail,
			RoleSlug:          newInvitation.Role,
			InvitedByUserSlug: invitedByUserSlug,
			TokenHash:         tokenHash,
			ExpiresAt:         expiresAt,
		})
		if err != nil {
			return nil, xerror.Wrapf("create member invitation: %w", err)
		}

		email := s.memberInvitationEmail(*org, inviter, actor, *invitation)
		if _, err := s.emailProducer.EnqueueMemberInvitation(ctx, tx, email, s.memberInvitationAcceptanceURL(), token); err != nil {
			return nil, xerror.Wrapf("enqueue member invitation email: %w", err)
		}

		ret := toMemberInvitation(invitation)
		if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberInvitationCreatedEvent{
			invitation: *ret,
		}); err != nil {
			return nil, err
		}
		return ret, nil
	})
}

// ResendMemberInvitation rotates the token for a pending invitation and queues a new email.
func (s *service) ResendMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	var ret *model.MemberInvitation
	var expired bool
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		now := s.clock.Now().UTC()
		_, org, inviter, err := s.pendingInvitationEmailContext(ctx, tx, orgSlug, invitationSlug, now)
		if err != nil {
			if !xerror.Is(err, ErrInvitationExpired) {
				return err
			}

			expiredInvitation, expireErr := s.memberInvitationsRepo.MarkExpired(ctx, tx, memberinvitations.MarkExpiredParams{
				OrganizationSlug: orgSlug,
				InvitationSlug:   invitationSlug,
				ExpiredAt:        now,
			})
			if expireErr != nil {
				return xerror.Wrapf("expire member invitation before resend: %w", expireErr)
			}
			ret = toMemberInvitation(expiredInvitation)
			if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberInvitationExpiredEvent{
				invitation: *ret,
			}); err != nil {
				return err
			}
			expired = true
			return nil
		}

		token, tokenHash, err := generateInvitationToken()
		if err != nil {
			return err
		}
		expiresAt := now.Add(invitationExpiry)

		refreshed, err := s.memberInvitationsRepo.RefreshToken(ctx, tx, memberinvitations.RefreshTokenParams{
			OrganizationSlug: orgSlug,
			InvitationSlug:   invitationSlug,
			TokenHash:        tokenHash,
			ExpiresAt:        expiresAt,
			LastSentAt:       now,
		})
		if err != nil {
			return xerror.Wrapf("refresh member invitation token: %w", err)
		}

		email := s.memberInvitationEmail(*org, inviter, actor, *refreshed)
		if _, err := s.emailProducer.EnqueueMemberInvitation(ctx, tx, email, s.memberInvitationAcceptanceURL(), token); err != nil {
			return xerror.Wrapf("enqueue resent member invitation email: %w", err)
		}

		ret = toMemberInvitation(refreshed)
		if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberInvitationResentEvent{
			invitation: *ret,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return ret, ErrInvitationExpired
	}
	return ret, nil
}

// RevokeMemberInvitation marks a pending invitation revoked so existing links cannot be accepted.
func (s *service) RevokeMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitation, error) {
		now := s.clock.Now().UTC()
		invitation, err := s.memberInvitationsRepo.Find(ctx, tx, orgSlug, invitationSlug)
		if err != nil {
			return nil, xerror.Wrapf("find member invitation: %w", err)
		}
		if invitation.Status != model.MemberInvitationStatusPending {
			return nil, ErrInvitationNotPending
		}
		if !now.Before(invitation.ExpiresAt) {
			expired, err := s.memberInvitationsRepo.MarkExpired(ctx, tx, memberinvitations.MarkExpiredParams{
				OrganizationSlug: orgSlug,
				InvitationSlug:   invitationSlug,
				ExpiredAt:        now,
			})
			if err != nil {
				return nil, xerror.Wrapf("expire member invitation before revoke: %w", err)
			}
			ret := toMemberInvitation(expired)
			if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberInvitationExpiredEvent{
				invitation: *ret,
			}); err != nil {
				return nil, err
			}
			return ret, nil
		}

		revoked, err := s.memberInvitationsRepo.MarkRevoked(ctx, tx, memberinvitations.MarkRevokedParams{
			OrganizationSlug: orgSlug,
			InvitationSlug:   invitationSlug,
			RevokedAt:        now,
		})
		if err != nil {
			return nil, xerror.Wrapf("revoke member invitation: %w", err)
		}

		ret := toMemberInvitation(revoked)
		if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberInvitationRevokedEvent{
			invitation: *ret,
		}); err != nil {
			return nil, err
		}
		return ret, nil
	})
}

// AcceptMemberInvitation accepts a pending invitation for the authenticated verified user.
func (s *service) AcceptMemberInvitation(
	ctx context.Context,
	userSlug slug.Slug,
	token string,
	actor model.EventActor,
) (*model.MemberInvitationAcceptance, error) {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return nil, xerror.ValidationFailed().Detail("invitation token is required")
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitationAcceptance, error) {
		now := s.clock.Now().UTC()
		invitation, err := s.memberInvitationsRepo.FindByTokenHash(ctx, tx, hashInvitationToken(trimmedToken))
		if err != nil {
			if xerror.Is(err, memberinvitations.ErrNotFound) {
				return nil, ErrInvitationNotFound.With(err)
			}
			return nil, xerror.Wrapf("find member invitation by token hash: %w", err)
		}

		user, err := s.usersService.GetUserBySlug(ctx, userSlug)
		if err != nil {
			return nil, xerror.Wrapf("get invitation accepting user: %w", err)
		}

		return s.acceptInvitationForVerifiedUser(ctx, tx, invitation, *user, actor, now)
	})
}

// AcceptMyMemberInvitation accepts a pending invitation selected from the user's invitation list.
func (s *service) AcceptMyMemberInvitation(
	ctx context.Context,
	userSlug, orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitationAcceptance, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitationAcceptance, error) {
		now := s.clock.Now().UTC()
		invitation, err := s.memberInvitationsRepo.Find(ctx, tx, orgSlug, invitationSlug)
		if err != nil {
			if xerror.Is(err, memberinvitations.ErrNotFound) {
				return nil, ErrInvitationNotFound.With(err)
			}
			return nil, xerror.Wrapf("find listed member invitation: %w", err)
		}

		user, err := s.usersService.GetUserBySlug(ctx, userSlug)
		if err != nil {
			return nil, xerror.Wrapf("get invitation accepting user: %w", err)
		}

		return s.acceptInvitationForVerifiedUser(ctx, tx, invitation, *user, actor, now)
	})
}

// DeclineMemberInvitation records an authenticated user's decision to decline a pending invitation.
func (s *service) DeclineMemberInvitation(
	ctx context.Context,
	userSlug slug.Slug,
	token string,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return nil, xerror.ValidationFailed().Detail("invitation token is required")
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitation, error) {
		now := s.clock.Now().UTC()
		invitation, err := s.memberInvitationsRepo.FindByTokenHash(ctx, tx, hashInvitationToken(trimmedToken))
		if err != nil {
			if xerror.Is(err, memberinvitations.ErrNotFound) {
				return nil, ErrInvitationNotFound.With(err)
			}
			return nil, xerror.Wrapf("find member invitation by token hash: %w", err)
		}

		user, err := s.usersService.GetUserBySlug(ctx, userSlug)
		if err != nil {
			return nil, xerror.Wrapf("get invitation declining user: %w", err)
		}
		if err := validateInvitationUser(*invitation, *user); err != nil {
			return nil, err
		}

		return s.declineInvitationForVerifiedUser(ctx, tx, invitation, actor, now)
	})
}

// DeclineMyMemberInvitation declines a pending invitation selected from the user's invitation list.
func (s *service) DeclineMyMemberInvitation(
	ctx context.Context,
	userSlug, orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.MemberInvitation, error) {
		now := s.clock.Now().UTC()
		invitation, err := s.memberInvitationsRepo.Find(ctx, tx, orgSlug, invitationSlug)
		if err != nil {
			if xerror.Is(err, memberinvitations.ErrNotFound) {
				return nil, ErrInvitationNotFound.With(err)
			}
			return nil, xerror.Wrapf("find listed member invitation: %w", err)
		}

		user, err := s.usersService.GetUserBySlug(ctx, userSlug)
		if err != nil {
			return nil, xerror.Wrapf("get invitation declining user: %w", err)
		}
		if err := validateInvitationUser(*invitation, *user); err != nil {
			return nil, err
		}

		return s.declineInvitationForVerifiedUser(ctx, tx, invitation, actor, now)
	})
}

// acceptInvitationForVerifiedUser creates or reuses membership and marks an invitation accepted.
func (s *service) acceptInvitationForVerifiedUser(
	ctx context.Context,
	tx postgres.DBTX,
	invitation *memberinvitations.Invitation,
	user model.User,
	actor model.EventActor,
	now time.Time,
) (*model.MemberInvitationAcceptance, error) {
	if err := validateInvitationUser(*invitation, user); err != nil {
		return nil, err
	}

	if invitation.Status == model.MemberInvitationStatusAccepted {
		if invitation.AcceptedUserSlug == nil || *invitation.AcceptedUserSlug != user.Slug {
			return nil, ErrInvitationNotFound
		}
		member, err := s.membersRepo.Find(ctx, tx, invitation.OrganizationSlug, user.Slug)
		if err != nil {
			return nil, xerror.Wrapf("find accepted invitation member: %w", err)
		}
		if s.accepted != nil {
			if err := s.accepted(ctx, tx, invitation, user, true); err != nil {
				return nil, err
			}
		}
		return &model.MemberInvitationAcceptance{
			Invitation: *toMemberInvitation(invitation),
			Member:     *toMember(member),
		}, nil
	}
	if invitation.Status != model.MemberInvitationStatusPending {
		return nil, ErrInvitationNotFound
	}
	if !now.Before(invitation.ExpiresAt) {
		expired, err := s.memberInvitationsRepo.MarkExpired(ctx, tx, memberinvitations.MarkExpiredParams{
			OrganizationSlug: invitation.OrganizationSlug,
			InvitationSlug:   invitation.Slug,
			ExpiredAt:        now,
		})
		if err != nil {
			return nil, xerror.Wrapf("expire member invitation before accept: %w", err)
		}
		if err := s.enqueueRequiredAuditEvent(ctx, tx, invitation.OrganizationSlug, actor, memberInvitationExpiredEvent{
			invitation: *toMemberInvitation(expired),
		}); err != nil {
			return nil, err
		}
		return nil, ErrInvitationNotFound
	}

	member, created, err := s.ensureInvitationMember(ctx, tx, invitation, user.Slug)
	if err != nil {
		return nil, err
	}

	accepted, err := s.memberInvitationsRepo.MarkAccepted(ctx, tx, memberinvitations.MarkAcceptedParams{
		OrganizationSlug: invitation.OrganizationSlug,
		InvitationSlug:   invitation.Slug,
		AcceptedUserSlug: user.Slug,
		AcceptedAt:       now,
	})
	if err != nil {
		return nil, xerror.Wrapf("accept member invitation: %w", err)
	}
	if s.accepted != nil {
		if err := s.accepted(ctx, tx, accepted, user, false); err != nil {
			return nil, err
		}
	}
	if created {
		if err := s.enqueueRequiredAuditEvent(
			ctx,
			tx,
			invitation.OrganizationSlug,
			actor,
			memberCreatedEvent{member: *toMember(member)},
		); err != nil {
			return nil, err
		}
	}
	if err := s.enqueueRequiredAuditEvent(ctx, tx, invitation.OrganizationSlug, actor, memberInvitationAcceptedEvent{
		invitation: *toMemberInvitation(accepted),
	}); err != nil {
		return nil, err
	}

	return &model.MemberInvitationAcceptance{
		Invitation: *toMemberInvitation(accepted),
		Member:     *toMember(member),
	}, nil
}

// declineInvitationForVerifiedUser marks a verified user's own pending invitation declined.
func (s *service) declineInvitationForVerifiedUser(
	ctx context.Context,
	tx postgres.DBTX,
	invitation *memberinvitations.Invitation,
	actor model.EventActor,
	now time.Time,
) (*model.MemberInvitation, error) {
	if invitation.Status == model.MemberInvitationStatusDeclined {
		return toMemberInvitation(invitation), nil
	}
	if invitation.Status != model.MemberInvitationStatusPending {
		return nil, ErrInvitationNotFound
	}
	if !now.Before(invitation.ExpiresAt) {
		expired, err := s.memberInvitationsRepo.MarkExpired(ctx, tx, memberinvitations.MarkExpiredParams{
			OrganizationSlug: invitation.OrganizationSlug,
			InvitationSlug:   invitation.Slug,
			ExpiredAt:        now,
		})
		if err != nil {
			return nil, xerror.Wrapf("expire member invitation before decline: %w", err)
		}
		if err := s.enqueueRequiredAuditEvent(ctx, tx, invitation.OrganizationSlug, actor, memberInvitationExpiredEvent{
			invitation: *toMemberInvitation(expired),
		}); err != nil {
			return nil, err
		}
		return nil, ErrInvitationNotFound
	}

	declined, err := s.memberInvitationsRepo.MarkDeclined(ctx, tx, memberinvitations.MarkDeclinedParams{
		OrganizationSlug: invitation.OrganizationSlug,
		InvitationSlug:   invitation.Slug,
		DeclinedAt:       now,
	})
	if err != nil {
		return nil, xerror.Wrapf("decline member invitation: %w", err)
	}

	ret := toMemberInvitation(declined)
	if err := s.enqueueRequiredAuditEvent(ctx, tx, invitation.OrganizationSlug, actor, memberInvitationDeclinedEvent{
		invitation: *ret,
	}); err != nil {
		return nil, err
	}
	return ret, nil
}

// ensureInvitationMember creates a membership or reuses an existing one for idempotent acceptance.
func (s *service) ensureInvitationMember(
	ctx context.Context,
	tx postgres.DBTX,
	invitation *memberinvitations.Invitation,
	userSlug slug.Slug,
) (*members.Member, bool, error) {
	existing, err := s.membersRepo.Find(ctx, tx, invitation.OrganizationSlug, userSlug)
	if err == nil {
		if existing.RoleSlug != invitation.RoleSlug {
			return nil, false, ErrInvitationNotFound
		}
		return existing, false, nil
	}
	if !xerror.Is(err, members.ErrNotFound) {
		return nil, false, xerror.Wrapf("find invitation member: %w", err)
	}

	member, err := s.membersRepo.Create(ctx, tx, &members.CreateParams{
		OrganizationSlug: invitation.OrganizationSlug,
		UserSlug:         userSlug,
		RoleSlug:         invitation.RoleSlug,
	})
	if err != nil {
		if xerror.Is(err, members.ErrAlreadyMember) {
			existing, findErr := s.membersRepo.Find(ctx, tx, invitation.OrganizationSlug, userSlug)
			if findErr != nil {
				return nil, false, xerror.Wrapf("find concurrently created invitation member: %w", findErr)
			}
			if existing.RoleSlug != invitation.RoleSlug {
				return nil, false, ErrInvitationNotFound.With(err)
			}
			return existing, false, nil
		}
		return nil, false, xerror.Wrapf("create invitation member: %w", err)
	}
	member.UserEmail = invitation.Email

	return member, true, nil
}

// validateInvitationUser ensures the acting user owns the invitation email before state changes.
func validateInvitationUser(invitation memberinvitations.Invitation, user model.User) error {
	if normalizeInvitationEmail(user.Email) != invitation.Email {
		return ErrInvitationNotFound
	}
	if user.VerifiedAt.IsZero() {
		return xerror.EmailNotVerified()
	}
	return nil
}

// pendingInvitationEmailContext loads and validates the data needed to resend an invitation.
func (s *service) pendingInvitationEmailContext(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, invitationSlug slug.Slug,
	now time.Time,
) (*memberinvitations.Invitation, *organizations.Organization, *model.User, error) {
	invitation, err := s.memberInvitationsRepo.Find(ctx, tx, orgSlug, invitationSlug)
	if err != nil {
		return nil, nil, nil, xerror.Wrapf("find member invitation: %w", err)
	}
	if invitation.Status != model.MemberInvitationStatusPending {
		return nil, nil, nil, ErrInvitationNotPending
	}
	if !now.Before(invitation.ExpiresAt) {
		return nil, nil, nil, ErrInvitationExpired
	}

	org, err := s.orgsRepo.FindBySlug(ctx, tx, orgSlug)
	if err != nil {
		return nil, nil, nil, xerror.Wrapf("find organization: %w", err)
	}
	if invitation.InvitedByUserSlug == nil {
		return nil, nil, nil, xerror.Unexpected().Detail("member invitation inviter is missing")
	}
	inviter, err := s.usersService.GetUserBySlug(ctx, *invitation.InvitedByUserSlug)
	if err != nil {
		return nil, nil, nil, xerror.Wrapf("get invitation inviter: %w", err)
	}

	return invitation, org, inviter, nil
}

// expireExistingPendingEmailInvitation blocks duplicates unless the previous pending invite is expired.
func (s *service) expireExistingPendingEmailInvitation(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	email string,
	actor model.EventActor,
	now time.Time,
) error {
	existing, err := s.memberInvitationsRepo.FindPendingByEmail(ctx, tx, orgSlug, email)
	if err != nil {
		if xerror.Is(err, memberinvitations.ErrNotFound) {
			return nil
		}
		return xerror.Wrapf("find pending member invitation by email: %w", err)
	}

	if now.Before(existing.ExpiresAt) {
		return ErrInvitationPending
	}

	expired, err := s.memberInvitationsRepo.MarkExpired(ctx, tx, memberinvitations.MarkExpiredParams{
		OrganizationSlug: orgSlug,
		InvitationSlug:   existing.Slug,
		ExpiredAt:        now,
	})
	if err != nil {
		return xerror.Wrapf("expire existing member invitation: %w", err)
	}
	if err := s.enqueueRequiredAuditEvent(ctx, tx, orgSlug, actor, memberInvitationExpiredEvent{
		invitation: *toMemberInvitation(expired),
	}); err != nil {
		return err
	}
	return nil
}

// expireInvitationIfNeeded lazily persists expiration before returning a pending invitation.
func (s *service) expireInvitationIfNeeded(
	ctx context.Context,
	tx postgres.DBTX,
	invitation *memberinvitations.Invitation,
) (*memberinvitations.Invitation, error) {
	now := s.clock.Now().UTC()
	if invitation.Status != model.MemberInvitationStatusPending || now.Before(invitation.ExpiresAt) {
		return invitation, nil
	}

	expired, err := s.memberInvitationsRepo.MarkExpired(ctx, tx, memberinvitations.MarkExpiredParams{
		OrganizationSlug: invitation.OrganizationSlug,
		InvitationSlug:   invitation.Slug,
		ExpiredAt:        now,
	})
	if err != nil {
		return nil, xerror.Wrapf("expire member invitation: %w", err)
	}
	if err := s.enqueueRequiredAuditEvent(ctx, tx, invitation.OrganizationSlug, model.SystemEventActor(), memberInvitationExpiredEvent{
		invitation: *toMemberInvitation(expired),
	}); err != nil {
		return nil, err
	}

	return expired, nil
}

// memberInvitationEmail builds the non-secret email payload for a member invitation job.
func (s *service) memberInvitationEmail(
	org organizations.Organization,
	inviter *model.User,
	actor model.EventActor,
	invitation memberinvitations.Invitation,
) model.MemberInvitationEmail {
	locale := "en"
	inviterName := "Organization automation"
	inviterEmail := "no-reply@braidplane.local"
	if actor.Type == model.EventActorTypeAPIKey && actor.Slug != nil {
		inviterName = "API key " + string(*actor.Slug)
	}
	if inviter != nil {
		locale = inviter.Locale
		inviterName = inviter.FullName
		inviterEmail = inviter.Email
	}
	if strings.TrimSpace(locale) == "" {
		locale = "en"
	}
	if strings.TrimSpace(inviterName) == "" {
		inviterName = inviterEmail
	}

	return model.MemberInvitationEmail{
		Email:            invitation.Email,
		OrganizationName: org.Name,
		InviterName:      inviterName,
		InviterEmail:     inviterEmail,
		Role:             invitation.RoleSlug,
		ExpiresAt:        invitation.ExpiresAt,
		Locale:           locale,
	}
}

// memberInvitationAcceptanceURL returns the frontend endpoint that receives raw invitation tokens.
func (s *service) memberInvitationAcceptanceURL() string {
	return fmt.Sprintf("%s/member-invitations/accept", strings.TrimRight(s.frontendURL, "/"))
}

// normalizeInvitationEmail trims and lowercases invitation email addresses before persistence.
func normalizeInvitationEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// generateInvitationSlug creates a human-readable invitation slug with one random suffix.
func generateInvitationSlug(email string) (slug.Slug, error) {
	base, err := slug.FromName(email)
	if err != nil {
		return "", ErrInvalidUserSlug.With(err)
	}
	withSuffix, err := slug.WithRandomSuffix(base, 6)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return withSuffix, nil
}

// generateInvitationToken returns a raw token and the SHA-256 hash stored in the database.
func generateInvitationToken() (string, string, error) {
	tokenBytes := make([]byte, invitationTokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", xerror.Unexpected().With(err)
	}

	token := hex.EncodeToString(tokenBytes)
	return token, hashInvitationToken(token), nil
}

// hashInvitationToken creates the deterministic token hash used for lookup and rotation.
func hashInvitationToken(token string) string {
	hashBytes := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hashBytes[:])
}
