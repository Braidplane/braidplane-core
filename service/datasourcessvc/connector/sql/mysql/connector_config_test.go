package mysql

import (
	"context"
	"fmt"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestMapStartupErrorClassifiesMySQLFailuresAndPreservesCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		number uint16
		want   error
	}{
		{name: "invalid credentials", number: 1045, want: xerror.InvalidCredentials()},
		{name: "forbidden", number: 1044, want: xerror.Forbidden(xerror.DataSource)},
		{name: "database not found", number: 1049, want: xerror.NotFound(xerror.DataSource)},
		{name: "unreachable", number: 2003, want: xerror.ConnectionFailed(xerror.DataSource)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := &mysqldriver.MySQLError{Number: tt.number, Message: "provider diagnostic"}
			err := mapStartupError(fmt.Errorf("startup: %w", cause))

			require.True(t, xerror.Is(err, tt.want))
			var got *mysqldriver.MySQLError
			require.True(t, xerror.As(err, &got))
			require.Same(t, cause, got)
			require.Empty(t, xerror.PublicDetail(err))
		})
	}
}

func TestNewRejectsPrivateHostWithoutOptIn(t *testing.T) {
	t.Parallel()

	conn, err := New(context.Background(), Config{
		Host:              "127.0.0.1",
		Port:              3306,
		Database:          "app",
		User:              "user",
		Password:          "secret",
		ConnectTimeout:    1,
		AllowPrivateHosts: false,
	})

	require.Nil(t, conn)
	require.ErrorContains(t, err, "non-public ip address")
}
