// Package remotefile adapts provider-owned file transports to Braidplane's
// shared file connector execution contract.
package remotefile

import (
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
)

const (
	OperationRead  = "read"
	OperationWrite = "write"

	targetFieldKey = "key"
)

// Statement is the shared remote-file query shape after connector-specific
// JSON fields are decoded into a common Braidplane-owned contract.
type Statement struct {
	Type        string               `json:"type,omitempty"`
	Operation   string               `json:"operation,omitempty"`
	Path        string               `json:"path,omitempty"`
	Key         string               `json:"key,omitempty"`
	Bucket      string               `json:"bucket,omitempty"`
	Format      filedata.QueryFormat `json:"format,omitempty"`
	Delimiter   string               `json:"delimiter,omitempty"`
	HasHeader   *bool                `json:"has_header,omitempty"`
	Sheet       string               `json:"sheet,omitempty"`
	Range       string               `json:"range,omitempty"`
	Content     any                  `json:"content,omitempty"`
	Overwrite   *bool                `json:"overwrite,omitempty"`
	ContentType string               `json:"content_type,omitempty"`
	Metadata    map[string]string    `json:"metadata,omitempty"`
	Branch      string               `json:"branch,omitempty"`
	Message     string               `json:"message,omitempty"`
	ContentSet  bool                 `json:"-"`
}

// WriteOverrideOptions controls which runtime parameter names can override
// common write statement fields.
type WriteOverrideOptions struct {
	TargetParamName string
}

// DecodeStatement unmarshals a remote-file statement and records whether
// content was explicitly supplied, including a JSON null.
func DecodeStatement(raw json.RawMessage) (Statement, error) {
	var stmt Statement
	if err := json.Unmarshal(raw, &stmt); err != nil {
		return Statement{}, err
	}

	var rawQuery map[string]json.RawMessage
	_ = json.Unmarshal(raw, &rawQuery)
	_, stmt.ContentSet = rawQuery["content"]

	return stmt, nil
}

// OperationOrDefault normalizes operation text and defaults empty operations to read.
func OperationOrDefault(operation string) string {
	normalized := strings.ToLower(strings.TrimSpace(operation))
	if normalized == "" {
		return OperationRead
	}
	return normalized
}

// Target returns the connector-specific path/key field used for provider access.
func (stmt Statement) Target(fieldName string) string {
	switch strings.TrimSpace(fieldName) {
	case targetFieldKey:
		return stmt.Key
	default:
		return stmt.Path
	}
}

// SetTarget updates the connector-specific path/key field used for provider access.
func (stmt *Statement) SetTarget(fieldName, value string) {
	if stmt == nil {
		return
	}

	switch strings.TrimSpace(fieldName) {
	case targetFieldKey:
		stmt.Key = value
	default:
		stmt.Path = value
	}
}

// ApplyWriteParamOverrides lets runtime sink bindings override saved common
// write statement fields without provider-specific branching.
func ApplyWriteParamOverrides(stmt *Statement, params map[string]any, opts WriteOverrideOptions) {
	if stmt == nil || len(params) == 0 {
		return
	}

	targetParamName := strings.TrimSpace(opts.TargetParamName)
	if targetParamName == "" {
		targetParamName = "path"
	}

	if target, ok := params[targetParamName].(string); ok && strings.TrimSpace(target) != "" {
		stmt.SetTarget(targetParamName, target)
	}

	if content, ok := params[sinkbinding.Input]; ok {
		stmt.Content = content
		stmt.ContentSet = true
	}

	if stmt.Delimiter == "" {
		if delimiter, ok := params["delimiter"].(string); ok {
			stmt.Delimiter = delimiter
		}
	}

	if stmt.HasHeader == nil {
		if hasHeader, ok := params["has_header"].(bool); ok {
			stmt.HasHeader = &hasHeader
		}
	}

	if stmt.Overwrite == nil {
		if overwrite, ok := params["overwrite"].(bool); ok {
			stmt.Overwrite = &overwrite
		}
	}

	if stmt.Format == filedata.QueryFormatUnknown || stmt.Format == filedata.QueryFormatAuto {
		if rawFormat, ok := params["format"].(string); ok {
			switch filedata.ParseQueryFormat(strings.ToLower(strings.TrimSpace(rawFormat))) {
			case filedata.QueryFormatUnknown, filedata.QueryFormatAuto:
				// Keep auto-detection behavior.
			case filedata.QueryFormatCSV:
				stmt.Format = filedata.QueryFormatCSV
			case filedata.QueryFormatJSON:
				stmt.Format = filedata.QueryFormatJSON
			case filedata.QueryFormatFile:
				stmt.Format = filedata.QueryFormatFile
			case filedata.QueryFormatSpreadsheet:
				stmt.Format = filedata.QueryFormatSpreadsheet
			default:
				// Ignore unsupported runtime override; execution validates the final format.
			}
		}
	}

	if sheet, ok := params["sheet"].(string); ok && strings.TrimSpace(sheet) != "" {
		stmt.Sheet = sheet
	}

	if cellRange, ok := params["range"].(string); ok && strings.TrimSpace(cellRange) != "" {
		stmt.Range = cellRange
	}
}

// SubstituteCommonParams resolves placeholders in the shared target, sheet, and range fields.
func SubstituteCommonParams(stmt *Statement, params map[string]any, targetField string) error {
	if stmt == nil {
		return nil
	}

	if strings.TrimSpace(stmt.Target(targetField)) != "" {
		target, err := param.SubstituteString(stmt.Target(targetField), params)
		if err != nil {
			return xerror.Wrapf("failed to substitute %s parameters: %w", targetLabel(targetField), err)
		}
		stmt.SetTarget(targetField, target)
	}

	if strings.TrimSpace(stmt.Sheet) != "" {
		sheet, err := param.SubstituteString(stmt.Sheet, params)
		if err != nil {
			return xerror.Wrapf("failed to substitute sheet parameters: %w", err)
		}
		stmt.Sheet = sheet
	}

	if strings.TrimSpace(stmt.Range) != "" {
		cellRange, err := param.SubstituteString(stmt.Range, params)
		if err != nil {
			return xerror.Wrapf("failed to substitute range parameters: %w", err)
		}
		stmt.Range = cellRange
	}

	return nil
}

// SubstituteStringField resolves placeholders in a connector-specific string field.
func SubstituteStringField(label string, value *string, params map[string]any) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}

	substituted, err := param.SubstituteString(*value, params)
	if err != nil {
		return xerror.Wrapf("failed to substitute %s parameters: %w", label, err)
	}
	*value = substituted
	return nil
}

// SubstituteMetadata resolves placeholders in safe provider metadata values.
func SubstituteMetadata(metadata map[string]string, params map[string]any) error {
	for key, value := range metadata {
		substituted, err := param.SubstituteString(value, params)
		if err != nil {
			return xerror.Wrapf("failed to substitute metadata.%s parameters: %w", key, err)
		}
		metadata[key] = substituted
	}
	return nil
}

func targetLabel(targetField string) string {
	if strings.TrimSpace(targetField) == targetFieldKey {
		return targetFieldKey
	}
	return "path"
}
