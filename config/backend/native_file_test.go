package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileByteLimitConfiguration(t *testing.T) {
	setTestGitHubAppEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"

[execution_limits]
max_file_bytes = 1073741825
`), 0o600))
	cfg, err := LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1073741825, cfg.ExecutionLimits.MaxFileBytes)
	t.Setenv("EXECUTION_LIMITS_MAX_FILE_BYTES", "2147483648")
	cfg, err = LoadWithArgs(path, []string{"--execution-limits-max-file-bytes=3221225472"})
	require.NoError(t, err)
	require.EqualValues(t, 3221225472, cfg.ExecutionLimits.MaxFileBytes)
	_, err = LoadWithArgs(path, []string{"--execution-limits-max-file-bytes=0"})
	require.ErrorContains(t, err, "max_file_bytes")
}
