package s3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testScope() resourcescope.Scope {
	return resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "store", Operation: "export", Contract: "orders", Revision: "v1", ObjectID: "orders", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports", DynamicKey: true}}
}

func TestScopedPartitionsAndSafeEvidence(t *testing.T) {
	t.Parallel()
	var keys []string
	client := &fakeProviderClient{putObjectFunc: func(_ context.Context, request s3client.PutObjectRequest) (*s3client.PutObjectResult, error) {
		require.Equal(t, "fixed-bucket", request.Bucket)
		keys = append(keys, request.Key)
		return &s3client.PutObjectResult{WrittenBytes: len(request.Content)}, nil
	}}
	conn := newWithClient(Config{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports"}, client)
	statement := json.RawMessage(`{"type":"s3","operation":"write","key":"{{partition}}/orders.json","format":"json","overwrite":true}`)
	original := string(statement)
	scope := testScope()
	for _, partition := range []string{"2026-10-18", "2026-10-19"} {
		params := map[string]types.TypedParam{"partition": {Value: partition}, sinkbinding.Input: {Value: []any{map[string]any{"id": 1}}}}
		output, err := conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), statement, params, scope)
		require.NoError(t, err)
		require.NoError(t, output.Error())
		file, ok := output.S3()
		require.True(t, ok)
		require.Equal(t, "write", file.Operation)
		require.Empty(t, file.TargetPath)
		require.Empty(t, file.Name)
		require.Nil(t, file.File)
		raw, err := json.Marshal(output)
		require.NoError(t, err)
		require.NotContains(t, string(raw), partition)
		require.NotContains(t, string(raw), "fixed-bucket")
		require.Equal(t, partition, params["partition"].Value)
	}
	require.Equal(t, []string{"exports/2026-10-18/orders.json", "exports/2026-10-19/orders.json"}, keys)
	require.Equal(t, original, string(statement))
}

func TestScopedExecutionRejectsChangesBeforeProviderDispatch(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*Config, *resourcescope.Scope, map[string]types.TypedParam){
		"BucketOverride": func(_ *Config, _ *resourcescope.Scope, p map[string]types.TypedParam) {
			p["bucket"] = types.TypedParam{Value: "other-bucket"}
		},
		"EndpointChange": func(c *Config, _ *resourcescope.Scope, _ map[string]types.TypedParam) {
			c.Endpoint = "https://other.example"
		},
		"RegionChange":       func(c *Config, _ *resourcescope.Scope, _ map[string]types.TypedParam) { c.Region = "other" },
		"AddressingChange":   func(c *Config, _ *resourcescope.Scope, _ map[string]types.TypedParam) { c.ForcePathStyle = true },
		"BucketConfigChange": func(c *Config, _ *resourcescope.Scope, _ map[string]types.TypedParam) { c.Bucket = "other" },
		"PrefixConfigChange": func(c *Config, _ *resourcescope.Scope, _ map[string]types.TypedParam) { c.Prefix = "exports/other" },
		"UndeclaredSelector": func(_ *Config, s *resourcescope.Scope, _ map[string]types.TypedParam) { s.S3.DynamicKey = false },
		"WritePolicyOverride": func(_ *Config, _ *resourcescope.Scope, p map[string]types.TypedParam) {
			p["overwrite"] = types.TypedParam{Value: true}
		},
		"SheetOverride": func(_ *Config, _ *resourcescope.Scope, p map[string]types.TypedParam) {
			p["sheet"] = types.TypedParam{Value: "private"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := Config{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports"}
			scope := testScope()
			params := map[string]types.TypedParam{"key": {Value: "allowed.json"}}
			change(&config, &scope, params)
			client := &fakeProviderClient{putObjectFunc: func(context.Context, s3client.PutObjectRequest) (*s3client.PutObjectResult, error) {
				t.Fatal("unexpected dispatch")
				return nil, resourcescope.ErrInvalid
			}}
			_, err := newWithClient(config, client).ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), json.RawMessage(`{"type":"s3","operation":"write","key":"fixed.json","format":"json"}`), params, scope)
			require.ErrorIs(t, err, resourcescope.ErrInvalid)
		})
	}
	for _, key := range []string{"../private", "/absolute", "a/../../private", "a/./b", "a//b", `a\b`, "%2e%2e/private", "%252e%252e/private", "a%2fb", "https://other.example/key", "a?token=private", "a#private", "a\x00b", "{{secret}}", "a/"} {
		t.Run(key, func(t *testing.T) {
			conn := newWithClient(Config{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports"}, &fakeProviderClient{})
			_, err := conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), json.RawMessage(`{"type":"s3","operation":"read","key":"{{key}}","format":"json"}`), map[string]types.TypedParam{"key": {Value: key}}, testScope())
			require.ErrorIs(t, err, resourcescope.ErrInvalid)
			require.NotContains(t, err.Error(), key)
		})
	}
}

