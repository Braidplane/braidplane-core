package dbpool

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewPoolConfigWithTracingInstallsPGXTracer(t *testing.T) {
	t.Parallel()

	cfg, err := newPoolConfig(
		"postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable",
		WithTracing(),
	)
	require.NoError(t, err)

	require.NotNil(t, cfg.ConnConfig.Tracer)
	require.NotNil(t, cfg.AfterConnect)
}

func TestPoolAuthenticationPreservesStricterPolicy(t *testing.T) {
	for _, tc := range []struct{ configured, expected string }{
		{"", "!md5"},
		{"scram-sha-256", "scram-sha-256"},
		{"md5,scram-sha-256", "scram-sha-256"},
		{"!password,!none", "!password,!none,!md5"},
		{"!md5,!password", "!md5,!password"},
		{"md5", ""},
	} {
		t.Run(tc.configured, func(t *testing.T) {
			t.Setenv("PGREQUIREAUTH", tc.configured)
			cfg, err := parsePoolConfig("postgres://fixture@localhost/fixture?sslmode=disable")
			if tc.expected == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, cfg.ConnConfig.RequireAuth)
		})
	}
}
