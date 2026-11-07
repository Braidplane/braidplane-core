package memberssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
)

func toMember(m *members.Member) *model.Member {
	return &model.Member{
		UserSlug: m.UserSlug,
		Email:    m.UserEmail,
		Role:     m.RoleSlug,
		Timestamps: model.Timestamps{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
	}
}

func toMembers(ms []*members.Member) []model.Member {
	result := make([]model.Member, 0, len(ms))
	for _, m := range ms {
		result = append(result, model.Member{
			UserSlug: m.UserSlug,
			Email:    m.UserEmail,
			Role:     m.RoleSlug,
			Timestamps: model.Timestamps{
				CreatedAt: m.CreatedAt,
				UpdatedAt: m.UpdatedAt,
			},
		})
	}
	return result
}

func toMemberInvitation(invitation *memberinvitations.Invitation) *model.MemberInvitation {
	return &model.MemberInvitation{
		Slug:              invitation.Slug,
		OrganizationSlug:  invitation.OrganizationSlug,
		Email:             invitation.Email,
		Role:              invitation.RoleSlug,
		Status:            invitation.Status,
		InvitedByUserSlug: invitation.InvitedByUserSlug,
		AcceptedUserSlug:  invitation.AcceptedUserSlug,
		ExpiresAt:         invitation.ExpiresAt,
		LastSentAt:        invitation.LastSentAt,
		AcceptedAt:        invitation.AcceptedAt,
		DeclinedAt:        invitation.DeclinedAt,
		RevokedAt:         invitation.RevokedAt,
		ExpiredAt:         invitation.ExpiredAt,
		Timestamps: model.Timestamps{
			CreatedAt: invitation.CreatedAt,
			UpdatedAt: invitation.UpdatedAt,
		},
	}
}

func toMemberInvitations(invitations []*memberinvitations.Invitation) []model.MemberInvitation {
	result := make([]model.MemberInvitation, 0, len(invitations))
	for _, invitation := range invitations {
		result = append(result, *toMemberInvitation(invitation))
	}
	return result
}
