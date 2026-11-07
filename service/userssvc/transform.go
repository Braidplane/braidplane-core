package userssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

func toDomain(u *users.UserPublic) *model.User {
	return &model.User{
		AuthenticationVersion: u.AuthenticationVersion,
		Slug:                  u.Slug,
		Email:                 u.Email,
		FullName:              u.FullName,
		Role:                  u.Role,
		Locale:                u.Locale,
		AvatarURL:             u.AvatarURL,
		PhoneNumber:           u.PhoneNumber,
		JobTitle:              u.JobTitle,
		Timezone:              u.Timezone,
		MFAEmailEnabled:       u.MFAEmailEnabled,
		HasLocalPassword:      u.HasLocalPassword,
		VerifiedAt:            u.VerifiedAt,
		Timestamps: model.Timestamps{
			CreatedAt: u.CreatedAt,
			UpdatedAt: u.UpdatedAt,
		},
	}
}
