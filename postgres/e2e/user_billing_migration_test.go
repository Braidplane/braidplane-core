package e2e

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestUserBillingRemovalPreservesPopulatedFieldsOnRefusal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	admin := e2edb.OpenPostgresPool(t, "user billing removal")
	database := fmt.Sprintf("billing_removal_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{database}.Sanitize()
	_, err := admin.Exec(t.Context(), "CREATE DATABASE "+identifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err)
	})
	config := admin.Config().ConnConfig.Copy()
	config.Database = database
	conn, err := pgx.ConnectConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close(context.Background())) })
	_, err = conn.Exec(t.Context(), `CREATE TABLE public.users (
		id integer PRIMARY KEY, full_name text,
		billing_line1 text, billing_line2 text, billing_city text,
		billing_region text, billing_postal_code text, billing_country text
	);
	INSERT INTO public.users VALUES (1, 'Synthetic Owner', 'Preserve this address', '', NULL, NULL, NULL, 'PL');`)
	require.NoError(t, err)
	catalog, err := postgres.EmbeddedMigrationCatalog()
	require.NoError(t, err)
	migration, err := fs.ReadFile(catalog, "000002_remove_user_billing_address.up.sql")
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), string(migration))
	require.ErrorContains(t, err, "Export and clear existing user billing addresses")
	_, err = conn.Exec(t.Context(), "ROLLBACK")
	require.NoError(t, err)
	var line1, line2, country string
	var city *string
	require.NoError(t, conn.QueryRow(t.Context(), "SELECT billing_line1, billing_line2, billing_city, billing_country FROM users WHERE id=1").Scan(&line1, &line2, &city, &country))
	require.Equal(t, "Preserve this address", line1)
	require.Empty(t, line2)
	require.Nil(t, city)
	require.Equal(t, "PL", country)
	_, err = conn.Exec(t.Context(), `UPDATE users SET billing_line1=NULL, billing_line2=NULL, billing_city=NULL, billing_region=NULL, billing_postal_code=NULL, billing_country=NULL`)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), string(migration))
	require.NoError(t, err)
	var columns int
	require.NoError(t, conn.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name LIKE 'billing_%'").Scan(&columns))
	require.Zero(t, columns)
	var name string
	require.NoError(t, conn.QueryRow(t.Context(), "SELECT full_name FROM users WHERE id=1").Scan(&name))
	require.Equal(t, "Synthetic Owner", name)
}
