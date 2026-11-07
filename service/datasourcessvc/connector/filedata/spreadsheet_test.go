package filedata

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestParseSpreadsheet_SelectsSheetRangeAndHeaders(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Orders", [][]any{
		{"id", "email", "total"},
		{1, "alice@example.com", 42},
		{2, "bob@example.com", 19},
	})

	parsed, err := ParseSpreadsheet(content, SpreadsheetOptions{
		Sheet: "Orders",
		Range: "B1:C3",
	})
	require.NoError(t, err)

	require.Equal(t, QueryFormatSpreadsheet.String(), parsed.Format)
	require.Equal(t, "Orders", parsed.Sheet)
	require.Equal(t, "B1:C3", parsed.Range)
	require.Equal(t, []string{"email", "total"}, parsed.ColumnNames)
	require.Equal(t, 2, parsed.RowCount)
	require.Equal(t, "alice@example.com", parsed.Rows[0]["email"])
	require.Equal(t, "42", parsed.Rows[0]["total"])
}

func TestParseSpreadsheet_HeadlessDetection(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Sheet1", [][]any{
		{1, 2},
		{3, 4},
	})

	parsed, err := ParseSpreadsheet(content, SpreadsheetOptions{})
	require.NoError(t, err)

	require.True(t, parsed.IsHeadlessSpreadsheet)
	require.Equal(t, []string{"col_0", "col_1"}, parsed.ColumnNames)
	require.Equal(t, []any{"1", "2"}, parsed.Rows[0]["spreadsheet_row"])
}

func TestParseSpreadsheet_InfersUsedRangeWhenRangeOmitted(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Orders", [][]any{
		{nil, nil, nil},
		{nil, "email", "id", "total"},
		{nil, "alice@example.com", 1, 42},
		{nil, "bob@example.com", 2, 19},
	})

	parsed, err := ParseSpreadsheet(content, SpreadsheetOptions{Sheet: "Orders"})
	require.NoError(t, err)
	require.Equal(t, []string{"email", "id", "total"}, parsed.ColumnNames)
	require.Equal(t, 2, parsed.RowCount)
	require.Equal(t, "alice@example.com", parsed.Rows[0]["email"])
	require.Equal(t, "42", parsed.Rows[0]["total"])
}

func TestParseSpreadsheet_HeadersRemainUniqueAfterSuffixing(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Sheet1", [][]any{
		{"email", "email", "email_2", "", "col_3"},
		{"first@example.com", "second@example.com", "literal@example.com", "blank", "literal-col"},
	})

	parsed, err := ParseSpreadsheet(content, SpreadsheetOptions{})
	require.NoError(t, err)

	require.Equal(t, []string{"email", "email_2", "email_2_2", "col_3", "col_3_2"}, parsed.ColumnNames)
	require.Equal(t, "first@example.com", parsed.Rows[0]["email"])
	require.Equal(t, "second@example.com", parsed.Rows[0]["email_2"])
	require.Equal(t, "literal@example.com", parsed.Rows[0]["email_2_2"])
	require.Equal(t, "blank", parsed.Rows[0]["col_3"])
	require.Equal(t, "literal-col", parsed.Rows[0]["col_3_2"])
}

func TestParseSpreadsheet_ReturnsRowLimitViolation(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Sheet1", [][]any{
		{"id"},
		{1},
		{2},
	})

	_, err := ParseSpreadsheet(content, SpreadsheetOptions{MaxRows: 1})
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
	require.ErrorContains(t, err, "spreadsheet range contains more than 1 rows")
}

func TestParseSpreadsheet_RejectsOversizedRangeBeforeCropping(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Sheet1", [][]any{
		{"id"},
		{1},
	})

	_, err := ParseSpreadsheet(content, SpreadsheetOptions{
		Range:   "A1:A20",
		MaxRows: 2,
	})
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
	require.ErrorContains(t, err, "spreadsheet range contains more than 2 rows")
}

