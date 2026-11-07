package filedata

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/nativetype"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const DefaultSpreadsheetSheetName = "Sheet1"

const (
	defaultSpreadsheetUnzipSizeLimit    int64 = 64 << 20
	defaultSpreadsheetUnzipXMLSizeLimit int64 = 16 << 20
	spreadsheetStringSlotBytes          int64 = 16
	spreadsheetRowHeaderBytes           int64 = 24
)

// ParseSpreadsheet parses workbook content into row objects.
func ParseSpreadsheet(content []byte, opts SpreadsheetOptions) (*result.FileResult, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultSpreadsheetUnzipSizeLimit
	}
	if opts.MaxBytes > 0 && int64(len(content)) > opts.MaxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			opts.MaxBytes,
		)
	}

	workbook, err := openSpreadsheetWorkbook(content, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = workbook.Close() }()

	sheetName, err := resolveSpreadsheetSheet(workbook, opts.Sheet)
	if err != nil {
		return nil, err
	}

	if err := preflightSpreadsheetWorksheet(content, workbook, sheetName, opts); err != nil {
		return nil, err
	}

	records, cellRange, err := spreadsheetRecords(workbook, sheetName, opts)
	if err != nil {
		return nil, err
	}

	return spreadsheetRowsToFileResult(records, sheetName, cellRange, opts)
}

// EncodeSpreadsheetContent serializes row-object content into a workbook.
func EncodeSpreadsheetContent(content any, opts SpreadsheetOptions) ([]byte, int, error) {
	objectRows, err := spreadsheetObjectRows(content)
	if err != nil {
		return nil, 0, err
	}

	sheetName := strings.TrimSpace(opts.Sheet)
	if sheetName == "" {
		sheetName = DefaultSpreadsheetSheetName
	}
	if err := validateSpreadsheetSheetName(sheetName); err != nil {
		return nil, 0, err
	}

	startColumn, startRow, err := spreadsheetWriteStartCell(opts.Range)
	if err != nil {
		return nil, 0, err
	}

	headers := spreadsheetWriteHeaders(objectRows)
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	applySpreadsheetWorkbookPath(workbook, opts.FilePath)

	defaultSheet := workbook.GetSheetName(0)
	if defaultSheet == "" {
		defaultSheet = DefaultSpreadsheetSheetName
	}
	if defaultSheet != sheetName {
		if err := workbook.SetSheetName(defaultSheet, sheetName); err != nil {
			return nil, 0, xerror.Unexpected().Detailf("set spreadsheet sheet name: %w", err)
		}
	}

	hasHeader := true
	if opts.HasHeader != nil {
		hasHeader = *opts.HasHeader
	}

	currentRow := startRow
	if hasHeader {
		if err := writeSpreadsheetRecord(workbook, sheetName, currentRow, startColumn, stringsToAny(headers)); err != nil {
			return nil, 0, err
		}
		currentRow++
	}

	for _, objectRow := range objectRows {
		record := make([]any, len(headers))
		for i, header := range headers {
			record[i] = objectRow[header]
		}
		if err := writeSpreadsheetRecord(workbook, sheetName, currentRow, startColumn, record); err != nil {
			return nil, 0, err
		}
		currentRow++
	}

	var buffer bytes.Buffer
	if _, err := workbook.WriteTo(&buffer); err != nil {
		return nil, 0, xerror.Unexpected().Detailf("encode spreadsheet content: %w", err)
	}

	return buffer.Bytes(), len(objectRows), nil
}

// InferSpreadsheetFields infers string fields from the selected worksheet.
func InferSpreadsheetFields(content []byte, opts SpreadsheetOptions) ([]Field, error) {
	parsed, err := ParseSpreadsheet(content, opts)
	if err != nil {
		return nil, err
	}

	fields := make([]Field, 0, len(parsed.ColumnNames))
	for _, column := range parsed.ColumnNames {
		if strings.TrimSpace(column) == "" {
			continue
		}
		fields = append(fields, Field{Name: column, Type: nativetype.TypeString})
	}

	if parsed.IsHeadlessSpreadsheet {
		fields = append(fields, Field{Name: "spreadsheet_row", Type: nativetype.TypeArray})
	}

	return fields, nil
}

