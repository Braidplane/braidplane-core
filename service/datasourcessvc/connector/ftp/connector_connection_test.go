package ftp

import (
	"fmt"
	"net/textproto"
	"testing"

	ftpclient "github.com/jlaffaye/ftp"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestMapConnectionErrorClassifiesFTPLoginRejectionAndPreservesCause(t *testing.T) {
	t.Parallel()

	cause := &textproto.Error{Code: ftpclient.StatusNotLoggedIn, Msg: "provider diagnostic"}
	err := mapConnectionError(fmt.Errorf("login: %w", cause))

	require.True(t, xerror.Is(err, xerror.InvalidCredentials()))
	var got *textproto.Error
	require.True(t, xerror.As(err, &got))
	require.Same(t, cause, got)
	require.Empty(t, xerror.PublicDetail(err))
}

func TestMapConnectionErrorKeepsFTPNetworkFailuresAsConnectionFailures(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("network unavailable")
	err := mapConnectionError(cause)

	require.True(t, xerror.Is(err, xerror.ConnectionFailed(xerror.DataSource)))
	require.True(t, xerror.Is(err, cause))
}
