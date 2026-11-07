package containerfixture

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
)

func TestDatabaseFixtureIsIndependentOfCompose(t *testing.T) {
	t.Parallel()
	service, secrets, err := loadService(context.Background(), filepath.Join(t.TempDir(), "absent"), "database")
	require.NoError(t, err)
	require.Nil(t, secrets)
	require.Contains(t, service.Image, "postgres:18.6@sha256:")
	require.Equal(t, "braidplane", service.Environment["POSTGRES_DB"])
	require.Equal(t, 5432, service.Ports[0].Target)
	require.Contains(t, service.Healthcheck.Test, "127.0.0.1")
}

func TestFixtureRecipesBelongToSelectedCoreModule(t *testing.T) {
	root := t.TempDir()
	core := filepath.Join(root, "core")
	owner := filepath.Join(root, "managed")
	nested := filepath.Join(owner, "postgres", "e2e")
	require.NoError(t, os.MkdirAll(core, 0o700))
	require.NoError(t, os.MkdirAll(nested, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(core, "go.mod"), []byte("module github.com/Braidplane/braidplane\n\ngo 1.26.0\n"), 0o600))
	config := "module github.com/Braidplane/braidplane-core/managed\n\ngo 1.26.0\nrequire github.com/Braidplane/braidplane-core v0.0.0\nreplace github.com/Braidplane/braidplane-core => " + core + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(owner, "go.mod"), []byte(config), 0o600))
	t.Chdir(nested)
	actual, err := rootDir(t.Context())
	require.NoError(t, err)
	expected, err := filepath.EvalSymlinks(core)
	require.NoError(t, err)
	actual, err = filepath.EvalSymlinks(actual)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func TestNetworkIPAMUsesOnlyExplicitCanonicalIPv4Subnet(t *testing.T) {
	t.Parallel()
	ipam, err := networkIPAM("")
	require.NoError(t, err)
	require.Empty(t, ipam.Config)
	ipam, err = networkIPAM("10.253.239.0/24")
	require.NoError(t, err)
	require.Equal(t, "default", ipam.Driver)
	require.Len(t, ipam.Config, 1)
	require.Equal(t, netip.MustParsePrefix("10.253.239.0/24"), ipam.Config[0].Subnet)
	for _, value := range []string{"not-a-cidr", "10.253.239.7/24", "2001:db8::/64"} {
		_, err = networkIPAM(value)
		require.ErrorContains(t, err, "canonical IPv4 CIDR")
	}
}

func TestLoopbackPortBindingsPreserveOnlyRequestedFixedPorts(t *testing.T) {
	t.Parallel()
	control := dockernetwork.MustParsePort("21/tcp")
	passive := dockernetwork.MustParsePort("37123/tcp")
	bindings := loopbackPortBindings([]string{control.String(), passive.String()}, map[dockernetwork.Port]string{passive: "37123"})
	require.Len(t, bindings, 2)
	for _, values := range bindings {
		require.Len(t, values, 1)
		require.Equal(t, netip.MustParseAddr("127.0.0.1"), values[0].HostIP)
	}
	require.Empty(t, bindings[control][0].HostPort)
	require.Equal(t, "37123", bindings[passive][0].HostPort)
	require.Empty(t, loopbackPortBindings(nil, nil))
}
