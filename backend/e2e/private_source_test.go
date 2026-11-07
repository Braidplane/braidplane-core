package backend_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/backend"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/stretchr/testify/require"
)

// The source-defined internal-network fixture supplies the customer DNS, CA and DB identity.
func TestSelfHostedPinnedPrivatePostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the isolated self-hosted acceptance fixture")
	}
	if os.Getenv("SELF_HOSTED_CONNECTOR_ACCEPTANCE") != "true" {
		runPrivateSourceFixture(t)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", "database.customer.test")
	require.NoError(t, err)
	require.Len(t, addresses, 1)
	pinned := netip.AddrPortFrom(addresses[0], 5432)
	cfg, err := backendconfig.LoadWithArgs("/fixture/config.toml", []string{
		"--private-postgres-organization=customer", "--private-postgres-source=warehouse",
		"--private-postgres-host=database.customer.test", "--private-postgres-address=" + pinned.String(),
	})
	require.NoError(t, err)
	require.False(t, cfg.Security.AllowPrivateDataSourceHosts)
	factory := backend.NewConnectorFactory(cfg, nil)
	payload := []byte(`{"host":"database.customer.test","port":5432,"database":"braidplane","ssl_mode":"verify-full"}`)
	credentials, err := json.Marshal(map[string]string{"username": cfg.Database.User, "password": cfg.Database.Password})
	require.NoError(t, err)
	identity := func(org, source string) context.Context {
		return connector.WithDataSourceIdentity(ctx, slug.FromString(org), slug.FromString(source))
	}
	connection, err := factory(identity("customer", "warehouse"), model.DataSourceTypePostgres, payload, credentials)
	if os.Getenv("SELF_HOSTED_EXPECT_UNTRUSTED_CA") == "true" {
		require.ErrorContains(t, err, "certificate")
		return
	}
	require.NoError(t, err)
	defer func() { require.NoError(t, connection.Close()) }()
	require.NoError(t, connection.TestConnection(ctx))
	result, err := connection.Execute(ctx, []byte(`{"type":"postgres","sql":"SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()"}`), nil)
	require.NoError(t, err)
	require.NoError(t, result.Error())
	sql, ok := result.SQL()
	require.True(t, ok)
	require.Len(t, sql.Rows, 1)
	require.Equal(t, true, sql.Rows[0]["ssl"], "private source must use verified TLS")
	for _, scope := range []struct{ org, source string }{{"other-customer", "warehouse"}, {"customer", "other-source"}} {
		denied, err := factory(identity(scope.org, scope.source), model.DataSourceTypePostgres, payload, credentials)
		require.ErrorContains(t, err, "non-public")
		require.Nil(t, denied)
	}
	cfg.PrivatePostgres.Address = netip.AddrPortFrom(addresses[0], 5433).String()
	wrongSocket := backend.NewConnectorFactory(cfg, nil)
	denied, err := wrongSocket(identity("customer", "warehouse"), model.DataSourceTypePostgres, payload, credentials)
	require.ErrorContains(t, err, "non-public")
	require.Nil(t, denied)
	// DNS still resolves to the real address; the granted dialer must reject another pin.
	cfg.PrivatePostgres.Address = "10.255.255.254:5432"
	wrongAddress := backend.NewConnectorFactory(cfg, nil)
	denied, err = wrongAddress(identity("customer", "warehouse"), model.DataSourceTypePostgres, payload, credentials)
	require.ErrorContains(t, err, "operator-pinned endpoint")
	require.Nil(t, denied)
}

// Run the existing private-network probe inside its source-owned TLS fixture.
func runPrivateSourceFixture(t *testing.T) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "fixture")
	run := func(name string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), name, args...)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s: %s", name, output)
		return output
	}
	run("python3", "../../scripts/self_hosted_acceptance/prepare.py", directory)
	architecture := strings.TrimSpace(string(run("docker", "version", "--format", "{{.Server.Arch}}")))
	binary := filepath.Join(directory, "private-source.test")
	compile := exec.CommandContext(t.Context(), "bash", "../../scripts/go-toolchain.sh", "test", "-c", "-o", binary, ".")
	compile.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+architecture, "CGO_ENABLED=0")
	output, err := compile.CombinedOutput()
	require.NoError(t, err, "compile private-network probe: %s", output)
	overlay := filepath.Join(directory, "probe.yml")
	require.NoError(t, os.WriteFile(overlay, []byte(`services:
  private-source:
    image: postgres:18.6@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6
    user: "0:0"
`), 0o600))
	project := fmt.Sprintf("core-private-source-%d", time.Now().UnixNano())
	args := []string{"compose", "--project-name", project, "-f", filepath.Join(directory, "compose.yml"), "-f", overlay}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, "docker", append(args, "down", "--volumes", "--remove-orphans")...)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "private-network cleanup: %s", output)
	})
	run("docker", append(args, "up", "-d", "--wait", "database")...)
	run("docker", append(args, "run", "--rm", "--no-deps", "private-source")...)
	run("docker", append(args, "run", "--rm", "--no-deps", "-e", "SELF_HOSTED_EXPECT_UNTRUSTED_CA=true", "-e", "SSL_CERT_FILE=/missing-ca.pem", "private-source")...)
}
