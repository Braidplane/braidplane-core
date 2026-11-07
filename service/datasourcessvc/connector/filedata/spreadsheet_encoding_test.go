package filedata

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

func TestSpreadsheetEncodedXMLBudgetCompatibility(t *testing.T) {
	f := excelize.NewFile()
	original, err := f.WriteToBuffer()
	require.NoError(t, err)
	require.NoError(t, f.Close())
	archive, err := zip.NewReader(bytes.NewReader(original.Bytes()), int64(original.Len()))
	require.NoError(t, err)
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, part := range archive.File {
		reader, err := part.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		if part.Name == "xl/worksheets/sheet1.xml" {
			data = []byte("<?xml version=\"1.0\" encoding=\"windows-1252\"?><worksheet xmlns=\"http://schemas.openxmlformats.org/spreadsheetml/2006/main\"><sheetData><row r=\"1\"><c r=\"A1\" t=\"inlineStr\"><is><t>id</t></is></c></row><row r=\"2\"><c r=\"A2\" t=\"inlineStr\"><is><t>caf\xe9</t></is></c></row></sheetData></worksheet>")
		}
		w, err := writer.Create(part.Name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	header := true
	for _, cellRange := range []string{"", "A1:A2"} {
		t.Run("range="+cellRange, func(t *testing.T) {
			result, err := ParseSpreadsheet(output.Bytes(), SpreadsheetOptions{Range: cellRange, HasHeader: &header, MaxBytes: 1 << 20})
			require.NoError(t, err)
			require.Equal(t, "café", result.Rows[0]["id"])
		})
	}
}

func TestSpreadsheetSharedStringEncodingBudget(t *testing.T) {
	f := excelize.NewFile()
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, f.SetCellStr("Sheet1", "A1", "placeholder"))
	f.SharedStrings = nil
	f.Pkg.Store("xl/sharedStrings.xml", []byte("<?xml version=\"1.0\" encoding=\"windows-1252\"?><sst xmlns=\"http://schemas.openxmlformats.org/spreadsheetml/2006/main\"><si><t>caf\xe9</t></si></sst>"))
	value, err := f.GetCellValue("Sheet1", "A1")
	require.NoError(t, err)
	require.Equal(t, "café", value)
	size, err := spreadsheetValueExpansion(nil, f, 1<<20)
	require.NoError(t, err)
	require.Equal(t, int64(len("café")), size)
}
