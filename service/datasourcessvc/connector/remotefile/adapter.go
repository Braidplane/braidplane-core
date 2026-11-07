package remotefile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// ErrorKind selects the xerror sentinel used for adapter-owned validation failures.
type ErrorKind int

const (
	ErrorKindUnexpected ErrorKind = iota
	ErrorKindInvalidQuery
)

// LimitPolicy captures existing connector differences in how byte limits are applied.
type LimitPolicy struct {
	UseHTTPFallbackForRead  bool
	UseHTTPFallbackForParse bool
}

// Adapter contains the common Braidplane-owned remote-file behavior shared by
// provider-backed connectors.
type Adapter struct {
	ConnectorName       string
	ErrorKind           ErrorKind
	LimitPolicy         LimitPolicy
	SpreadsheetFilePath bool
	SchemaParsedColumns bool
}

// Provider performs protocol-owned remote file operations for one connector.
type Provider interface {
	ReadFile(context.Context, string, int64) (*ReadResult, error)
	WriteFile(context.Context, WriteRequest) (*WriteResult, error)
}

// ReadResult carries bytes and safe provider metadata returned by a remote read.
type ReadResult struct {
	Content     []byte
	ContentType string
}

// WriteRequest carries serialized bytes and safe metadata into a provider write.
type WriteRequest struct {
	Target      string
	Content     []byte
	ContentType string
	Metadata    map[string]string
	Overwrite   bool
}

// WriteResult reports provider-accepted write metadata.
type WriteResult struct {
	WrittenBytes int
}

// ExecuteOperation dispatches a normalized remote-file read or write operation.
func (adapter Adapter) ExecuteOperation(
	ctx context.Context,
	provider Provider,
	target string,
	stmt Statement,
	limits types.ExecutionLimits,
) (*result.FileResult, error) {
	switch OperationOrDefault(stmt.Operation) {
	case OperationRead:
		return adapter.ExecuteRead(ctx, provider, target, stmt, limits)
	case OperationWrite:
		return adapter.ExecuteWrite(ctx, provider, target, stmt)
	default:
		return nil, adapter.queryErrorf("unsupported %s operation %q", adapter.connectorName(), stmt.Operation)
	}
}

// ExecuteRead fetches remote bytes through the provider and normalizes them into a file result.
func (adapter Adapter) ExecuteRead(
	ctx context.Context,
	provider Provider,
	target string,
	stmt Statement,
	limits types.ExecutionLimits,
) (*result.FileResult, error) {
	readLimit := adapter.responseByteLimit(limits, adapter.LimitPolicy.UseHTTPFallbackForRead)
	content, err := provider.ReadFile(ctx, target, readLimit)
	if err != nil {
		return nil, err
	}
	if readLimit > 0 && int64(len(content.Content)) > readLimit {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			readLimit,
		)
	}

	format, err := filedata.DetectFormat(stmt.Format, target, content.Content)
	if err != nil {
		return nil, err
	}

	parseMaxBytes := adapter.responseByteLimit(limits, adapter.LimitPolicy.UseHTTPFallbackForParse)
	var fileResult *result.FileResult
	switch format {
	case filedata.QueryFormatFile:
		fileResult = filedata.BuildFileResultWithMIMEType(
			content.Content,
			target,
			OperationRead,
			content.ContentType,
		)
	case filedata.QueryFormatCSV:
		fileResult, err = filedata.ParseCSV(content.Content, filedata.ParseCSVOptions{
			Delimiter: stmt.Delimiter,
			HasHeader: stmt.HasHeader,
			MaxRows:   limits.MaxResultRows,
			MaxBytes:  parseMaxBytes,
		})
	case filedata.QueryFormatJSON:
		fileResult, err = filedata.ParseJSONWithLimits(content.Content, filedata.ParseJSONOptions{
			MaxRows:  limits.MaxResultRows,
			MaxBytes: parseMaxBytes,
		})
	case filedata.QueryFormatSpreadsheet:
		fileResult, err = filedata.ParseSpreadsheet(content.Content, filedata.SpreadsheetOptions{
			Sheet:     stmt.Sheet,
			Range:     stmt.Range,
			HasHeader: stmt.HasHeader,
			MaxRows:   limits.MaxResultRows,
			MaxBytes:  parseMaxBytes,
			FilePath:  adapter.spreadsheetPath(target),
		})
	default:
		err = adapter.queryErrorf("unsupported %s file format %q", adapter.connectorName(), format)
	}
	if err != nil {
		return nil, err
	}

	fileResult.Operation = OperationRead
	fileResult.TargetPath = target
	return fileResult, nil
}

