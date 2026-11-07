package instanceadminsvc

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/stretchr/testify/require"
)

func TestBootstrapOnlyWithoutAdministrator(t *testing.T) {
	for _, count := range []int64{0, 1} {
		t.Run(strconv.FormatInt(count, 10), func(t *testing.T) {
			admins := &fakeRepository{admins: count}
			accounts := &bootstrapUsers{}
			hasher := crypto.NewArgon2WithParams(8, 1, 1, 16, 32)
			svc, err := NewBootstrapper(fakeTransactionManager{}, admins, accounts, hasher)
			require.NoError(t, err)
			err = svc.Bootstrap(t.Context(), must.Slug("owner"), "Owner", "owner@example.test", "synthetic-password")
			require.Equal(t, 1, admins.lockCalls)
			if count != 0 {
				require.ErrorContains(t, err, "bootstrap is closed")
				require.Nil(t, accounts.created)
				require.Zero(t, admins.setCalls)
				return
			}
			require.NoError(t, err)
			require.True(t, accounts.verified)
			require.Equal(t, model.RoleUser, accounts.created.Role)
			matches, err := hasher.CompareHash("synthetic-password", accounts.created.Password)
			require.NoError(t, err)
			require.True(t, matches)
			require.Equal(t, 1, admins.setCalls)
			require.Equal(t, 1, admins.auditCalls)
		})
	}
}

func TestBootstrapRejectsInvalidAccountBeforeTransaction(t *testing.T) {
	admins := &fakeRepository{}
	svc, err := NewBootstrapper(fakeTransactionManager{}, admins, &bootstrapUsers{}, crypto.NewArgon2())
	require.NoError(t, err)
	err = svc.Bootstrap(t.Context(), must.Slug("owner"), "Owner", "invalid", "short")
	require.Error(t, err)
	require.Zero(t, admins.lockCalls)
}

type bootstrapUsers struct {
	users.Repository

	created  *users.CreateParams
	verified bool
}

func (r *bootstrapUsers) CreateUser(_ context.Context, _ postgres.DBTX, p *users.CreateParams) (*users.UserPublic, error) {
	r.created = p
	return &users.UserPublic{Slug: p.Slug}, nil
}

func (r *bootstrapUsers) MarkEmailAsVerified(_ context.Context, _ postgres.DBTX, _ slug.Slug, at time.Time) error {
	r.verified = !at.IsZero()
	return nil
}
