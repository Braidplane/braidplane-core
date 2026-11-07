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
