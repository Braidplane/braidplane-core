package fakes

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// UsersService is a test double for userssvc.UsersService.
type UsersService struct {
	CreateUserFn              func(ctx context.Context, newUser model.NewUser) (*model.User, error)
	BeginLoginFn              func(ctx context.Context, email, password string) (*model.SessionLoginResult, error)
	LoginUserFn               func(ctx context.Context, email, password string) (*model.User, error)
	GetUserBySlugFn           func(ctx context.Context, userSlug slug.Slug) (*model.User, error)
	LoginOIDCUserFn           func(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
	LoginOrCreateOIDCUserFn   func(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
	VerifyEmailFn             func(ctx context.Context, token string) error
	RequestPasswordResetFn    func(ctx context.Context, email string) error
	ResetPasswordFn           func(ctx context.Context, token, password string) error
	ChangePasswordFn          func(ctx context.Context, userSlug slug.Slug, currentPassword, newPassword string) error
	ResendVerificationEmailFn func(ctx context.Context, email string) error
	UpdateProfileFn           func(ctx context.Context, userSlug slug.Slug, update model.UpdateUserProfile) (*model.User, error)
	VerifySessionMFAFn        func(ctx context.Context, challengeToken, code string) (*model.User, error)
}

func (f *UsersService) CreateUser(ctx context.Context, newUser model.NewUser) (*model.User, error) {
	if f.CreateUserFn != nil {
		return f.CreateUserFn(ctx, newUser)
	}
	return &model.User{Slug: "test-user", Email: newUser.Email}, nil
}

func (f *UsersService) LoginUser(ctx context.Context, email, password string) (*model.User, error) {
	if f.LoginUserFn != nil {
		return f.LoginUserFn(ctx, email, password)
	}
	return &model.User{}, nil
}

func (f *UsersService) BeginLogin(
	ctx context.Context,
	email,
	password string,
) (*model.SessionLoginResult, error) {
	if f.BeginLoginFn != nil {
		return f.BeginLoginFn(ctx, email, password)
	}
	user, err := f.LoginUser(ctx, email, password)
	if err != nil {
		return nil, err
	}
	return &model.SessionLoginResult{User: user}, nil
}

func (f *UsersService) GetUserBySlug(ctx context.Context, userSlug slug.Slug) (*model.User, error) {
	if f.GetUserBySlugFn != nil {
		return f.GetUserBySlugFn(ctx, userSlug)
	}
	return &model.User{Slug: userSlug}, nil
}

func (f *UsersService) LoginOIDCUser(
	ctx context.Context,
	oidcUser *model.OIDCUserInfo,
) (*model.User, error) {
	if f.LoginOIDCUserFn != nil {
		return f.LoginOIDCUserFn(ctx, oidcUser)
	}
	return &model.User{}, nil
}

func (f *UsersService) LoginOrCreateOIDCUser(
	ctx context.Context,
	oidcUser *model.OIDCUserInfo,
) (*model.User, error) {
	if f.LoginOrCreateOIDCUserFn != nil {
		return f.LoginOrCreateOIDCUserFn(ctx, oidcUser)
	}
	return &model.User{}, nil
}

func (f *UsersService) VerifyEmail(ctx context.Context, token string) error {
	if f.VerifyEmailFn != nil {
		return f.VerifyEmailFn(ctx, token)
	}
	return nil
}

func (f *UsersService) RequestPasswordReset(ctx context.Context, email string) error {
	if f.RequestPasswordResetFn != nil {
		return f.RequestPasswordResetFn(ctx, email)
	}
	return nil
}

func (f *UsersService) ResetPassword(ctx context.Context, token, password string) error {
	if f.ResetPasswordFn != nil {
		return f.ResetPasswordFn(ctx, token, password)
	}
	return nil
}

func (f *UsersService) ChangePassword(
	ctx context.Context,
	userSlug slug.Slug,
	currentPassword,
	newPassword string,
) error {
	if f.ChangePasswordFn != nil {
		return f.ChangePasswordFn(ctx, userSlug, currentPassword, newPassword)
	}
	return nil
}

func (f *UsersService) ResendVerificationEmail(ctx context.Context, email string) error {
	if f.ResendVerificationEmailFn != nil {
		return f.ResendVerificationEmailFn(ctx, email)
	}
	return nil
}

func (f *UsersService) UpdateProfile(
	ctx context.Context,
	userSlug slug.Slug,
	update model.UpdateUserProfile,
) (*model.User, error) {
	if f.UpdateProfileFn != nil {
		return f.UpdateProfileFn(ctx, userSlug, update)
	}
	return &model.User{Slug: userSlug}, nil
}

func (f *UsersService) VerifySessionMFA(
	ctx context.Context,
	challengeToken string,
	code string,
) (*model.User, error) {
	if f.VerifySessionMFAFn != nil {
		return f.VerifySessionMFAFn(ctx, challengeToken, code)
	}
	return &model.User{}, nil
}
