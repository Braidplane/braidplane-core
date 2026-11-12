//go:build nativefilequalification

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
)

func TestNativeFileExecutorWorker(t *testing.T) {
	if os.Getenv("BP_NATIVE_FILE_EXECUTOR_WORKER") != "1" {
		t.Skip("subprocess entrypoint")
	}
	encryption, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	factory := func(ctx context.Context, kind model.DataSourceType, config, credentials []byte) (connector.Connector, error) {
		return connector.NewConnector(transportsecurity.WithPlaintext(ctx), kind, config, credentials)
	}
	broker, err := executionbroker.New(postgres.NewTransactionManager(openE2EPool(t)), encryption, executionbroker.NewPollerDispatcher(), 5*time.Minute, factory, slog.New(slog.DiscardHandler), testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	defer func() { require.NoError(t, broker.Close()) }()
	require.NoError(t, broker.Start(t.Context(), 2))
	fmt.Println("BP_NATIVE_FILE_EXECUTOR_READY")
	<-t.Context().Done()
}

func startNativeFileExecutor(ctx context.Context, t *testing.T) *exec.Cmd {
	t.Helper()
	require.NotEmpty(t, os.Getenv("DATABASE_URL"), "child must inherit the package's isolated database")
	binary, err := os.Executable()
	require.NoError(t, err)
	// #nosec G204 G702 -- re-executes this test binary against its own isolated database and synthetic data.
	command := exec.CommandContext(ctx, binary, "-test.run=^TestNativeFileExecutorWorker$", "-test.timeout=10m", "-test.v")
	command.Env = append(os.Environ(), "BP_NATIVE_FILE_EXECUTOR_WORKER=1")
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	ready := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "BP_NATIVE_FILE_EXECUTOR_READY" {
				ready <- struct{}{}
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("native file executor process did not become ready")
	}
	return command
}
