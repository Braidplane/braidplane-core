// Package containerfixture owns disposable, package-scoped Go E2E dependencies.
package containerfixture

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	containerlog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

type composeService struct {
	Image               string                               `json:"image"`
	Build               struct{ Context, Dockerfile string } `json:"build"`
	Environment         map[string]string                    `json:"environment"`
	Command, Entrypoint []string
	Ports               []struct {
		Target   int
		Protocol string
	}
	Volumes     []struct{ Type, Source, Target string }
	Secrets     []struct{ Source, Target string }
	Healthcheck struct{ Test []string }
}

var portVariables = map[string]map[int]string{
	"database": {5432: "DB_PORT"}, "email-mailpit": {1025: "MAILPIT_SMTP_PORT", 8025: "MAILPIT_WEB_PORT"},
	"postgres-connector-test": {5432: "POSTGRES_CONNECTOR_PORT"}, "mysql-connector-test": {3306: "MYSQL_CONNECTOR_PORT"},
	"mongo-connector-test": {27017: "MONGO_CONNECTOR_PORT"}, "http-connector-test": {8080: "HTTP_CONNECTOR_PORT"},
	"openapi-server": {80: "OPENAPI_SERVER_PORT"}, "hubspot-connector-test": {8080: "HUBSPOT_MOCK_PORT"},
	"bigquery-connector-test": {8080: "BIGQUERY_MOCK_PORT"}, "git-provider-mock": {8080: "GIT_PROVIDER_MOCK_PORT"},
	"git-connector-test": {23231: "GIT_SSH_PORT", 23232: "GIT_HTTP_PORT"}, "s3-connector-test": {9000: "S3_CONNECTOR_PORT"},
	"ftp-connector-test": {21: "FTP_CONNECTOR_PORT"},
}