// resolveSpreadsheetSheet returns the requested sheet or the workbook's first sheet.
func resolveSpreadsheetSheet(workbook *excelize.File, requested string) (string, error) {
	sheetName := strings.TrimSpace(requested)
	sheets := workbook.GetSheetList()
	if len(sheets) == 0 {
		return "", xerror.Unexpected().Detail("spreadsheet workbook has no sheets")
	}
	if sheetName == "" {
		return sheets[0], nil
	}

	for _, available := range sheets {
		if available == sheetName {
			return sheetName, nil
		}
	}

	return "", xerror.Unexpected().Detailf("spreadsheet sheet %q was not found", sheetName)
}

// openSpreadsheetWorkbook applies compressed and decompressed workbook size limits.
func openSpreadsheetWorkbook(content []byte, opts SpreadsheetOptions) (*excelize.File, error) {
	workbook, err := excelize.OpenReader(bytes.NewReader(content), spreadsheetOpenOptions(opts.MaxBytes))
	if err != nil {
		if isSpreadsheetSizeLimitError(err) {
			return nil, xerror.ExecutionResponseSizeExceeded().Detail(
				"spreadsheet decompressed content exceeds the configured byte limit",
			)
		}
		return nil, xerror.Unexpected().Detailf("parse spreadsheet content: %w", err)
	}

	return workbook, nil
}

// spreadsheetOpenOptions returns bounded unzip settings for workbook parsing.
func spreadsheetOpenOptions(maxBytes int64) excelize.Options {
	unzipSizeLimit := defaultSpreadsheetUnzipSizeLimit
	if maxBytes > 0 {
		unzipSizeLimit = maxBytes
	}

	unzipXMLSizeLimit := defaultSpreadsheetUnzipXMLSizeLimit
	if unzipXMLSizeLimit > unzipSizeLimit {
		unzipXMLSizeLimit = unzipSizeLimit
	}

	return excelize.Options{
		UnzipSizeLimit:    unzipSizeLimit,
		UnzipXMLSizeLimit: unzipXMLSizeLimit,
	}
}

// isSpreadsheetSizeLimitError identifies Excelize unzip guardrail failures.
func isSpreadsheetSizeLimitError(err error) bool {
	if err == nil {
		return false
	}

	message := err.Error()
	return strings.Contains(message, "unzip size exceeds") ||
		strings.Contains(message, "UnzipSizeLimit")
}

// spreadsheetRecords reads only the selected or discovered worksheet range.
func spreadsheetRecords(
	workbook *excelize.File,
	sheetName string,
	opts SpreadsheetOptions,
) ([][]string, string, error) {
	cellRange := strings.TrimSpace(opts.Range)
	if cellRange != "" {
		startColumn, startRow, endColumn, endRow, err := boundedSpreadsheetRange(cellRange, opts.MaxRows)
		if err != nil {
			return nil, "", err
		}
		records, err := readSpreadsheetCellRange(workbook, sheetName, startColumn, startRow, endColumn, endRow, opts.MaxBytes)
		return records, cellRange, err
	}

	records, err := readSpreadsheetAutoRange(workbook, sheetName, opts)
	if err != nil {
		return nil, "", err
	}
	return trimSpreadsheetUsedRange(records), "", nil
}

