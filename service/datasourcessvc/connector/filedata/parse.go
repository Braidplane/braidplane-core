package filedata

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ParseCSV parses CSV content using the provided options and returns a FileResult.
func ParseCSV(content []byte, opts ParseCSVOptions) (*result.FileResult, error) {
	if opts.MaxBytes > 0 && int64(len(content)) > opts.MaxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			opts.MaxBytes,
		)
	}

	reader := csv.NewReader(bytes.NewReader(content))
	if opts.Delimiter != "" {
		runes := []rune(opts.Delimiter)
		if len(runes) != 1 {
			return nil, xerror.Unexpected().Detail("CSV delimiter must be a single character")
		}
		reader.Comma = runes[0]
	}

	records, err := reader.ReadAll()
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if len(records) == 0 {
		return &result.FileResult{
			Rows:        []map[string]any{},
			ColumnNames: []string{},
			RowCount:    0,
			Format:      QueryFormatCSV.String(),
		}, nil
	}

	var hasHeader bool
	if opts.HasHeader != nil {
		hasHeader = *opts.HasHeader
	} else {
		hasHeader = !isLikelyHeadless(records[0])
	}

	startDataRow := 0
	width := maxRecordWidth(records)
	headers := make([]string, width)
	isHeadless := false

	if hasHeader {
		copy(headers, records[0])
		for i := len(records[0]); i < width; i++ {
			headers[i] = fmt.Sprintf("col_%d", i)
		}
		startDataRow = 1
	} else {
		isHeadless = true
		for i := 0; i < width; i++ {
			headers[i] = fmt.Sprintf("col_%d", i)
		}
	}

	rows := make([]map[string]any, 0, len(records)-startDataRow)
	for i := startDataRow; i < len(records); i++ {
		if opts.MaxRows > 0 && len(rows) >= opts.MaxRows {
			return nil, xerror.ExecutionRowLimitExceeded().Detailf(
				"file content contains more than %d rows",
				opts.MaxRows,
			)
		}

		record := records[i]
		row := make(map[string]any, len(headers)+1)
		for j := range headers {
			value := ""
			if j < len(record) {
				value = record[j]
			}
			row[headers[j]] = value
		}
		if isHeadless {
			csvRow := make([]any, len(headers))
			for j := range headers {
				csvRow[j] = row[headers[j]]
			}
			row["csv_row"] = csvRow
		}
		rows = append(rows, row)
	}

	return &result.FileResult{
		Rows:          rows,
		ColumnNames:   headers,
		RowCount:      len(rows),
		Format:        QueryFormatCSV.String(),
		IsHeadlessCSV: isHeadless,
	}, nil
}

// ParseJSON parses JSON content and returns a FileResult with rows and sorted column names.
func ParseJSON(content []byte) (*result.FileResult, error) {
	return ParseJSONWithLimits(content, ParseJSONOptions{})
}

// ParseJSONWithLimits parses JSON content with row and byte safety limits.
func ParseJSONWithLimits(content []byte, opts ParseJSONOptions) (*result.FileResult, error) {
	if opts.MaxBytes > 0 && int64(len(content)) > opts.MaxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			opts.MaxBytes,
		)
	}

	var raw any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, xerror.Unexpected().Detailf("parse JSON content: %w", err)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, xerror.Unexpected().Detail("parse JSON content: trailing data")
	}
	rows := make([]map[string]any, 0)
	switch typed := raw.(type) {
	case []any:
		for i, item := range typed {
			if opts.MaxRows > 0 && len(rows) >= opts.MaxRows {
				return nil, xerror.ExecutionRowLimitExceeded().Detailf(
					"file content contains more than %d rows",
					opts.MaxRows,
				)
			}

			asMap, ok := item.(map[string]any)
			if !ok {
				rows = append(rows, map[string]any{"value": item, "index": i})
				continue
			}
			rows = append(rows, asMap)
		}
	case map[string]any:
		if opts.MaxRows > 0 && len(rows) >= opts.MaxRows {
			return nil, xerror.ExecutionRowLimitExceeded().Detailf(
				"file content contains more than %d rows",
				opts.MaxRows,
			)
		}
		rows = append(rows, typed)
	default:
		if opts.MaxRows > 0 && len(rows) >= opts.MaxRows {
			return nil, xerror.ExecutionRowLimitExceeded().Detailf(
				"file content contains more than %d rows",
				opts.MaxRows,
			)
		}
		rows = append(rows, map[string]any{"value": typed})
	}

	columnNames := collectColumnNames(rows)
	return &result.FileResult{
		Rows:        rows,
		ColumnNames: columnNames,
		RowCount:    len(rows),
		Format:      QueryFormatJSON.String(),
	}, nil
}

func collectColumnNames(rows []map[string]any) []string {
	nameSet := make(map[string]struct{})
	for _, row := range rows {
		for key := range row {
			nameSet[key] = struct{}{}
		}
	}

	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func maxRecordWidth(records [][]string) int {
	maxWidth := 0
	for _, record := range records {
		if len(record) > maxWidth {
			maxWidth = len(record)
		}
	}
	return maxWidth
}

func isLikelyHeadless(row []string) bool {
	if len(row) == 0 {
		return false
	}
	for _, value := range row {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return false
		}
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return false
		}
	}
	return true
}