// Run starts only the selected fixture services and terminates them after m.Run.
// Call from TestMain; all tests in the package share the discovered endpoints.
func Run(m *testing.M, services ...string) (code int) {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	containerlog.SetDefault(containerlog.NewNoopLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := rootDir(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "containerfixture: setup failed:", err)
		return 1
	}
	if err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "containerfixture: Docker infrastructure is unavailable; start Docker and rerun the same test task:", err)
		return 1
	}
	// Go shares the default reaper across packages; keep cleanup package-owned.
	_ = os.Setenv("TESTCONTAINERS_SESSION_ID", strconv.Itoa(os.Getpid())+"-"+rand.Text())
	if testcontainers.ReadConfig().Config.RyukDisabled {
		fmt.Fprintln(os.Stderr, "containerfixture: resource reaper must remain enabled")
		return 1
	}
	// These fixtures use public images and must not consult operator registry credentials.
	_ = os.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{}}`)
	var containers []testcontainers.Container
	var netw *testcontainers.DockerNetwork
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		for index := len(containers) - 1; index >= 0; index-- {
			if code != 0 {
				if state, err := containers[index].State(cleanupCtx); err == nil {
					fmt.Fprintf(os.Stderr, "containerfixture: failed fixture status=%s exit=%d oom=%t\n", state.Status, state.ExitCode, state.OOMKilled)
				}
			}
			if err := containers[index].Terminate(cleanupCtx); err != nil {
				fmt.Fprintln(os.Stderr, "containerfixture: owned resource cleanup failed:", err)
				code = 1
			}
		}
		if netw != nil {
			if err := netw.Remove(cleanupCtx); err != nil {
				fmt.Fprintln(os.Stderr, "containerfixture: network cleanup failed:", err)
				code = 1
			}
		}
	}()

	if contains(services, "s3-connector-test") {
		ipam, err := networkIPAM(os.Getenv("BRAIDPLANE_E2E_NETWORK_SUBNET"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: network configuration failed:", err)
			return 1
		}
		var options []network.NetworkCustomizer
		if len(ipam.Config) > 0 {
			options = append(options, network.WithIPAM(&ipam))
		}
		netw, err = network.New(ctx, options...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: network setup failed:", err)
			return 1
		}
		// The initializer uses the fixture's network alias, never a worktree port.
		services = append(services, "s3-connector-test-setup")
	}
	if contains(services, "git-connector-test") {
		keyDir, err := os.MkdirTemp("", "braidplane-go-e2e-git-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: key setup failed:", err)
			return 1
		}
		defer func() { _ = os.RemoveAll(keyDir) }()
		keyPath := filepath.Join(keyDir, "id_ed25519")
		// #nosec G204 -- fixed command with a package-owned temporary output path.
		cmd := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath)
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: local fixture key generation failed:", err)
			return 1
		}
		// #nosec G304 -- read only the public key generated in this package-owned directory.
		publicKey, err := os.ReadFile(keyPath + ".pub")
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: fixture public key unavailable:", err)
			return 1
		}
		_ = os.Setenv("GIT_TEST_SSH_PUBLIC_KEY", strings.TrimSpace(string(publicKey)))
		_ = os.Setenv("GIT_TEST_SSH_KEY_PATH", keyPath)
	}
	var mailpitDirectory string
	if contains(services, "email-mailpit") {
		var err error
		mailpitDirectory, err = os.MkdirTemp("", "braidplane-mailpit-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: Mailpit TLS directory:", err)
			return 1
		}
		defer func() { _ = os.RemoveAll(mailpitDirectory) }()
		cert, key := filepath.Join(mailpitDirectory, "cert.pem"), filepath.Join(mailpitDirectory, "key.pem")
		// Generate only an isolated fixture identity; the host trust store stays unchanged.
		// #nosec G204 -- fixed OpenSSL command writes only into the package-owned fixture directory.
		command := exec.CommandContext(ctx, "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-keyout", key, "-out", cert)
		if err := command.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: generate Mailpit TLS:", err)
			return 1
		}
		_ = os.Setenv("SMTP_CA_CERT_FILE", cert)
	}
	for _, name := range services {
		service, secrets, err := loadService(ctx, root, name)
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture:", name, "setup failed:", err)
			return 1
		}
		if name == "email-mailpit" {
			secrets["mailpit-cert"] = composeSecret{File: filepath.Join(mailpitDirectory, "cert.pem")}
			secrets["mailpit-key"] = composeSecret{File: filepath.Join(mailpitDirectory, "key.pem")}
		}
		req := testcontainers.ContainerRequest{Image: service.Image, Env: service.Environment, Cmd: service.Command, Entrypoint: service.Entrypoint}
		if service.Build.Context != "" {
			req.Image = ""
			req.FromDockerfile = testcontainers.FromDockerfile{Context: service.Build.Context, Dockerfile: service.Build.Dockerfile}
		}
		if netw != nil {
			req.Networks = []string{netw.Name}
			req.NetworkAliases = map[string][]string{netw.Name: {name}}
		}
		var binds []string
		for _, volume := range service.Volumes {
			if volume.Type == "bind" {
				binds = append(binds, volume.Source+":"+volume.Target+":ro")
			}
		}
		for _, secret := range service.Secrets {
			binds = append(binds, secrets[secret.Source].File+":"+secretTarget(secret.Source, secret.Target)+":ro")
		}
		var fixedPorts map[dockernetwork.Port]string

		for _, port := range service.Ports {
			req.ExposedPorts = append(req.ExposedPorts, strconv.Itoa(port.Target)+"/"+port.Protocol)
		}
		if name == "ftp-connector-test" {
			var listenerConfig net.ListenConfig
			listener, err := listenerConfig.Listen(ctx, "tcp", "127.0.0.1:0")
			if err != nil {
				fmt.Fprintln(os.Stderr, "containerfixture: FTP passive port setup failed:", err)
				return 1
			}
			passive := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			req.Env["FTP_PASV_MIN"], req.Env["FTP_PASV_MAX"] = strconv.Itoa(passive), strconv.Itoa(passive)
			passivePort := dockernetwork.MustParsePort(strconv.Itoa(passive) + "/tcp")
			req.ExposedPorts = []string{"21/tcp", passivePort.String()}
			// PASV advertises the listening port; preserve that mapping on the host.
			fixedPorts = map[dockernetwork.Port]string{passivePort: strconv.Itoa(passive)}
		}
		// Synthetic fixture credentials must remain bound to loopback.
		req.HostConfigModifier = func(config *container.HostConfig) {
			if name == "spire-registration-test" {
				config.NetworkMode = "none"
			}
			config.Binds = binds
			config.PortBindings = loopbackPortBindings(req.ExposedPorts, fixedPorts)
		}

		var strategies []wait.Strategy
		if len(req.ExposedPorts) > 0 {
			strategies = append(strategies, wait.ForListeningPort(req.ExposedPorts[0]))
		}
		if health := service.Healthcheck.Test; len(health) > 1 {
			args := health[1:]
			if health[0] == "CMD-SHELL" {
				args = []string{"sh", "-c", health[1]}
			}
			strategies = append(strategies, wait.ForExec(args))
		}
		req.WaitingFor = wait.ForAll(strategies...).WithDeadline(2 * time.Minute)
		fmt.Fprintln(os.Stderr, "containerfixture: provisioning", name)
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
		if ctr != nil {
			containers = append(containers, ctr)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture:", name, "readiness failed:", err)
			return 1
		}
		if name == "spire-registration-test" {
			directory, err := os.MkdirTemp("", "braidplane-spire-")
			if err != nil {
				fmt.Fprintln(os.Stderr, "containerfixture: SPIRE client directory:", err)
				return 1
			}
			defer func() { _ = os.RemoveAll(directory) }()
			binary := filepath.Join(directory, "spire-server")
			wrapper := fmt.Sprintf("#!/bin/sh\nexec docker exec %s /opt/spire/bin/spire-server \"$@\"\n", ctr.GetContainerID())
			// #nosec G306 -- executable wrapper contains only a disposable container ID, no secrets.
			if err := os.WriteFile(binary, []byte(wrapper), 0o700); err != nil {
				fmt.Fprintln(os.Stderr, "containerfixture: SPIRE client wrapper:", err)
				return 1
			}
			_ = os.Setenv("SPIRE_TEST_BINARY", binary)
		}
		host, err := ctr.Host(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: endpoint unavailable:", err)
			return 1
		}
		for target, env := range portVariables[name] {
			port, err := ctr.MappedPort(ctx, strconv.Itoa(target)+"/tcp")
			if err != nil {
				fmt.Fprintln(os.Stderr, "containerfixture: endpoint unavailable:", err)
				return 1
			}
			_ = os.Setenv(env, port.Port())
			if name == "database" {
				for key, value := range map[string]string{"DB_HOST": host, "DB_USER": "postgres", "DB_PASSWORD": "postgres", "DB_DATABASE": "braidplane", "DB_SSL_MODE": "disable"} {
					_ = os.Setenv(key, value)
				}
				_ = os.Setenv("DATABASE_URL", "postgres://postgres:postgres@"+net.JoinHostPort(host, port.Port())+"/braidplane?sslmode=disable")
			}
			if name == "email-mailpit" && target == 1025 {
				_ = os.Setenv("SMTP_HOST", net.JoinHostPort(host, port.Port()))
			}
		}
	}
	if contains(services, "database") {
		catalog, err := postgres.EmbeddedMigrationCatalog()
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: catalog setup failed:", err)
			return 1
		}
		selected, err := postgres.MigrationDatabaseURL(os.Getenv("DATABASE_URL"), "core_schema_migrations")
		if err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: ledger setup failed:", err)
			return 1
		}
		if _, err := postgres.ApplyMigrationCatalog(selected, catalog); err != nil {
			fmt.Fprintln(os.Stderr, "containerfixture: migration setup failed:", err)
			return 1
		}
	}
	return m.Run()
}

func secretTarget(source, target string) string {
	if target == "" {
		target = source
	}
	if filepath.IsAbs(target) {
		return target
	}
	return "/run/secrets/" + target
}

type composeSecret struct{ File string }

func loadService(ctx context.Context, root, name string) (composeService, map[string]composeSecret, error) {
	if name == "database" {
		return composeService{
			Image: "postgres:18.6@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6",
			// #nosec G101 -- Fixed credentials for package-owned disposable PostgreSQL fixtures.
			Environment: map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": "postgres", "POSTGRES_DB": "braidplane"},
			Ports: []struct {
				Target   int
				Protocol string
			}{{5432, "tcp"}},
			Healthcheck: struct{ Test []string }{Test: []string{"CMD", "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "braidplane"}},
		}, nil, nil
	}

	file := "docker-compose.connectors.yml"
	if name == "database" || name == "email-mailpit" {
		file = "docker-compose.yml"
	}
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", file, "config", "--format", "json")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return composeService{}, nil, xerror.Wrapf("render fixture definition: %w", err)
	}
	var config struct {
		Services map[string]composeService
		Secrets  map[string]composeSecret
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return composeService{}, nil, err
	}
	service, ok := config.Services[name]
	if !ok {
		return composeService{}, nil, xerror.ValidationFailed().Detailf("unknown fixture service %q", name)
	}
	return service, config.Secrets, nil
}

func rootDir(ctx context.Context) (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", xerror.Wrapf("find fixture source root: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			// Recipes belong to the selected core module, including in managed tests.
			command := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "github.com/Braidplane/braidplane-core")
			command.Dir = root
			command.Env = append(os.Environ(), "GOWORK=off")
			output, err := command.Output()
			if err != nil {
				return "", xerror.Wrapf("resolve selected core fixture source: %w", err)
			}
			selected := strings.TrimSpace(string(output))
			if selected == "" {
				return "", xerror.ValidationFailed().Detail("selected core fixture source directory unavailable")
			}
			return selected, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", xerror.ValidationFailed().Detailf("fixture source root unavailable")
		}
		root = parent
	}
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func networkIPAM(value string) (dockernetwork.IPAM, error) {
	if value == "" {
		return dockernetwork.IPAM{}, nil
	}
	subnet, err := netip.ParsePrefix(value)
	if err != nil || !subnet.Addr().Is4() || subnet != subnet.Masked() {
		return dockernetwork.IPAM{}, xerror.ValidationFailed().Detail("BRAIDPLANE_E2E_NETWORK_SUBNET must be a canonical IPv4 CIDR")
	}
	return dockernetwork.IPAM{Driver: "default", Config: []dockernetwork.IPAMConfig{{Subnet: subnet}}}, nil
}

func loopbackPortBindings(ports []string, fixed map[dockernetwork.Port]string) dockernetwork.PortMap {
	bindings := make(dockernetwork.PortMap, len(ports))
	for _, value := range ports {
		port := dockernetwork.MustParsePort(value)
		bindings[port] = []dockernetwork.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: fixed[port]}}
	}
	return bindings
}