func TestParseSpreadsheet_RejectsSparseUsedRangeBeforeExpandingRows(t *testing.T) {
	t.Parallel()

	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()

	require.NoError(t, workbook.SetCellValue("Sheet1", "A1", "id"))
	require.NoError(t, workbook.SetCellValue("Sheet1", "A1000", "tail"))
	buffer, err := workbook.WriteToBuffer()
	require.NoError(t, err)

	_, err = ParseSpreadsheet(bytes.Clone(buffer.Bytes()), SpreadsheetOptions{MaxRows: 10})
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
	require.ErrorContains(t, err, "spreadsheet range contains more than 10 rows")
}

func TestParseSpreadsheet_RejectsOversizedDecompressedWorkbook(t *testing.T) {
	t.Parallel()

	content := testSpreadsheetContent(t, "Sheet1", [][]any{
		{"id", "email", "total"},
		{1, "alice@example.com", 42},
		{2, "bob@example.com", 19},
	})

	_, err := ParseSpreadsheet(content, SpreadsheetOptions{MaxBytes: int64(len(content))})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.ErrorContains(t, err, "spreadsheet decompressed content exceeds the configured byte limit")
}

func TestEncodeSpreadsheetContent_WritesRowsFromStartRange(t *testing.T) {
	t.Parallel()

	content, writtenRows, err := EncodeSpreadsheetContent(
		[]any{
			map[string]any{"id": 1, "email": "alice@example.com"},
			map[string]any{"id": 2, "email": "bob@example.com"},
		},
		SpreadsheetOptions{Sheet: "Orders", Range: "B2"},
	)
	require.NoError(t, err)
	require.Equal(t, 2, writtenRows)

	parsed, err := ParseSpreadsheet(content, SpreadsheetOptions{
		Sheet: "Orders",
		Range: "B2:C4",
	})
	require.NoError(t, err)

	require.Equal(t, []string{"email", "id"}, parsed.ColumnNames)
	require.Equal(t, "alice@example.com", parsed.Rows[0]["email"])
	require.Equal(t, "1", parsed.Rows[0]["id"])
}

func TestEncodeSpreadsheetContent_UsesWorkbookExtensionContentType(t *testing.T) {
	t.Parallel()

	content, writtenRows, err := EncodeSpreadsheetContent(
		[]map[string]any{{"id": 1, "email": "alice@example.com"}},
		SpreadsheetOptions{Sheet: "Orders", FilePath: "exports/users.xlsm"},
	)
	require.NoError(t, err)
	require.Equal(t, 1, writtenRows)

	contentTypes := readSpreadsheetZipEntry(t, content, "[Content_Types].xml")
	require.Contains(t, contentTypes, "application/vnd.ms-excel.sheet.macroEnabled.main+xml")
}

// testSpreadsheetContent builds an in-memory workbook fixture for parser tests.
func testSpreadsheetContent(t *testing.T, sheetName string, rows [][]any) []byte {
	t.Helper()

	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()

	defaultSheet := workbook.GetSheetName(0)
	require.NoError(t, workbook.SetSheetName(defaultSheet, sheetName))

	for rowIndex, row := range rows {
		for columnIndex, value := range row {
			cellName, err := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+1)
			require.NoError(t, err)
			require.NoError(t, workbook.SetCellValue(sheetName, cellName, value))
		}
	}

	buffer, err := workbook.WriteToBuffer()
	require.NoError(t, err)
	return bytes.Clone(buffer.Bytes())
}

// readSpreadsheetZipEntry returns one XML part from an encoded workbook fixture.
func readSpreadsheetZipEntry(t *testing.T, content []byte, name string) string {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	require.NoError(t, err)

	for _, file := range reader.File {
		if file.Name != name {
			continue
		}

		entry, err := file.Open()
		require.NoError(t, err)
		defer func() { _ = entry.Close() }()

		data, err := io.ReadAll(entry)
		require.NoError(t, err)
		return string(data)
	}

	t.Fatalf("missing spreadsheet zip entry %q", name)
	return ""
}
