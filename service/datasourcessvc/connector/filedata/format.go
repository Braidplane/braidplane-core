package filedata

import (
	"mime"
	"path/filepath"
	"strings"

	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// DetectFormat resolves a read format and requires a matching extension for structured data.
func DetectFormat(format QueryFormat, filePath string) (QueryFormat, error) {
	expected := formatByExtension(filePath)
	switch format {
	case QueryFormatUnknown, QueryFormatAuto:
		if expected != QueryFormatUnknown {
			return expected, nil
		}
		return QueryFormatUnknown, xerror.InvalidInput(xerror.Query).
			WithPublicDetail("Use a supported file extension for a structured read, or select the raw file format.")
	case QueryFormatFile:
		return format, nil
	case QueryFormatCSV, QueryFormatJSON, QueryFormatSpreadsheet:
		if format == expected {
			return format, nil
		}
		return QueryFormatUnknown, xerror.InvalidInput(xerror.Query).
			WithPublicDetail("The file extension must match the selected read format.")
	default:
		return QueryFormatUnknown, xerror.Unexpected().Detailf("unsupported file query format %q", format)
	}
}

func formatByExtension(filePath string) QueryFormat {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".csv":
		return QueryFormatCSV
	case ".json":
		return QueryFormatJSON
	}
	if IsSpreadsheetExtension(filePath) {
		return QueryFormatSpreadsheet
	}
	return QueryFormatUnknown
}

// DetectWriteFormat determines the write format from an explicit setting or file
// extension, defaulting to JSON for unknown extensions.
func DetectWriteFormat(format QueryFormat, filePath string) (QueryFormat, error) {
	switch format {
	case QueryFormatCSV, QueryFormatJSON, QueryFormatFile, QueryFormatSpreadsheet:
		return format, nil
	case QueryFormatUnknown, QueryFormatAuto:
		if expected := formatByExtension(filePath); expected != QueryFormatUnknown {
			return expected, nil
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
	return spreadsheetContentType(filePath) != ""
}