// ExecuteWrite serializes statement content, writes it through the provider, and normalizes metadata.
func (adapter Adapter) ExecuteWrite(
	ctx context.Context,
	provider Provider,
	target string,
	stmt Statement,
) (*result.FileResult, error) {
	if stmt.Content == nil && !stmt.ContentSet {
		return nil, adapter.queryErrorf("%s write operation requires content", adapter.connectorName())
	}

	serialized, format, writtenRows, err := adapter.SerializeWriteContent(stmt, target)
	if err != nil {
		return nil, err
	}

	overwrite := true
	if stmt.Overwrite != nil {
		overwrite = *stmt.Overwrite
	}
	contentType := strings.TrimSpace(stmt.ContentType)
	if contentType == "" {
		contentType = filedata.MIMETypeForPath(target, "")
	}

	writeResult, err := provider.WriteFile(ctx, WriteRequest{
		Target:      target,
		Content:     serialized,
		ContentType: contentType,
		Metadata:    stmt.Metadata,
		Overwrite:   overwrite,
	})
	if err != nil {
		return nil, err
	}

	writtenBytes := writeResult.WrittenBytes
	fileResult := &result.FileResult{
		Format:       format.String(),
		Operation:    OperationWrite,
		TargetPath:   target,
		WrittenBytes: &writtenBytes,
	}
	if format == filedata.QueryFormatFile {
		nativeResult := filedata.BuildFileResultWithMIMEType(
			serialized,
			target,
			OperationWrite,
			contentType,
		)
		fileResult.File = nativeResult.File
		fileResult.Content = nativeResult.Content
	}
	if writeFormatSupportsRowCount(format) {
		fileResult.WrittenRows = &writtenRows
	}
	if format == filedata.QueryFormatSpreadsheet {
		fileResult.Sheet = SpreadsheetResultSheet(stmt.Sheet)
		fileResult.Range = strings.TrimSpace(stmt.Range)
	}

	return fileResult, nil
}

