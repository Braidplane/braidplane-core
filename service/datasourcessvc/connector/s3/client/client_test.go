package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNewRejectsMissingCredentials(t *testing.T) {
	t.Parallel()

	_, err := New(transportsecurity.WithPlaintext(context.Background()), Config{Region: "us-east-1"}, Credentials{})

	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

func TestParseEndpointValidatesSchemeAndHost(t *testing.T) {
	t.Parallel()

	_, err := parseEndpoint("ftp://s3.example.com")
	require.ErrorIs(t, err, xerror.ValidationFailed())

	_, err = parseEndpoint("https:///missing-host")
	require.ErrorIs(t, err, xerror.ValidationFailed())

	parsed, err := parseEndpoint("https://s3.example.com")
	require.NoError(t, err)
	require.Equal(t, "s3.example.com", parsed.Hostname())
}

func TestPinnedHTTPClientRejectsBlockedRedirectDestination(t *testing.T) {
	t.Parallel()

	client, err := newPinnedHTTPClient(defaultTimeout, hostguard.NewDestinationPolicy(false))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.Proxy)

	_, err = transport.DialContext(transportsecurity.WithPlaintext(context.Background()), "tcp", "127.0.0.1:80")
	require.ErrorContains(t, err, "non-public ip address")

	req, err := http.NewRequestWithContext(
		transportsecurity.WithPlaintext(context.Background()),
		http.MethodGet,
		"http://169.254.169.254/latest/meta-data",
		nil,
	)
	require.NoError(t, err)

	err = client.CheckRedirect(req, nil)

	require.ErrorContains(t, err, "non-public ip address")

	err = client.CheckRedirect(req, make([]*http.Request, 5))
	require.True(t, xerror.Is(err, xerror.ExecutionRedirectLimitExceeded()))
}

func TestReadLimitedRejectsOversizedBodies(t *testing.T) {
	t.Parallel()

	_, err := readLimited(strings.NewReader("abcdef"), 5)

	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestScopedRequestRejectsRedirectBeforeResolvingDestination(t *testing.T) {
	t.Parallel()
	client, err := newPinnedHTTPClient(defaultTimeout, hostguard.NewDestinationPolicy(true))
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)
	req, err := http.NewRequestWithContext(WithRedirectsDisabled(transportsecurity.WithPlaintext(t.Context())), http.MethodGet, "https://unpermitted.example/private", nil)
	require.NoError(t, err)
	require.ErrorIs(t, client.CheckRedirect(req, nil), xerror.ExecutionRedirectLimitExceeded())
}

func TestReadLimitedCopiesBodiesWithinLimit(t *testing.T) {
	t.Parallel()

	content, err := readLimited(strings.NewReader("abcde"), 5)

	require.NoError(t, err)
	require.Equal(t, []byte("abcde"), content)
}

