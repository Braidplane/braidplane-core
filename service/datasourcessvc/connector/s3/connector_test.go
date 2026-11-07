package s3

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestConnectorTestConnectionUsesConfiguredBucket(t *testing.T) {
	t.Parallel()

	fake := &fakeProviderClient{
		testConnectionFunc: func(_ context.Context, req s3client.TestConnectionRequest) error {
			require.Equal(t, "braidplane-fixtures", req.Bucket)
			return nil
		},
	}
	conn := newWithClient(Config{Bucket: "braidplane-fixtures"}, fake)

	err := conn.TestConnection(context.Background())

	require.NoError(t, err)
}

func TestConnectorIntrospectSchemaListsConfiguredPrefixAndSamplesFiles(t *testing.T) {
	t.Parallel()

	fake := &fakeProviderClient{
		listObjectsFunc: func(_ context.Context, req s3client.ListObjectsRequest) ([]s3client.Object, error) {
			require.Equal(t, "braidplane-fixtures", req.Bucket)
			require.Equal(t, "imports/", req.Prefix)
			return []s3client.Object{
				{Key: "imports/customers.csv"},
				{Key: "imports/folder/"},
				{Key: "imports/nested/report.pdf", SizeBytes: 42},
				{Key: ""},
			}, nil
		},
		getObjectFunc: func(_ context.Context, req s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
			require.Equal(t, "braidplane-fixtures", req.Bucket)
			require.Equal(t, "imports/customers.csv", req.Key)
			require.Equal(t, int64(introspectionMaxObjectBytes), req.MaxBytes)
			return &s3client.ObjectContent{
				Key:     req.Key,
				Content: []byte("id,name\n1,Ada\n"),
			}, nil
		},
	}
	conn := newWithClient(Config{Bucket: "braidplane-fixtures", Prefix: "/imports/"}, fake)

	schema, err := conn.IntrospectSchema(context.Background())

	require.NoError(t, err)
	require.Len(t, schema.Files, 3)
	require.Equal(t, "imports/folder/", schema.Files[1].Name)
	require.Empty(t, schema.Files[1].Fields)
	require.Equal(t, "imports/nested/report.pdf", schema.Files[2].Name)
	require.Empty(t, schema.Files[2].Fields)
	require.Equal(t, int64(42), *schema.Files[2].SizeBytes)
	assert.Equal(t, "imports/customers.csv", schema.Files[0].Name)
	require.Len(t, schema.Files[0].Fields, 2)
	assert.Equal(t, "id", schema.Files[0].Fields[0].Name)
	assert.Equal(t, "name", schema.Files[0].Fields[1].Name)
}

