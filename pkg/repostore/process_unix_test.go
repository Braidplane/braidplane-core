//go:build linux || darwin

package repostore

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitCommandCancellationStopsTransportChild(t *testing.T) {
	if os.Getenv("BP_GIT_TEST_CHILD_ADDRESS") != "" {
		runGitTransportChild(t)
		return
	}
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	ready := filepath.Join(dir, "child-address")
	// The shell represents Git and the test subprocess represents its transport.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n\"$BP_GIT_TEST_EXECUTABLE\" -test.run=^TestGitCommandCancellationStopsTransportChild$ &\nwait\n"), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(t.Context())
	done, result := make(chan struct{}), make(chan error, 1)
	go func() {
		defer close(done)
		_, err := runGitCommand(ctx, "", []string{"BP_GIT_TEST_EXECUTABLE=" + executable, "BP_GIT_TEST_CHILD_ADDRESS=" + ready}, "ls-remote", "https://fixture.invalid/repo")
		result <- err
	}()
	t.Cleanup(func() {
		cancel()
		if address, err := os.ReadFile(ready); err == nil {
			// Also release the test child when a regression leaves it alive.
			if connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.WithoutCancel(t.Context()), "tcp", strings.TrimSpace(string(address))); err == nil {
				_ = connection.Close()
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Git command did not finish during fixture cleanup")
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(ready); return err == nil }, 5*time.Second, 10*time.Millisecond)
	address, err := os.ReadFile(ready)
	require.NoError(t, err)
	cancel()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Git cancellation left a transport child holding the output pipe")
	}
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", strings.TrimSpace(string(address)))
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err, "Git transport must stop when the command is cancelled")
}

func runGitTransportChild(t *testing.T) {
	t.Helper()
	ready := os.Getenv("BP_GIT_TEST_CHILD_ADDRESS")
	require.NotEmpty(t, ready)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	require.NoError(t, os.WriteFile(ready, []byte(listener.Addr().String()), 0o600)) // #nosec G703 -- parent passes a path inside its isolated t.TempDir.
	connection, err := listener.Accept()
	require.NoError(t, err)
	require.NoError(t, connection.Close())
}