func TestClientExecutesPathStyleProviderOperations(t *testing.T) {
	t.Parallel()

	var putBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/braidplane-fixtures":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/braidplane-fixtures" &&
			r.URL.Query().Get("list-type") == "2":
			assert.Equal(t, "imports/", r.URL.Query().Get("prefix"))
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>braidplane-fixtures</Name>
  <Prefix>imports/</Prefix>
  <KeyCount>1</KeyCount>
  <MaxKeys>1000</MaxKeys>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>imports/customers.csv</Key>
    <LastModified>2026-01-01T00:00:00.000Z</LastModified>
    <ETag>"etag-1"</ETag>
    <Size>14</Size>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
</ListBucketResult>`))
		case r.Method == http.MethodGet && r.URL.Path == "/braidplane-fixtures/imports/customers.csv":
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("X-Amz-Meta-Source", "fixture")
			_, _ = w.Write([]byte("id,name\n1,Ada\n"))
		case r.Method == http.MethodHead && r.URL.Path == "/braidplane-fixtures/exports/existing.json":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead && r.URL.Path == "/braidplane-fixtures/exports/new.json":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/braidplane-fixtures/exports/existing.json":
			t.Errorf("PutObject should not PUT an existing object when overwrite is false")
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodPut && r.URL.Path == "/braidplane-fixtures/exports/new.json":
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "run-001", r.Header.Get("X-Amz-Meta-Run"))
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			putBody = body
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected S3 fixture request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	client, err := New(transportsecurity.WithPlaintext(context.Background()), Config{
		Endpoint:          server.URL,
		Region:            "us-east-1",
		ForcePathStyle:    true,
		TimeoutSeconds:    5,
		AllowPrivateHosts: true,
	}, Credentials{AccessKeyID: "access-key", SecretAccessKey: "secret-key"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	require.NoError(t, client.TestConnection(transportsecurity.WithPlaintext(context.Background()), TestConnectionRequest{
		Bucket: "braidplane-fixtures",
	}))

	objects, err := client.ListObjects(transportsecurity.WithPlaintext(context.Background()), ListObjectsRequest{
		Bucket: "braidplane-fixtures",
		Prefix: "imports/",
	})
	require.NoError(t, err)
	require.Len(t, objects, 1)
	require.Equal(t, Object{
		Key:       "imports/customers.csv",
		SizeBytes: 14,
		ETag:      "etag-1",
	}, Object{
		Key:       objects[0].Key,
		SizeBytes: objects[0].SizeBytes,
		ETag:      objects[0].ETag,
	})
	require.NotNil(t, objects[0].LastModified)
	_, err = client.ListObjects(schemaquota.WithQuota(transportsecurity.WithPlaintext(context.Background()), 1, 1), ListObjectsRequest{Bucket: "braidplane-fixtures", Prefix: "imports/"})
	require.ErrorContains(t, err, "byte limit")

	content, err := client.GetObject(transportsecurity.WithPlaintext(context.Background()), GetObjectRequest{
		Bucket:   "braidplane-fixtures",
		Key:      "imports/customers.csv",
		MaxBytes: 1024,
	})
	require.NoError(t, err)
	require.Equal(t, []byte("id,name\n1,Ada\n"), content.Content)
	require.Equal(t, "text/csv", content.ContentType)
	require.Equal(t, map[string]string{"source": "fixture"}, content.Metadata)

	exists, err := client.HeadObject(transportsecurity.WithPlaintext(context.Background()), HeadObjectRequest{
		Bucket: "braidplane-fixtures",
		Key:    "exports/existing.json",
	})
	require.NoError(t, err)
	require.True(t, exists)

	writeResult, err := client.PutObject(transportsecurity.WithPlaintext(context.Background()), PutObjectRequest{
		Bucket:      "braidplane-fixtures",
		Key:         "exports/new.json",
		Content:     []byte(`{"ok":true}`),
		ContentType: "application/json",
		Metadata:    map[string]string{"run": "run-001"},
		Overwrite:   false,
	})
	require.NoError(t, err)
	require.Equal(t, len(`{"ok":true}`), writeResult.WrittenBytes)
	require.True(t, bytes.Equal([]byte(`{"ok":true}`), putBody))

	_, err = client.PutObject(transportsecurity.WithPlaintext(context.Background()), PutObjectRequest{
		Bucket:      "braidplane-fixtures",
		Key:         "exports/existing.json",
		Content:     []byte(`{"ok":false}`),
		ContentType: "application/json",
		Overwrite:   false,
	})
	require.ErrorIs(t, err, xerror.AlreadyExists(xerror.DataSource))
}

func TestMapProviderErrorMapsCredentialAndNotFoundFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		code    string
		wantErr error
	}{
		{name: "invalid access key", code: "InvalidAccessKeyId", wantErr: xerror.InvalidCredentials()},
		{name: "signature rejected", code: "SignatureDoesNotMatch", wantErr: xerror.InvalidCredentials()},
		{name: "missing bucket", code: "NoSuchBucket", wantErr: xerror.NotFound(xerror.DataSource)},
		{name: "missing object", code: "NoSuchKey", wantErr: xerror.NotFound(xerror.DataSource)},
		{name: "access denied", code: "AccessDenied", wantErr: xerror.Forbidden(xerror.DataSource)},
		{name: "minio forbidden", code: "Forbidden", wantErr: xerror.Forbidden(xerror.DataSource)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := mapProviderError(&smithy.GenericAPIError{Code: tc.code, Message: "provider failure"})

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestMapProviderErrorMapsContextDeadlineToTimeout(t *testing.T) {
	t.Parallel()

	err := mapProviderError(context.DeadlineExceeded)

	require.ErrorIs(t, err, xerror.Timeout(xerror.DataSource))
}

func TestIsProviderNotFoundRecognizesProvider404Codes(t *testing.T) {
	t.Parallel()

	require.True(t, isProviderNotFound(&smithy.GenericAPIError{Code: "NoSuchKey"}))
	require.True(t, isProviderNotFound(&smithy.GenericAPIError{Code: "404"}))
	require.False(t, isProviderNotFound(&smithy.GenericAPIError{Code: "AccessDenied"}))
}

func TestProviderDiagnosticsExposeOnlyAllowlistedCodesAndFixedExplanations(t *testing.T) {
	const private = "secret-token customer@example.test https://private.example/tenant"
	for _, code := range []string{"AccessDenied", "NoSuchBucket", "NoSuchKey", "SignatureDoesNotMatch", "SlowDown", "InternalError", "RequestTimeout", "InvalidArgument", private} {
		t.Run(code, func(t *testing.T) {
			cause := &smithy.GenericAPIError{Code: code, Message: private}
			err := mapProviderError(cause)
			require.ErrorIs(t, err, cause)
			public := xerror.PublicDetail(err)
			require.NotEmpty(t, public)
			require.NotContains(t, public, private)
			if code != private {
				require.Contains(t, public, code)
			} else {
				require.Contains(t, public, "No supported provider error code")
			}
			if code == "SlowDown" || code == "InternalError" || code == "InvalidArgument" || code == private {
				require.ErrorIs(t, err, xerror.Unexpected())
			}
		})
	}
}

func TestProviderDiagnosticRetainsHTTPStatusWithoutResponseContent(t *testing.T) {
	cause := &smithy.GenericAPIError{Code: "private-code", Message: "private-message"}
	err := mapProviderError(&smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 503}}, Err: cause})
	require.ErrorIs(t, err, cause)
	require.Contains(t, xerror.PublicDetail(err), "HTTP status 503")
	require.NotContains(t, xerror.PublicDetail(err), "private-code")
	require.NotContains(t, xerror.PublicDetail(err), "private-message")
}

func TestConnectionWithPrefixDoesNotRequireBucketWideListing(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/fixtures", r.URL.Path)
		assert.Equal(t, "settlement-review/tester/", r.URL.Query().Get("prefix"))
		assert.Equal(t, "1", r.URL.Query().Get("max-keys"))
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>fixtures</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
	}))
	t.Cleanup(server.Close)
	ctx := transportsecurity.WithPlaintext(t.Context())
	client, err := New(ctx, Config{Endpoint: server.URL, Region: "us-east-1", ForcePathStyle: true, AllowPrivateHosts: true}, Credentials{AccessKeyID: "local-fixture", SecretAccessKey: "local-fixture"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.TestConnection(ctx, TestConnectionRequest{Bucket: "fixtures", Prefix: "settlement-review/tester/"}))
}
