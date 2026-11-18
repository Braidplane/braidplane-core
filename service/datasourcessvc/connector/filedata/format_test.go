package filedata

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestDetectFormat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		format QueryFormat
		path   string
		want   QueryFormat
	}{
		{"explicit CSV", QueryFormatCSV, "users.CSV", QueryFormatCSV},
		{"explicit JSON", QueryFormatJSON, "users.json", QueryFormatJSON},
		{"explicit workbook", QueryFormatSpreadsheet, "users.xlsx", QueryFormatSpreadsheet},
		{"auto CSV", QueryFormatAuto, "users.csv", QueryFormatCSV},
		{"default JSON", QueryFormatUnknown, "users.json", QueryFormatJSON},
		{"auto workbook", QueryFormatAuto, "users.xltx", QueryFormatSpreadsheet},
		{"raw", QueryFormatFile, "users", QueryFormatFile},
		{"mismatched explicit format", QueryFormatJSON, "users.csv", QueryFormatUnknown},
		{"unknown explicit extension", QueryFormatSpreadsheet, "users.bin", QueryFormatUnknown},
		{"unknown auto extension", QueryFormatAuto, "users.txt", QueryFormatUnknown},
		{"missing auto extension", QueryFormatAuto, "users", QueryFormatUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := DetectFormat(tc.format, tc.path)
			require.Equal(t, tc.want, actual)
			if tc.want == QueryFormatUnknown {
				require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
				return
			}
			require.NoError(t, err)
		})
	}
	_, err := DetectFormat(QueryFormat(99), "users.json")
	require.ErrorContains(t, err, "unsupported file query format")
}

func TestDetectWriteFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		format      QueryFormat
		path        string
		wantFormat  QueryFormat
		wantErrText string
	}{
		{
			name:       "ExplicitSpreadsheet",
			format:     QueryFormatSpreadsheet,
			path:       "report.bin",
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "AutoFromXLSXExtension",
			format:     QueryFormatAuto,
			path:       "report.xlsx",
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "AutoFromMacroWorkbookExtension",
			format:     QueryFormatAuto,
			path:       "report.xlsm",
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "AutoFromTemplateExtension",
			format:     QueryFormatAuto,
			path:       "report.xltm",
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "ExplicitSpreadsheetAllowsUnknownExtension",
			format:     QueryFormatSpreadsheet,
			path:       "report.data",
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "UnknownExtensionDefaultsJSON",
			format:     QueryFormatAuto,
			path:       "report.data",
			wantFormat: QueryFormatJSON,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := DetectWriteFormat(tc.format, tc.path)

			if tc.wantErrText != "" {
				require.ErrorContains(t, err, tc.wantErrText)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantFormat, got)
		})
	}
}

func TestIsSpreadsheetExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "AddInMacro", path: "report.xlam", want: true},
		{name: "MacroWorkbook", path: "report.xlsm", want: true},
		{name: "Workbook", path: "report.xlsx", want: true},
		{name: "MacroTemplate", path: "report.xltm", want: true},
		{name: "Template", path: "report.xltx", want: true},
		{name: "OpenDocumentSpreadsheet", path: "report.ods", want: false},
		{name: "CSV", path: "report.csv", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, IsSpreadsheetExtension(tc.path))
		})
	}
}
