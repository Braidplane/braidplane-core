package client

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchesObjectRequiresExactPreparedOutput(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, metadata string
		expectedMetadata                  map[string]string
		status                            int
		matched                           bool
	}{
		{name: "match", body: "original", contentType: "application/json", metadata: "fixture", status: 200, matched: true},
		{name: "uppercase key", body: "original", contentType: "application/json", metadata: "fixture", expectedMetadata: map[string]string{"Source": "fixture"}, status: 200, matched: true},
		{name: "conflicting keys", body: "original", contentType: "application/json", metadata: "fixture", expectedMetadata: map[string]string{"Source": "fixture", "source": "other"}, status: 200},
		{name: "different bytes", body: "modified", contentType: "application/json", metadata: "fixture", status: 200},
		{name: "different size", body: "larger output", contentType: "application/json", metadata: "fixture", status: 200},
		{name: "different type", body: "original", contentType: "text/plain", metadata: "fixture", status: 200},
		{name: "different metadata", body: "original", contentType: "application/json", metadata: "other", status: 200},
		{name: "missing", status: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, http.MethodGet, r.Method, "reconciliation must never write")
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Amz-Meta-Source", tc.metadata)
				w.WriteHeader(tc.status)
				_, err := w.Write([]byte(tc.body))
				assert.NoError(t, err)
			}))
			defer server.Close()
			client, err := New(transportsecurity.WithPlaintext(t.Context()), Config{Region: "test", Endpoint: server.URL, ForcePathStyle: true, AllowPrivateHosts: true}, Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture-secret"})
			require.NoError(t, err)
			defer func() { require.NoError(t, client.Close()) }()
			expected := tc.expectedMetadata
			if expected == nil {
				expected = map[string]string{"source": "fixture"}
			}
			matched, err := client.MatchesObject(t.Context(), MatchObjectRequest{Bucket: "fixture", Key: "output.json", SizeBytes: 8, SHA256: ContentDigest([]byte("original")), ContentType: "application/json", Metadata: expected})
			if tc.status == 404 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.matched, matched)
			require.EqualValues(t, 1, requests.Load())
		})
	}
}
