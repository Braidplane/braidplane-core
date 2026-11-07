package hostguard

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDialContextPreservesAddressParseError(t *testing.T) {
	t.Parallel()
	_, err := NewDestinationPolicy(false).DialContextWithDialer(t.Context(), "tcp", "missing-port", nil, nil)
	var cause *net.AddrError
	require.ErrorAs(t, err, &cause)
	require.Equal(t, "missing-port", cause.Addr)
	require.ErrorContains(t, err, "split hostguard dial address")
}

func TestIsBlockedHostnameLiteral(t *testing.T) {
	t.Parallel()

	require.True(t, IsBlockedHostnameLiteral("127.0.0.1"))
	require.True(t, IsBlockedHostnameLiteral("[::1]"))
	require.True(t, IsBlockedHostnameLiteral("169.254.169.254"))
	require.False(t, IsBlockedHostnameLiteral("8.8.8.8"))
	require.False(t, IsBlockedHostnameLiteral("example.test"))
}

func TestIsBlockedIP(t *testing.T) {
	t.Parallel()

	require.False(t, IsBlockedIP(nil))
	require.True(t, IsBlockedIP(net.ParseIP("10.0.0.1")))
	require.True(t, IsBlockedIP(net.ParseIP("127.0.0.1")))
	require.True(t, IsBlockedIP(net.ParseIP("169.254.1.1")))
	require.True(t, IsBlockedIP(net.ParseIP("ff02::1")))
	require.False(t, IsBlockedIP(net.ParseIP("8.8.8.8")))
}

func TestIsBlockedIP_PublicOnlyPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		{name: "IPv4Unspecified", ip: "0.0.0.0", blocked: true},
		{name: "IPv4CGNAT", ip: "100.64.0.1", blocked: true},
		{name: "IPv4Multicast", ip: "224.0.0.1", blocked: true},
		{name: "IPv4Benchmarking", ip: "198.18.0.1", blocked: true},
		{name: "IPv4Documentation", ip: "192.0.2.1", blocked: true},
		{name: "IPv6Unspecified", ip: "::", blocked: true},
		{name: "IPv6Multicast", ip: "ff00::1", blocked: true},
		{name: "IPv6Documentation", ip: "2001:db8::1", blocked: true},
		{name: "PublicIPv4", ip: "93.184.216.34", blocked: false},
		{name: "PublicIPv6", ip: "2606:2800:220:1:248:1893:25c8:1946", blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.blocked, IsBlockedIP(net.ParseIP(tt.ip)))
		})
	}
}

func TestValidateResolvedPublicHostHandlesLiteralIPs(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateResolvedPublicHost(context.Background(), "8.8.8.8"))
	require.ErrorContains(t, ValidateResolvedPublicHost(context.Background(), "127.0.0.1"), "non-public ip address")
	require.ErrorContains(t, ValidateResolvedPublicHost(context.Background(), "   "), "hostname is empty")
}

func TestDialPublicHostWithDialerDialsResolvedPublicIP(t *testing.T) {
	t.Parallel()

	var dialedAddress string
	conn, err := DialPublicHostWithDialer(
		context.Background(),
		"tcp",
		"db.example.com:5432",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialedAddress = address
			serverConn, clientConn := net.Pipe()
			t.Cleanup(func() {
				_ = serverConn.Close()
				_ = clientConn.Close()
			})
			return clientConn, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, "93.184.216.34:5432", dialedAddress)
}

func TestDialPublicHostWithDialerRejectsPrivateResolvedIPBeforeDial(t *testing.T) {
	t.Parallel()

	dialed := false
	conn, err := DialPublicHostWithDialer(
		context.Background(),
		"tcp",
		"db.example.com:5432",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, net.ErrClosed
		},
	)

	require.Nil(t, conn)
	require.ErrorContains(t, err, "non-public ip address")
	require.False(t, dialed)
}

// TestDestinationPolicyPinsFirstResolutionAgainstRebinding verifies a socket uses its first
// vetted DNS answer and cannot be redirected by a later DNS rebinding answer.
func TestDestinationPolicyPinsFirstResolutionAgainstRebinding(t *testing.T) {
	t.Parallel()

	lookups := 0
	dialedAddress := ""
	conn, err := NewDestinationPolicy(false).DialContextWithDialer(
		context.Background(),
		"tcp",
		"git.example.com:443",
		func(context.Context, string) ([]net.IPAddr, error) {
			lookups++
			if lookups == 1 {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialedAddress = address
			serverConn, clientConn := net.Pipe()
			t.Cleanup(func() {
				_ = serverConn.Close()
				_ = clientConn.Close()
			})
			return clientConn, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 1, lookups)
	require.Equal(t, "93.184.216.34:443", dialedAddress)
}

// TestDestinationPolicyRejectsMixedPublicAndPrivateAnswers verifies mixed DNS results fail
// closed so a public answer cannot permit access to a private destination.
func TestDestinationPolicyRejectsMixedPublicAndPrivateAnswers(t *testing.T) {
	t.Parallel()

	dialed := false
	_, err := NewDestinationPolicy(false).DialContextWithDialer(
		context.Background(),
		"tcp",
		"s3.example.com:443",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("93.184.216.34")},
				{IP: net.ParseIP("169.254.169.254")},
			}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, net.ErrClosed
		},
	)

	require.ErrorContains(t, err, "non-public ip address")
	require.False(t, dialed)
}

// TestDestinationPolicyPinsPrivateHostWhenExplicitlyAllowed verifies private destinations are
// reachable only when the connector policy explicitly permits private-host access.
func TestDestinationPolicyPinsPrivateHostWhenExplicitlyAllowed(t *testing.T) {
	t.Parallel()

	dialedAddress := ""
	conn, err := NewDestinationPolicy(true).DialContextWithDialer(
		context.Background(),
		"tcp",
		"ftp.internal.example:21",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialedAddress = address
			serverConn, clientConn := net.Pipe()
			t.Cleanup(func() {
				_ = serverConn.Close()
				_ = clientConn.Close()
			})
			return clientConn, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, "127.0.0.1:21", dialedAddress)
}

// TestDestinationPolicyPreservesIPv6ScopeZone verifies scoped IPv6 destinations retain the
// interface required to reach an explicitly allowed link-local address.
func TestDestinationPolicyPreservesIPv6ScopeZone(t *testing.T) {
	t.Parallel()

	dialedAddress := ""
	conn, err := NewDestinationPolicy(true).DialContextWithDialer(
		context.Background(),
		"tcp6",
		"ftp.internal.example:21",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("fe80::1"), Zone: "en0"}}, nil
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialedAddress = address
			serverConn, clientConn := net.Pipe()
			t.Cleanup(func() {
				_ = serverConn.Close()
				_ = clientConn.Close()
			})
			return clientConn, nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, "[fe80::1%en0]:21", dialedAddress)
}

// TestDestinationPolicyWrapsDialFailure verifies errors.Is compatibility survives the typed xerror wrapper.
func TestDestinationPolicyWrapsDialFailure(t *testing.T) {
	t.Parallel()

	_, err := NewDestinationPolicy(false).DialContextWithDialer(
		context.Background(),
		"tcp",
		"ftp.example.com:21",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			return nil, net.ErrClosed
		},
	)

	require.ErrorIs(t, err, net.ErrClosed)
}
