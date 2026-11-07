package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/wishlistregistrations"
)

func TestRepository_UpsertPersistsCommunicationRequestVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "wishlist wording version repository integration test")
	repo := wishlistregistrations.NewRepository()
	email := fmt.Sprintf("wishlist-wording-%d@example.com", time.Now().UnixNano())

	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM wishlist_registrations WHERE work_email = $1`, email)
		require.NoError(t, err)
	})

	registration, err := repo.Upsert(ctx, pool, wishlistregistrations.UpsertParams{
		WorkEmail:                   email,
		CareMostAbout:               model.WishlistFocusCLIFirstETLRuns,
		CommunicationRequestVersion: model.WishlistCommunicationRequestVersion,
	})
	require.NoError(t, err)
	require.Equal(t, model.WishlistCommunicationRequestVersion, registration.CommunicationRequestVersion)

	var storedVersion *string
	err = pool.QueryRow(
		ctx,
		`SELECT communication_request_version FROM wishlist_registrations WHERE work_email = $1`,
		email,
	).Scan(&storedVersion)
	require.NoError(t, err)
	require.NotNil(t, storedVersion)
	require.Equal(t, model.WishlistCommunicationRequestVersion, *storedVersion)
}

func TestRepository_UpsertRefreshesCommunicationRequestVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "wishlist wording version conflict integration test")
	repo := wishlistregistrations.NewRepository()
	email := fmt.Sprintf("wishlist-wording-conflict-%d@example.com", time.Now().UnixNano())
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM wishlist_registrations WHERE work_email = $1`, email)
		require.NoError(t, err)
	})

	_, err := repo.Upsert(ctx, pool, wishlistregistrations.UpsertParams{
		WorkEmail:                   email,
		CareMostAbout:               model.WishlistFocusCLIFirstETLRuns,
		CommunicationRequestVersion: model.WishlistCommunicationRequestVersionPreAlphaInvitationV1,
	})
	require.NoError(t, err)

	registration, err := repo.Upsert(ctx, pool, wishlistregistrations.UpsertParams{
		WorkEmail:                   email,
		CareMostAbout:               model.WishlistFocusContractsAndValidation,
		CommunicationRequestVersion: model.WishlistCommunicationRequestVersion,
	})
	require.NoError(t, err)
	require.Equal(t, model.WishlistCommunicationRequestVersion, registration.CommunicationRequestVersion)

	var storedVersion *string
	err = pool.QueryRow(
		ctx,
		`SELECT communication_request_version FROM wishlist_registrations WHERE work_email = $1`,
		email,
	).Scan(&storedVersion)
	require.NoError(t, err)
	require.NotNil(t, storedVersion)
	require.Equal(t, model.WishlistCommunicationRequestVersion, *storedVersion)
}

func TestRepository_DeletesRegistrationsAtOrBeforeCutoff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "wishlist retention repository integration test")
	repo := wishlistregistrations.NewRepository()
	prefix := fmt.Sprintf("wishlist-retention-%d", time.Now().UnixNano())
	expiredEmail := prefix + "-expired@example.com"
	boundaryEmail := prefix + "-boundary@example.com"
	freshEmail := prefix + "-fresh@example.com"
	cutoff := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)

	t.Cleanup(func() {
		_, err := pool.Exec(
			context.Background(),
			`DELETE FROM wishlist_registrations WHERE work_email = ANY($1)`,
			[]string{expiredEmail, boundaryEmail, freshEmail},
		)
		require.NoError(t, err)
	})

	for email, updatedAt := range map[string]time.Time{
		expiredEmail:  cutoff.Add(-time.Nanosecond),
		boundaryEmail: cutoff,
		freshEmail:    cutoff.Add(time.Microsecond),
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO wishlist_registrations (
				work_email,
				care_most_about,
				workflow_to_test_first,
				created_at,
				updated_at
			)
			VALUES ($1, 'cli_first_etl_runs', 'synthetic test note', $2, $2)
		`, email, updatedAt)
		require.NoError(t, err)
	}

	deleted, err := repo.DeleteExpired(ctx, pool, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	var remaining []string
	rows, err := pool.Query(ctx, `
		SELECT work_email
		FROM wishlist_registrations
		WHERE work_email = ANY($1)
		ORDER BY work_email
	`, []string{expiredEmail, boundaryEmail, freshEmail})
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var email string
		require.NoError(t, rows.Scan(&email))
		remaining = append(remaining, email)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{freshEmail}, remaining)
}
