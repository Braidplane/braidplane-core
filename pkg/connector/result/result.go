package result

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"

	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=ExecutionResultType -json -trimprefix=ExecutionResultType

// ExecutionResultType identifies which connector type produced the result.
type ExecutionResultType int

const (
	ExecutionResultTypeUnknown ExecutionResultType = iota
	ExecutionResultTypeSQL
	ExecutionResultTypeMongoDB
	ExecutionResultTypeHTTP
	ExecutionResultTypeObjectRows
	ExecutionResultTypeFTP
	ExecutionResultTypeGit
	ExecutionResultTypeS3
	ExecutionResultTypeWorkspaceFile
)

// ExecutionResult is a discriminated union containing execution results from connector types.
// Only one typed payload field will be non-nil.
type ExecutionResult struct {
	resultType ExecutionResultType
	vendorType string // e.g., "postgres", "mysql", "mongodb", "http"

	// Exactly one of these is populated based on resultType
	sql        *SQLResult
	mongo      *MongoDBResult
	http       *HTTPResult
	objectRows *ObjectRowsResult
	file       *FileResult

	// Common metadata
	durationMs int64
	err        error
}

// Constructor functions
func NewSQLResult(
	vendorType string,
	result *SQLResult,
	durationMs int64,
	err error,
) *ExecutionResult {
	return &ExecutionResult{
		resultType: ExecutionResultTypeSQL,
		vendorType: vendorType,
		sql:        result,
		durationMs: durationMs,
		err:        err,
	}
}

func NewMongoDBResult(result *MongoDBResult, durationMs int64, err error) *ExecutionResult {
	return &ExecutionResult{
		resultType: ExecutionResultTypeMongoDB,
		vendorType: "mongodb",
		mongo:      result,
		durationMs: durationMs,
		err:        err,
	}
}

func NewHTTPResult(result *HTTPResult, durationMs int64, err error) *ExecutionResult {
	return &ExecutionResult{
		resultType: ExecutionResultTypeHTTP,
		vendorType: "http",
		http:       result,
		durationMs: durationMs,
		err:        err,
	}
}

func NewObjectRowsResult(
	vendorType string,
	result *ObjectRowsResult,
	durationMs int64,
	err error,
) *ExecutionResult {
	return &ExecutionResult{
		resultType: ExecutionResultTypeObjectRows,
		vendorType: vendorType,
		objectRows: result,
		durationMs: durationMs,
		err:        err,
	}
}

func NewFTPResult(result *FileResult, durationMs int64, err error) *ExecutionResult {
	return newFileResult(ExecutionResultTypeFTP, "ftp", result, durationMs, err)
}

func NewGitResult(result *FileResult, durationMs int64, err error) *ExecutionResult {
	return newFileResult(ExecutionResultTypeGit, "git", result, durationMs, err)
}

func NewWorkspaceFileResult(r *FileResult, durationMs int64, err error) *ExecutionResult {
	return newFileResult(ExecutionResultTypeWorkspaceFile, "google_workspace", r, durationMs, err)
}

func NewS3Result(result *FileResult, durationMs int64, err error) *ExecutionResult {
	return newFileResult(ExecutionResultTypeS3, "s3", result, durationMs, err)
}

func newFileResult(
	resultType ExecutionResultType,
	vendorType string,
	result *FileResult,
	durationMs int64,
	err error,
) *ExecutionResult {
	return &ExecutionResult{
		resultType: resultType,
		vendorType: vendorType,
		file:       result,
		durationMs: durationMs,
		err:        err,
	}
}

// UnmarshalJSON restores the discriminated result envelope used by persisted
// worker execution responses. Error text is carried only inside the encrypted
// response payload; callers must not log the raw envelope.
func (r *ExecutionResult) UnmarshalJSON(data []byte) error {
	var envelope struct {
		ResultType    ExecutionResultType `json:"result_type"`
		VendorType    string              `json:"vendor_type"`
		DurationMs    int64               `json:"duration_ms"`
		Error         *string             `json:"error,omitempty"`
		SQL           *SQLResult          `json:"sql,omitempty"`
		MongoDB       *MongoDBResult      `json:"mongodb,omitempty"`
		HTTP          *HTTPResult         `json:"http,omitempty"`
		ObjectRows    *ObjectRowsResult   `json:"object_rows,omitempty"`
		FTP           *FileResult         `json:"ftp,omitempty"`
		Git           *FileResult         `json:"git,omitempty"`
		S3            *FileResult         `json:"s3,omitempty"`
		WorkspaceFile *FileResult         `json:"workspace_file,omitempty"`
	}
	if err := jsonvalue.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.SQL != nil {
		jsonvalue.Normalize(envelope.SQL.Rows)
	}
	if envelope.MongoDB != nil {
		jsonvalue.Normalize(envelope.MongoDB.Documents)
	}
	if envelope.ObjectRows != nil {
		jsonvalue.Normalize(envelope.ObjectRows.Rows)
	}
	for _, file := range []*FileResult{envelope.FTP, envelope.Git, envelope.S3, envelope.WorkspaceFile} {
		if file != nil {
			jsonvalue.Normalize(file.Rows)
		}
	}

	var resultErr error
	if envelope.Error != nil {
		resultErr = xerror.Unexpected().Detail(*envelope.Error)
	}
	*r = ExecutionResult{
		resultType: envelope.ResultType,
		vendorType: envelope.VendorType,
		sql:        envelope.SQL,
		mongo:      envelope.MongoDB,
		http:       envelope.HTTP,
		objectRows: envelope.ObjectRows,
		durationMs: envelope.DurationMs,
		err:        resultErr,
	}
	switch envelope.ResultType {
	case ExecutionResultTypeFTP:
		r.file = envelope.FTP
	case ExecutionResultTypeGit:
		r.file = envelope.Git
	case ExecutionResultTypeS3:
		r.file = envelope.S3
	case ExecutionResultTypeWorkspaceFile:
		r.file = envelope.WorkspaceFile
	case ExecutionResultTypeUnknown, ExecutionResultTypeSQL, ExecutionResultTypeMongoDB,
		ExecutionResultTypeHTTP, ExecutionResultTypeObjectRows:
	}
	return nil
}

