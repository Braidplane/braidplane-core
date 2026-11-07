package filedata

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type spreadsheetRelationships struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
		Type   string `xml:"Type,attr"`
	} `xml:"Relationship"`
}

func spreadsheetWorksheetXML(content []byte, workbook *excelize.File, sheetName string) (io.ReadCloser, error) {
	fail := func(message string) (io.ReadCloser, error) {
		return nil, xerror.Unexpected().Detail("resolve spreadsheet worksheet: " + message)
	}
	readRels := func(path string) (spreadsheetRelationships, error) {
		var rels spreadsheetRelationships
		cached, ok := workbook.Relationships.Load(path)
		if !ok {
			return rels, xerror.Unexpected().Detailf("missing relationships %q", path)
		}
		data, err := xml.Marshal(cached)
		if err == nil {
			err = xml.Unmarshal(data, &rels)
		}
		if err != nil {
			return rels, xerror.Unexpected().Detailf("read spreadsheet relationships %q: %w", path, err)
		}
		return rels, nil
	}
	root, err := readRels("_rels/.rels")
	if err != nil {
		return fail(err.Error())
	}
	wbPath := ""
	for _, rel := range root.Relationships {
		if rel.Type == excelize.SourceRelationshipOfficeDocument {
			if wbPath != "" {
				return fail("ambiguous workbook relationship")
			}
			wbPath = strings.TrimPrefix(rel.Target, "/")
		}
	}
	if wbPath == "" || workbook.WorkBook == nil {
		return fail("missing workbook")
	}
	wbRelsPath := strings.TrimPrefix(filepath.Dir(wbPath)+"/_rels/"+filepath.Base(wbPath)+".rels", "/")
	if filepath.Dir(wbPath) == "." {
		wbRelsPath = "_rels/" + filepath.Base(wbPath) + ".rels"
	}
	rels, err := readRels(wbRelsPath)
	if err != nil {
		return fail(err.Error())
	}
	id := ""
	for _, sheet := range workbook.WorkBook.Sheets.Sheet {
		if strings.EqualFold(sheet.Name, sheetName) {
			if id != "" || sheet.ID == "" {
				return fail("ambiguous sheet name")
			}
			id = sheet.ID
		}
	}
	if id == "" {
		return fail("missing sheet")
	}
	target := ""
	for _, rel := range rels.Relationships {
		if rel.ID == id {
			if target != "" || rel.Target == "" {
				return fail("ambiguous sheet relationship")
			}
			target = rel.Target
		}
	}
	if target == "" {
		return fail("missing sheet relationship")
	}
	part := filepath.Clean(fmt.Sprintf("%s/%s", filepath.Dir(wbPath), target))
	if strings.HasPrefix(target, "/") {
		part = filepath.Clean(target)
	}
	part = filepath.ToSlash(strings.TrimPrefix(strings.ReplaceAll(part, "\\", "/"), "/"))
	if value, ok := workbook.Pkg.Load(part); ok {
		data, ok := value.([]byte)
		if !ok {
			return fail("invalid worksheet content")
		}
		if len(data) > 0 {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return fail(err.Error())
	}
	var match *zip.File
	for _, entry := range archive.File {
		if strings.ReplaceAll(entry.Name, "\\", "/") == part {
			if match != nil {
				return fail("duplicate worksheet part")
			}
			match = entry
		}
	}
	if match == nil {
		return fail("missing worksheet part")
	}
	reader, err := match.Open()
	if err != nil {
		return fail(err.Error())
	}
	return reader, nil
}

// preflightSpreadsheetWorksheet bounds Excelize's worksheet expansion before it reads cells.
func preflightSpreadsheetWorksheet(content []byte, workbook *excelize.File, sheetName string, opts SpreadsheetOptions) error {
	reader, err := spreadsheetWorksheetXML(content, workbook, sheetName)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	decoder := xml.NewDecoder(reader)
	decoder.CharsetReader = workbook.CharsetReader
	maxRow, maxColumn, row, column, cellCount, rowCount := 0, 0, 0, 0, 0, 0
	inData, inRow := false, false
	irregularRows := false
	hasRange := strings.TrimSpace(opts.Range) != ""
	limit := opts.MaxBytes
	if limit <= 0 {
		limit = defaultSpreadsheetUnzipSizeLimit
	}
	for {
		token, err := decoder.Token()
		if xerror.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return xerror.Unexpected().Detailf("parse spreadsheet worksheet: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "sheetData":
				inData = true
			case "row":
				if !inData || inRow {
					return xerror.Unexpected().Detail("invalid spreadsheet row placement")
				}
				inRow = true
				rowCount++
				previousRow := row
				explicitRow := false
				row++
				column = 0
				cellCount = 0
				for _, attr := range element.Attr {
					if attr.Name.Local == "r" {
						explicitRow = true
						n, parseErr := strconv.Atoi(attr.Value)
						if parseErr != nil || n < 0 || n > excelize.TotalRows {
							return xerror.Unexpected().Detail("invalid spreadsheet row coordinate")
						}
						if n > 0 {
							irregularRows = irregularRows || n <= previousRow
							row = n
						} else {
							irregularRows = true
						}
					}
				}
				irregularRows = irregularRows || !explicitRow
				maxRow = max(maxRow, row)
			case "c":
				if !inRow {
					return xerror.Unexpected().Detail("invalid spreadsheet cell placement")
				}
				cellCount++
				column++
				for _, attr := range element.Attr {
					if attr.Name.Local == "r" && attr.Value != "" {
						c, r, parseErr := excelize.CellNameToCoordinates(attr.Value)
						if parseErr != nil || c <= 0 || r <= 0 || c > 16384 || r > excelize.TotalRows {
							return xerror.Unexpected().Detail("invalid spreadsheet cell coordinate")
						}
						column = c
						maxRow = max(maxRow, r)
					}
				}
				maxColumn = max(maxColumn, column, cellCount)
			}
		case xml.EndElement:
			if element.Name.Local == "row" {
				inRow = false
			}
			if element.Name.Local == "sheetData" {
				inData = false
			}
		}
		// Physical cells, implicit coordinates and gaps can all allocate in Excelize.
		if int64(rowCount) > limit/256 || int64(maxRow) > limit/256 || int64(maxColumn) > limit/256 || (max(maxRow, rowCount) > 0 && int64(maxColumn) > limit/256/int64(max(maxRow, rowCount))) {
			return spreadsheetExpansionError(limit)
		}
		// Only cell lookup normalizes irregular rows by repeatedly removing slice entries.
		if hasRange && irregularRows && rowCount > 0 && int64(rowCount) > limit/8/int64(rowCount) {
			return spreadsheetExpansionError(limit)
		}
	}
	valueSize, err := spreadsheetValueExpansion(content, workbook, limit)
	if err != nil {
		return err
	}
	valueCells := int64(max(maxRow, rowCount)) * int64(maxColumn)
	if hasRange {
		startColumn, startRow, endColumn, endRow, err := boundedSpreadsheetRange(opts.Range, opts.MaxRows)
		if err != nil {
			return err
		}
		cells := int64(endColumn-startColumn+1) * int64(endRow-startRow+1)
		valueCells = max(valueCells, cells)
		// GetCellValue scans each normalized row for every requested cell.
		if maxColumn > 0 && cells > limit/8/int64(maxColumn) {
			return spreadsheetExpansionError(limit)
		}
	}
	// Shared strings and formatting can both copy text before row checks.
	if valueSize > 0 && valueCells > limit/valueSize {
		return spreadsheetExpansionError(limit)
	}
	return nil
}

