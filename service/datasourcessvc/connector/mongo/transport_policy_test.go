package mongo_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"

	"github.com/stretchr/testify/require"
)

func TestMongoRejectsUntrustedTLSBeforeSendingDatabaseTraffic(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted connection accepted") }))
	defer server.Close()
	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	_, err = connector.New(t.Context(), connector.Config{Host: host, Port: number, Database: "test", ConnectTimeout: 1, AllowPrivateHosts: true}, connector.Credentials{Username: "synthetic", Password: "synthetic"})
	require.ErrorContains(t, err, "certificate")
}

func TestCollectionMetadataStartsWithTLSWithoutDevelopmentPolicy(t *testing.T) {
	t.Parallel()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	first := make(chan byte, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			first <- 0
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		data := make([]byte, 1)
		_, _ = conn.Read(data)
		first <- data[0]
		_ = listener.Close()
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.ParseUint(port, 10, 16)
	require.NoError(t, err)
	observer := connector.CollectionMetadataObserver{Host: host, Port: uint16(number), Database: "test", Collection: "test"}
	plan, err := observer.Plan()
	require.NoError(t, err)
	transport := &testutil.ProofNativeTransport{Plan: plan, Address: listener.Addr().String()}
	_, _, outcome := observer.ReadMetadata(t.Context(), transport, []byte(`{"username":"synthetic","password":"synthetic"}`), proofevidence.Limits{Timeout: 300 * time.Millisecond, MaxRows: 1, MaxBytes: 1024})
	require.Equal(t, proofevidence.OutcomeProviderError, outcome)
	require.Equal(t, byte(22), <-first, "metadata must begin with a TLS handshake")
}
