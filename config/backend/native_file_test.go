package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNativeFileProfileConfiguration(t *testing.T) {
	setTestGitHubAppEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"

[execution_limits.native_files]
organization = "fixture"
max_bytes = 1073741825
timeout = "5m"
transfers = 1
connectors = 2
`), 0o600))
	cfg, err := LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1073741825, cfg.ExecutionLimits.NativeFiles.MaxBytes)
	require.Equal(t, 5*time.Minute, cfg.ExecutionLimits.NativeFiles.Timeout.TimeDuration())
	t.Setenv("NATIVE_FILE_PROFILE_MAX_BYTES", "2147483648")
	t.Setenv("NATIVE_FILE_PROFILE_CONNECTORS", "3")
	cfg, err = LoadWithArgs(path, []string{"--native-file-profile-max-bytes=3221225472", "--native-file-profile-transfers=2"})
	require.NoError(t, err)
	require.EqualValues(t, 3221225472, cfg.ExecutionLimits.NativeFiles.MaxBytes)
	require.Equal(t, 3, cfg.ExecutionLimits.NativeFiles.Connectors)
	require.Equal(t, 2, cfg.ExecutionLimits.NativeFiles.Transfers)
	_, err = LoadWithArgs(path, []string{"--native-file-profile-transfers=0"})
	require.ErrorContains(t, err, "native file profile")
}

func TestNativeFileProfileDisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg := validBackendConfig()
	require.Equal(t, NativeFileProfileConfig{}, cfg.ExecutionLimits.NativeFiles)
	require.NoError(t, cfg.validateExecutionLimits())
	cfg.ExecutionLimits.NativeFiles.Organization = "fixture"
	require.ErrorContains(t, cfg.validateExecutionLimits(), "native file profile")
}
