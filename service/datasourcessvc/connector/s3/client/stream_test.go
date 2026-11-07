package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func streamTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(transportsecurity.WithPlaintext(t.Context()), Config{Endpoint: server.URL, Region: "us-east-1", ForcePathStyle: true, AllowPrivateHosts: true}, Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func streamDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestReadObjectToEnforcesLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int64{4, 5, 6} {
		t.Run(strconv.FormatInt(limit, 10), func(t *testing.T) {
			client := streamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "5")
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = io.WriteString(w, "hello")
			})
			var output bytes.Buffer
			result, err := client.ReadObjectTo(transportsecurity.WithPlaintext(t.Context()), GetObjectRequest{Bucket: "fixture", Key: "input", MaxBytes: limit}, &output)
			if limit < 5 {
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Empty(t, output.Bytes())
				return
			}
			require.NoError(t, err)
			require.Equal(t, "hello", output.String())
			require.Equal(t, int64(5), result.SizeBytes)
			require.Equal(t, streamDigest(output.Bytes()), result.SHA256)
		})
	}
}

func TestPutObjectStreamVerifiesBeforePublication(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("a"), streamPartBytes+3)
	for _, scenario := range []string{"success", "checksum", "short", "long", "limit", "cancel", "completion"} {
		t.Run(scenario, func(t *testing.T) {
			var sizes []int
			uploadedHash := sha256.New()
			published, aborted := false, false
			ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(t.Context()))
			defer cancel()
			client := streamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
					_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>fixture-upload</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == http.MethodPut:
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					sizes = append(sizes, len(body))
					_, _ = uploadedHash.Write(body)
					w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, len(sizes)))
					if scenario == "cancel" {
						cancel()
					}
				case r.Method == http.MethodPost:
					published = true
					assert.Equal(t, "*", r.Header.Get("If-None-Match"))
					if scenario == "completion" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `<Error><Code>InvalidRequest</Code></Error>`)
						return
					}
					_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
				case r.Method == http.MethodDelete:
					aborted = true
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s", r.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			req := StreamWriteRequest{Bucket: "fixture", Key: "output", Body: bytes.NewReader(content), SizeBytes: int64(len(content)), MaxBytes: int64(len(content)), SHA256: streamDigest(content)}
			switch scenario {
			case "checksum":
				req.SHA256 = strings.Repeat("0", 64)
			case "short":
				req.SizeBytes++
				req.MaxBytes++
			case "long":
				req.SizeBytes--
			case "limit":
				req.MaxBytes--
			}
			result, err := client.PutObjectStream(ctx, req)
			switch scenario {
			case "success":
				require.NoError(t, err)
				require.Equal(t, []int{streamPartBytes, 3}, sizes)
				require.Equal(t, req.SHA256, hex.EncodeToString(uploadedHash.Sum(nil)))
				require.True(t, published)
				require.False(t, aborted)
				require.Equal(t, int64(len(content)), result.WrittenBytes)
			case "completion":
				require.ErrorIs(t, err, xerror.Unexpected())
				require.True(t, result.OutcomeUnknown)
				require.True(t, aborted)
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
				require.False(t, published)
				require.True(t, aborted)
			default:
				require.Error(t, err)
				require.False(t, published)
				require.Equal(t, scenario != "limit", aborted)
			}
		})
	}
}
