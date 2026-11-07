package filedata

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// EncodeCSVContent serializes content as CSV bytes using the provided options.
// content must be a []any of map[string]any objects.
// Raw CSV strings are handled by the caller before invoking this function.
// Returns the encoded bytes, the number of written rows, and any error.
func EncodeCSVContent(content any, opts ParseCSVOptions) ([]byte, int, error) {
	rows, ok := content.([]any)
	if !ok {
		return nil, 0, xerror.Unexpected().Detail(
			"CSV write content must be an array of objects or a raw CSV string",
		)
	}

	headers := make([]string, 0)
	headerSet := map[string]struct{}{}
	objectRows := make([]map[string]any, 0, len(rows))

	for idx, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok {
			return nil, 0, xerror.Unexpected().Detailf("CSV write row %d must be an object", idx)
		}
		objectRows = append(objectRows, obj)
		for key := range obj {
			if _, exists := headerSet[key]; exists {
				continue
			}
			headerSet[key] = struct{}{}
			headers = append(headers, key)
		}
	}

	sort.Strings(headers)

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if opts.Delimiter != "" {
		runes := []rune(opts.Delimiter)
		if len(runes) != 1 {
			return nil, 0, xerror.Unexpected().Detail("CSV delimiter must be a single character")
		}
		writer.Comma = runes[0]
	}

	hasHeader := true
	if opts.HasHeader != nil {
		hasHeader = *opts.HasHeader
	}
	if hasHeader {
		if err := writer.Write(headers); err != nil {
			return nil, 0, xerror.Unexpected().With(err)
		}
	}

	for _, row := range objectRows {
		record := make([]string, len(headers))
		for i, header := range headers {
			value, exists := row[header]
			if !exists || value == nil {
				record[i] = ""
				continue
			}
			record[i] = fmt.Sprint(value)
		}
		if err := writer.Write(record); err != nil {
			return nil, 0, xerror.Unexpected().With(err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, 0, xerror.Unexpected().With(err)
	}

	return buffer.Bytes(), len(objectRows), nil
}

// EstimateWrittenRows returns the number of rows in content if it is a slice, or 0 otherwise.
func EstimateWrittenRows(content any) int {
	rows, ok := content.([]any)
	if !ok {
		return 0
	}
	return len(rows)
}
