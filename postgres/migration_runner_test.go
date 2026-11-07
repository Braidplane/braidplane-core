package postgres

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizeMigrationErrorRedactsDatabaseURL prevents secret leakage in logs.
func TestSanitizeMigrationErrorRedactsDatabaseURL(t *testing.T) {
	t.Parallel()

	databaseURL := "postgres://user:super-secret@example.com/braidplane?sslmode=require"
	message := sanitizeMigrationError(
		assert.AnError,
		databaseURL,
	)
	assert.NotContains(t, message, databaseURL)

	message = sanitizeMigrationError(
		xerror.Wrapf("failed for postgres://user:super-secret@example.com/braidplane?sslmode=require"),
		databaseURL,
	)
	assert.NotContains(t, message, databaseURL)
	assert.NotContains(t, message, "super-secret")
	assert.Contains(t, message, "<redacted database url>")
}

func TestMigrationCatalogRequiresExplicitLedgerBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()
	_, err := ApplyMigrationCatalog("postgres://fixture:synthetic@example.test/db", nil)
	require.ErrorContains(t, err, "explicit migration ledger")
	require.NotContains(t, err.Error(), "synthetic")
}