// Type-safe accessors - return (payload, true) if type matches, (nil, false) otherwise
func (r *ExecutionResult) SQL() (*SQLResult, bool) {
	if r.resultType != ExecutionResultTypeSQL {
		return nil, false
	}
	return r.sql, true
}

func (r *ExecutionResult) MongoDB() (*MongoDBResult, bool) {
	if r.resultType != ExecutionResultTypeMongoDB {
		return nil, false
	}
	return r.mongo, true
}

func (r *ExecutionResult) HTTP() (*HTTPResult, bool) {
	if r.resultType != ExecutionResultTypeHTTP {
		return nil, false
	}
	return r.http, true
}

func (r *ExecutionResult) ObjectRows() (*ObjectRowsResult, bool) {
	if r.resultType != ExecutionResultTypeObjectRows {
		return nil, false
	}
	return r.objectRows, true
}

func (r *ExecutionResult) FTP() (*FileResult, bool) {
	if r.resultType != ExecutionResultTypeFTP {
		return nil, false
	}
	return r.file, true
}

func (r *ExecutionResult) Git() (*FileResult, bool) {
	if r.resultType != ExecutionResultTypeGit {
		return nil, false
	}
	return r.file, true
}

func (r *ExecutionResult) S3() (*FileResult, bool) {
	if r.resultType != ExecutionResultTypeS3 {
		return nil, false
	}
	return r.file, true
}

// File returns the shared file-shaped payload used by FTP, Git, and S3 results.
// Call FTP, Git, or S3 when the caller needs to preserve the exact execution family.
func (r *ExecutionResult) File() (*FileResult, bool) {
	if r.resultType != ExecutionResultTypeFTP &&
		r.resultType != ExecutionResultTypeGit &&
		r.resultType != ExecutionResultTypeS3 && r.resultType != ExecutionResultTypeWorkspaceFile {
		return nil, false
	}
	return r.file, true
}

// Metadata accessors
func (r *ExecutionResult) Type() ExecutionResultType { return r.resultType }
func (r *ExecutionResult) VendorType() string        { return r.vendorType }
func (r *ExecutionResult) DurationMs() int64         { return r.durationMs }
func (r *ExecutionResult) Error() error              { return r.err }
func (r *ExecutionResult) IsSuccess() bool           { return r.err == nil }

// MarshalJSON implements json.Marshaler for storage.
func (r *ExecutionResult) MarshalJSON() ([]byte, error) {
	var errMsg *string
	if r.err != nil {
		msg := r.err.Error()
		errMsg = &msg
	}

	storageResultType, ftpResult, gitResult, s3Result := storageFileEnvelope(
		r.resultType,
		r.file,
	)

	result := struct {
		ResultType    string            `json:"result_type"`
		VendorType    string            `json:"vendor_type"`
		DurationMs    int64             `json:"duration_ms"`
		Error         *string           `json:"error,omitempty"`
		SQL           *SQLResult        `json:"sql,omitempty"`
		MongoDB       *MongoDBResult    `json:"mongodb,omitempty"`
		HTTP          *HTTPResult       `json:"http,omitempty"`
		ObjectRows    *ObjectRowsResult `json:"object_rows,omitempty"`
		FTP           *FileResult       `json:"ftp,omitempty"`
		Git           *FileResult       `json:"git,omitempty"`
		S3            *FileResult       `json:"s3,omitempty"`
		WorkspaceFile *FileResult       `json:"workspace_file,omitempty"`
	}{
		ResultType: storageResultType,
		VendorType: r.vendorType,
		DurationMs: r.durationMs,
		Error:      errMsg,
		SQL:        r.sql,
		MongoDB:    r.mongo,
		HTTP:       r.http,
		ObjectRows: r.objectRows,
		FTP:        ftpResult,
		Git:        gitResult,
		S3:         s3Result,
	}

	if r.resultType == ExecutionResultTypeWorkspaceFile {
		result.WorkspaceFile = r.file
	}
	return json.Marshal(result)
}