func TestScopedProviderErrorsDoNotExposePathsURLsOrHashes(t *testing.T) {
	t.Parallel()
	client := &fakeProviderClient{getObjectFunc: func(context.Context, s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
		return nil, xerror.Unexpected().Detail("https://private.example/secret/key sha256:sensitive-input-hash")
	}}
	conn := newWithClient(Config{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports"}, client)
	_, err := conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), json.RawMessage(`{"type":"s3","operation":"read","key":"private.json","format":"json"}`), nil, testScope())
	require.ErrorIs(t, err, errScopedExecutionFailed)
	require.NotContains(t, err.Error(), "private")
	require.NotContains(t, err.Error(), "sha256")
}

func TestScopedProviderFailureRetainsOnlyPublicDiagnostic(t *testing.T) {
	t.Parallel()
	const private = "https://private.example/secret/key?signature=private sha256:sensitive-input-hash"
	const public = "S3 AccessDenied: access to the requested operation was denied. HTTP status 403."
	client := &fakeProviderClient{putObjectFunc: func(context.Context, s3client.PutObjectRequest) (*s3client.PutObjectResult, error) {
		return nil, xerror.Forbidden(xerror.DataSource).Detail(private).WithPublicDetail(public)
	}}
	conn := newWithClient(Config{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports"}, client)
	_, err := conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), json.RawMessage(`{"type":"s3","operation":"write","key":"orders.json","format":"json","overwrite":true}`), map[string]types.TypedParam{sinkbinding.Input: {Value: []any{map[string]any{"id": 1}}}}, testScope())
	require.ErrorIs(t, err, errScopedExecutionFailed)
	require.Equal(t, public, xerror.PublicDetail(err))
	require.NotContains(t, err.Error(), private)
	require.NotContains(t, err.Error(), "sha256")
	require.NotContains(t, err.Error(), "orders.json")
}

func TestScopedSafetyLimitsKeepIdentityWithoutSensitiveDetails(t *testing.T) {
	t.Parallel()
	for _, limit := range []error{xerror.ExecutionRowLimitExceeded(), xerror.ExecutionResponseSizeExceeded()} {
		client := &fakeProviderClient{getObjectFunc: func(context.Context, s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
			return nil, xerror.Unexpected().With(limit).Detail("https://private.example/key sha256:sensitive-hash")
		}}
		conn := newWithClient(Config{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports"}, client)
		_, err := conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), json.RawMessage(`{"type":"s3","operation":"read","key":"orders.json","format":"json"}`), nil, testScope())
		require.ErrorIs(t, err, limit)
		require.Equal(t, limit.Error(), err.Error())
	}
}

func TestScopedExplicitEndpointOverridesAmbientSettingsAndBlocksRedirects(t *testing.T) { // Sets AWS SDK endpoint environment.
	var unpermittedCalls atomic.Int32
	unpermitted := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { unpermittedCalls.Add(1) }))
	t.Cleanup(unpermitted.Close)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasPrefix(r.URL.Path, "/fixed-bucket/exports/"))
		if strings.Contains(r.URL.Path, "/redirect/") {
			http.Redirect(w, r, unpermitted.URL+"/private-key", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(endpoint.Close)
	t.Setenv("AWS_ENDPOINT_URL_S3", unpermitted.URL)
	t.Setenv("AWS_ENDPOINT_URL", unpermitted.URL)
	scope := testScope()
	scope.S3.Endpoint, scope.S3.ForcePathStyle = endpoint.URL, true
	conn, err := New(transportsecurity.WithPlaintext(t.Context()), Config{Endpoint: endpoint.URL, Region: "test-region", Bucket: "fixed-bucket", Prefix: "exports", ForcePathStyle: true, AllowPrivateHosts: true}, Credentials{AccessKeyID: "test", SecretAccessKey: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	statement := json.RawMessage(`{"type":"s3","operation":"read","key":"{{partition}}/orders.json","format":"json"}`)
	output, err := conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), statement, map[string]types.TypedParam{"partition": {Value: "allowed"}}, scope)
	require.NoError(t, err)
	require.NoError(t, output.Error())
	_, err = conn.ExecuteScoped(transportsecurity.WithPlaintext(t.Context()), statement, map[string]types.TypedParam{"partition": {Value: "redirect"}}, scope)
	require.ErrorIs(t, err, errScopedExecutionFailed)
	require.NotContains(t, err.Error(), "private-key")
	require.Zero(t, unpermittedCalls.Load())
}
