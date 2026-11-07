package instanceadminsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
)

func TestGrantRequiresVerifiedUserAndAuditsChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		user       instanceadmins.User
		wantErr    error
		wantWrites int
	}{
		{name: "unverified", user: instanceadmins.User{Slug: must.Slug("alice"), Role: model.RoleUser}, wantErr: ErrUserUnverified},
		{name: "verified", user: instanceadmins.User{Slug: must.Slug("alice"), Role: model.RoleUser, VerifiedAt: time.Now()}, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{user: tc.user}
			svc, err := New(fakeTransactionManager{}, repo)
			require.NoError(t, err)

			err = svc.Grant(t.Context(), tc.user.Slug)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantWrites, repo.setCalls)
			require.Equal(t, tc.wantWrites, repo.auditCalls)
		})
	}
}

func TestRevokeRefusesLastAdminAndIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		user       instanceadmins.User
		admins     int64
		wantErr    error
		wantWrites int
	}{
		{name: "last admin", user: instanceadmins.User{Slug: must.Slug("alice"), Role: model.RoleSuperAdmin, VerifiedAt: time.Now()}, admins: 1, wantErr: ErrLastAdmin},
		{name: "already revoked", user: instanceadmins.User{Slug: must.Slug("alice"), Role: model.RoleUser, VerifiedAt: time.Now()}, wantWrites: 0},
		{name: "another admin remains", user: instanceadmins.User{Slug: must.Slug("alice"), Role: model.RoleSuperAdmin, VerifiedAt: time.Now()}, admins: 2, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{user: tc.user, admins: tc.admins}
			svc, err := New(fakeTransactionManager{}, repo)
			require.NoError(t, err)

			err = svc.Revoke(t.Context(), tc.user.Slug)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantWrites, repo.setCalls)
			require.Equal(t, tc.wantWrites, repo.auditCalls)
			require.Equal(t, 1, repo.lockCalls)
		})
	}
}

type fakeTransactionManager struct{}

func (fakeTransactionManager) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

func (fakeTransactionManager) RunRead(context.Context, func(context.Context, postgres.DBTX) error) error {
	return nil
}

type fakeRepository struct {
	user                            instanceadmins.User
	admins                          int64
	lockCalls, setCalls, auditCalls int
}

func (r *fakeRepository) LockRoleChanges(context.Context, postgres.DBTX) error {
	r.lockCalls++
	return nil
}

func (r *fakeRepository) FindUserForUpdate(context.Context, postgres.DBTX, slug.Slug) (*instanceadmins.User, error) {
	return &r.user, nil
}

func (r *fakeRepository) CountSuperAdmins(context.Context, postgres.DBTX) (int64, error) {
	return r.admins, nil
}

func (r *fakeRepository) SetUserRole(context.Context, postgres.DBTX, slug.Slug, model.Role) error {
	r.setCalls++
	return nil
}

func (r *fakeRepository) CreateAudit(context.Context, postgres.DBTX, slug.Slug, model.Role, model.Role) error {
	r.auditCalls++
	return nil
}
