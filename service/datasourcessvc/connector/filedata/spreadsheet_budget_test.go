package filedata

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestParseSpreadsheetRejectsExpandedWorksheetBudget(t *testing.T) {
	t.Parallel()
	workbook := excelize.NewFile()
	t.Cleanup(func() { require.NoError(t, workbook.Close()) })
	require.NoError(t, workbook.SetCellValue("Sheet1", "A1", "id"))
	require.NoError(t, workbook.SetCellValue("Sheet1", "CV31", "tail"))
	content, err := workbook.WriteToBuffer()
	require.NoError(t, err)

	for _, cellRange := range []string{"A1:CV31", ""} {
		t.Run("range="+cellRange, func(t *testing.T) {
			parsed, parseErr := ParseSpreadsheet(content.Bytes(), SpreadsheetOptions{Range: cellRange, MaxBytes: 32 << 10})
			require.Nil(t, parsed)
			require.ErrorIs(t, parseErr, xerror.ExecutionResponseSizeExceeded())
			require.ErrorContains(t, parseErr, "expanded worksheet")
		})
	}
}

func TestSpreadsheetDefaultBudgetRejectsExpansionBeforeCellLookup(t *testing.T) {
	t.Parallel()
	_, err := readSpreadsheetCellRange(nil, "Sheet1", 1, 1, 100, 50_000, 0)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.ErrorContains(t, err, "67108864 byte storage budget")
	_, err = spreadsheetValueBudget(math.MaxInt, math.MaxInt, math.MaxInt64)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestParseSpreadsheetChargesBlankRowWork(t *testing.T) {
	t.Parallel()
	workbook := excelize.NewFile()
	t.Cleanup(func() { require.NoError(t, workbook.Close()) })
	for row := 1; row <= 21; row++ {
		require.NoError(t, workbook.SetCellValue("Sheet1", fmt.Sprintf("CV%d", row), " "))
	}
	content, err := workbook.WriteToBuffer()
	require.NoError(t, err)
	_, err = ParseSpreadsheet(content.Bytes(), SpreadsheetOptions{MaxBytes: 32 << 10})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.ErrorContains(t, err, "expanded worksheet")
}

func TestParseSpreadsheetExpandedBudgetBoundary(t *testing.T) {
	t.Parallel()
	workbook := excelize.NewFile()
	t.Cleanup(func() { require.NoError(t, workbook.Close()) })
	require.NoError(t, workbook.SetCellValue("Sheet1", "A1", "id"))
	require.NoError(t, workbook.SetCellValue("Sheet1", "CV31", "tail"))
	content, err := workbook.WriteToBuffer()
	require.NoError(t, err)

	// The physical worksheet rectangle is admitted before Excelize expands it.
	const physicalBudget = 31 * 100 * 256
	for _, cellRange := range []string{"A1:CV31", ""} {
		t.Run("range="+cellRange, func(t *testing.T) {
			budget := int64(physicalBudget)
			if cellRange != "" {
				budget = 31 * 100 * 100 * 8
			}
			parsed, parseErr := ParseSpreadsheet(content.Bytes(), SpreadsheetOptions{Range: cellRange, MaxBytes: budget})
			require.NoError(t, parseErr)
			require.Len(t, parsed.ColumnNames, 100)
			require.Equal(t, 30, parsed.RowCount)
			require.Equal(t, "tail", parsed.Rows[29][parsed.ColumnNames[99]])
			_, parseErr = ParseSpreadsheet(content.Bytes(), SpreadsheetOptions{Range: cellRange, MaxBytes: budget - 1})
			require.ErrorIs(t, parseErr, xerror.ExecutionResponseSizeExceeded())
		})
	}
}

func TestSpreadsheetPreflightSparseAndImplicitCoordinates(t *testing.T) {
	workbook := excelize.NewFile()
	var original bytes.Buffer
	_, err := workbook.WriteTo(&original)
	require.NoError(t, err)
	require.NoError(t, workbook.Close())
	cases := []struct {
		name, sheet string
		rejected    bool
	}{
		{"sparse row", `<row r="1000"><c r="A1000" t="inlineStr"><is><t>x</t></is></c></row>`, true},
		{"implicit cells", `<row r="1">` + strings.Repeat(`<c t="inlineStr"><is><t>x</t></is></c>`, 150) + `</row>`, true},
		{"control", `<row r="1"><c r="A1" t="inlineStr"><is><t>ok</t></is></c></row>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := zip.NewReader(bytes.NewReader(original.Bytes()), int64(original.Len()))
			require.NoError(t, err)
			var modified bytes.Buffer
			out := zip.NewWriter(&modified)
			for _, part := range src.File {
				r, err := part.Open()
				require.NoError(t, err)
				data, err := io.ReadAll(r)
				require.NoError(t, err)
				require.NoError(t, r.Close())
				if part.Name == "xl/worksheets/sheet1.xml" {
					data = []byte(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + tc.sheet + `</sheetData></worksheet>`)
				}
				w, err := out.Create(part.Name)
				require.NoError(t, err)
				_, err = w.Write(data)
				require.NoError(t, err)
			}
			require.NoError(t, out.Close())
			parsed, err := ParseSpreadsheet(modified.Bytes(), SpreadsheetOptions{Range: "A1", MaxBytes: 32 << 10})
			if tc.rejected {
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"ok"}, parsed.ColumnNames)
			}
		})
	}
}

func TestSpreadsheetPreflightBoundsRepeatedValues(t *testing.T) {
	for _, kind := range []string{"shared", "rich", "format"} {
		t.Run(kind, func(t *testing.T) {
			wb := excelize.NewFile()
			defer func() { _ = wb.Close() }()
			format := `"` + strings.Repeat("x", 2000) + `"0`
			var style int
			var err error
			if kind == "format" {
				style, err = wb.NewStyle(&excelize.Style{CustomNumFmt: &format})
				require.NoError(t, err)
			}
			for i := 1; i <= 20; i++ {
				cell, err := excelize.CoordinatesToCellName(i, 1)
				require.NoError(t, err)
				switch kind {
				case "shared":
					err = wb.SetCellStr("Sheet1", cell, strings.Repeat("x", 2000))
				case "rich":
					err = wb.SetCellRichText("Sheet1", cell, []excelize.RichTextRun{{Text: strings.Repeat("x", 1000)}, {Text: strings.Repeat("y", 1000)}})
				case "format":
					err = wb.SetCellInt("Sheet1", cell, 1)
					if err == nil {
						err = wb.SetCellStyle("Sheet1", cell, cell, style)
					}
				}
				require.NoError(t, err)
			}
			buf, err := wb.WriteToBuffer()
			require.NoError(t, err)
			opened, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
			require.NoError(t, err)
			defer func() { _ = opened.Close() }()
			for _, cellRange := range []string{"", "A1:T1"} {
				err = preflightSpreadsheetWorksheet(buf.Bytes(), opened, "Sheet1", SpreadsheetOptions{Range: cellRange, MaxBytes: 16 << 10})
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				err = preflightSpreadsheetWorksheet(buf.Bytes(), opened, "Sheet1", SpreadsheetOptions{Range: cellRange, MaxBytes: 1 << 20})
				require.NoError(t, err)
			}
			parsed, err := ParseSpreadsheet(buf.Bytes(), SpreadsheetOptions{Range: "A1:T1", MaxBytes: 1 << 20})
			require.NoError(t, err)
			require.Len(t, parsed.ColumnNames, 20)
			if kind == "format" {
				require.Contains(t, parsed.ColumnNames[0], "x")
			} else {
				require.Len(t, parsed.ColumnNames[0], 2000)
			}
		})
	}
}

func TestSpreadsheetOrderedRowsStayWithinLinearBudget(t *testing.T) {
	t.Parallel()
	workbook := excelize.NewFile()
	t.Cleanup(func() { _ = workbook.Close() })
	require.NoError(t, workbook.SetCellStr("Sheet1", "A1", "id"))
	for row := 2; row <= 100; row++ {
		require.NoError(t, workbook.SetCellStr("Sheet1", fmt.Sprintf("A%d", row), "item"))
	}
	content, err := workbook.WriteToBuffer()
	require.NoError(t, err)
	header := true
	for _, cellRange := range []string{"", "A1:A100", " "} {
		t.Run("range="+cellRange, func(t *testing.T) {
			parsed, err := ParseSpreadsheet(content.Bytes(), SpreadsheetOptions{Range: cellRange, HasHeader: &header, MaxBytes: 64 << 10})
			require.NoError(t, err)
			require.Equal(t, 99, parsed.RowCount)
		})
	}
}
