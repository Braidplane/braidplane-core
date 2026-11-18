package postgres

import (
	"net/url"
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

func TestMigrationConnectionTLSDefault(t *testing.T) {
	for _, tc := range []struct {
		name, query, environment string
		wantTLS                  bool
	}{
		{name: "no configured mode", wantTLS: true},
		{name: "explicit URL mode", query: "?sslmode=disable", environment: "require"},
		{name: "explicit environment mode", environment: "disable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGSSLMODE", tc.environment)
			address, err := url.Parse("postgres://fixture@db.example.test/core" + tc.query)
			require.NoError(t, err)
			config, err := parseMigrationConnection(address)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTLS, config.TLSConfig != nil)
			for _, fallback := range config.Fallbacks {
				assert.Equal(t, tc.wantTLS, fallback.TLSConfig != nil)
			}
		})
	}
}
