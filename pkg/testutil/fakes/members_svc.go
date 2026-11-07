package fakes

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// MembersService is a test double for memberssvc.Service.
type MembersService struct {
	ListMembersFn               func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.Member], error)
	ListMemberInvitationsFn     func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.MemberInvitation], error)
	ListMyMemberInvitationsFn   func(ctx context.Context, userSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.MemberInvitation], error)
	GetMemberInvitationFn       func(ctx context.Context, orgSlug, invitationSlug slug.Slug) (*model.MemberInvitation, error)
	CreateMemberInvitationFn    func(ctx context.Context, orgSlug, inviterUserSlug slug.Slug, actor model.EventActor, newInvitation model.NewMemberInvitation) (*model.MemberInvitation, error)
	ResendMemberInvitationFn    func(ctx context.Context, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitation, error)
	RevokeMemberInvitationFn    func(ctx context.Context, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitation, error)
	AcceptMemberInvitationFn    func(ctx context.Context, userSlug slug.Slug, token string, actor model.EventActor) (*model.MemberInvitationAcceptance, error)
	AcceptMyMemberInvitationFn  func(ctx context.Context, userSlug, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitationAcceptance, error)
	DeclineMemberInvitationFn   func(ctx context.Context, userSlug slug.Slug, token string, actor model.EventActor) (*model.MemberInvitation, error)
	DeclineMyMemberInvitationFn func(ctx context.Context, userSlug, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitation, error)
	CreateMemberFn              func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newMember model.NewMember) (*model.Member, error)
	CreateBootstrapMemberFn     func(ctx context.Context, orgSlug slug.Slug, newMember model.NewMember) (*model.Member, error)
	UpdateMemberFn              func(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor, update model.UpdateMember) (*model.Member, error)
	RemoveMemberFn              func(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor) error
}

