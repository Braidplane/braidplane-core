package proofevidencesvc

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/stretchr/testify/require"
)

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestMetadataTransportClosesProviderBodyBeforeReturningBufferedMetadata(t *testing.T) {
	t.Parallel()
	const address = "https://bigquery.googleapis.com/metadata"
	body := &trackedBody{Reader: strings.NewReader("{}")}
	transport := &metadataTransport{grant: testutil.AdmitProofEvidence(t, testutil.ProofEvidenceRequest()), routes: []route{{http.MethodGet, address}}, next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
	})}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	require.NoError(t, err)
	res, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, res.Body.Close()) }()
	require.True(t, body.closed)
	buffered, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "{}", string(buffered))
}

func TestMetadataTransportRejectsUnapprovedRequestsBeforeDispatch(t *testing.T) {
	t.Parallel()
	const allowed = "https://bigquery.googleapis.com/bigquery/v2/projects/project-1/datasets/crm/tables/contacts"
	for _, tc := range []struct{ name, method, url, host, body string }{
		{name: "Write", method: http.MethodPost, url: allowed + "/insertAll"},
		{name: "Records", method: http.MethodGet, url: allowed + "/data"},
		{name: "Query", method: http.MethodGet, url: allowed + "?selectedFields=private"},
		{name: "AnotherTable", method: http.MethodGet, url: allowed + "-other"},
		{name: "AnotherHost", method: http.MethodGet, url: "https://attacker.example"},
		{name: "HostOverride", method: http.MethodGet, url: allowed, host: "attacker.example"},
		{name: "Body", method: http.MethodGet, url: allowed, body: "unexpected"},
		{name: "Plaintext", method: http.MethodGet, url: strings.Replace(allowed, "https:", "http:", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := testutil.AdmitProofEvidence(t, testutil.ProofEvidenceRequest())
			transport := &metadataTransport{grant: grant, routes: []route{{http.MethodGet, allowed}}, next: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("unexpected dispatch")
				return nil, proofevidence.ErrAuthorizationDenied
			})}
			req, err := http.NewRequestWithContext(t.Context(), tc.method, tc.url, strings.NewReader(tc.body))
			require.NoError(t, err)
			if tc.host != "" {
				req.Host = tc.host
			}
			rejected, err := transport.RoundTrip(req)
			if rejected != nil {
				require.NoError(t, rejected.Body.Close())
			}
			require.Nil(t, rejected)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			require.Equal(t, proofevidence.OutcomeDenied, transport.outcome)
			require.Zero(t, transport.usage.Requests)
		})
	}
}

func TestMetadataTransportChargesAcrossRequestsAndRejectsReplay(t *testing.T) {
	t.Parallel()
	for _, maximum := range []int64{3, 4} {
		r := testutil.ProofEvidenceRequest()
		r.Limits.MaxBytes = maximum
		grant := testutil.AdmitProofEvidence(t, r)
		const address = "https://bigquery.googleapis.com/metadata"
		transport := &metadataTransport{grant: grant, routes: []route{{http.MethodGet, address}, {http.MethodGet, address}}, next: roundTripFunc(func(*http.Request) (*http.Response, error) { return response("{}", 200), nil })}
		for i := range 2 {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
			require.NoError(t, err)
			res, err := transport.RoundTrip(req)
			if i == 1 && maximum == 3 {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
				require.Equal(t, proofevidence.OutcomeOverBudget, transport.outcome)
			} else {
				require.NoError(t, err)
				require.NoError(t, res.Body.Close())
			}
		}
		require.EqualValues(t, 4, transport.usage.Bytes)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
		require.NoError(t, err)
		rejected, err := transport.RoundTrip(req)
		if rejected != nil {
			require.NoError(t, rejected.Body.Close())
		}
		require.Nil(t, rejected)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
		require.EqualValues(t, 2, transport.usage.Requests)
	}
}

func TestMetadataTransportCancellationCannotBecomeComplete(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const address = "https://bigquery.googleapis.com/metadata"
	transport := &metadataTransport{grant: testutil.AdmitProofEvidence(t, testutil.ProofEvidenceRequest()), routes: []route{{http.MethodGet, address}}, next: roundTripFunc(func(*http.Request) (*http.Response, error) { cancel(); return response("{}", 200), nil })}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	require.NoError(t, err)
	rejected, err := transport.RoundTrip(req)
	if rejected != nil {
		require.NoError(t, rejected.Body.Close())
	}
	require.Nil(t, rejected)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.Equal(t, proofevidence.OutcomeCanceled, transport.outcome)
}

func TestNativeTransportChargesOnlyTheAdmittedOperationAndSocketBytes(t *testing.T) {
	t.Parallel()
	request := testutil.ProofEvidenceRequest()
	request.ConnectorType, request.Operation = model.DataSourceTypePostgres, "postgres.table_metadata"
	request.Target = proofevidence.Endpoint{Scheme: "postgres", Host: "database.example.com", Port: 5432}
	request.Egress = []proofevidence.Endpoint{request.Target}
	request.Limits.MaxBytes = 4
	grant := testutil.AdmitProofEvidence(t, request)
	transport := &metadataTransport{grant: grant, routes: []route{{"metadata", "postgres.table_metadata"}}}
	require.NoError(t, transport.BeginNative(proofevidence.MetadataRoute{Method: "metadata", URL: "postgres.table_metadata"}))
	require.EqualValues(t, 1, transport.usage.Requests)
	_, err := transport.DialContext(t.Context(), "tcp", "another.example.com:5432")
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.ErrorIs(t, transport.BeginNative(proofevidence.MetadataRoute{Method: "metadata", URL: "postgres.table_metadata"}), proofevidence.ErrAuthorizationDenied)

	transport = &metadataTransport{grant: grant}
	local, remote := net.Pipe()
	defer func() { require.NoError(t, local.Close()) }()
	defer func() { require.NoError(t, remote.Close()) }()
	connection := &metadataConn{Conn: local, transport: transport}
	done := make(chan error, 1)
	go func() { _, err := remote.Write([]byte("12345")); done <- err }()
	buffer := make([]byte, 10)
	n, err := connection.Read(buffer)
	require.Equal(t, 5, n)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.NoError(t, <-done)
	require.EqualValues(t, 5, transport.usage.Bytes)
	require.Equal(t, proofevidence.OutcomeOverBudget, transport.outcome)
	_, err = connection.Write([]byte("x"))
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
}

func TestNativeDriverCannotRemoveAdmittedDeadline(t *testing.T) {
	t.Parallel()
	request := testutil.ProofEvidenceRequest()
	request.Limits.Timeout = 100 * time.Millisecond
	grant := testutil.AdmitProofEvidence(t, request)
	local, remote := net.Pipe()
	defer func() { require.NoError(t, local.Close()) }()
	defer func() { require.NoError(t, remote.Close()) }()
	connection := &metadataConn{Conn: local, transport: &metadataTransport{grant: grant}}
	require.NoError(t, connection.SetDeadline(time.Now().Add(time.Hour)))
	require.NoError(t, connection.SetReadDeadline(time.Time{}))
	require.NoError(t, connection.SetWriteDeadline(time.Time{}))
	_, err := connection.Read(make([]byte, 1))
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
}
