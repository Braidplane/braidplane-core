package googleworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type Connector struct {
	config workspace.Config
	client *provider.Client
}

func New(ctx context.Context, config workspace.Config, creds provider.Credentials) (*Connector, error) {
	if err := config.Validate(); err != nil {
		return nil, xerror.Wrapf("validate Workspace config: %w", err)
	}
	client, err := provider.New(ctx, creds)
	if err != nil {
		return nil, err
	}
	if err := client.CheckAccess(false); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Connector{config: config, client: client}, nil
}
func (c *Connector) Close() error { return c.client.Close() }
func (c *Connector) TestConnection(ctx context.Context) error {
	return c.client.TestConnection(ctx, provider.Limits{})
}

func (c *Connector) IntrospectSchema(ctx context.Context) (*filedata.Schema, error) {
	schema := &filedata.Schema{Files: []filedata.File{}}
	page, err := c.client.Discovery(ctx, workspace.ListOptions{}, provider.Limits{}, 100)
	if err != nil {
		return nil, err
	}
	for _, f := range page.Files {
		schema.Files = append(schema.Files, filedata.File{Name: f.Name, ResourceID: f.FileID, MIMEType: f.MIMEType, Fields: []filedata.Field{}})
	}
	return schema, nil
}

func (c *Connector) Execute(ctx context.Context, raw json.RawMessage, params map[string]types.TypedParam, limits ...types.ExecutionLimits) (*result.ExecutionResult, error) {
	start := time.Now()
	statement, err := workspace.DecodeStatement(raw)
	fail := func(err error) (*result.ExecutionResult, error) {
		return executionError(statement, start, err), nil
	}
	if err != nil {
		return fail(err)
	}
	lim := types.ResolveExecutionLimits(limits...)
	if !statement.IsRead() && lim.ReadOnly {
		return fail(xerror.DataSourceReadOnly())
	}
	pl := providerLimits(lim)
	ctx, cancel := context.WithTimeout(ctx, pl.Timeout)
	defer cancel()
	if statement.Resource == "file" && statement.Operation == "create" || statement.IsNative() && !statement.IsRead() {
		if policy, ok := params[sinkbinding.InternalIdempotencyClass]; ok && policy.Value != "unsafe" {
			return fail(xerror.ReplayUnsafe().Detail("Workspace native writes and Drive create require unsafe replay policy"))
		}
	}
	if statement.IsNative() {
		return c.executeNative(ctx, statement, params, pl, start, lim.MaxFileBytes)
	}
	if statement.Resource == "file" {
		return c.executeFile(ctx, statement, params, lim, pl, start)
	}
	rectangle, err := workspace.ParseRange(statement.Range)
	if err != nil {
		return fail(err)
	}
	height, width := rectangle.EndRow-rectangle.StartRow+1, rectangle.EndColumn-rectangle.StartColumn+1
	rowCount := height
	if statement.Operation == "read" {
		rowCount--
	}
	if lim.MaxResultRows > 0 && rowCount > lim.MaxResultRows {
		return fail(xerror.ExecutionRowLimitExceeded())
	}
	// Even an empty provider response expands to the full requested rectangle.
	if int64(width) > pl.MaxResponseBytes/4 || int64(rowCount) > pl.MaxResponseBytes/(int64(width)*4+3) {
		return fail(xerror.ExecutionResponseSizeExceeded())
	}
	var values [][]any
	if statement.Operation == "update" {
		values, err = updateValues(statement, params, height)
		if err != nil {
			return fail(err)
		}
	}
	metadata, err := c.client.Metadata(ctx, statement.SpreadsheetID, pl)
	if err != nil {
		return fail(err)
	}
	var title string
	found := false
	for _, sheet := range metadata.Worksheets {
		if sheet.WorksheetID == *statement.WorksheetID {
			title = sheet.Title
			found = true
			if rectangle.EndRow > sheet.RowCount || rectangle.EndColumn > sheet.ColumnCount {
				return fail(xerror.ValidationFailed().Detail("The target rectangle exceeds the current worksheet grid"))
			}
			break
		}
	}
	if !found {
		return fail(xerror.ValidationFailed().Detail("The selected worksheet no longer exists"))
	}
	target := "'" + strings.ReplaceAll(title, "'", "''") + "'!" + statement.Range
	output := &result.ObjectRowsResult{ObjectType: "worksheet", Operation: statement.Operation, Rows: []map[string]any{}, ColumnNames: []string{}, RowCount: rowCount}
	if statement.Operation == "update" {
		encoded, err := json.Marshal(output)
		if err != nil {
			return fail(xerror.ExecutionFailed().Detail("Invalid Sheets result"))
		}
		if int64(len(encoded)) > pl.MaxResponseBytes {
			return fail(xerror.ExecutionResponseSizeExceeded())
		}
		if err := c.client.Update(ctx, statement.SpreadsheetID, target, values, pl); err != nil {
			return fail(err)
		}
	} else {
		values, err = c.client.Read(ctx, statement.SpreadsheetID, target, pl)
		if err != nil {
			return fail(err)
		}
		output.Rows, output.ColumnNames, err = readRows(values, height, width, pl.MaxResponseBytes)
		if err != nil {
			return fail(err)
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return fail(xerror.ExecutionFailed().Detail("Invalid Sheets result"))
	}
	if int64(len(encoded)) > pl.MaxResponseBytes {
		return fail(xerror.ExecutionResponseSizeExceeded())
	}
	return result.NewObjectRowsResult("google_workspace", output, time.Since(start).Milliseconds(), nil), nil
}

func providerLimits(l types.ExecutionLimits) provider.Limits {
	timeout := 30 * time.Second
	if l.MaxFileBytes > 0 && l.ConnectorTimeout > 0 {
		timeout = l.ConnectorTimeout
	}
	for _, value := range []time.Duration{l.ConnectorTimeout, l.HTTPTimeout} {
		if value > 0 && value < timeout {
			timeout = value
		}
	}
	response := int64(10 * 1024 * 1024)
	for _, value := range []int64{l.MaxResponseBytes, l.HTTPMaxResponseBytes} {
		if value > 0 && value < response {
			response = value
		}
	}
	return provider.Limits{Timeout: timeout, MaxResponseBytes: response, MaxRequestBytes: l.HTTPMaxRequestBytes, RetryAttempts: l.HTTPRetryAttempts, RetryBackoff: l.HTTPRetryBackoff}
}

func updateValues(s workspace.Statement, params map[string]types.TypedParam, height int) ([][]any, error) {
	normalized, err := types.NormalizeParams(params)
	if err != nil {
		return nil, err
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(s.Rows))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, xerror.ValidationFailed().Detail("Invalid update rows")
	}
	// Only the contract's exact input binding is substituted; literal cell strings stay literal RAW values.
	if binding, ok := value.(string); ok {
		value, err = param.SubstituteValue(binding, normalized)
		if err != nil {
			return nil, xerror.ValidationFailed().Detail("The upstream input binding is missing")
		}
	}
	if text, ok := value.(string); ok {
		var parsed *result.FileResult
		if normalized[sinkbinding.InternalInputRepresentation] == "csv" {
			header := true
			parsed, err = filedata.ParseCSV([]byte(text), filedata.ParseCSVOptions{HasHeader: &header, MaxRows: height, MaxBytes: 2_000_000})
		} else {
			parsed, err = filedata.ParseJSONWithLimits([]byte(text), filedata.ParseJSONOptions{MaxRows: height, MaxBytes: 2_000_000})
		}
		if err != nil {
			return nil, err
		}
		value = parsed.Rows
	}
	var rows []map[string]any
	switch v := value.(type) {
	case []map[string]any:
		rows = v
	case []any:
		if len(v) != height {
			return nil, xerror.ValidationFailed().Detail("Update row count must match the rectangle height")
		}
		rows = make([]map[string]any, len(v))
		for i, row := range v {
			var ok bool
			rows[i], ok = row.(map[string]any)
			if !ok {
				return nil, xerror.ValidationFailed().Detail("Update rows must be objects")
			}
		}
	default:
		return nil, xerror.ValidationFailed().Detail("Update rows must resolve to an array of row objects")
	}
	if len(rows) != height {
		return nil, xerror.ValidationFailed().Detail("Update row count must match the rectangle height")
	}
	values := make([][]any, len(rows))
	for i, row := range rows {
		values[i] = make([]any, len(s.Columns))
		for j, key := range s.Columns {
			cell, ok := row[key]
			if !ok {
				return nil, xerror.ValidationFailed().Detail("Every mapped update key must be present")
			}
			switch cell.(type) {
			case nil:
				cell = ""
			case string, bool, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			default:
				return nil, xerror.ValidationFailed().Detail("Update cells must be scalar values")
			}
			values[i][j] = cell
		}
	}
	return values, nil
}

