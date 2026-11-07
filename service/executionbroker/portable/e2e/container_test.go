//go:build portable_executor

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	containerlog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
)

const postgresImage = "postgres:17.11-bookworm@sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826"

// This suite runs only against explicitly supplied local images and synthetic, test-owned resources.
func TestPortableExecutorContainers(t *testing.T) {
	backendImage := os.Getenv("PORTABLE_BACKEND_IMAGE")
	migrationImage := os.Getenv("PORTABLE_MIGRATION_IMAGE")
	require.NotEmpty(t, backendImage, "build and set PORTABLE_BACKEND_IMAGE")
	require.NotEmpty(t, migrationImage, "build and set PORTABLE_MIGRATION_IMAGE")
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{}}`)
	containerlog.SetDefault(containerlog.NewNoopLogger())
	require.False(t, testcontainers.ReadConfig().Config.RyukDisabled, "resource reaper must remain enabled")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	netw, err := network.New(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, netw.Remove(cleanup))
	})
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	config, err := os.ReadFile(filepath.Join(root, "docker/braidplane/config.staging.toml"))
	require.NoError(t, err)
	cert, key := fixtureCertificate(t)
	files := []testcontainers.ContainerFile{
		{Reader: strings.NewReader(string(cert)), ContainerFilePath: "/fixture/server.crt", FileMode: 0o644},
		{Reader: strings.NewReader(string(key)), ContainerFilePath: "/fixture/server.key", FileMode: 0o600},
		{HostFilePath: filepath.Join(root, "deploy/staging/configure-runtime-roles.sql"), ContainerFilePath: "/fixture/roles.sql", FileMode: 0o644},
	}
	db := startContainer(t, ctx, testcontainers.ContainerRequest{
		Image: postgresImage, Networks: []string{netw.Name}, NetworkAliases: map[string][]string{netw.Name: {"database"}}, ExposedPorts: []string{"5432/tcp"}, Files: files,
		Env:        map[string]string{"POSTGRES_USER": "braidplane", "POSTGRES_PASSWORD": "fixture-bootstrap-password", "POSTGRES_DB": "braidplane"},
		Entrypoint: []string{"sh", "-c"}, Cmd: []string{"chown postgres:postgres /fixture/server.key; exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/fixture/server.crt -c ssl_key_file=/fixture/server.key"},
		WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute),
	})
	// Use the production migration runner, then the production role provisioning script.
	migrationConfig := `[deployment]
