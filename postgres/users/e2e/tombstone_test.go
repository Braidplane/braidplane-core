package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/pats"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

func TestTombstoneRejectsAuthenticationAndLateProfileWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "account tombstone access revocation")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	userSlug := slug.Slug(fmt.Sprintf("tombstone-%d", time.Now().UnixNano()))
	email := userSlug.String() + "@example.com"
	var id int
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO users (slug, email, full_name, password, role, oidc_provider,
			oidc_provider_id, email_verification_token_hash, email_verification_token_expires_at,
			password_reset_token_hash, password_reset_token_expires_at)
		VALUES ($1::text, $2, 'Original', 'original-hash', 'user', 'google', $1::text,
			$1::text, NOW() + INTERVAL '1 day', $1::text, NOW() + INTERVAL '1 day') RETURNING id
	`, userSlug, email).Scan(&id))
	repo := users.NewRepository()
	reserved, err := repo.IsSlugReserved(ctx, tx, userSlug)
	require.NoError(t, err)
	require.True(t, reserved)
	reserved, err = repo.IsSlugReserved(ctx, tx, userSlug+"-absent")
	require.NoError(t, err)
	require.False(t, reserved)
	require.NoError(t, repo.CreateMFAChallenge(ctx, tx, userSlug, userSlug.String(), "code-hash", time.Now().Add(time.Hour)))
	_, err = tx.Exec(ctx, `
		INSERT INTO personal_access_tokens (user_id, slug, name, token_hash, token_prefix)
		VALUES ($1, 'test-pat', 'Test', $2, 'test')
	`, id, userSlug.String())
	require.NoError(t, err)

	lookups := []struct {
		name string
		find func() error
	}{
		{"session subject", func() error { _, err := repo.FindBySlug(ctx, tx, userSlug); return err }},
		{"password", func() error { _, err := repo.FindByEmailWithPassword(ctx, tx, email); return err }},
		{"email", func() error { _, err := repo.FindByEmail(ctx, tx, email); return err }},
		{"locked password", func() error { _, err := repo.FindBySlugWithPassword(ctx, tx, userSlug); return err }},
		{"OIDC", func() error { _, err := repo.FindByOIDCProvider(ctx, tx, "google", userSlug.String()); return err }},
		{"verification", func() error { _, err := repo.FindByEmailVerificationToken(ctx, tx, userSlug.String()); return err }},
		{"recovery", func() error { _, err := repo.FindByPasswordResetToken(ctx, tx, userSlug.String()); return err }},
		{"MFA", func() error { _, err := repo.FindMFAChallengeByToken(ctx, tx, userSlug.String()); return err }},
	}
	for _, lookup := range lookups {
		require.NoError(t, lookup.find(), lookup.name)
	}
	patRepo := pats.NewRepository()
	_, err = patRepo.FindActivePrincipalByTokenHash(ctx, tx, userSlug.String())
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE users SET deleted_at = NOW() WHERE id = $1`, id)
	require.NoError(t, err)
	reserved, err = repo.IsSlugReserved(ctx, tx, userSlug)
	require.NoError(t, err)
	require.True(t, reserved, "retained account identity must not be reused by registration")
	for _, lookup := range lookups {
		t.Run(lookup.name, func(t *testing.T) {
			require.ErrorIs(t, lookup.find(), users.ErrUserNotFound)
		})
	}
	_, err = patRepo.FindActivePrincipalByTokenHash(ctx, tx, userSlug.String())
	require.ErrorIs(t, err, pats.ErrNotFound)

	// A request authorized before closure must not repopulate the removed profile.
	name := "Late write"
	require.NoError(t, repo.UpdateProfile(ctx, tx, &users.UpdateProfileParams{Slug: userSlug, FullName: &name}))
	require.NoError(t, repo.UpdatePassword(ctx, tx, userSlug, "late-password"))
	require.NoError(t, repo.MarkEmailAsVerified(ctx, tx, userSlug, time.Now()))
	require.NoError(t, repo.CreatePasswordResetToken(ctx, tx, users.CreatePasswordResetTokenParams{
		Slug: userSlug, PasswordResetTokenHash: "late-reset", PasswordResetTokenExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.CreateEmailVerificationToken(ctx, tx, users.CreateEmailVerificationTokenParams{
		Slug: userSlug, EmailVerificationTokenHash: "late-verify", EmailVerificationTokenExpiresAt: time.Now().Add(time.Hour),
	}))
	var fullName, password, reset, verification string
	var unverified bool
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT full_name, password, password_reset_token_hash, email_verification_token_hash,
			verified_at IS NULL FROM users WHERE id = $1
	`, id).Scan(&fullName, &password, &reset, &verification, &unverified))
	require.Equal(t, "Original", fullName)
	require.Equal(t, "original-hash", password)
	require.Equal(t, userSlug.String(), reset)
	require.Equal(t, userSlug.String(), verification)
	require.True(t, unverified)
}
