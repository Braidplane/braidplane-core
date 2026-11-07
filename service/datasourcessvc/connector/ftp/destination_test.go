package ftp

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
)

// TestFTPDialFuncPinsControlAndPassiveDataDestinations verifies control and PASV sockets pin destinations independently.
func TestFTPDialFuncPinsControlAndPassiveDataDestinations(t *testing.T) {
	t.Parallel()

	dialedAddresses := make([]string, 0, 2)
	dial := ftpDialFuncWithLookup(
		context.Background(),
		hostguard.NewDestinationPolicy(false),
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			switch host {
			case "ftp.example.com":
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			case "pasv.example.com":
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.35")}}, nil
			default:
				return nil, net.InvalidAddrError(host)
			}
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dialedAddresses = append(dialedAddresses, address)
			serverConn, clientConn := net.Pipe()
			t.Cleanup(func() {
				_ = serverConn.Close()
				_ = clientConn.Close()
			})
			return clientConn, nil
		},
	)

	controlConn, err := dial("tcp", "ftp.example.com:21")
	require.NoError(t, err)
	dataConn, err := dial("tcp", "pasv.example.com:49152")
	require.NoError(t, err)
	require.NotNil(t, controlConn)
	require.NotNil(t, dataConn)
	require.Equal(t, []string{"93.184.216.34:21", "93.184.216.35:49152"}, dialedAddresses)
}

// TestFTPDialFuncRejectsBlockedPassiveDataDestinationBeforeDial verifies blocked PASV destinations fail before socket creation.
func TestFTPDialFuncRejectsBlockedPassiveDataDestinationBeforeDial(t *testing.T) {
	t.Parallel()

	dial := ftpDialFuncWithLookup(
		context.Background(),
		hostguard.NewDestinationPolicy(false),
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dial should not be reached for a rejected destination")
			return nil, net.ErrClosed
		},
	)

	_, err := dial("tcp", "pasv.example.com:49152")
	require.ErrorContains(t, err, "non-public ip address")
}