// spreadsheetRowsToFileResult maps worksheet rows into the shared file result shape.
func spreadsheetRowsToFileResult(
	records [][]string,
	sheetName string,
	cellRange string,
	opts SpreadsheetOptions,
) (*result.FileResult, error) {
	if len(records) == 0 {
		return &result.FileResult{
			Rows:        []map[string]any{},
			ColumnNames: []string{},
			RowCount:    0,
			Format:      QueryFormatSpreadsheet.String(),
			Sheet:       sheetName,
			Range:       cellRange,
		}, nil
	}

	width := maxRecordWidth(records)
	var hasHeader bool
	if opts.HasHeader != nil {
		hasHeader = *opts.HasHeader
	} else {
		hasHeader = !isLikelyHeadless(records[0])
	}

	budget := result.NewResponseBudget(opts.MaxBytes)
	for i := 0; i < width; i++ {
		length := 0
		if i < len(records[0]) {
			length = len(records[0][i])
		}
		if err := budget.Charge(int64(length) + 96); err != nil {
			return nil, err
		}
	}
	startDataRow := 0
	headers := spreadsheetSyntheticHeaders(width)
	isHeadless := !hasHeader
	if hasHeader {
		headers = spreadsheetHeaders(records[0], width)
		startDataRow = 1
	}

	for _, header := range headers {
		if err := budget.Charge(int64(len(header)) + 64); err != nil {
			return nil, err
		}
	}
	rows := make([]map[string]any, 0, max(0, len(records)-startDataRow))
	for i := startDataRow; i < len(records); i++ {
		if opts.MaxRows > 0 && len(rows) >= opts.MaxRows {
			return nil, xerror.ExecutionRowLimitExceeded().Detailf(
				"file content contains more than %d rows",
				opts.MaxRows,
			)
		}

		record := records[i]
		// Charge object slots and escaped JSON strings before allocating the row.
		if err := budget.Charge(int64(len(headers))*64 + 64); err != nil {
			return nil, err
		}
		for j, header := range headers {
			if err := budget.ChargeJSON(header); err != nil {
				return nil, err
			}
			value := ""
			if j < len(record) {
				value = record[j]
			}
			if err := budget.ChargeJSON(value); err != nil {
				return nil, err
			}
			if isHeadless {
				if err := budget.ChargeJSON(value); err != nil {
					return nil, err
				}
			}
		}
		row := make(map[string]any, len(headers)+1)
		for j, header := range headers {
			value := ""
			if j < len(record) {
				value = record[j]
			}
			row[header] = value
		}
		if isHeadless {
			spreadsheetRow := make([]any, len(headers))
			for j, header := range headers {
				spreadsheetRow[j] = row[header]
			}
			row["spreadsheet_row"] = spreadsheetRow
		}
		rows = append(rows, row)
	}

	return &result.FileResult{
		Rows:                  rows,
		ColumnNames:           headers,
		RowCount:              len(rows),
		Format:                QueryFormatSpreadsheet.String(),
		IsHeadlessSpreadsheet: isHeadless,
		Sheet:                 sheetName,
		Range:                 cellRange,
	}, nil
}

// boundedSpreadsheetRange parses an A1 range and rejects oversized row spans before reading cells.
func boundedSpreadsheetRange(cellRange string, maxRows int) (int, int, int, int, error) {
	cellRange = strings.TrimSpace(cellRange)
	if cellRange == "" {
		return 0, 0, 0, 0, xerror.Unexpected().Detail("spreadsheet range is required")
	}
	if strings.Contains(cellRange, "!") {
		return 0, 0, 0, 0, xerror.Unexpected().Detail(
			"spreadsheet range must not include a sheet name; use the sheet field",
		)
	}

	startColumn, startRow, endColumn, endRow, err := spreadsheetRangeCoordinates(cellRange)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	rangeRows := endRow - startRow + 1
	if maxRows > 0 && rangeRows-1 > maxRows {
		return 0, 0, 0, 0, xerror.ExecutionRowLimitExceeded().Detailf(
			"spreadsheet range contains more than %d rows",
			maxRows,
		)
	}

	return startColumn, startRow, endColumn, endRow, nil
}

