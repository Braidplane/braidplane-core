package e2e

import (
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/spireregistration"
	"github.com/google/uuid"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

func TestPinnedSPIRERegistration(t *testing.T) {
	binary := os.Getenv("SPIRE_TEST_BINARY")
	if binary == "" {
		t.Skip("run task test-snowflake-wif-spire for the isolated SPIRE fixture")
	}
	client := spireregistration.Client{Binary: binary, Socket: "/tmp/api.sock", Parent: spiffeid.RequireFromString("spiffe://issuer.test/agent/executor"), Selector: "unix:uid:65532"}
	ctx := t.Context()
	require.NoError(t, client.Check(ctx))
	ids := []string{uuid.NewString(), uuid.NewString()}
	subjects := []spiffeid.ID{
		spiffeid.RequireFromString("spiffe://issuer.test/tenant/alpha/snowflake/account/connection/" + ids[0]),
		spiffeid.RequireFromString("spiffe://issuer.test/tenant/beta/snowflake/account/connection/" + ids[1]),
	}
	for i, id := range ids {
		require.NoError(t, client.Ensure(ctx, id, subjects[i]))
		require.NoError(t, client.Ensure(ctx, id, subjects[i]))
	}
	require.ErrorIs(t, client.Ensure(ctx, uuid.NewString(), subjects[0]), spireregistration.ErrConflict)
	require.ErrorIs(t, client.Revoke(ctx, ids[1], subjects[0]), spireregistration.ErrConflict)
	require.NoError(t, client.Revoke(ctx, ids[0], subjects[0]))
	require.NoError(t, client.Revoke(ctx, ids[0], subjects[0]))
	require.NoError(t, client.Ensure(ctx, ids[1], subjects[1]))
	require.NoError(t, client.Revoke(ctx, ids[1], subjects[1]))
}
