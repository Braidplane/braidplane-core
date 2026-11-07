package filedata

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestSpreadsheetWorksheetXML(t *testing.T) {
	f := excelize.NewFile()
	if _, err := f.NewSheet("Second"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Second", "A1", "selected"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, limit := range []int64{1, 1 << 20} {
		wb, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()), excelize.Options{UnzipSizeLimit: 1 << 20, UnzipXMLSizeLimit: limit})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := spreadsheetWorksheetXML(buf.Bytes(), wb, "Second")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		_ = wb.Close()
		if err != nil || !bytes.Contains(data, []byte(`r="A1"`)) {
			t.Fatalf("selected worksheet: %q, %v", data, err)
		}
	}
}

func TestSpreadsheetWorksheetXMLCustomAndAmbiguous(t *testing.T) {
	f := excelize.NewFile()
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	src, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var modified bytes.Buffer
	out := zip.NewWriter(&modified)
	for _, part := range src.File {
		r, err := part.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		name := part.Name
		if name == "xl/_rels/workbook.xml.rels" {
			data = bytes.ReplaceAll(data, []byte("worksheets/sheet1.xml"), []byte("/xl/custom.xml"))
		}
		if name == "xl/worksheets/sheet1.xml" {
			name = "xl/custom.xml"
		}
		w, err := out.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	wb, err := excelize.OpenReader(bytes.NewReader(modified.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wb.Close() }()
	reader, err := spreadsheetWorksheetXML(modified.Bytes(), wb, "Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	wb.WorkBook.Sheets.Sheet = append(wb.WorkBook.Sheets.Sheet, wb.WorkBook.Sheets.Sheet[0])
	if _, err := spreadsheetWorksheetXML(modified.Bytes(), wb, "Sheet1"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity, got %v", err)
	}
}

func TestSpreadsheetValueExpansionUsesExcelizePartPriority(t *testing.T) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range []struct{ name, text string }{
		{"XL/SHAREDSTRINGS.XML", strings.Repeat("a", 1500)},
		{`xl\sharedStrings.xml`, strings.Repeat("b", 2000)},
		{"xl/ſharedStrings.xml", strings.Repeat("c", 1500)},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.WriteString(w, `<sst><si><r><t>`+entry.text+`</t></r></si></sst>`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Pkg.Delete("xl/sharedStrings.xml")
	f.Pkg.Store("xl/styles.xml", []byte(`<styleSheet><numFmts><numFmt formatCode16="abcdef"/></numFmts></styleSheet>`))
	got, err := spreadsheetValueExpansion(buf.Bytes(), f, 1024)
	if err != nil || got != 2006 {
		t.Fatalf("spilled last entry: %d, %v", got, err)
	}
	f.Pkg.Store("xl/sharedStrings.xml", []byte(`<sst><si><t>cached</t></si></sst>`))
	got, err = spreadsheetValueExpansion(buf.Bytes(), f, 1024)
	if err != nil || got != 12 {
		t.Fatalf("cached entry: %d, %v", got, err)
	}
}

func TestSpreadsheetRejectsNestedSharedStringItems(t *testing.T) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	if err := f.SetCellStr("Sheet1", "A1", "placeholder"); err != nil {
		t.Fatal(err)
	}
	f.SharedStrings = nil
	f.Pkg.Store("xl/sharedStrings.xml", []byte(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>first<si/>last</t></si></sst>`))
	value, err := f.GetCellValue("Sheet1", "A1")
	if err != nil || value != "firstlast" {
		t.Fatalf("loader control: %q, %v", value, err)
	}
	if _, err := spreadsheetValueExpansion(nil, f, 1024); err == nil || !strings.Contains(err.Error(), "nested shared string") {
		t.Fatalf("nested item must fail before repeated cell copying: %v", err)
	}
}
