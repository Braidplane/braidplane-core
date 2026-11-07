package filedata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		format      QueryFormat
		path        string
		content     []byte
		wantFormat  QueryFormat
		wantErrText string
	}{
		{
			name:       "ExplicitCSV",
			format:     QueryFormatCSV,
			path:       "file.txt",
			content:    []byte("a,b"),
			wantFormat: QueryFormatCSV,
		},
		{
			name:       "ExplicitJSON",
			format:     QueryFormatJSON,
			path:       "file.txt",
			content:    []byte(`{}`),
			wantFormat: QueryFormatJSON,
		},
		{
			name:       "ExplicitSpreadsheet",
			format:     QueryFormatSpreadsheet,
			path:       "file.bin",
			content:    []byte("xlsx"),
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "AutoFromCSVExtension",
			format:     QueryFormatAuto,
			path:       "data.csv",
			content:    []byte("a,b"),
			wantFormat: QueryFormatCSV,
		},
		{
			name:       "AutoFromJSONExtension",
			format:     QueryFormatAuto,
			path:       "data.json",
			content:    []byte(`[]`),
			wantFormat: QueryFormatJSON,
		},
		{
			name:       "AutoFromSpreadsheetExtension",
			format:     QueryFormatAuto,
			path:       "data.xlsm",
			content:    []byte("xlsx"),
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "AutoFromXLTXExtension",
			format:     QueryFormatAuto,
			path:       "data.xltx",
			content:    []byte("xlsx"),
			wantFormat: QueryFormatSpreadsheet,
		},
		{
			name:       "AutoDetectJSONByContent",
			format:     QueryFormatAuto,
			path:       "data.txt",
			content:    []byte(`[{"a":1}]`),
			wantFormat: QueryFormatJSON,
		},
		{
			name:       "AutoDetectCSVByContent",
			format:     QueryFormatAuto,
			path:       "data.txt",
			content:    []byte("a,b,c"),
			wantFormat: QueryFormatCSV,
		},
		{
			name:        "UnsupportedFormat",
			format:      QueryFormat(99),
			path:        "data.xml",
			content:     []byte("<root/>"),
			wantErrText: "unsupported file query format",
		},
		{
			name:        "EmptyContentAutoFails",
			format:      QueryFormatAuto,
			path:        "data.txt",
			content:     []byte(""),
			wantErrText: "file is empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := DetectFormat(tc.format, tc.path, tc.content)

			if tc.wantErrText != "" {
				require.ErrorContains(t, err, tc.wantErrText)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantFormat, got)
		})
	}
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