func storageFileEnvelope(
	resultType ExecutionResultType,
	fileResult *FileResult,
) (string, *FileResult, *FileResult, *FileResult) {
	switch resultType {
	case ExecutionResultTypeFTP:
		return resultType.String(), fileResult, nil, nil
	case ExecutionResultTypeGit:
		return resultType.String(), nil, fileResult, nil
	case ExecutionResultTypeS3:
		return resultType.String(), nil, nil, fileResult
	case ExecutionResultTypeWorkspaceFile:
		return resultType.String(), nil, nil, nil
	default:
		return resultType.String(), fileResult, nil, nil
	}
}

// SQLResult contains data from SQL query execution.
type SQLResult struct {
	Rows         []map[string]any                 `json:"rows"`
	ColumnNames  []string                         `json:"column_names"`
	RowCount     int                              `json:"row_count"`
	LastInsertID *int64                           `json:"last_insert_id,omitempty"`
	File         *filepayload.FilePayloadMetadata `json:"file,omitempty"`
	Reference    *filepayload.Reference           `json:"-"`
}

// MongoDBResult contains data from MongoDB operation execution.
type MongoDBResult struct {
	OperationType string `json:"operation_type"` // "find", "insert_one", etc.

	// Result fields - populated based on operation type
	Documents     []map[string]any `json:"documents"`
	DocumentCount *int             `json:"document_count,omitempty"`
	InsertedID    any              `json:"inserted_id,omitempty"`
	InsertedIDs   []any            `json:"inserted_ids,omitempty"`   // For insert_many
	InsertedCount *int64           `json:"inserted_count,omitempty"` // For insert_many
	MatchedCount  *int64           `json:"matched_count,omitempty"`
	ModifiedCount *int64           `json:"modified_count,omitempty"`
	DeletedCount  *int64           `json:"deleted_count,omitempty"`
	UpsertedID    any              `json:"upserted_id,omitempty"`
	UpsertedCount *int64           `json:"upserted_count,omitempty"`
}

// HTTPResult contains data from HTTP request execution.
type HTTPResult struct {
	StatusCode            int                              `json:"status_code"`
	Headers               map[string]string                `json:"headers"`
	Body                  json.RawMessage                  `json:"body,omitempty"`
	File                  *filepayload.FilePayloadMetadata `json:"file,omitempty"`
	Reference             *filepayload.Reference           `json:"-"`
	RawBody               []byte                           `json:"-"`
	PaginationRecordCount *int                             `json:"-"`
}

// RowCount counts selected pagination records or the ordinary top-level response.
func (r *HTTPResult) RowCount() int {
	if r.PaginationRecordCount != nil {
		return *r.PaginationRecordCount
	}
	if r.File != nil || len(r.Body) == 0 {
		return 0
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(r.Body, &rows); err == nil {
		return len(rows)
	}
	return 1
}

// ObjectRowsResult contains row-normalized object API operation output.
type ObjectRowsResult struct {
	ObjectType       string           `json:"object_type"`
	Operation        string           `json:"operation,omitempty"`
	Rows             []map[string]any `json:"rows"`
	ColumnNames      []string         `json:"column_names"`
	RowCount         int              `json:"row_count"`
	NextCursor       *string          `json:"next_cursor,omitempty"`
	HasMore          bool             `json:"has_more"`
	ProviderMetadata map[string]any   `json:"provider_metadata,omitempty"`
}

// FileResult contains data from FTP and Git file read/write execution.
type FileResult struct {
	ResourceID            string                           `json:"file_id,omitempty"`
	Name                  string                           `json:"name,omitempty"`
	MIMEType              string                           `json:"mime_type,omitempty"`
	Rows                  []map[string]any                 `json:"rows"`
	ColumnNames           []string                         `json:"column_names"`
	RowCount              int                              `json:"row_count"`
	Format                string                           `json:"format"`
	IsHeadlessCSV         bool                             `json:"is_headless_csv,omitempty"`
	IsHeadlessSpreadsheet bool                             `json:"is_headless_spreadsheet,omitempty"`
	Sheet                 string                           `json:"sheet,omitempty"`
	Range                 string                           `json:"range,omitempty"`
	Operation             string                           `json:"operation,omitempty"`
	TargetPath            string                           `json:"target_path,omitempty"`
	File                  *filepayload.FilePayloadMetadata `json:"file,omitempty"`
	Reference             *filepayload.Reference           `json:"-"`
	Content               []byte                           `json:"-"`
	WrittenBytes          *int                             `json:"written_bytes,omitempty"`
	WrittenRows           *int                             `json:"written_rows,omitempty"`
}
