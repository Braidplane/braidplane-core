package gitprovider

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSRFProtectedDialContext_DialsResolvedIPAddress(t *testing.T) {
	t.Parallel()

	var dialedNetwork string
	var dialedAddress string
	var resolvedHosts []string

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	dialContext := SSRFProtectedDialContextWithLookup(
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			resolvedHosts = append(resolvedHosts, host)
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(_ context.Context, network, address string) (net.Conn, error) {
			dialedNetwork = network
			dialedAddress = address
			return clientConn, nil
		},
	)

	conn, err := dialContext(context.Background(), "tcp", "api.example.test:443")
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, []string{"api.example.test"}, resolvedHosts)
	require.Equal(t, "tcp", dialedNetwork)
	require.Equal(t, "93.184.216.34:443", dialedAddress)
}

func TestSSRFProtectedDialContext_RejectsNonPublicResolvedIPAddress(t *testing.T) {
	t.Parallel()

	dialCalled := false

	dialContext := SSRFProtectedDialContextWithLookup(
		func(_ context.Context, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(_ context.Context, _, _ string) (net.Conn, error) {
			dialCalled = true
			return nil, assert.AnError
		},
	)

	_, err := dialContext(context.Background(), "tcp", "api.example.test:443")
	require.Error(t, err)
	require.ErrorContains(t, err, "non-public ip address")
	require.False(t, dialCalled)
}
