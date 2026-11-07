package memberssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type memberCreatedEvent struct {
	member model.Member
}

func (e memberCreatedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberRecord("evt-member-create", e.member.UserSlug, model.EventActionCreate, e.member.Role), nil
}

type memberUpdatedEvent struct {
	memberSlug slug.Slug
	roleSlug   slug.Slug
}

func (e memberUpdatedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberRecord("evt-member-update", e.memberSlug, model.EventActionUpdate, e.roleSlug), nil
}

type memberDeletedEvent struct {
	memberSlug slug.Slug
	roleSlug   slug.Slug
}

func (e memberDeletedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberRecord("evt-member-delete", e.memberSlug, model.EventActionDelete, e.roleSlug), nil
}

type memberInvitationCreatedEvent struct {
	invitation model.MemberInvitation
}

func (e memberInvitationCreatedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberInvitationRecord("evt-member-invitation-create", e.invitation, model.EventActionCreate), nil
}

type memberInvitationResentEvent struct {
	invitation model.MemberInvitation
}

func (e memberInvitationResentEvent) EventRecord() (eventrecorder.Record, error) {
	return memberInvitationRecord("evt-member-invitation-refresh", e.invitation, model.EventActionRefresh), nil
}

type memberInvitationRevokedEvent struct {
	invitation model.MemberInvitation
}

func (e memberInvitationRevokedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberInvitationRecord("evt-member-invitation-revoke", e.invitation, model.EventActionRevoke), nil
}

type memberInvitationAcceptedEvent struct {
	invitation model.MemberInvitation
}

func (e memberInvitationAcceptedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberInvitationRecord("evt-member-invitation-accept", e.invitation, model.EventActionAccept), nil
}

type memberInvitationDeclinedEvent struct {
	invitation model.MemberInvitation
}

func (e memberInvitationDeclinedEvent) EventRecord() (eventrecorder.Record, error) {
	return memberInvitationRecord("evt-member-invitation-decline", e.invitation, model.EventActionDecline), nil
}

type memberInvitationExpiredEvent struct {
	invitation model.MemberInvitation
}

func (e memberInvitationExpiredEvent) EventRecord() (eventrecorder.Record, error) {
	return memberInvitationRecord("evt-member-invitation-expire", e.invitation, model.EventActionExpire), nil
}

func memberRecord(
	slugPrefix slug.Slug,
	memberSlug slug.Slug,
	action model.EventAction,
	roleSlug slug.Slug,
) eventrecorder.Record {
	return eventrecorder.Record{
		SlugPrefix:    slugPrefix,
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeMember,
		EntitySlug:    memberSlug,
		Action:        action,
		Metadata: map[string]any{
			"role_slug": roleSlug.String(),
		},
	}
}

func memberInvitationRecord(
	slugPrefix slug.Slug,
	invitation model.MemberInvitation,
	action model.EventAction,
) eventrecorder.Record {
	metadata := map[string]any{
		"role_slug": invitation.Role.String(),
		"status":    invitation.Status.String(),
	}
	if invitation.InvitedByUserSlug != nil {
		metadata["invited_by_user_slug"] = invitation.InvitedByUserSlug.String()
	}
	if invitation.AcceptedUserSlug != nil {
		metadata["accepted_user_slug"] = invitation.AcceptedUserSlug.String()
	}

	return eventrecorder.Record{
		SlugPrefix:    slugPrefix,
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeMemberInvitation,
		EntitySlug:    invitation.Slug,
		Action:        action,
		Metadata:      metadata,
	}
}

func (s *service) enqueueRequiredAuditEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	if s.eventProducer == nil {
		return xerror.Unexpected().Detail("required membership audit event producer is unavailable")
	}
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event); err != nil {
		return xerror.Wrapf("enqueue required membership audit event: %w", err)
	}
	return nil
}