// spreadsheetValueExpansion measures the largest shared string and number format.
func spreadsheetValueExpansion(content []byte, workbook *excelize.File, limit int64) (int64, error) {
	maxValue := int64(0)
	maxFormat := int64(0)
	for _, part := range []string{"xl/sharedStrings.xml", "xl/styles.xml"} {
		var reader io.ReadCloser
		if value, ok := workbook.Pkg.Load(part); ok {
			data, valid := value.([]byte)
			if !valid {
				return 0, xerror.Unexpected().Detail("invalid spreadsheet part")
			}
			if len(data) > 0 {
				reader = io.NopCloser(bytes.NewReader(data))
			}
		}
		if reader == nil && part == "xl/sharedStrings.xml" {
			archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
			if err != nil {
				return 0, xerror.Unexpected().Detailf("parse spreadsheet archive: %w", err)
			}
			var spilled *zip.File
			xmlLimit := uint64(defaultSpreadsheetUnzipXMLSizeLimit)
			if limit > 0 && limit < defaultSpreadsheetUnzipXMLSizeLimit {
				xmlLimit = uint64(limit)
			}
			for _, entry := range archive.File {
				if strings.ToLower(strings.ReplaceAll(entry.Name, "\\", "/")) == "xl/sharedstrings.xml" && entry.UncompressedSize64 > xmlLimit {
					spilled = entry
				}
			}
			if spilled != nil {
				reader, err = spilled.Open()
				if err != nil {
					return 0, xerror.Unexpected().Detailf("read shared strings: %w", err)
				}
			}
		}
		if reader == nil {
			continue
		}
		decoder := xml.NewDecoder(reader)
		decoder.CharsetReader = workbook.CharsetReader
		var length int64
		inItem := false
		for {
			token, err := decoder.Token()
			if xerror.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = reader.Close()
				return 0, xerror.Unexpected().Detailf("parse spreadsheet part: %w", err)
			}
			switch element := token.(type) {
			case xml.StartElement:
				if part == "xl/sharedStrings.xml" && element.Name.Local == "si" {
					if inItem {
						_ = reader.Close()
						return 0, xerror.Unexpected().Detail("invalid nested shared string item")
					}
					inItem = true
					length = 0
				}
				if part == "xl/styles.xml" && element.Name.Local == "numFmt" {
					for _, attr := range element.Attr {
						if attr.Name.Local == "formatCode" || attr.Name.Local == "formatCode16" {
							maxFormat = max(maxFormat, int64(len(attr.Value)))
						}
					}
				}
			case xml.CharData:
				if inItem {
					length += int64(len(element))
					maxValue = max(maxValue, length)
				}
			case xml.EndElement:
				if element.Name.Local == "si" {
					inItem = false
				}
			}
		}
		_ = reader.Close()
	}
	return maxValue + maxFormat, nil
}