func readRows(values [][]any, height, width int, maxBytes int64) ([]map[string]any, []string, error) {
	if len(values) == 0 || len(values[0]) != width {
		return nil, nil, xerror.ValidationFailed().Detail("Every selected column must have a string header").WithPublicDetail("Every selected column must have a string header")
	}
	if len(values) > height {
		return nil, nil, xerror.ExecutionFailed().Detail("Sheets returned values outside the target rectangle")
	}
	columns := make([]string, width)
	seen := make(map[string]bool, width)
	// Count JSON object keys and null padding before allocating the padded rows.
	rowBytes := int64(3)
	for i, value := range values[0] {
		header, ok := value.(string)
		header = strings.TrimSpace(header)
		if !ok || header == "" || seen[header] {
			return nil, nil, xerror.ValidationFailed().Detail("Headers must be nonblank unique strings").WithPublicDetail("Headers must be nonblank unique strings")
		}
		columns[i] = header
		seen[header] = true
		encoded, err := json.Marshal(header)
		if err != nil {
			return nil, nil, xerror.ExecutionFailed().Detail("Invalid Sheets header")
		}
		rowBytes += int64(len(encoded) + 6)
	}
	if rowBytes > maxBytes || int64(height-1) > maxBytes/rowBytes {
		return nil, nil, xerror.ExecutionResponseSizeExceeded()
	}
	rows := make([]map[string]any, height-1)
	for i := 1; i < height; i++ {
		row := make(map[string]any, width)
		if i < len(values) && len(values[i]) > width {
			return nil, nil, xerror.ExecutionFailed().Detail("Sheets returned values outside the target rectangle")
		}
		for j, key := range columns {
			var cell any
			if i < len(values) && j < len(values[i]) {
				cell = values[i][j]
			}
			switch cell.(type) {
			case nil, string, bool, json.Number, float64:
			default:
				return nil, nil, xerror.ExecutionFailed().Detail("Sheets returned a non-scalar cell")
			}
			row[key] = cell
		}
		rows[i-1] = row
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return nil, nil, xerror.ExecutionFailed().Detail("Invalid Sheets values")
	}
	if int64(len(encoded)) > maxBytes {
		return nil, nil, xerror.ExecutionResponseSizeExceeded()
	}
	return rows, columns, nil
}

