package filedata

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// EncodeCSVContent serializes content as CSV bytes using the provided options.
// content must be a []any of map[string]any objects.
// Raw CSV strings are handled by the caller before invoking this function.
// Returns the encoded bytes, the number of written rows, and any error.
func EncodeCSVContent(content any, opts ParseCSVOptions) ([]byte, int, error) {
	return encodeCSVContent(content, opts, false)
}

// EncodeArtifactCSV preserves records that the standard reader would discard as blank lines.
func EncodeArtifactCSV(content any, opts ParseCSVOptions) ([]byte, int, error) {
	return encodeCSVContent(content, opts, true)
}

func encodeCSVContent(content any, opts ParseCSVOptions, artifact bool) ([]byte, int, error) {
	rows, ok := content.([]any)
	if !ok {
		return nil, 0, xerror.Unexpected().Detail(
			"CSV write content must be an array of objects or a raw CSV string",
		)
	}

	if artifact && opts.MaxRows > 0 && len(rows) > opts.MaxRows {
		return nil, 0, xerror.ExecutionRowLimitExceeded()
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

	buffer := csvWriteBuffer{maxBytes: opts.MaxBytes}
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
	if artifact && len(headers) == 0 && len(objectRows) > 0 {
		return nil, 0, xerror.InvalidInput(xerror.Query).Detail("CSV artifact rows require at least one column")
	}
	if hasHeader {
		if artifact {
			for _, header := range headers {
				if strings.TrimSpace(header) == "" {
					return nil, 0, xerror.InvalidInput(xerror.Query).Detail("CSV artifact header contains a blank column name")
				}
			}
		}
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
		if artifact && len(record) == 1 && record[0] == "" {
			writer.Flush()
			if err := writer.Error(); err != nil {
				return nil, 0, xerror.Unexpected().With(err)
			}
			if _, err := buffer.Write([]byte{'"', '"', '\n'}); err != nil {
				return nil, 0, err
			}
			continue
		}
		if err := writer.Write(record); err != nil {
			return nil, 0, xerror.Unexpected().With(err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, 0, xerror.Unexpected().With(err)
	}

	if artifact && !validCSVText(buffer.Bytes()) {
		return nil, 0, xerror.InvalidInput(xerror.Query).WithPublicDetail("CSV content must be valid UTF-8 text.")
	}
	return buffer.Bytes(), len(objectRows), nil
}

type csvWriteBuffer struct {
	bytes.Buffer
	maxBytes int64
}

func (b *csvWriteBuffer) Write(p []byte) (int, error) {
	if b.maxBytes > 0 && int64(len(p)) > b.maxBytes-int64(b.Len()) {
		return 0, xerror.ExecutionResponseSizeExceeded()
	}
	n, _ := b.Buffer.Write(p)
	return n, nil
}

// EstimateWrittenRows returns the number of rows in content if it is a slice, or 0 otherwise.
func EstimateWrittenRows(content any) int {
	rows, ok := content.([]any)
	if !ok {
		return 0
	}
	return len(rows)
}