// spreadsheetValueBudget admits the rectangle before allocating string slots and row headers.
func spreadsheetValueBudget(rows, columns int, maxBytes int64) (int64, error) {
	if maxBytes <= 0 {
		maxBytes = defaultSpreadsheetUnzipSizeLimit
	}
	if rows == 0 {
		return maxBytes, nil
	}
	// Use 64-bit header sizes, conservatively covering smaller architectures too.
	perRow := maxBytes / int64(rows)
	if perRow < spreadsheetRowHeaderBytes || int64(columns) > (perRow-spreadsheetRowHeaderBytes)/spreadsheetStringSlotBytes {
		return 0, spreadsheetExpansionError(maxBytes)
	}
	return maxBytes - int64(rows)*(spreadsheetRowHeaderBytes+int64(columns)*spreadsheetStringSlotBytes), nil
}

func spreadsheetExpansionError(maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = defaultSpreadsheetUnzipSizeLimit
	}
	return xerror.ExecutionResponseSizeExceeded().Detailf("expanded worksheet exceeds %d byte storage budget", maxBytes)
}

// readSpreadsheetCellRange reads a bounded rectangular worksheet range.
func readSpreadsheetCellRange(
	workbook *excelize.File,
	sheetName string,
	startColumn int,
	startRow int,
	endColumn int,
	endRow int,
	maxBytes int64,
) ([][]string, error) {
	width := endColumn - startColumn + 1
	valueBudget, err := spreadsheetValueBudget(endRow-startRow+1, width, maxBytes)
	if err != nil {
		return nil, err
	}
	records := make([][]string, 0, endRow-startRow+1)
	for rowIndex := startRow; rowIndex <= endRow; rowIndex++ {
		record := make([]string, width)
		for columnIndex := startColumn; columnIndex <= endColumn; columnIndex++ {
			cellName, err := excelize.CoordinatesToCellName(columnIndex, rowIndex)
			if err != nil {
				return nil, xerror.Unexpected().Detailf("build spreadsheet cell name: %w", err)
			}
			value, err := workbook.GetCellValue(sheetName, cellName)
			if err != nil {
				return nil, xerror.Unexpected().Detailf("read spreadsheet cell %s: %w", cellName, err)
			}
			if int64(len(value)) > valueBudget {
				return nil, spreadsheetExpansionError(maxBytes)
			}
			valueBudget -= int64(len(value))
			record[columnIndex-startColumn] = value
		}
		records = append(records, record)
	}

	return records, nil
}

// readSpreadsheetAutoRange streams worksheet rows while bounding sparse row spans.
func readSpreadsheetAutoRange(workbook *excelize.File, sheetName string, opts SpreadsheetOptions) ([][]string, error) {
	rows, err := workbook.Rows(sheetName)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("read spreadsheet rows %q: %w", sheetName, err)
	}
	defer func() { _ = rows.Close() }()

	records := make([][]string, 0)
	rowIndex := 0
	firstContentRow := 0
	width := 0
	var valueBytes int64
	processingBudget := opts.MaxBytes
	if processingBudget <= 0 {
		processingBudget = defaultSpreadsheetUnzipSizeLimit
	}
	byteLimit := processingBudget
	for rows.Next() {
		rowIndex++

		record, err := rows.Columns()
		if err != nil {
			return nil, xerror.Unexpected().Detailf("read spreadsheet row %d: %w", rowIndex, err)
		}
		rowValueBudget, err := spreadsheetValueBudget(1, len(record), byteLimit)
		if err != nil {
			return nil, err
		}
		rowBytes := byteLimit - rowValueBudget
		if rowBytes > processingBudget {
			return nil, spreadsheetExpansionError(byteLimit)
		}
		processingBudget -= rowBytes
		for _, value := range record {
			if int64(len(value)) > processingBudget {
				return nil, spreadsheetExpansionError(byteLimit)
			}
			processingBudget -= int64(len(value))
			valueBytes += int64(len(value))
		}

		if firstContentRow == 0 {
			if isBlankSpreadsheetRecord(record) {
				continue
			}
			firstContentRow = rowIndex
		}

		rangeRows := rowIndex - firstContentRow + 1
		if opts.MaxRows > 0 && rangeRows-1 > opts.MaxRows {
			return nil, xerror.ExecutionRowLimitExceeded().Detailf(
				"spreadsheet range contains more than %d rows",
				opts.MaxRows,
			)
		}
		width = max(width, len(record))
		valueBudget, err := spreadsheetValueBudget(rangeRows, width, opts.MaxBytes)
		if err != nil {
			return nil, err
		}
		if valueBytes > valueBudget {
			return nil, spreadsheetExpansionError(opts.MaxBytes)
		}

		records = append(records, record)
	}
	if err := rows.Error(); err != nil {
		return nil, xerror.Unexpected().Detailf("read spreadsheet rows %q: %w", sheetName, err)
	}

	return records, nil
}

