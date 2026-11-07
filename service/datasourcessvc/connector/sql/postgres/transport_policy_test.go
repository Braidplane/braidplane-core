package postgres_test

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/stretchr/testify/require"
)

func TestSelectedTableMetadataRejectsUnverifiedTransportBeforeDial(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"disable", "require", "verify-ca"} {
		observer := connector.TableMetadataObserver{TableMetadataResource: sqlconnector.TableMetadataResource{Host: "example.test", Port: 5432, Database: "test", Schema: "public", Table: "test", SSLMode: mode}}
		plan, err := observer.Plan()
		require.NoError(t, err)
		transport := &testutil.ProofNativeTransport{Plan: plan, Address: "example.test:5432"}
		_, _, outcome := observer.ReadMetadata(t.Context(), transport, []byte(`{"username":"synthetic","password":"synthetic"}`), proofevidence.Limits{Timeout: time.Second, MaxRows: 1, MaxBytes: 1024})
		require.Equal(t, proofevidence.OutcomeDenied, outcome)
		require.Zero(t, transport.Dials)
	}
}

func TestPostgresDefaultRejectsUntrustedTLSBeforeAuthentication(t *testing.T) {
	t.Parallel()
	certificate := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certificate.Close()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	result := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			result <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		request := make([]byte, 8)
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			result <- readErr
			return
		}
		if !bytes.Equal(request, []byte{0, 0, 0, 8, 4, 210, 22, 47}) {
			result <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := conn.Write([]byte{'S'}); writeErr != nil {
			result <- writeErr
			return
		}
		result <- tls.Server(conn, certificate.TLS).HandshakeContext(t.Context())
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	_, err = connector.New(t.Context(), connector.Config{Host: host, Port: number, Database: "test", User: "synthetic", Password: "synthetic", ConnectTimeout: 1, AllowPrivateHosts: true})
	require.ErrorContains(t, err, "certificate")
	require.ErrorContains(t, <-result, "certificate")
}
