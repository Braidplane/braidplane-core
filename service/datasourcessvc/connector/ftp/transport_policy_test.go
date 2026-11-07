package ftp

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsRawFTPBeforeLogin(t *testing.T) {
	t.Parallel()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	commands := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			commands <- err.Error()
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = fmt.Fprint(conn, "220 test FTP\r\n")
		line, _ := bufio.NewReader(conn).ReadString('\n')
		commands <- line
		_, _ = fmt.Fprint(conn, "502 TLS unavailable\r\n")
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	_, err = New(t.Context(), Config{Host: host, Port: number, AllowPrivateHosts: true, ConnectTimeout: 1}, Credentials{Username: "synthetic-user", Password: "synthetic-password"})
	require.Error(t, err)
	require.Equal(t, "AUTH TLS\r\n", <-commands)
}

func TestPassiveDataDialRequiresVerifiedTLS(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	for _, trusted := range []bool{false, true} {
		config := transportsecurity.TLS("127.0.0.1")
		if trusted {
			config.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		}
		dial := ftpTLSDataDialFunc(net.Dial, config)
		control, err := dial("tcp", server.Listener.Addr().String())
		require.NoError(t, err)
		_, encrypted := control.(*tls.Conn)
		require.False(t, encrypted)
		_ = control.Close()
		data, err := dial("tcp", server.Listener.Addr().String())
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err = data.(*tls.Conn).HandshakeContext(ctx)
		cancel()
		_ = data.Close()
		if trusted {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "certificate")
		}
	}
}

func TestFTPSControlHandshakeHonorsTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	for _, cancelHandshake := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelHandshake), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer func() { _ = listener.Close() }()
			firstTLSByte := make(chan byte, 1)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					firstTLSByte <- 0
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
				_, _ = fmt.Fprint(conn, "220 test FTP\r\n")
				line, _ := bufio.NewReader(conn).ReadString('\n')
				if line != "AUTH TLS\r\n" {
					firstTLSByte <- 0
					return
				}
				_, _ = fmt.Fprint(conn, "234 begin TLS\r\n")
				data := make([]byte, 1)
				_, _ = io.ReadFull(conn, data)
				firstTLSByte <- data[0]
				if cancelHandshake {
					cancel()
				}
				_, _ = io.Copy(io.Discard, conn)
			}()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			require.NoError(t, err)
			number, err := strconv.Atoi(port)
			require.NoError(t, err)
			timeout, bound := 1, 3*time.Second
			if cancelHandshake {
				timeout, bound = 5, time.Second
			}
			started := time.Now()
			_, err = New(ctx, Config{Host: host, Port: number, AllowPrivateHosts: true, ConnectTimeout: timeout}, Credentials{Username: "synthetic", Password: "synthetic"})
			require.Error(t, err)
			require.Equal(t, byte(22), <-firstTLSByte)
			require.Less(t, time.Since(started), bound, "TLS negotiation must respect the connection timeout and caller cancellation")
		})
	}
}

func TestFTPSetupDeadlineDoesNotLimitLaterControlRequests(t *testing.T) {
	t.Parallel()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	go func() {
		socket, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = socket.Close() }()
		_ = socket.SetDeadline(time.Now().Add(4 * time.Second))
		_, _ = fmt.Fprint(socket, "220 test FTP\r\n")
		reader := bufio.NewReader(socket)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			code := 200
			switch strings.Fields(line)[0] {
			case "USER":
				code = 230
			case "FEAT":
				code = 211
			case "QUIT":
				_, _ = fmt.Fprint(socket, "221 goodbye\r\n")
				return
			case "NOOP":
				// A reply beyond connect_timeout proves the setup deadline was cleared.
				select {
				case <-time.After(1200 * time.Millisecond):
				case <-t.Context().Done():
					return
				}
			}
			if _, writeErr := fmt.Fprintf(socket, "%d ok\r\n", code); writeErr != nil {
				return
			}
		}
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	connector, err := New(transportsecurity.WithPlaintext(t.Context()), Config{Host: host, Port: number, AllowPrivateHosts: true, ConnectTimeout: 1}, Credentials{Username: "synthetic", Password: "synthetic"})
	require.NoError(t, err)
	defer func() { _ = connector.Close() }()
	require.NoError(t, connector.TestConnection(t.Context()))
}