// isBlankSpreadsheetRecord reports whether a streamed row has no visible cells.
func isBlankSpreadsheetRecord(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

// trimSpreadsheetUsedRange removes leading/trailing blank worksheet space for auto-range reads.
func trimSpreadsheetUsedRange(records [][]string) [][]string {
	minRow, maxRow := -1, -1
	minColumn, maxColumn := -1, -1
	for rowIndex, record := range records {
		for columnIndex, value := range record {
			if strings.TrimSpace(value) == "" {
				continue
			}
			if minRow == -1 || rowIndex < minRow {
				minRow = rowIndex
			}
			if rowIndex > maxRow {
				maxRow = rowIndex
			}
			if minColumn == -1 || columnIndex < minColumn {
				minColumn = columnIndex
			}
			if columnIndex > maxColumn {
				maxColumn = columnIndex
			}
		}
	}
	if minRow == -1 {
		return [][]string{}
	}

	trimmed := make([][]string, 0, maxRow-minRow+1)
	for rowIndex := minRow; rowIndex <= maxRow; rowIndex++ {
		record := make([]string, maxColumn-minColumn+1)
		if rowIndex < len(records) {
			source := records[rowIndex]
			for columnIndex := minColumn; columnIndex <= maxColumn; columnIndex++ {
				if columnIndex < len(source) {
					record[columnIndex-minColumn] = source[columnIndex]
				}
			}
		}
		trimmed = append(trimmed, record)
	}

	return trimmed
}

// spreadsheetRangeCoordinates parses a single cell or A1 range into coordinates.
func spreadsheetRangeCoordinates(
	cellRange string,
) (startColumn, startRow, endColumn, endRow int, err error) {
	parts := strings.Split(cellRange, ":")
	if len(parts) == 0 || len(parts) > 2 {
		return 0, 0, 0, 0, xerror.Unexpected().Detailf("invalid spreadsheet range %q", cellRange)
	}

	startColumn, startRow, err = spreadsheetCellCoordinates(parts[0])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	endColumn, endRow = startColumn, startRow
	if len(parts) == 2 {
		endColumn, endRow, err = spreadsheetCellCoordinates(parts[1])
		if err != nil {
			return 0, 0, 0, 0, err
		}
	}

	if startColumn > endColumn || startRow > endRow {
		return 0, 0, 0, 0, xerror.Unexpected().Detailf("invalid spreadsheet range %q", cellRange)
	}

	return startColumn, startRow, endColumn, endRow, nil
}

// spreadsheetCellCoordinates parses one A1 cell reference.
func spreadsheetCellCoordinates(cell string) (int, int, error) {
	cell = strings.ToUpper(strings.TrimSpace(cell))
	if cell == "" {
		return 0, 0, xerror.Unexpected().Detail("spreadsheet range cell is required")
	}

	column, row, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return 0, 0, xerror.Unexpected().Detailf("invalid spreadsheet cell %q: %w", cell, err)
	}

	return column, row, nil
}

// spreadsheetWriteStartCell resolves where spreadsheet export should start writing.
func spreadsheetWriteStartCell(cellRange string) (int, int, error) {
	cellRange = strings.TrimSpace(cellRange)
	if cellRange == "" {
		return 1, 1, nil
	}

	startColumn, startRow, _, _, err := spreadsheetRangeCoordinates(cellRange)
	if err != nil {
		return 0, 0, err
	}

	return startColumn, startRow, nil
}