// SerializeWriteContent converts remote-file write content into provider bytes.
func (adapter Adapter) SerializeWriteContent(
	stmt Statement,
	target string,
) ([]byte, filedata.QueryFormat, int, error) {
	if (stmt.Format == filedata.QueryFormatUnknown || stmt.Format == filedata.QueryFormatAuto) &&
		isByteContent(stmt.Content) {
		payload, err := filedata.EncodeFileContent(stmt.Content)
		return payload, filedata.QueryFormatFile, 0, err
	}

	format, err := filedata.DetectWriteFormat(stmt.Format, target)
	if err != nil {
		return nil, filedata.QueryFormatUnknown, 0, err
	}

	switch format {
	case filedata.QueryFormatFile:
		payload, err := filedata.EncodeFileContent(stmt.Content)
		return payload, filedata.QueryFormatFile, 0, err
	case filedata.QueryFormatJSON:
		if !isStructuredJSONContent(stmt.Content) {
			return nil, filedata.QueryFormatUnknown, 0, adapter.queryErrorf(
				"JSON %s write requires object or array content; scalar and null content are not supported",
				adapter.connectorName(),
			)
		}

		payload, err := json.Marshal(stmt.Content)
		if err != nil {
			return nil, filedata.QueryFormatUnknown, 0, xerror.Unexpected().With(err)
		}
		writtenRows := filedata.EstimateWrittenRows(stmt.Content)
		return payload, filedata.QueryFormatJSON, writtenRows, nil
	case filedata.QueryFormatCSV:
		if raw, ok := stmt.Content.(string); ok {
			return []byte(raw), filedata.QueryFormatCSV, 0, nil
		}
		payload, writtenRows, err := filedata.EncodeCSVContent(
			stmt.Content,
			filedata.ParseCSVOptions{Delimiter: stmt.Delimiter, HasHeader: stmt.HasHeader},
		)
		if err != nil {
			return nil, filedata.QueryFormatUnknown, 0, err
		}
		return payload, filedata.QueryFormatCSV, writtenRows, nil
	case filedata.QueryFormatSpreadsheet:
		payload, writtenRows, err := filedata.EncodeSpreadsheetContent(
			stmt.Content,
			filedata.SpreadsheetOptions{
				Sheet:     stmt.Sheet,
				Range:     stmt.Range,
				HasHeader: stmt.HasHeader,
				FilePath:  target,
			},
		)
		if err != nil {
			return nil, filedata.QueryFormatUnknown, 0, err
		}
		return payload, filedata.QueryFormatSpreadsheet, writtenRows, nil
	default:
		return nil, filedata.QueryFormatUnknown, 0, adapter.queryErrorf(
			"unsupported %s write format %q",
			adapter.connectorName(),
			format,
		)
	}
}

// ReadAllLimited reads a provider stream while enforcing the supplied byte guardrail.
func ReadAllLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return content, nil
	}

	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if int64(len(content)) > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			maxBytes,
		)
	}

	return content, nil
}

// SpreadsheetResultSheet reports the sheet name used in spreadsheet write results.
func SpreadsheetResultSheet(sheet string) string {
	if strings.TrimSpace(sheet) == "" {
		return filedata.DefaultSpreadsheetSheetName
	}

	return strings.TrimSpace(sheet)
}

// responseByteLimit resolves the connector-specific byte limit policy for read and parse phases.
func (adapter Adapter) responseByteLimit(
	limits types.ExecutionLimits,
	useHTTPFallback bool,
) int64 {
	if limits.MaxResponseBytes > 0 {
		return limits.MaxResponseBytes
	}
	if useHTTPFallback {
		return limits.HTTPMaxResponseBytes
	}
	return 0
}

// spreadsheetPath returns the file path hint only for connectors that preserve workbook extensions.
func (adapter Adapter) spreadsheetPath(target string) string {
	if adapter.SpreadsheetFilePath {
		return target
	}
	return ""
}

// connectorName returns a stable connector label for shared adapter diagnostics.
func (adapter Adapter) connectorName() string {
	if strings.TrimSpace(adapter.ConnectorName) == "" {
		return "remote file"
	}
	return strings.TrimSpace(adapter.ConnectorName)
}

// queryErrorf maps adapter validation failures through ErrorKind to InvalidInput or Unexpected.
func (adapter Adapter) queryErrorf(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if adapter.ErrorKind == ErrorKindInvalidQuery {
		return xerror.InvalidInput(xerror.Query).Detail(detail)
	}
	return xerror.Unexpected().Detail(detail)
}

// isByteContent reports whether write content is an already-encoded native file payload.
func isByteContent(value any) bool {
	_, ok := value.([]byte)
	return ok
}

// isStructuredJSONContent accepts JSON object or array payloads and rejects scalar or null content.
func isStructuredJSONContent(value any) bool {
	switch value.(type) {
	case map[string]any, []any, []map[string]any:
		return true
	default:
		return false
	}
}

// writeFormatSupportsRowCount reports whether zero written rows is meaningful for the format.
func writeFormatSupportsRowCount(format filedata.QueryFormat) bool {
	switch format {
	case filedata.QueryFormatCSV, filedata.QueryFormatJSON, filedata.QueryFormatSpreadsheet:
		return true
	default:
		return false
	}
}