func TestConnectorExecuteReadCSVUsesBucketPrefixAndLimits(t *testing.T) {
	t.Parallel()

	fake := &fakeProviderClient{
		getObjectFunc: func(_ context.Context, req s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
			require.Equal(t, "braidplane-fixtures", req.Bucket)
			require.Equal(t, "imports/customers.csv", req.Key)
			require.Equal(t, int64(128), req.MaxBytes)
			return &s3client.ObjectContent{
				Key:         req.Key,
				Content:     []byte("id,name\n1,Ada\n"),
				ContentType: "text/csv",
			}, nil
		},
	}
	conn := newWithClient(Config{Bucket: "braidplane-fixtures", Prefix: "imports"}, fake)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"s3","operation":"read","key":"customers.csv","format":"csv"}`),
		nil,
		types.ExecutionLimits{MaxResultRows: 10, MaxResponseBytes: 128},
	)

	require.NoError(t, err)
	require.NoError(t, execResult.Error())
	fileResult, ok := execResult.S3()
	require.True(t, ok)
	require.NotNil(t, fileResult)
	assert.Equal(t, "read", fileResult.Operation)
	assert.Equal(t, "imports/customers.csv", fileResult.TargetPath)
	assert.Equal(t, "csv", fileResult.Format)
	assert.Equal(t, 1, fileResult.RowCount)
	assert.Equal(t, []string{"id", "name"}, fileResult.ColumnNames)
	assert.Equal(t, "Ada", fileResult.Rows[0]["name"])
}

func TestConnectorExecuteWriteBindsContentKeyBucketMetadataAndOverwrite(t *testing.T) {
	t.Parallel()

	fake := &fakeProviderClient{
		putObjectFunc: func(_ context.Context, req s3client.PutObjectRequest) (*s3client.PutObjectResult, error) {
			require.Equal(t, "archive-fixtures", req.Bucket)
			require.Equal(t, "exports/runs/run-001/customers.json", req.Key)
			require.Equal(t, "application/json", req.ContentType)
			require.True(t, req.Overwrite)
			assert.JSONEq(t, `[{"id":"1","name":"Ada"}]`, string(req.Content))
			assert.Equal(t, map[string]string{"run": "run-001"}, req.Metadata)
			return &s3client.PutObjectResult{WrittenBytes: len(req.Content)}, nil
		},
	}
	conn := newWithClient(Config{Bucket: "braidplane-fixtures", Prefix: "exports"}, fake)

	params := map[string]types.TypedParam{
		"bucket": {
			Value: "archive-fixtures",
		},
		"key": {
			Value: "runs/{{run_slug}}/customers.json",
		},
		sinkbinding.Input: {
			Value: []any{map[string]any{"id": "1", "name": "Ada"}},
		},
		"metadata": {
			Value: map[string]string{"run": "{{run_slug}}"},
		},
		"content_type": {
			Value: "application/json",
		},
		"overwrite": {
			Value:    true,
			DataType: "boolean",
		},
		"run_slug": {
			Value: "run-001",
		},
	}

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"s3","operation":"write","key":"ignored.json","format":"json"}`),
		params,
	)

	require.NoError(t, err)
	require.NoError(t, execResult.Error())
	fileResult, ok := execResult.S3()
	require.True(t, ok)
	require.NotNil(t, fileResult)
	assert.Equal(t, "write", fileResult.Operation)
	assert.Equal(t, "exports/runs/run-001/customers.json", fileResult.TargetPath)
	require.NotNil(t, fileResult.WrittenRows)
	assert.Equal(t, 1, *fileResult.WrittenRows)
	require.NotNil(t, fileResult.WrittenBytes)
	assert.Positive(t, *fileResult.WrittenBytes)
}

func TestConnectorExecuteRejectsKeyTraversal(t *testing.T) {
	t.Parallel()

	fake := &fakeProviderClient{
		getObjectFunc: func(context.Context, s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
			t.Fatal("GetObject must not be called for an escaping key")
			return nil, xerror.Unexpected().Detail("unexpected GetObject call")
		},
	}
	conn := newWithClient(Config{Bucket: "braidplane-fixtures", Prefix: "imports"}, fake)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"s3","operation":"read","key":"../secrets.csv"}`),
		nil,
	)

	require.NoError(t, err)
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
}

func TestConnectorExecuteReadEnforcesRowLimit(t *testing.T) {
	t.Parallel()

	fake := &fakeProviderClient{
		getObjectFunc: func(_ context.Context, req s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
			require.Equal(t, "imports/customers.csv", req.Key)
			return &s3client.ObjectContent{
				Key:     req.Key,
				Content: []byte("id\n1\n2\n"),
			}, nil
		},
	}
	conn := newWithClient(Config{Bucket: "braidplane-fixtures", Prefix: "imports"}, fake)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"s3","operation":"read","key":"customers.csv","format":"csv"}`),
		nil,
		types.ExecutionLimits{MaxResultRows: 1},
	)

	require.NoError(t, err)
	require.ErrorIs(t, execResult.Error(), xerror.ExecutionRowLimitExceeded())
}

