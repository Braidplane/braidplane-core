package run

import (
	"fmt"
	"strings"

	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrRefUnknownStep  = xerror.RefUnknownStep()
	ErrRefUnknownField = xerror.RefUnknownField()
	ErrRefStepNoResult = xerror.RefStepNoResult()
)

const (
	refPrefix = "$steps."

	refFieldRows        = "rows"
	refFieldColumnNames = "column_names"
	refFieldRowCount    = "row_count"
)

// isRef reports whether v is a step output reference string.
func isRef(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	if !strings.HasPrefix(s, refPrefix) {
		return "", false
	}
	return s, true
}

// resolveParams substitutes step output references in params with actual values
// from completed step results. Plain values pass through unchanged.
func resolveParams(
	params map[string]any,
	completed map[slug.Slug]*StepResult,
) (map[string]any, error) {
	if len(params) == 0 {
		return params, nil
	}

	resolved := make(map[string]any, len(params))
	for k, v := range params {
		val, err := resolveParamValue(v, completed, "parameter \""+k+"\"")
		if err != nil {
			return nil, err
		}
		resolved[k] = val
	}
	return resolved, nil
}

func resolveParamValue(
	value any,
	completed map[slug.Slug]*StepResult,
	path string,
) (any, error) {
	if literal, ok := value.(pipelineruntime.LiteralString); ok {
		return string(literal), nil
	}

	if ref, ok := isRef(value); ok {
		resolved, err := resolveRef(ref, completed)
		if err != nil {
			return nil, xerror.Wrapf("%s: %w", path, err)
		}
		return resolved, nil
	}

	switch typed := value.(type) {
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		for key, nestedValue := range typed {
			next, err := resolveParamValue(
				nestedValue,
				completed,
				fmt.Sprintf("%s.%s", path, key),
			)
			if err != nil {
				return nil, err
			}
			resolved[key] = next
		}
		return resolved, nil
	case []any:
		resolved := make([]any, len(typed))
		for index, nestedValue := range typed {
			next, err := resolveParamValue(
				nestedValue,
				completed,
				fmt.Sprintf("%s[%d]", path, index),
			)
			if err != nil {
				return nil, err
			}
			resolved[index] = next
		}
		return resolved, nil
	default:
		return value, nil
	}
}

// resolveRef resolves a single reference string of the form $steps.<slug>.<field>.
func resolveRef(ref string, completed map[slug.Slug]*StepResult) (any, error) {
	// ref is guaranteed to start with "$steps." here
	rest := strings.TrimPrefix(ref, refPrefix)
	dotIdx := strings.Index(rest, ".")
	if dotIdx < 0 {
		return nil, ErrRefUnknownField.Detailf("%q (expected $steps.<slug>.<field>)", ref)
	}

	stepSlugStr := rest[:dotIdx]
	field := rest[dotIdx+1:]

	stepSlug, err := slug.New(stepSlugStr)
	if err != nil {
		return nil, ErrRefUnknownStep.Detailf("%q", stepSlugStr)
	}

	res, ok := completed[stepSlug]
	if !ok {
		return nil, ErrRefUnknownStep.Detailf("%q", stepSlug)
	}
	if res == nil {
		return nil, ErrRefStepNoResult.Detailf("%q", stepSlug)
	}

	return extractField(ref, res, field)
}

// extractField pulls a named top-level field from a StepResult.
// Supported fields map to the connector result types:
//   - SQL:     rows, column_names, row_count
//   - MongoDB: documents, document_count, operation_type
//   - HTTP:    status_code, body, headers, file
//   - Object rows: rows, column_names, row_count, object_type, operation, next_cursor
//   - FTP/Git: file
//   - Transform: rows, column_names, row_count, document, csv
func extractField(ref string, res *StepResult, field string) (any, error) {
	if sql, ok := res.SQL(); ok {
		switch field {
		case refFieldRows:
			return sql.Rows, nil
		case refFieldColumnNames:
			return sql.ColumnNames, nil
		case refFieldRowCount:
			return sql.RowCount, nil
		}
	}

	if mongo, ok := res.MongoDB(); ok {
		switch field {
		case "documents":
			return mongo.Documents, nil
		case "document_count":
			return mongo.DocumentCount, nil
		case "operation_type":
			return mongo.OperationType, nil
		}
	}

	if http, ok := res.HTTP(); ok {
		switch field {
		case "status_code":
			return http.StatusCode, nil
		case "body":
			return http.Body, nil
		case "headers":
			return http.Headers, nil
		case "file":
			return http.File, nil
		}
	}

	if objectRows, ok := res.ObjectRows(); ok {
		switch field {
		case refFieldRows:
			return objectRows.Rows, nil
		case refFieldColumnNames:
			return objectRows.ColumnNames, nil
		case refFieldRowCount:
			return objectRows.RowCount, nil
		case "object_type":
			return objectRows.ObjectType, nil
		case "operation":
			return objectRows.Operation, nil
		case "next_cursor":
			return objectRows.NextCursor, nil
		}
	}

	if file, ok := res.File(); ok {
		if field == "file" {
			return file.File, nil
		}
	}

	if transform, ok := res.Transform(); ok {
		switch field {
		case refFieldRows:
			return transform.Rows, nil
		case refFieldColumnNames:
			return transform.ColumnNames, nil
		case refFieldRowCount:
			return transform.RowCount, nil
		case "document":
			return transform.Document, nil
		case "csv":
			return transform.CSV, nil
		}
	}

	return nil, ErrRefUnknownField.Detailf("%q in reference %q", field, ref)
}