func (f *MembersService) ListMembers(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.Member], error) {
	if f.ListMembersFn != nil {
		return f.ListMembersFn(ctx, orgSlug, req)
	}
	return model.ListResponse[model.Member]{
		Items:      []model.Member{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *MembersService) ListMemberInvitations(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.MemberInvitation], error) {
	if f.ListMemberInvitationsFn != nil {
		return f.ListMemberInvitationsFn(ctx, orgSlug, req)
	}
	return model.ListResponse[model.MemberInvitation]{
		Items:      []model.MemberInvitation{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *MembersService) ListMyMemberInvitations(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.MemberInvitation], error) {
	if f.ListMyMemberInvitationsFn != nil {
		return f.ListMyMemberInvitationsFn(ctx, userSlug, req)
	}
	return model.ListResponse[model.MemberInvitation]{
		Items:      []model.MemberInvitation{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *MembersService) GetMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
) (*model.MemberInvitation, error) {
	if f.GetMemberInvitationFn != nil {
		return f.GetMemberInvitationFn(ctx, orgSlug, invitationSlug)
	}
	return &model.MemberInvitation{Slug: invitationSlug, OrganizationSlug: orgSlug}, nil
}

func (f *MembersService) CreateMemberInvitation(
	ctx context.Context,
	orgSlug slug.Slug,
	inviterUserSlug slug.Slug,
	actor model.EventActor,
	newInvitation model.NewMemberInvitation,
) (*model.MemberInvitation, error) {
	if f.CreateMemberInvitationFn != nil {
		return f.CreateMemberInvitationFn(ctx, orgSlug, inviterUserSlug, actor, newInvitation)
	}
	return &model.MemberInvitation{
		OrganizationSlug:  orgSlug,
		Email:             newInvitation.Email,
		Role:              newInvitation.Role,
		Status:            model.MemberInvitationStatusPending,
		InvitedByUserSlug: &inviterUserSlug,
	}, nil
}

func (f *MembersService) ResendMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	if f.ResendMemberInvitationFn != nil {
		return f.ResendMemberInvitationFn(ctx, orgSlug, invitationSlug, actor)
	}
	return &model.MemberInvitation{Slug: invitationSlug, OrganizationSlug: orgSlug}, nil
}

func (f *MembersService) RevokeMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	if f.RevokeMemberInvitationFn != nil {
		return f.RevokeMemberInvitationFn(ctx, orgSlug, invitationSlug, actor)
	}
	return &model.MemberInvitation{
		Slug:             invitationSlug,
		OrganizationSlug: orgSlug,
		Status:           model.MemberInvitationStatusRevoked,
	}, nil
}

func (f *MembersService) AcceptMemberInvitation(
	ctx context.Context,
	userSlug slug.Slug,
	token string,
	actor model.EventActor,
) (*model.MemberInvitationAcceptance, error) {
	if f.AcceptMemberInvitationFn != nil {
		return f.AcceptMemberInvitationFn(ctx, userSlug, token, actor)
	}
	return &model.MemberInvitationAcceptance{
		Invitation: model.MemberInvitation{
			Email:            "invitee@example.com",
			Status:           model.MemberInvitationStatusAccepted,
			AcceptedUserSlug: &userSlug,
		},
		Member: model.Member{UserSlug: userSlug},
	}, nil
}

func (f *MembersService) AcceptMyMemberInvitation(
	ctx context.Context,
	userSlug, orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitationAcceptance, error) {
	if f.AcceptMyMemberInvitationFn != nil {
		return f.AcceptMyMemberInvitationFn(ctx, userSlug, orgSlug, invitationSlug, actor)
	}
	return &model.MemberInvitationAcceptance{
		Invitation: model.MemberInvitation{
			Slug:             invitationSlug,
			OrganizationSlug: orgSlug,
			Email:            "invitee@example.com",
			Status:           model.MemberInvitationStatusAccepted,
			AcceptedUserSlug: &userSlug,
		},
		Member: model.Member{UserSlug: userSlug},
	}, nil
}

func (f *MembersService) DeclineMemberInvitation(
	ctx context.Context,
	userSlug slug.Slug,
	token string,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	if f.DeclineMemberInvitationFn != nil {
		return f.DeclineMemberInvitationFn(ctx, userSlug, token, actor)
	}
	return &model.MemberInvitation{
		Email:  "invitee@example.com",
		Status: model.MemberInvitationStatusDeclined,
	}, nil
}

func (f *MembersService) DeclineMyMemberInvitation(
	ctx context.Context,
	userSlug, orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	if f.DeclineMyMemberInvitationFn != nil {
		return f.DeclineMyMemberInvitationFn(ctx, userSlug, orgSlug, invitationSlug, actor)
	}
	return &model.MemberInvitation{
		Slug:             invitationSlug,
		OrganizationSlug: orgSlug,
		Email:            "invitee@example.com",
		Status:           model.MemberInvitationStatusDeclined,
	}, nil
}

func (f *MembersService) CreateMember(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newMember model.NewMember,
) (*model.Member, error) {
	if f.CreateMemberFn != nil {
		return f.CreateMemberFn(ctx, orgSlug, actor, newMember)
	}
	return &model.Member{
		UserSlug: newMember.UserSlug,
		Role:     newMember.Role,
	}, nil
}

func (f *MembersService) CreateBootstrapMember(
	ctx context.Context,
	orgSlug slug.Slug,
	newMember model.NewMember,
) (*model.Member, error) {
	if f.CreateBootstrapMemberFn != nil {
		return f.CreateBootstrapMemberFn(ctx, orgSlug, newMember)
	}
	return &model.Member{
		UserSlug: newMember.UserSlug,
		Role:     newMember.Role,
	}, nil
}

func (f *MembersService) UpdateMember(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateMember,
) (*model.Member, error) {
	if f.UpdateMemberFn != nil {
		return f.UpdateMemberFn(ctx, orgSlug, userSlug, actor, update)
	}
	return &model.Member{UserSlug: userSlug, Role: update.Role}, nil
}

func (f *MembersService) RemoveMember(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RemoveMemberFn != nil {
		return f.RemoveMemberFn(ctx, orgSlug, userSlug, actor)
	}
	return nil
}
