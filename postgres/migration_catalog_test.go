package postgres

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestMigrationCatalogIdentityUsesVersionOrderAndRawBytes(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"migrations/10_last.up.sql": {Data: []byte("last\n")},
		"migrations/2_first.up.sql": {Data: []byte("first\r\n")},
	}
	version, digest, err := MigrationCatalogIdentity(files, "migrations")
	require.NoError(t, err)
	require.Equal(t, uint(10), version)
	require.Equal(t, "ef6fe1f12bda95d6e99f231a0a3a43f79ca6941f3090f9b821abd23d6406ccd4", digest)
	files["migrations/2_first.up.sql"].Data = []byte("first\n")
	_, changed, err := MigrationCatalogIdentity(files, "migrations")
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
}

func TestMigrationCatalogIdentityRejectsAmbiguousInput(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]fstest.MapFS{
		"Empty":            {},
		"DuplicateVersion": {"2_first.up.sql": {}, "000002_second.up.sql": {}},
		"InvalidVersion":   {"bad_name.up.sql": {}},
		"ZeroVersion":      {"0_first.up.sql": {}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := MigrationCatalogIdentity(files, ".")
			require.Error(t, err)
		})
	}
	_, _, err := MigrationCatalogIdentity(nil, ".")
	require.Error(t, err)
}
