package googleworkspace

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func (c *Connector) executeNative(ctx context.Context, s workspace.Statement, params map[string]types.TypedParam, pl provider.Limits, start time.Time, maxFileBytes int64) (*result.ExecutionResult, error) {
	fail := func(err error) (*result.ExecutionResult, error) { return executionError(s, start, err), nil }
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
			return fail(xerror.Forbidden(xerror.DataSource).Detail("Choose a writable Drive folder"))
		}
	} else {
		if metadata.MIMEType != s.NativeMIMEType() {
			return fail(xerror.ValidationFailed().Detail("The file type does not match the native operation"))
		}
		if s.Operation == "update" && !metadata.CanEdit || s.Operation == "export" && !metadata.CanDownload {
			return fail(xerror.Forbidden(xerror.DataSource).Detail("Google denies the requested native operation"))
		}
	}
	row := map[string]any{"file_id": metadata.FileID, "name": metadata.Name, "mime_type": metadata.MIMEType}
	columns := []string{"file_id", "name", "mime_type"}
	if !s.IsRead() {
		preview := row
		if s.Operation == "create" {
			preview = map[string]any{"file_id": strings.Repeat("x", 256), "name": s.Name, "mime_type": s.NativeMIMEType()}
		} else {
			preview["occurrences_changed"] = int64(math.MaxInt64)
		}
		output := result.ObjectRowsResult{ObjectType: s.Resource, Operation: s.Operation, Rows: []map[string]any{preview}, ColumnNames: append(append([]string{}, columns...), "occurrences_changed"), RowCount: 1}
		raw, err := json.Marshal(output)
		if err != nil || int64(len(raw)) > pl.MaxResponseBytes {
			return fail(xerror.ExecutionResponseSizeExceeded())
		}
	}
	switch s.Operation {
	case "read":
		raw, err := c.client.ReadNative(ctx, s.Resource, s.FileID, pl)
		if err != nil {
			return fail(err)
		}
		var content map[string]any
		if err := json.Unmarshal(raw, &content); err != nil {
			return fail(xerror.ExecutionFailed().Detail("Invalid native content"))
		}
		row["content"] = content
		columns = append(columns, "content")
	case "create":
		metadata, err = c.client.CreateNative(ctx, s.FolderID, s.Name, s.NativeMIMEType(), pl)
		if err != nil {
			return fail(err)
		}
		row = map[string]any{"file_id": metadata.FileID, "name": metadata.Name, "mime_type": metadata.MIMEType}
	case "update":
		var text string
		if err := json.Unmarshal(s.Content, &text); err != nil {
			return fail(xerror.ValidationFailed().Detail("Native replacement content must be a string"))
		}
		if text == "{{input}}" {
			normalized, err := types.NormalizeParams(params)
			if err != nil {
				return fail(err)
			}
			value, err := param.SubstituteValue(text, normalized)
			if err != nil {
				return fail(err)
			}
			if _, binary := value.([]byte); binary {
				return fail(xerror.ValidationFailed().Detail("Native replacement does not accept binary input"))
			}
			var ok bool
			text, ok = value.(string)
			if !ok {
				raw, err := json.Marshal(value)
				if err != nil {
					return fail(xerror.ValidationFailed().Detail("Native replacement input must be JSON data or text"))
				}
				text = string(raw)
			}
		}
		changed, err := c.client.ReplaceNative(ctx, s.Resource, s.FileID, s.MatchText, text, pl)
		if err != nil {
			return fail(err)
		}
		row["occurrences_changed"] = changed
		columns = append(columns, "occurrences_changed")
	case "export":
		if maxFileBytes > 0 {
			return c.streamPDF(ctx, s, metadata, pl, maxFileBytes, start)
		}
		content, err := c.client.ExportPDF(ctx, s.FileID, pl)
		if err != nil {
			return fail(err)
		}
		return nativePDFResult(metadata, content, time.Since(start).Milliseconds()), nil
	}
	output := &result.ObjectRowsResult{ObjectType: s.Resource, Operation: s.Operation, Rows: []map[string]any{row}, ColumnNames: columns, RowCount: 1}
	encoded, err := json.Marshal(output)
	if err != nil {
		return fail(xerror.ExecutionFailed().Detail("Invalid native result"))
	}
	if int64(len(encoded)) > pl.MaxResponseBytes {
		return fail(xerror.ExecutionResponseSizeExceeded())
	}
	return result.NewObjectRowsResult("google_workspace", output, time.Since(start).Milliseconds(), nil), nil
}

func nativePDFResult(metadata *workspace.File, content []byte, duration int64) *result.ExecutionResult {
	file := filepayload.NewFilePayloadMetadata(metadata.Name+".pdf", "application/pdf", content)
	output := &result.FileResult{ResourceID: metadata.FileID, TargetPath: metadata.FileID, Name: metadata.Name, MIMEType: "application/pdf", Operation: "export", Format: "file", Content: content, File: &file}
	return result.NewWorkspaceFileResult(output, duration, nil)
}
