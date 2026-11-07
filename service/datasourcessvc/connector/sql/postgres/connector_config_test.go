package postgres

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const postgresSecretForTest = "synthetic-secret-password"

func TestMapPostgresStartupError_InvalidPassword(t *testing.T) {
	t.Parallel()

	err := mapPostgresError(&pgconn.PgError{
		Code:    postgresErrCodeInvalidPassword,
		Message: "password authentication failed for user with " + postgresSecretForTest,
	})

	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.NotContains(t, err.Error(), postgresSecretForTest)
	require.Equal(t, "PostgreSQL rejected the configured username or password. Check the saved credentials and recheck access.", xerror.PublicDetail(err))
	require.NotContains(t, xerror.PublicDetail(err), postgresSecretForTest)
}

func TestMapPostgresStartupError_WrappedInvalidAuthorization(t *testing.T) {
	t.Parallel()

	err := mapPostgresError(
		fmt.Errorf("wrapped postgres auth: %w", &pgconn.PgError{
			Code:    postgresErrCodeInvalidAuthorizationSpecification,
			Message: "role rejected",
		}),
	)

	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

func TestMapPostgresStartupError_NonCredentialFailure(t *testing.T) {
	t.Parallel()

	err := mapPostgresError(fmt.Errorf("connection refused"))

	require.Error(t, err)
	require.False(t, xerror.Is(err, xerror.InvalidCredentials()))
}

func TestNewRejectsPrivateHostWithoutOptIn(t *testing.T) {
	t.Parallel()

	conn, err := New(context.Background(), Config{
		Host:           "127.0.0.1",
		Port:           5432,
		Database:       "app",
		SSLMode:        "verify-full",
		User:           "user",
		Password:       "secret",
		ConnectTimeout: 1,
	})

	require.Nil(t, conn)
	require.ErrorContains(t, err, "non-public ip address")
}

func TestBuildDSN_EncodesCredentialsAndSSLMode(t *testing.T) {
	t.Parallel()

	dsn := buildDSN(Config{
		Host:           "db.example.com",
		Port:           5432,
		Database:       "app",
		SSLMode:        "require",
		User:           "user@corp",
		Password:       "p@ss/w?rd#x%y",
		ConnectTimeout: 5,
	})

	require.Contains(t, dsn, "sslmode=require")
	require.Contains(t, dsn, "connect_timeout=5")
	require.Contains(t, dsn, "user%40corp")
	require.Contains(t, dsn, "p%40ss%2Fw%3Frd%23x%25y")
}

func TestNewUsesRuntimeDialerWithoutPrivateHostOptIn(t *testing.T) {
	t.Parallel()
	called := false
	conn, err := New(t.Context(), Config{
		Host: "127.0.0.1", Port: 5432, Database: "fixture", User: "reader", Password: "secret", SSLMode: "verify-full", ConnectTimeout: 1,
		DialFunc: func(_ context.Context, network, address string) (net.Conn, error) {
			called = true
			require.Equal(t, "tcp", network)
			require.Equal(t, "127.0.0.1:5432", address)
			return nil, fmt.Errorf("pinned dial sentinel")
		},
	})
	require.Nil(t, conn)
	require.True(t, called)
	require.ErrorContains(t, err, "pinned dial sentinel")
}
