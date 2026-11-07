package datasourcessvc

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/stretchr/testify/require"
)

func TestConnectionTestDiagnostic(t *testing.T) {
	t.Parallel()
	const secret = "private-token-and-provider-body"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"explicit", xerror.InvalidCredentials().WithPublicDetail("Reconnect the selected account."), "Reconnect the selected account."},
		{"authentication", fmt.Errorf("%w: %w", ErrConnectionFailed, xerror.InvalidCredentials().Detail(secret)), "Authentication failed."},
		{"permission", ErrConnectionFailed.With(xerror.Forbidden(xerror.DataSource).Detail(secret)), "Access denied."},
		{"missing resource", xerror.NotFound(xerror.DataSource).Detail(secret), "selected resource was not found"},
		{"configuration", xerror.InvalidInput(xerror.DataSource).Detail(secret), "settings are invalid or incomplete"},
		{"timeout", ErrConnectionFailed.With(context.DeadlineExceeded), "timed out"},
		{"typed timeout", xerror.Timeout(xerror.DataSource), "timed out"},
		{"canceled", ErrConnectionFailed.With(context.Canceled), "was canceled"},
		{"refused", ErrConnectionFailed.With(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), "Could not reach"},
		{"dns", xerror.Unexpected().With(&net.DNSError{Err: secret, Name: secret, IsNotFound: true}), "hostname could not be resolved"},
		{"tls", xerror.Unexpected().With(x509.UnknownAuthorityError{}), "TLS connection failed"},
		{"service unavailable", xerror.Unavailable(xerror.DataSource), "data source service is unavailable"},
		{"execution unavailable", xerror.ExecutionUnavailable(), "execution service is unavailable"},
		{"unknown", xerror.Unexpected().Detail(secret), "Connection test failed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			detail := ConnectionTestDiagnostic(tc.err)
			require.Contains(t, detail, tc.want)
			require.NotContains(t, detail, secret)
		})
	}
	require.Empty(t, ConnectionTestDiagnostic(nil))
}

func TestLocalConnectionTestPreservesTypedDiagnostic(t *testing.T) {
	t.Parallel()
	svc := &service{connectionTester: localConnectionTestExecutor{factory: func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return nil, xerror.InvalidCredentials().Detail("private-provider-password")
	}}}
	err := svc.runConnectionTest(t.Context(), "acme", "source", model.DataSourceTypeS3, nil, nil)
	require.ErrorIs(t, err, ErrConnectionFailed)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.Contains(t, xerror.PublicDetail(err), "Authentication failed.")
	require.NotContains(t, xerror.PublicDetail(err), "private-provider-password")
}