// writeSpreadsheetRecord writes one exported row at a fixed row and start column.
func writeSpreadsheetRecord(
	workbook *excelize.File,
	sheetName string,
	rowNumber int,
	startColumn int,
	record []any,
) error {
	for i, value := range record {
		cellName, err := excelize.CoordinatesToCellName(startColumn+i, rowNumber)
		if err != nil {
			return xerror.Unexpected().Detailf("build spreadsheet cell name: %w", err)
		}
		if err := workbook.SetCellValue(sheetName, cellName, value); err != nil {
			return xerror.Unexpected().Detailf("write spreadsheet cell %s: %w", cellName, err)
		}
	}

	return nil
}

// spreadsheetObjectRows validates the write payload as an array of row objects.
func spreadsheetObjectRows(content any) ([]map[string]any, error) {
	switch typed := content.(type) {
	case []any:
		rows := make([]map[string]any, 0, len(typed))
		for idx, row := range typed {
			obj, ok := row.(map[string]any)
			if !ok {
				return nil, xerror.Unexpected().Detailf("spreadsheet write row %d must be an object", idx)
			}
			rows = append(rows, obj)
		}
		return rows, nil
	case []map[string]any:
		return typed, nil
	default:
		return nil, xerror.Unexpected().Detail(
			"spreadsheet write content must be an array of objects",
		)
	}
}

// spreadsheetWriteHeaders returns a stable sorted union of all row keys.
func spreadsheetWriteHeaders(rows []map[string]any) []string {
	headerSet := map[string]struct{}{}
	headers := make([]string, 0)
	for _, row := range rows {
		for key := range row {
			if _, exists := headerSet[key]; exists {
				continue
			}
			headerSet[key] = struct{}{}
			headers = append(headers, key)
		}
	}

	sort.Strings(headers)
	return headers
}

// spreadsheetHeaders normalizes a worksheet header row into unique column names.
func spreadsheetHeaders(record []string, width int) []string {
	headers := make([]string, width)
	seen := make(map[string]struct{}, width)
	nextSuffix := make(map[string]int, width)
	for i := 0; i < width; i++ {
		header := ""
		if i < len(record) {
			header = strings.TrimSpace(record[i])
		}
		if header == "" {
			header = fmt.Sprintf("col_%d", i)
		}

		base := header
		suffix := nextSuffix[base]
		if suffix < 2 {
			suffix = 2
		}
		for {
			if _, exists := seen[header]; !exists {
				break
			}
			header = fmt.Sprintf("%s_%d", base, suffix)
			suffix++
		}
		nextSuffix[base] = suffix
		seen[header] = struct{}{}
		headers[i] = header
	}

	return headers
}

// spreadsheetSyntheticHeaders creates deterministic column names for headless sheets.
func spreadsheetSyntheticHeaders(width int) []string {
	headers := make([]string, width)
	for i := 0; i < width; i++ {
		headers[i] = fmt.Sprintf("col_%d", i)
	}

	return headers
}

// stringsToAny adapts header values for excelize cell writing.
func stringsToAny(values []string) []any {
	converted := make([]any, len(values))
	for i, value := range values {
		converted[i] = value
	}

	return converted
}

// validateSpreadsheetSheetName enforces the worksheet naming rules we depend on.
func validateSpreadsheetSheetName(sheetName string) error {
	if strings.TrimSpace(sheetName) == "" {
		return xerror.Unexpected().Detail("spreadsheet sheet name is required")
	}
	if len([]rune(sheetName)) > 31 {
		return xerror.Unexpected().Detail("spreadsheet sheet name must be 31 characters or fewer")
	}
	if strings.ContainsAny(sheetName, `:\/?*[]`) {
		return xerror.Unexpected().Detail("spreadsheet sheet name contains unsupported characters")
	}

	return nil
}

// applySpreadsheetWorkbookPath selects the workbook content type for known target extensions.
func applySpreadsheetWorkbookPath(workbook *excelize.File, filePath string) {
	if workbook == nil || !IsSpreadsheetExtension(filePath) {
		return
	}

	workbook.Path = filePath
}
