package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

func TestRepository_FindBySlugWithPasswordLocksUserRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "users password row lock repository integration test")
	repo := users.NewRepository()

	runID := time.Now().UnixNano()
	userSlug := slug.Slug(fmt.Sprintf("password-lock-%d", runID))
	_, err := repo.CreateUser(ctx, pool, &users.CreateParams{
		Slug:     userSlug,
		FullName: "Password Lock",
		Email:    fmt.Sprintf("password-lock-%d@example.com", runID),
		Password: "stored-current-hash",
		Role:     model.RoleUser,
		Locale:   "en",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM users WHERE slug = $1`, userSlug)
		require.NoError(t, err)
	})

	lockTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = lockTx.Rollback(context.Background()) }()

	lockedUser, err := repo.FindBySlugWithPassword(ctx, lockTx, userSlug)
	require.NoError(t, err)
	require.Equal(t, "stored-current-hash", lockedUser.Password)

	waitTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = waitTx.Rollback(context.Background()) }()

	_, err = waitTx.Exec(ctx, `SET LOCAL lock_timeout = '100ms'`)
	require.NoError(t, err)

	_, err = repo.FindBySlugWithPassword(ctx, waitTx, userSlug)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lock timeout")
}