var driveFileAdapter = remotefile.Adapter{ConnectorName: "Google Drive", ErrorKind: remotefile.ErrorKindInvalidQuery, LimitPolicy: remotefile.LimitPolicy{UseHTTPFallbackForRead: true, UseHTTPFallbackForParse: true}}

type driveFileProvider struct {
	client    *provider.Client
	statement workspace.Statement
	metadata  *workspace.File
	limits    provider.Limits
	written   *workspace.File
}

func (p *driveFileProvider) ReadFile(ctx context.Context, _ string, _ int64) (*remotefile.ReadResult, error) {
	content, err := p.client.Download(ctx, p.statement.FileID, p.limits)
	if err != nil {
		return nil, err
	}
	return &remotefile.ReadResult{Content: content, ContentType: p.metadata.MIMEType}, nil
}

func (p *driveFileProvider) WriteFile(ctx context.Context, r remotefile.WriteRequest) (*remotefile.WriteResult, error) {
	f, err := p.client.Upload(ctx, p.statement.FileID, p.statement.FolderID, p.statement.Name, r.ContentType, r.Content, p.limits)
	if err != nil {
		return nil, err
	}
	p.written = f
	return &remotefile.WriteResult{WrittenBytes: len(r.Content)}, nil
}

func (c *Connector) executeFile(ctx context.Context, s workspace.Statement, params map[string]types.TypedParam, limits types.ExecutionLimits, pl provider.Limits, start time.Time) (*result.ExecutionResult, error) {
	fail := func(err error) (*result.ExecutionResult, error) {
		return executionError(s, start, err), nil
	}
	if s.Operation == "list" {
		page, err := c.client.Discovery(ctx, s.ListOptions(), pl, limits.MaxResultRows)
		if err != nil {
			return fail(err)
		}
		output := &result.ObjectRowsResult{ObjectType: "file", Operation: "list", Rows: []map[string]any{}, ColumnNames: []string{"file_id", "name", "mime_type", "drive_id"}}
		for _, f := range page.Files {
			output.Rows = append(output.Rows, map[string]any{"file_id": f.FileID, "name": f.Name, "mime_type": f.MIMEType, "drive_id": f.DriveID})
		}
		output.RowCount = len(output.Rows)
		if page.NextPageToken != "" {
			output.NextCursor = &page.NextPageToken
			output.HasMore = true
		}
		raw, err := json.Marshal(output)
		if err != nil {
			return fail(xerror.Unexpected().With(err))
		}
		if int64(len(raw)) > pl.MaxResponseBytes {
			return fail(xerror.ExecutionResponseSizeExceeded())
		}
		return result.NewObjectRowsResult("google_workspace", output, time.Since(start).Milliseconds(), nil), nil
	}
	target := s.FileID
	if s.Operation == "create" {
		target = s.FolderID
	}
	metadata, err := c.client.FileMetadata(ctx, target, pl)
	if err != nil {
		return fail(err)
	}
	if s.Operation == "create" {
		if metadata.MIMEType != "application/vnd.google-apps.folder" || !metadata.CanAddChildren {
			return fail(xerror.Forbidden(xerror.DataSource).Detail("Choose a writable Drive destination folder"))
		}
	} else {
		if strings.HasPrefix(metadata.MIMEType, "application/vnd.google-apps.") {
			return fail(xerror.ValidationFailed().Detail("Use a native Sheets operation; native Docs/Slides and shortcuts are unsupported"))
		}
		if s.Operation == "read" && !metadata.CanDownload || s.Operation == "update" && !metadata.CanEdit {
			return fail(xerror.Forbidden(xerror.DataSource).Detail("Google denies the requested file operation"))
		}
	}
	fileProvider := &driveFileProvider{client: c.client, statement: s, metadata: metadata, limits: pl}
	remote := remotefile.Statement{Format: filedata.ParseQueryFormat(s.Format), Delimiter: s.Delimiter, HasHeader: s.HasHeader, ContentType: s.MIMEType}
	var output *result.FileResult
	if s.Operation == "read" {
		output, err = driveFileAdapter.ExecuteRead(ctx, fileProvider, metadata.Name, remote, limits)
	} else {
		normalized, e := types.NormalizeParams(params)
		if e != nil {
			return fail(e)
		}
		decoder := json.NewDecoder(bytes.NewReader(s.Content))
		decoder.UseNumber()
		if e := decoder.Decode(&remote.Content); e != nil {
			return fail(xerror.ValidationFailed().With(e))
		}
		remote.ContentSet = true
		if binding, ok := remote.Content.(string); ok && binding == "{{input}}" {
			remote.Content, e = param.SubstituteValue(binding, normalized)
			if e != nil {
				return fail(e)
			}
		}
		filename := metadata.Name
		if s.Operation == "create" {
			filename = s.Name
		}
		if remote.ContentType == "" {
			if s.Operation == "update" {
				remote.ContentType = metadata.MIMEType
			} else {
				switch s.Format {
				case "json":
					remote.ContentType = "application/json"
				case "csv":
					remote.ContentType = "text/csv"
				default:
					remote.ContentType = filedata.MIMETypeForPath(filename, "")
				}
			}
		}
		output, err = driveFileAdapter.ExecuteWrite(ctx, fileProvider, filename, remote, limits)
		if fileProvider.written != nil {
			metadata = fileProvider.written
		}
	}
	if err != nil {
		return fail(err)
	}
	output.Operation = s.Operation
	output.TargetPath = metadata.FileID
	output.ResourceID = metadata.FileID
	output.Name = metadata.Name
	output.MIMEType = metadata.MIMEType
	if output.File != nil {
		output.File.Filename = metadata.Name
		output.File.MIMEType = metadata.MIMEType
	}
	return result.NewWorkspaceFileResult(output, time.Since(start).Milliseconds(), nil), nil
}

func executionError(s workspace.Statement, start time.Time, err error) *result.ExecutionResult {
	duration := time.Since(start).Milliseconds()
	if s.Resource == "file" && s.Operation != "list" || s.IsNative() && s.Operation == "export" {
		format, mimeType := s.Format, s.MIMEType
		if s.IsNative() {
			format, mimeType = "file", "application/pdf"
		}
		return result.NewWorkspaceFileResult(&result.FileResult{Operation: s.Operation, Format: format, ResourceID: s.FileID, TargetPath: s.FileID, Name: s.Name, MIMEType: mimeType}, duration, err)
	}
	var payload *result.ObjectRowsResult
	if s.IsNative() {
		payload = &result.ObjectRowsResult{ObjectType: s.Resource, Operation: s.Operation, Rows: []map[string]any{{"file_id": s.FileID, "folder_id": s.FolderID}}}
	}
	if s.Resource == "file" {
		payload = &result.ObjectRowsResult{ObjectType: "file", Operation: "list", Rows: []map[string]any{}}
	}
	return result.NewObjectRowsResult("google_workspace", payload, duration, err)
}
