package spireregistration

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

func TestRegistrationCLI(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "spire-server")
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("SPIRE_TEST_EXECUTABLE", executable)
	t.Setenv("SPIRE_TEST_STATE", filepath.Join(dir, "entry.json"))
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nexec \"$SPIRE_TEST_EXECUTABLE\" -test.run=^TestSPIRECLIHelper$ -- \"$@\"\n"), 0o700))
	parent := spiffeid.RequireFromString("spiffe://issuer.test/agent/executor")
	subject := spiffeid.RequireFromString("spiffe://issuer.test/tenant/alpha/snowflake/account/connection/source")
	client := Client{Binary: binary, Socket: "/operator/api.sock", Parent: parent, Selector: "unix:uid:65532"}
	id := "owned-id"
	ctx := t.Context()
	require.NoError(t, client.Ensure(ctx, id, subject))
	require.NoError(t, client.Ensure(ctx, id, subject))
	current, err := client.find(ctx, id, subject)
	require.NoError(t, err)
	require.True(t, client.matches(*current))
	// Repair changed TTL and selector without changing identity ownership.
	current.JWTTTL = 900
	current.Selectors[0].Value = "0"
	writeCLIEntry(t, current)
	require.NoError(t, client.Ensure(ctx, id, subject))
	current, err = client.find(ctx, id, subject)
	require.NoError(t, err)
	require.True(t, client.matches(*current))
	// A different registration, even with the same subject, is never adopted or deleted.
	current.ID = "foreign-id"
	writeCLIEntry(t, current)
	require.ErrorIs(t, client.Ensure(ctx, id, subject), ErrConflict)
	require.ErrorIs(t, client.Revoke(ctx, id, subject), ErrConflict)
	current.ID = id
	current.Subject.Path = "/tenant/beta/snowflake/account/connection/source"
	writeCLIEntry(t, current)
	require.ErrorIs(t, client.Revoke(ctx, id, subject), ErrConflict)
	current.Subject.Path = subject.Path()
	writeCLIEntry(t, current)
	t.Setenv("SPIRE_TEST_FAIL", "1")
	err = client.Revoke(ctx, id, subject)
	require.ErrorIs(t, err, ErrUnavailable)
	require.NotContains(t, err.Error(), "secret-canary")
	t.Setenv("SPIRE_TEST_FAIL", "")
	require.NoError(t, client.Revoke(ctx, id, subject))
	require.NoError(t, client.Revoke(ctx, id, subject))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, client.Ensure(canceled, id, subject), ErrUnavailable)
}

func writeCLIEntry(t *testing.T, value *entry) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	// #nosec G703 -- the parent test sets this path inside t.TempDir for its subprocess.
	require.NoError(t, os.WriteFile(os.Getenv("SPIRE_TEST_STATE"), data, 0o600))
}

// TestSPIRECLIHelper is a subprocess implementing the pinned CLI JSON envelope.
func TestSPIRECLIHelper(t *testing.T) {
	boundary := slices.Index(os.Args, "--")
	if boundary < 0 {
		return
	}
	if os.Getenv("SPIRE_TEST_FAIL") != "" {
		_, _ = os.Stderr.WriteString("secret-canary\n")
		os.Exit(1)
	}
	args := os.Args[boundary+1:]
	value := func(flag string) string {
		i := slices.Index(args, flag)
		if i < 0 || i+1 == len(args) {
			return ""
		}
		return args[i+1]
	}
	var current *entry
	// #nosec G703 -- only the parent test supplies this disposable fixture path.
	if data, err := os.ReadFile(os.Getenv("SPIRE_TEST_STATE")); err == nil {
		require.NoError(t, json.Unmarshal(data, &current))
	}
	switch args[1] {
	case "show":
		entries := []entry{}
		if current != nil {
			entries = append(entries, *current)
		}
		require.NoError(t, json.NewEncoder(os.Stdout).Encode(map[string]any{"entries": entries}))
	case "create", "update":
		subject := spiffeid.RequireFromString(value("-spiffeID"))
		parent := spiffeid.RequireFromString(value("-parentID"))
		desired := &entry{ID: value("-entryID"), Subject: spiffeID{subject.TrustDomain().String(), subject.Path()}, Parent: spiffeID{parent.TrustDomain().String(), parent.Path()}, JWTTTL: 300}
		require.NoError(t, json.Unmarshal([]byte(`[{"type":"unix","value":"uid:65532"}]`), &desired.Selectors))
		writeCLIEntry(t, desired)
		_, _ = os.Stdout.WriteString(`{"results":[{"status":{"code":0}}]}`)
	case "delete":
		// #nosec G703 -- deletion is limited to the parent test's disposable state file.
		require.NoError(t, os.Remove(os.Getenv("SPIRE_TEST_STATE")))
		_, _ = os.Stdout.WriteString(`{"results":[{"status":{"code":0}}]}`)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestRegistrationOutputLimit(t *testing.T) {
	var output boundedOutput
	// LimitReader follows the same io.Copy writer path as subprocess output.
	_, err := io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", 2<<20)), 2<<20))
	require.ErrorIs(t, err, ErrUnavailable)
	require.LessOrEqual(t, output.buffer.Len(), 1<<20)
}
