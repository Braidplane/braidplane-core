package mysql_test

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/stretchr/testify/require"
)

func TestMySQLRequiresTLSBeforeAuthentication(t *testing.T) {
	t.Parallel()
	certificate := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certificate.Close()
	for _, scenario := range []struct{ tls, metadata bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		supportsTLS := scenario.tls
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				result <- err
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			capabilities := uint32(1 | 0x200 | 0x8000 | 0x80000)
			if supportsTLS {
				capabilities |= 0x800
			}
			greeting := append([]byte{10}, []byte("8.0.0\x00")...)
			greeting = append(greeting, 1, 0, 0, 0)
			greeting = append(greeting, []byte("12345678\x00")...)
			greeting = binary.LittleEndian.AppendUint16(greeting, uint16(capabilities&0xffff))
			greeting = append(greeting, 33, 2, 0)
			greeting = binary.LittleEndian.AppendUint16(greeting, uint16(capabilities>>16))
			greeting = append(greeting, 21)
			greeting = append(greeting, make([]byte, 10)...)
			greeting = append(greeting, []byte("abcdefghijkl\x00mysql_native_password\x00")...)
			packet := append([]byte{byte(len(greeting) & 0xff), 0, 0, 0}, greeting...)
			if _, err := conn.Write(packet); err != nil {
				result <- err
				return
			}
			header := make([]byte, 4)
			if _, err := io.ReadFull(conn, header); err != nil {
				result <- err
				return
			}
			length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
			// Only the SSL request may precede verified TLS; authentication carries more bytes.
			if length != 32 {
				result <- io.ErrUnexpectedEOF
				return
			}
			request := make([]byte, length)
			if _, err := io.ReadFull(conn, request); err != nil {
				result <- err
				return
			}
			if binary.LittleEndian.Uint32(request)&0x800 == 0 {
				result <- io.ErrUnexpectedEOF
				return
			}
			secured := tls.Server(conn, certificate.TLS)
			result <- secured.HandshakeContext(t.Context())
		}()
		host, port, err := net.SplitHostPort(listener.Addr().String())
		require.NoError(t, err)
		number, err := strconv.Atoi(port)
		require.NoError(t, err)
		if scenario.metadata {
			portValue, parseErr := strconv.ParseUint(port, 10, 16)
			require.NoError(t, parseErr)
			observer := connector.TableMetadataObserver{TableMetadataResource: sqlconnector.TableMetadataResource{Host: host, Port: uint16(portValue), Database: "test", Schema: "test", Table: "test"}}
			plan, planErr := observer.Plan()
			require.NoError(t, planErr)
			transport := &testutil.ProofNativeTransport{Plan: plan, Address: listener.Addr().String()}
			_, _, outcome := observer.ReadMetadata(t.Context(), transport, []byte(`{"username":"synthetic","password":"synthetic"}`), proofevidence.Limits{Timeout: time.Second, MaxRows: 1, MaxBytes: 1024})
			require.Equal(t, proofevidence.OutcomeProviderError, outcome)
		} else {
			_, err = connector.New(t.Context(), connector.Config{Host: host, Port: number, Database: "test", User: "synthetic", Password: "synthetic", ConnectTimeout: 1, AllowPrivateHosts: true})
			require.Error(t, err)
			if supportsTLS {
				require.ErrorContains(t, err, "certificate")
			} else {
				require.ErrorContains(t, err, "TLS")
			}
		}
		serverErr := <-result
		if supportsTLS {
			require.ErrorContains(t, serverErr, "certificate")
		} else {
			require.ErrorIs(t, serverErr, io.EOF)
		}

		_ = listener.Close()
	}
}
