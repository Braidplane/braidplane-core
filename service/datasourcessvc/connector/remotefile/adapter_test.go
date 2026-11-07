package remotefile

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestAdapterExecuteReadParsesCSVAndEnforcesRowLimit(t *testing.T) {
	t.Parallel()

	adapter := Adapter{ConnectorName: "Test"}
	provider := &fakeProvider{
		files: map[string]fakeFile{
			"imports/users.csv": {content: []byte("id,name\n1,Ada\n2,Grace\n")},
		},
	}

	result, err := adapter.ExecuteRead(
		context.Background(),
		provider,
		"imports/users.csv",
		Statement{Format: filedata.QueryFormatCSV},
		types.ExecutionLimits{MaxResultRows: 1},
	)

	require.Nil(t, result)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

func TestAdapterExecuteReadBuildsFilePayloadMetadata(t *testing.T) {
	t.Parallel()

	adapter := Adapter{ConnectorName: "Test"}
	provider := &fakeProvider{
		files: map[string]fakeFile{
			"exports/archive.bin": {
				content:     []byte{0, 1, 2},
				contentType: "application/octet-stream",
			},
		},
	}

	result, err := adapter.ExecuteRead(
		context.Background(),
		provider,
		"exports/archive.bin",
		Statement{Format: filedata.QueryFormatFile},
		types.ExecutionLimits{},
	)

	require.NoError(t, err)
	require.NotNil(t, result.File)
	assert.Equal(t, "read", result.Operation)
	assert.Equal(t, "exports/archive.bin", result.TargetPath)
	assert.Equal(t, "file", result.Format)
	assert.Equal(t, "archive.bin", result.File.Filename)
	assert.Equal(t, []byte{0, 1, 2}, result.Content)
}

func TestAdapterExecuteWriteSerializesJSONAndPassesMetadata(t *testing.T) {
	t.Parallel()

	adapter := Adapter{ConnectorName: "Test"}
	provider := &fakeProvider{files: map[string]fakeFile{}}
	overwrite := false

	result, err := adapter.ExecuteWrite(
		context.Background(),
		provider,
		"exports/users.json",
		Statement{
			Format:      filedata.QueryFormatJSON,
			Content:     []any{map[string]any{"id": "1", "name": "Ada"}},
			ContentSet:  true,
			ContentType: "application/json",
			Metadata:    map[string]string{"run": "run-001"},
			Overwrite:   &overwrite,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result.WrittenRows)
	assert.Equal(t, 1, *result.WrittenRows)
	require.NotNil(t, result.WrittenBytes)
	assert.Positive(t, *result.WrittenBytes)
	assert.False(t, provider.lastWrite.Overwrite)
	assert.Equal(t, "application/json", provider.lastWrite.ContentType)
	assert.Equal(t, map[string]string{"run": "run-001"}, provider.lastWrite.Metadata)
	assert.JSONEq(t, `[{"id":"1","name":"Ada"}]`, string(provider.files["exports/users.json"].content))
}

func TestAdapterExecuteWriteRecordsZeroRowsForStructuredFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		stmt   Statement
	}{
		{
			name:   "JSON",
			target: "exports/users.json",
			stmt: Statement{
				Format:     filedata.QueryFormatJSON,
				Content:    []any{},
				ContentSet: true,
			},
		},
		{
			name:   "CSV",
			target: "exports/users.csv",
			stmt: Statement{
				Format:     filedata.QueryFormatCSV,
				Content:    []any{},
				ContentSet: true,
			},
		},
		{
			name:   "Spreadsheet",
			target: "exports/users.xlsx",
			stmt: Statement{
				Format:     filedata.QueryFormatSpreadsheet,
				Content:    []any{},
				ContentSet: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := (Adapter{ConnectorName: "Test"}).ExecuteWrite(
				context.Background(),
				&fakeProvider{files: map[string]fakeFile{}},
				tt.target,
				tt.stmt,
			)

			require.NoError(t, err)
			require.NotNil(t, result.WrittenRows)
			assert.Zero(t, *result.WrittenRows)
		})
	}
}

func TestAdapterValidationBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		run     func(t *testing.T) error
		wantErr error
	}{
		{
			name: "ExecuteOperationUnsupportedOperation",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := (Adapter{
					ConnectorName: "Test",
					ErrorKind:     ErrorKindInvalidQuery,
				}).ExecuteOperation(
					context.Background(),
					&fakeProvider{files: map[string]fakeFile{}},
					"exports/users.json",
					Statement{Operation: "delete"},
					types.ExecutionLimits{},
				)
				return err
			},
			wantErr: xerror.InvalidInput(xerror.Query),
		},
		{
			name: "ExecuteWriteRejectsMissingContent",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := (Adapter{
					ConnectorName: "Test",
					ErrorKind:     ErrorKindInvalidQuery,
				}).ExecuteWrite(
					context.Background(),
					&fakeProvider{files: map[string]fakeFile{}},
					"exports/users.json",
					Statement{Format: filedata.QueryFormatJSON},
				)
				return err
			},
			wantErr: xerror.InvalidInput(xerror.Query),
		},
		{
			name: "SerializeWriteContentRejectsUnsupportedFormat",
			run: func(t *testing.T) error {
				t.Helper()
				_, _, _, err := (Adapter{ConnectorName: "Test"}).SerializeWriteContent(
					Statement{
						Format:  filedata.QueryFormat(99),
						Content: map[string]any{"id": 1},
					},
					"exports/users.unsupported",
				)
				return err
			},
			wantErr: xerror.Unexpected(),
		},
		{
			name: "SerializeWriteContentRejectsScalarJSON",
			run: func(t *testing.T) error {
				t.Helper()
				_, _, _, err := (Adapter{
					ConnectorName: "Test",
					ErrorKind:     ErrorKindInvalidQuery,
				}).SerializeWriteContent(
					Statement{
						Format:  filedata.QueryFormatJSON,
						Content: "scalar",
					},
					"exports/users.json",
				)
				return err
			},
			wantErr: xerror.InvalidInput(xerror.Query),
		},
		{
			name: "ExecuteReadRejectsOversizedResponse",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := (Adapter{ConnectorName: "Test"}).ExecuteRead(
					context.Background(),
					&fakeProvider{
						files: map[string]fakeFile{
							"imports/users.csv": {content: []byte("id,name\n1,Ada\n")},
						},
					},
					"imports/users.csv",
					Statement{Format: filedata.QueryFormatCSV},
					types.ExecutionLimits{MaxResponseBytes: 4},
				)
				return err
			},
			wantErr: xerror.ExecutionResponseSizeExceeded(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.run(t)

			require.Error(t, err)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestStructuredJSONContentValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content any
		want    bool
	}{
		{name: "Object", content: map[string]any{"id": 1}, want: true},
		{name: "Array", content: []any{map[string]any{"id": 1}}, want: true},
		{name: "TypedObjectArray", content: []map[string]any{{"id": 1}}, want: true},
		{name: "Scalar", content: "value", want: false},
		{name: "Null", content: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isStructuredJSONContent(tt.content))
		})
	}
}

func TestAdapterIntrospectSchemaSamplesProviderFiles(t *testing.T) {
	t.Parallel()

	adapter := Adapter{ConnectorName: "Test"}
	provider := &fakeProvider{
		files: map[string]fakeFile{
			"imports/users.csv": {
				content: []byte("id,name\n1,Ada\n"),
			},
			"imports/orders.json": {
				content: []byte(`[{"order_id":1,"total":12.5}]`),
			},
		},
		listed: []SchemaFile{
			{Name: "orders.json", Target: "imports/orders.json"},
			{Name: "users.csv", Target: "imports/users.csv"},
		},
	}

	schema, err := adapter.IntrospectSchema(
		context.Background(),
		provider,
		SchemaOptions{MaxBytes: 1024},
	)

	require.NoError(t, err)
	require.Len(t, schema.Files, 2)
	assert.Empty(t, schema.Diagnostics)
	assert.Equal(t, "orders.json", schema.Files[0].Name)
	assert.Equal(t, []filedata.Field{
		{Name: "order_id", Type: "integer"},
		{Name: "total", Type: "number"},
	}, schema.Files[0].Fields)
	assert.Equal(t, "users.csv", schema.Files[1].Name)
	assert.Equal(t, []filedata.Field{
		{Name: "id", Type: "string"},
		{Name: "name", Type: "string"},
	}, schema.Files[1].Fields)
}