profile="local"
[database]
host="database"
port=5432
user="braidplane"
database="braidplane"
ssl_mode="disable"
`
	migration := startContainer(t, ctx, testcontainers.ContainerRequest{Image: migrationImage, Networks: []string{netw.Name}, Env: map[string]string{"DB_PASSWORD": "fixture-bootstrap-password", "CONFIG_PATH": "/fixture/config.toml"}, Files: []testcontainers.ContainerFile{{Reader: strings.NewReader(migrationConfig), ContainerFilePath: "/fixture/config.toml", FileMode: 0o644}}, WaitingFor: wait.ForExit().WithExitTimeout(time.Minute)})
	state, err := migration.State(ctx)
	require.NoError(t, err)
	require.Zero(t, state.ExitCode, "migration fixture failed")
	docker(t, ctx, "exec", db.GetContainerID(), "env", "BRAIDPLANE_MIGRATOR_PASSWORD=fixture-migrator-password", "BRAIDPLANE_BACKEND_PASSWORD=fixture-backend-password", "BRAIDPLANE_EXECUTOR_ENABLED=true", "BRAIDPLANE_EXECUTOR_PASSWORD=fixture-executor-password", "BRAIDPLANE_FRONTEND_SESSION_PASSWORD=fixture-session-password", "BRAIDPLANE_RIVER_UI_PASSWORD=fixture-river-password", "BRAIDPLANE_RIVER_UI_PASSWORD_AVAILABLE=true", "BRAIDPLANE_DATABASE_NAME=braidplane", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "braidplane", "-d", "braidplane", "-f", "/fixture/roles.sql")
	host, err := db.Host(ctx)
	require.NoError(t, err)
	port, err := db.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	pool, err := pgxpool.New(ctx, fmt.Sprintf("postgres://braidplane:fixture-bootstrap-password@%s/braidplane?sslmode=disable", net.JoinHostPort(host, port.Port())))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES('portable','Portable')`)
	require.NoError(t, err)
	envelopeKey := make([]byte, 32)
	_, err = rand.Read(envelopeKey)
	require.NoError(t, err)
	applicationKey := make([]byte, 32)
	_, err = rand.Read(applicationKey)
	require.NoError(t, err)
	baseEnv := map[string]string{"CONFIG_PATH": "/fixture/config.toml", "DB_HOST": "database", "DB_PORT": "5432", "DB_SSL_MODE": "verify-full", "DB_SSL_ROOT_CERT": "/fixture/ca.crt", "SERVER_HOST": "0.0.0.0", "SERVER_PORT": "3001", "EXECUTION_BACKEND": "poller", "EXECUTION_REQUEST_KEY_HEX": hex.EncodeToString(envelopeKey), "EXECUTION_REQUEST_TIMEOUT": "90s", "ALLOW_PLAINTEXT_CONNECTORS": "true", "ALLOW_PRIVATE_DATA_SOURCE_HOSTS": "true"}
	baseEnv["DEPLOYMENT_PROFILE"] = "local"
	runtimeDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(runtimeDir, "config.toml"), config, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runtimeDir, "ca.crt"), cert, 0o644))
	architecture := strings.TrimSpace(docker(t, ctx, "image", "inspect", backendImage, "--format", "{{.Architecture}}"))
	require.Contains(t, []string{"amd64", "arm64"}, architecture)
	build := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts/go-toolchain.sh"), "test", "-c", "-tags", "portable_executor", "-o", filepath.Join(runtimeDir, "panic-executor.test"), ".")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+architecture, "CGO_ENABLED=0")
	buildOutput, err := build.CombinedOutput()
	require.NoError(t, err, "compile test-only executor process: %s", buildOutput)
	apiDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(apiDir, "config.toml"), config, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(apiDir, "ca.crt"), cert, 0o644))
	appKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	// Readable only inside the API's fixture mount; never mounted into the executor.
	require.NoError(t, os.WriteFile(filepath.Join(apiDir, "github.pem"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey)}), 0o644))
	runtime := func(role string) testcontainers.ContainerRequest {
		env := make(map[string]string, len(baseEnv)+3)
		for k, v := range baseEnv {
			env[k] = v
		}
		mountDir := runtimeDir
		if role != "executor" {
			mountDir = apiDir
			env["DB_USER"] = "braidplane_backend"
			env["DB_PASSWORD"] = "fixture-backend-password"
			env["ENCRYPTION_KEY_HEX"] = hex.EncodeToString(applicationKey)
			env["GITHUB_PROOF_APP_ID"] = "1"
			env["GITHUB_PROOF_CLIENT_ID"] = "fixture"
			env["GITHUB_PROOF_CLIENT_SECRET"] = "fixture-only"
			env["GITHUB_PROOF_PRIVATE_KEY_FILE"] = "/fixture/github.pem"
			env["GITHUB_PROOF_CALLBACK_URL"] = "http://127.0.0.1/callback"
			env["GITHUB_PROOF_INSTALLATION_URL"] = "https://github.com/apps/fixture/installations/new"
			env["GITHUB_PROOF_WEBHOOK_SECRET"] = strings.Repeat("fixture-only", 4)
		} else {
			env["DB_USER"] = "braidplane_executor"
			env["DB_PASSWORD"] = "fixture-executor-password"
		}
		return testcontainers.ContainerRequest{Image: backendImage, Cmd: []string{"/braidplane-server", role}, Env: env, User: "65532:65532", Networks: []string{netw.Name}, ExposedPorts: []string{"3001/tcp"}, HostConfigModifier: func(h *container.HostConfig) {
			hardenFixture(h)
			h.Binds = []string{mountDir + ":/fixture:ro"}
		}, WaitingFor: wait.ForHTTP("/readyz").WithPort("3001/tcp").WithStartupTimeout(time.Minute)}
	}
	if !t.Run("incompatible runtime schema fails closed", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE schema_migrations SET version=version-1`)
		require.NoError(t, err)
		defer func() {
			_, err := pool.Exec(ctx, `UPDATE schema_migrations SET version=version+1`)
			require.NoError(t, err)
		}()
		request := runtime("executor")
		request.WaitingFor = wait.ForExit().WithExitTimeout(30 * time.Second)
		incompatible := startContainer(t, ctx, request)
		state, err := incompatible.State(ctx)
		require.NoError(t, err)
		require.NotZero(t, state.ExitCode)
		logs := docker(t, ctx, "logs", incompatible.GetContainerID())
		require.True(t, strings.Contains(logs, "migration preflight failed"))
		require.True(t, strings.Contains(logs, "behind required version"))
	}) {
		return
	}
	api := startContainer(t, ctx, runtime("api"))
	executor := startContainer(t, ctx, runtime("executor"))
	apiURL := containerURL(t, ctx, api)
	executorURL := containerURL(t, ctx, executor)
	monitorAvailability(t, apiURL+"/readyz")
	t.Run("filesystem and kernel limits", func(t *testing.T) {
		// Execute inside the actual runtime image and cgroup, without adding privileges.
		for _, probe := range []string{
			`test "$(id -u)" = 65532`,
			`! touch /forbidden 2>/dev/null`,
			`touch /tmp/ephemeral && test -w /tmp/ephemeral`,
			`test ! -S /var/run/docker.sock`,
			`test -z "${ENCRYPTION_KEY_HEX:-}"`,
			`test -z "${GITHUB_PROOF_CLIENT_SECRET:-}" && test ! -e /fixture/github.pem && test ! -e /fixture/server.key`,
			`grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status`,
			`grep -q '^CapEff:[[:space:]]*0000000000000000$' /proc/self/status`,
			`test "$(cat /sys/fs/cgroup/pids.max)" = 64`,
			`test "$(cat /sys/fs/cgroup/memory.max)" = 268435456`,
			`test "$(cat /sys/fs/cgroup/cpu.max)" = '50000 100000'`,
			`! dd if=/dev/zero of=/tmp/overflow bs=1M count=40 2>/dev/null && rm -f /tmp/overflow`,
		} {
			docker(t, ctx, "exec", executor.GetContainerID(), "sh", "-c", probe)
		}
		docker(t, ctx, "exec", executor.GetContainerID(), "sh", "-c", `cp /bin/true /tmp/program; chmod +x /tmp/program; ! /tmp/program 2>/dev/null`)
		requireHTTP(t, apiURL+"/readyz", http.StatusOK)
	})
	if !t.Run("CPU throttling and PID exhaustion", func(t *testing.T) {
		docker(t, ctx, "exec", executor.GetContainerID(), "sh", "-c", `before=$(awk '$1=="nr_throttled" {print $2}' /sys/fs/cgroup/cpu.stat); perl -e '$end=time+2; 1 while time<$end'; after=$(awk '$1=="nr_throttled" {print $2}' /sys/fs/cgroup/cpu.stat); test "$after" -gt "$before"`)
		_, probeErr := exec.CommandContext(ctx, "docker", "exec", executor.GetContainerID(), "perl", "-e", `$n=0; for(1..128){$p=fork; last unless defined $p; if(!$p){exec "sleep","2"; exit 127} $n++} 1 while wait()>0; exit($n>0 && $n<64 ? 0 : 1)`).CombinedOutput()
		if probeErr != nil {
			state, err := executor.State(ctx)
			require.NoError(t, err)
			logs := docker(t, ctx, "logs", executor.GetContainerID())
			// Go may hit the same PID ceiling when starting a runtime thread before fork reports EAGAIN.
			require.False(t, state.OOMKilled, "the PID probe must not substitute a memory failure")
			require.False(t, state.Running)
			require.Equal(t, 2, state.ExitCode)
			require.True(t, strings.Contains(logs, "failed to create new OS thread"))
			requireHTTP(t, apiURL+"/readyz", http.StatusOK)
			require.NoError(t, executor.Start(ctx))
			executorURL = containerURL(t, ctx, executor)
		}
		requireHTTP(t, apiURL+"/readyz", http.StatusOK)
	}) {
		return
	}
	t.Run("bounded container logs", func(t *testing.T) {
		docker(t, ctx, "exec", executor.GetContainerID(), "perl", "-e", `open(F,">","/proc/1/fd/1") or die; print F "fixture-output\n" x 200000; close F`)
		logs := docker(t, ctx, "logs", executor.GetContainerID())
		require.True(t, strings.Contains(logs, "fixture-output"), "the output probe must reach the configured log driver")
		require.LessOrEqual(t, len(logs), 2*1024*1024, "Docker log retention must remain bounded")
	})
	cipher, err := crypto.NewAES256(envelopeKey)
	require.NoError(t, err)
	broker, err := executionbroker.New(postgres.NewTransactionManager(pool), cipher, executionbroker.NewPollerDispatcher(), 90*time.Second, nil, nil, testutil.FakeOperationAdmission{}, filestream.Profile{})
	require.NoError(t, err)
	provider := startContainer(t, ctx, testcontainers.ContainerRequest{Image: backendImage, Networks: []string{netw.Name}, NetworkAliases: map[string][]string{netw.Name: {"provider"}}, Entrypoint: []string{"perl", "-MIO::Socket::INET", "-e"}, Cmd: []string{canaryHTTPServer}, ExposedPorts: []string{"8080/tcp"}, WaitingFor: wait.ForListeningPort("8080/tcp")})
	verifyAuthenticatedRouting(t, ctx, pool, apiURL, provider, func(t *testing.T) { startContainer(t, ctx, runtime("worker")) })
	if t.Failed() {
		return
	}
	request := datasourcessvc.ConnectionTestRequest{OrganizationSlug: "portable", DataSourceType: model.DataSourceTypeHTTP, ConfigJSON: []byte(`{"base_url":"http://provider:8080","allow_private_hosts":true}`), CredentialsJSON: []byte(`{}`)}
	t.Run("destination policy and sanitized failure", func(t *testing.T) {
		docker(t, ctx, "exec", provider.GetContainerID(), "rm", "-f", "/tmp/calls")
		denied := request
		denied.ConfigJSON = []byte(`{"base_url":"http://provider:8080"}`)
		denied.CredentialsJSON = []byte(`{"bearer_token":"synthetic-do-not-release"}`)
		err := broker.TestConnection(ctx, denied)
		require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
		require.False(t, strings.Contains(err.Error(), "synthetic-do-not-release"))
		docker(t, ctx, "exec", provider.GetContainerID(), "sh", "-c", `test ! -s /tmp/calls`)
		var ciphertext []byte
		require.NoError(t, pool.QueryRow(ctx, `SELECT result_ciphertext FROM execution_requests ORDER BY created_at DESC LIMIT 1`).Scan(&ciphertext))
		plaintext, err := cipher.Decrypt(ciphertext)
		require.NoError(t, err)
		var diagnostic struct {
			ErrorCode string `json:"error_code"`
		}
		require.NoError(t, json.Unmarshal(plaintext, &diagnostic))
		require.Equal(t, "connection_failed", diagnostic.ErrorCode)
		require.False(t, strings.Contains(string(plaintext), "synthetic-do-not-release"))
	})
	t.Run("operation time limit", func(t *testing.T) {
		docker(t, ctx, "exec", provider.GetContainerID(), "touch", "/tmp/slow")
		bounded := request
		bounded.ConfigJSON = []byte(`{"base_url":"http://provider:8080","allow_private_hosts":true,"timeout_seconds":1}`)
		started := time.Now()
		err := broker.TestConnection(ctx, bounded)
		require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
		require.Less(t, time.Since(started), 10*time.Second)
		docker(t, ctx, "exec", provider.GetContainerID(), "rm", "/tmp/slow")
	})
	t.Run("executor metrics", func(t *testing.T) {
		client := http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(executorURL + "/metrics")
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
		require.NoError(t, err)
		require.True(t, strings.Contains(string(body), "braidplane_execution_capacity"))
		require.True(t, strings.Contains(string(body), "braidplane_execution_last_successful_poll_timestamp_seconds"))
		require.False(t, strings.Contains(string(body), "synthetic-do-not-release"))
	})
	for _, fault := range []string{"kill", "memory limit", "network interruption", "panic"} {
		if !t.Run("executor "+fault+" and persisted recovery", func(t *testing.T) {
			if fault == "panic" {
				docker(t, ctx, "kill", "--signal", "KILL", executor.GetContainerID())
				fixture := runtime("executor")
				fixture.Cmd = []string{"/fixture/panic-executor.test", "-test.run", "^TestPortablePanicExecutorProcess$"}
				fixture.Env["PORTABLE_PANIC_HELPER"] = "1"
				executor = startContainer(t, ctx, fixture)
			}
			docker(t, ctx, "exec", executor.GetContainerID(), "touch", "/tmp/ephemeral")
			docker(t, ctx, "exec", provider.GetContainerID(), "touch", "/tmp/slow")
			finished := make(chan error, 1)
			go func() { finished <- broker.TestConnection(ctx, request) }()
			require.Eventually(t, func() bool {
				var n int
				err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_requests WHERE state='running'`).Scan(&n)
				return err == nil && n == 1
			}, 15*time.Second, 100*time.Millisecond)
			switch fault {
			case "panic":
				require.Eventually(t, func() bool {
					state, err := executor.State(ctx)
					return err == nil && !state.Running && state.ExitCode == 2 && !state.OOMKilled
				}, 15*time.Second, 100*time.Millisecond)
			case "kill":
				docker(t, ctx, "kill", "--signal", "KILL", executor.GetContainerID())
			case "memory limit":
				// Lower only this test-owned cgroup below the running process's footprint.
				docker(t, ctx, "update", "--memory=8m", "--memory-swap=8m", executor.GetContainerID())
				require.Eventually(t, func() bool {
					state, err := executor.State(ctx)
					return err == nil && !state.Running && state.OOMKilled && state.ExitCode == 137
				}, 15*time.Second, 100*time.Millisecond)
				docker(t, ctx, "update", "--memory=256m", "--memory-swap=256m", executor.GetContainerID())
			case "network interruption":
				docker(t, ctx, "network", "disconnect", netw.Name, executor.GetContainerID())
				require.Eventually(t, func() bool {
					output, err := exec.CommandContext(ctx, "docker", "exec", executor.GetContainerID(), "curl", "--silent", "--max-time", "4", "--output", "/dev/null", "--write-out", "%{http_code}", "http://127.0.0.1:3001/readyz").Output()
					return err == nil && string(output) == "503"
				}, 20*time.Second, 200*time.Millisecond)
				// Keep the partition through native lease expiry to exercise loss of ownership.
				require.Eventually(t, func() bool {
					var expired bool
					err := pool.QueryRow(ctx, `SELECT lease_expires_at <= clock_timestamp() FROM execution_requests WHERE state='running'`).Scan(&expired)
					return err == nil && expired
				}, 40*time.Second, 200*time.Millisecond)
			}
			requireHTTP(t, apiURL+"/readyz", http.StatusOK)
			docker(t, ctx, "exec", provider.GetContainerID(), "rm", "/tmp/slow")
			if fault == "panic" {
				executor = startContainer(t, ctx, runtime("executor"))
			} else if fault == "network interruption" {
				docker(t, ctx, "network", "connect", netw.Name, executor.GetContainerID())
			} else {
				require.NoError(t, executor.Start(ctx))
			}
			executorURL = containerURL(t, ctx, executor)
			require.Eventually(t, func() bool { return httpStatus(executorURL+"/readyz") == http.StatusOK }, 30*time.Second, 100*time.Millisecond)
			select {
			case err := <-finished:
				require.NoError(t, err)
			case <-time.After(50 * time.Second):
				t.Fatal("persisted request did not recover after native lease expiry")
			}
			var state string
			require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM execution_requests ORDER BY created_at DESC LIMIT 1`).Scan(&state))
			require.Equal(t, "succeeded", state)
			requireHTTP(t, executorURL+"/metrics", http.StatusOK)
			if fault != "network interruption" {
				docker(t, ctx, "exec", executor.GetContainerID(), "sh", "-c", `test ! -e /tmp/ephemeral`)
			}
		}) {
			return
		}
	}
}

func hardenFixture(h *container.HostConfig) {
	init := true
	h.Init = &init
	h.ReadonlyRootfs = true
	h.CapDrop = []string{"ALL"}
	h.SecurityOpt = []string{"no-new-privileges:true"}
	h.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=32m,mode=1777"}
	pids := int64(64)
	h.PidsLimit = &pids
	h.Memory = 256 * 1024 * 1024
	h.MemorySwap = h.Memory
	h.NanoCPUs = 500000000
	h.LogConfig = container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}}
}

func monitorAvailability(t *testing.T, url string) {
	t.Helper()
	stop, done := make(chan struct{}), make(chan struct{})
	failures := make(chan int, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if status := httpStatus(url); status != http.StatusOK {
					select {
					case failures <- status:
					default:
					}
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
		select {
		case status := <-failures:
			t.Errorf("API readiness probe failed during executor tests: HTTP %d", status)
		default:
		}
	})
}

func startContainer(t *testing.T, ctx context.Context, req testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if c != nil {
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			require.NoError(t, c.Terminate(cleanup))
		})
		if err != nil {
			logs, logErr := c.Logs(ctx)
			if logErr == nil {
				data, _ := io.ReadAll(io.LimitReader(logs, 4096))
				_ = logs.Close()
				detail := string(data)
				for name, value := range req.Env {
					if value != "" && (strings.Contains(name, "PASSWORD") || strings.Contains(name, "KEY") || strings.Contains(name, "TOKEN")) {
						detail = strings.ReplaceAll(detail, value, "<redacted>")
					}
				}
				t.Log("fixture startup diagnostics: " + detail)
			}
		}
	}
	require.NoError(t, err)
	return c
}

func docker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil && len(args) > 3 && args[0] == "exec" && args[2] == "docker" && args[3] == "run" {
		t.Logf("nested fixture startup failed: %.1024s", out)
	}
	require.NoError(t, err, "owned fixture Docker operation failed")
	return string(out)
}

func containerURL(t *testing.T, ctx context.Context, c testcontainers.Container) string {
	t.Helper()
	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "3001/tcp")
	require.NoError(t, err)
	return "http://" + net.JoinHostPort(host, port.Port())
}

func httpStatus(url string) int {
	client := http.Client{Timeout: 2 * time.Second}
	r, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
	return r.StatusCode
}

func requireHTTP(t *testing.T, url string, status int) {
	t.Helper()
	require.Equal(t, status, httpStatus(url))
}

func fixtureCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "database"}, DNSNames: []string{"database", "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	cert, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
}
