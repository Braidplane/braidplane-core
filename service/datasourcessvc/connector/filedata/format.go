package filedata

import (
	"bytes"
	"mime"
	"path/filepath"
	"strings"

	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// DetectFormat infers the query format from an explicit setting, the file extension,
// or a content heuristic.  An empty or "auto" format triggers auto-detection.
func DetectFormat(format QueryFormat, filePath string, content []byte) (QueryFormat, error) {
	switch format {
	case QueryFormatUnknown, QueryFormatAuto:
		// Auto-detect below.
	case QueryFormatCSV, QueryFormatJSON, QueryFormatFile, QueryFormatSpreadsheet:
		return format, nil
	default:
		return QueryFormatUnknown, xerror.Unexpected().Detailf("unsupported file query format %q", format)
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".csv":
		return QueryFormatCSV, nil
	case ".json":
		return QueryFormatJSON, nil
	}
	if IsSpreadsheetExtension(filePath) {
		return QueryFormatSpreadsheet, nil
	}

	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return QueryFormatUnknown, xerror.Unexpected().Detail("file is empty")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return QueryFormatJSON, nil
	}

	return QueryFormatCSV, nil
}

// DetectWriteFormat determines the write format from an explicit setting or file
// extension, defaulting to JSON for unknown extensions.
func DetectWriteFormat(format QueryFormat, filePath string) (QueryFormat, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch format {
	case QueryFormatCSV, QueryFormatJSON, QueryFormatFile, QueryFormatSpreadsheet:
		return format, nil
	case QueryFormatUnknown, QueryFormatAuto:
		switch ext {
		case ".csv":
			return QueryFormatCSV, nil
		case ".json":
			return QueryFormatJSON, nil
		}
		if IsSpreadsheetExtension(filePath) {
			return QueryFormatSpreadsheet, nil
		}
		return QueryFormatJSON, nil
	default:
		return QueryFormatUnknown, xerror.Unexpected().Detailf("unsupported file write format %q", format)
	}
}

// MIMETypeForPath returns a safe file payload media type based on extension or fallback.
func MIMETypeForPath(filePath, fallback string) string {
	if mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath))); mediaType != "" {
		return filepayload.NormalizeFilePayloadMIMEType(mediaType)
	}

	return filepayload.NormalizeFilePayloadMIMEType(fallback)
}

// IsSpreadsheetExtension reports whether the path uses an Excelize-supported workbook extension.
func IsSpreadsheetExtension(filePath string) bool {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".xlam", ".xlsm", ".xlsx", ".xltm", ".xltx":
		return true
	default:
		return false
	}
}