func TestConnectorExecuteWriteRequiresContent(t *testing.T) {
	t.Parallel()

	conn := newWithClient(Config{Bucket: "braidplane-fixtures"}, &fakeProviderClient{})

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"s3","operation":"write","key":"customers.json","format":"json"}`),
		nil,
	)

	require.NoError(t, err)
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
}

type fakeProviderClient struct {
	testConnectionFunc func(context.Context, s3client.TestConnectionRequest) error
	listObjectsFunc    func(context.Context, s3client.ListObjectsRequest) ([]s3client.Object, error)
	getObjectFunc      func(context.Context, s3client.GetObjectRequest) (*s3client.ObjectContent, error)
	headObjectFunc     func(context.Context, s3client.HeadObjectRequest) (bool, error)
	putObjectFunc      func(context.Context, s3client.PutObjectRequest) (*s3client.PutObjectResult, error)
	closeFunc          func() error
}

func (f *fakeProviderClient) TestConnection(
	ctx context.Context,
	req s3client.TestConnectionRequest,
) error {
	if f.testConnectionFunc == nil {
		return nil
	}

	return f.testConnectionFunc(ctx, req)
}

func (f *fakeProviderClient) ListObjects(
	ctx context.Context,
	req s3client.ListObjectsRequest,
) ([]s3client.Object, error) {
	if f.listObjectsFunc == nil {
		return nil, nil
	}

	return f.listObjectsFunc(ctx, req)
}

func (f *fakeProviderClient) GetObject(
	ctx context.Context,
	req s3client.GetObjectRequest,
) (*s3client.ObjectContent, error) {
	if f.getObjectFunc == nil {
		return nil, xerror.NotFound(xerror.DataSource)
	}

	return f.getObjectFunc(ctx, req)
}

func (f *fakeProviderClient) HeadObject(
	ctx context.Context,
	req s3client.HeadObjectRequest,
) (bool, error) {
	if f.headObjectFunc == nil {
		return false, nil
	}

	return f.headObjectFunc(ctx, req)
}

func (f *fakeProviderClient) PutObject(
	ctx context.Context,
	req s3client.PutObjectRequest,
) (*s3client.PutObjectResult, error) {
	if f.putObjectFunc == nil {
		return nil, xerror.Unexpected().Detail("unexpected PutObject call")
	}

	return f.putObjectFunc(ctx, req)
}

func (f *fakeProviderClient) Close() error {
	if f.closeFunc == nil {
		return nil
	}

	return f.closeFunc()
}

func TestSchemaListingPreservesWhitespaceObjectKeys(t *testing.T) {
	fake := &fakeProviderClient{listObjectsFunc: func(context.Context, s3client.ListObjectsRequest) ([]s3client.Object, error) {
		return []s3client.Object{{Key: " "}}, nil
	}}
	files, err := (s3SchemaProvider{connector: newWithClient(Config{Bucket: "fixtures"}, fake)}).ListFiles(context.Background())
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, " ", files[0].Name)
	require.Equal(t, " ", files[0].Target)
}

func TestConnectorIntrospectionRetainsSlashSuffixedObjects(t *testing.T) {
	for _, size := range []int64{0, 4} {
		fake := &fakeProviderClient{
			listObjectsFunc: func(context.Context, s3client.ListObjectsRequest) ([]s3client.Object, error) {
				return []s3client.Object{{Key: "imports/folder/", SizeBytes: size}}, nil
			},
			getObjectFunc: func(context.Context, s3client.GetObjectRequest) (*s3client.ObjectContent, error) {
				t.Fatal("raw object discovery must not sample bytes")
				return nil, assert.AnError
			},
		}
		schema, err := newWithClient(Config{Bucket: "fixtures", Prefix: "imports"}, fake).IntrospectSchema(context.Background())
		require.NoError(t, err)
		require.Len(t, schema.Files, 1)
		require.Equal(t, "imports/folder/", schema.Files[0].Name)
		require.Equal(t, size, *schema.Files[0].SizeBytes)
		require.Empty(t, schema.Files[0].Fields)
		require.Empty(t, schema.Diagnostics)
	}
}