func TestAdapterIntrospectSchemaReportsSkippedCandidates(t *testing.T) {
	t.Parallel()

	adapter := Adapter{ConnectorName: "Test"}
	provider := &fakeProvider{
		files: map[string]fakeFile{
			"imports/bad.json":   {content: []byte(`{`)},
			"imports/empty.bin":  {content: []byte{}},
			"imports/large.csv":  {content: []byte("id,name\n1,Ada\n")},
			"imports/users.csv":  {content: []byte("id\n1\n")},
			"imports/orders.csv": {content: []byte("id,total\n1,12.5\n")},
		},
		listed: []SchemaFile{
			{Name: "missing.csv", Target: "imports/missing.csv"},
			{Name: "bad.json", Target: "imports/bad.json"},
			{Name: "empty.bin", Target: "imports/empty.bin"},
			{Name: "large.csv", Target: "imports/large.csv"},
			{Name: "users.csv", Target: "imports/users.csv"},
		},
	}

	schema, err := adapter.IntrospectSchema(
		context.Background(),
		provider,
		SchemaOptions{MaxBytes: 8},
	)

	require.NoError(t, err)
	require.Len(t, schema.Files, 5)
	assert.Equal(t, "users.csv", schema.Files[4].Name)
	assert.Equal(t, []filedata.SchemaDiagnostic{
		{
			Name:     "missing.csv",
			Target:   "imports/missing.csv",
			Severity: "warning",
			Code:     "read_failed",
			Message:  "Fields were not inferred because the sample could not be read.",
		},
		{
			Name:     "bad.json",
			Target:   "imports/bad.json",
			Severity: "warning",
			Code:     "parse_failed",
			Message:  "Fields were not inferred because the sample could not be parsed.",
		},
		{
			Name:     "large.csv",
			Target:   "imports/large.csv",
			Severity: "warning",
			Code:     "sample_too_large",
			Message:  "Fields were not inferred because the sample exceeded the 8 byte limit.",
		},
	}, schema.Diagnostics)
}

func TestReadAllLimitedRejectsOversizedStreams(t *testing.T) {
	t.Parallel()

	content, err := ReadAllLimited(bytes.NewReader([]byte("abcdef")), 4)

	require.Nil(t, content)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

type fakeFile struct {
	content     []byte
	contentType string
}

type fakeProvider struct {
	files     map[string]fakeFile
	listed    []SchemaFile
	lastWrite WriteRequest
}

func (f *fakeProvider) ReadFile(
	_ context.Context,
	target string,
	_ int64,
) (*ReadResult, error) {
	file, ok := f.files[target]
	if !ok {
		return nil, xerror.NotFound(xerror.DataSource)
	}
	return &ReadResult{
		Content:     append([]byte(nil), file.content...),
		ContentType: file.contentType,
	}, nil
}

func (f *fakeProvider) WriteFile(
	_ context.Context,
	req WriteRequest,
) (*WriteResult, error) {
	f.lastWrite = req
	if f.files == nil {
		f.files = map[string]fakeFile{}
	}
	f.files[req.Target] = fakeFile{
		content:     append([]byte(nil), req.Content...),
		contentType: req.ContentType,
	}
	return &WriteResult{WrittenBytes: len(req.Content)}, nil
}

func (f *fakeProvider) ListFiles(_ context.Context) ([]SchemaFile, error) {
	return append([]SchemaFile(nil), f.listed...), nil
}

func TestAdapterIntrospectionKeepsOpaqueFilesWithoutReadingBytes(t *testing.T) {
	size := int64(123)
	provider := &fakeProvider{listed: []SchemaFile{{Name: "nested/report.pdf", Target: "nested/report.pdf", SizeBytes: &size}}}
	schema, err := (Adapter{}).IntrospectSchema(context.Background(), provider, SchemaOptions{})
	require.NoError(t, err)
	require.Len(t, schema.Files, 1)
	require.Empty(t, schema.Files[0].Fields)
	require.Equal(t, &size, schema.Files[0].SizeBytes)
	require.Equal(t, "application/pdf", schema.Files[0].MIMEType)
	require.Empty(t, schema.Diagnostics)
}

func TestAdapterIntrospectionInfersExtensionlessData(t *testing.T) {
	for _, content := range []string{"id,name\n1,Ada\n", `[{"id":1,"name":"Ada"}]`} {
		provider := &fakeProvider{files: map[string]fakeFile{"users": {content: []byte(content)}}, listed: []SchemaFile{{Name: "users", Target: "users"}}}
		schema, err := (Adapter{}).IntrospectSchema(context.Background(), provider, SchemaOptions{MaxBytes: 1024})
		require.NoError(t, err)
		require.Len(t, schema.Files, 1)
		require.Len(t, schema.Files[0].Fields, 2)
	}
}

func TestAdapterIntrospectionKeepsExtensionlessOpaqueContent(t *testing.T) {
	for _, content := range [][]byte{{0, 1, 2, 3}, {0xff, 0xfe, 'a', 0}, {0x80, 0x81}, []byte("%PDF-1.7\nopaque"), []byte("<html>opaque</html>")} {
		provider := &fakeProvider{files: map[string]fakeFile{"blob": {content: content}}, listed: []SchemaFile{{Name: "blob", Target: "blob"}}}
		schema, err := (Adapter{}).IntrospectSchema(context.Background(), provider, SchemaOptions{MaxBytes: 1024})
		require.NoError(t, err)
		require.Len(t, schema.Files, 1)
		require.Empty(t, schema.Files[0].Fields)
		require.Empty(t, schema.Diagnostics)
	}
}
