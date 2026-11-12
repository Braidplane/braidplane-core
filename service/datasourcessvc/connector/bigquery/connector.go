package bigquery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	bigqueryclient "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// Connector adapts BigQuery provider operations to Braidplane connector contracts.
type Connector struct {
	config Config
	client providerClient
}

// New creates a BigQuery connector backed by the provider REST client.
func New(ctx context.Context, config Config, creds Credentials) (*Connector, error) {
	client, err := bigqueryclient.New(ctx, config, creds)
	if err != nil {
		return nil, err
	}

	return newWithClient(config, client), nil
}

// newWithClient creates a connector with an injected provider client for deterministic tests.
func newWithClient(config Config, client providerClient) *Connector {
	return &Connector{config: config, client: client}
}

// Close releases provider client resources.
func (c *Connector) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

// TestConnection verifies that the configured BigQuery project can be reached.
func (c *Connector) TestConnection(ctx context.Context) error {
	return c.client.TestConnection(ctx, TestConnectionRequest{
		ProjectID: c.config.ProjectID,
		DatasetID: c.config.DefaultDataset,
		Location:  c.config.Location,
	})
}

// GetCapabilities reports BigQuery's SQL-shaped connector capabilities.
func (c *Connector) GetCapabilities() sqlconnector.Capabilities {
	return sqlconnector.Capabilities{
		SupportsSchemaIntrospection: true,
		SupportsPagination:          true,
		SupportsTransactions:        false,
	}
}

// IntrospectSchema maps BigQuery datasets, tables, and fields into the shared SQL schema shape.
func (c *Connector) IntrospectSchema(ctx context.Context) (*sqlconnector.Schema, error) {
	datasets, err := c.client.ListDatasets(ctx, ListDatasetsRequest{
		ProjectID: c.config.ProjectID,
		Location:  c.config.Location,
	})
	if err != nil {
		return nil, err
	}

	tables := make([]sqlconnector.Table, 0)
	for _, dataset := range datasets {
		providerTables, err := c.client.ListTables(ctx, ListTablesRequest{
			ProjectID: c.config.ProjectID,
			DatasetID: dataset.ID,
			Location:  c.config.Location,
		})
		if err != nil {
			return nil, err
		}

		for _, table := range providerTables {
			metadata, err := c.client.GetTable(ctx, GetTableRequest{
				ProjectID: c.config.ProjectID,
				DatasetID: table.DatasetID,
				TableID:   table.TableID,
				Location:  c.config.Location,
			})
			if err != nil {
				return nil, err
			}

			tables = append(tables, sqlconnector.Table{
				Name:    metadata.DatasetID + "." + metadata.TableID,
				Columns: columnsFromFields(metadata.Fields),
			})
		}
	}

	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	return &sqlconnector.Schema{Tables: tables}, nil
}

// Execute runs a BigQuery query or append-style row insert operation.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	start := time.Now()
	execLimits := types.ResolveExecutionLimits(limits...)

	normalizedParams, err := types.NormalizeParams(params)
	if err != nil {
		return bigQueryFailureResult(start, err), nil
	}

	var stmt statement
	if err := json.Unmarshal(queryData, &stmt); err != nil {
		return bigQueryFailureResult(
			start,
			xerror.InvalidInput(xerror.Query).Detailf("invalid BigQuery operation: %w", err),
		), nil
	}

	fileOutput, err := sqlconnector.FileOutputRequested(queryData)
	if err != nil {
		return bigQueryFailureResult(start, err), nil
	}
	if fileOutput && normalizedOperation(stmt.Operation) != operationQuery {
		return bigQueryFailureResult(start, sqlconnector.ErrInvalidConfig.WithPublicDetail("SQL file output requires a query operation.")), nil
	}
	switch normalizedOperation(stmt.Operation) {
	case operationQuery:
		return c.executeQuery(ctx, start, stmt, params, execLimits, fileOutput), nil
	case operationInsertRows:
		return c.executeInsertRows(ctx, start, stmt, normalizedParams, execLimits), nil
	default:
		return bigQueryFailureResult(
			start,
			xerror.InvalidInput(xerror.Query).Detail("bigquery operation must be query or insert_rows"),
		), nil
	}
}

func (c *Connector) executeQuery(
	ctx context.Context,
	start time.Time,
	stmt statement,
	params map[string]types.TypedParam,
	limits types.ExecutionLimits,
	fileOutput bool,
) *result.ExecutionResult {
	boundSQL, queryParams, err := bindQueryParameters(stmt.SQL, params)
	if err != nil {
		return bigQueryFailureResult(start, err)
	}

	request := QueryRequest{
		ProjectID:          c.config.ProjectID,
		Location:           c.config.Location,
		SQL:                boundSQL,
		Parameters:         queryParams,
		Timeout:            effectiveTimeout(c.config.TimeoutSeconds, stmt.TimeoutSeconds, limits),
		MaximumBytesBilled: effectiveMaximumBytesBilled(c.config.MaximumBytesBilled, stmt.MaximumBytesBilled),
		MaxResultRows:      providerRowLimit(limits.MaxResultRows),
		MaxResponseBytes:   effectiveResponseBytes(limits),
	}
	if fileOutput {
		request.MaxResultRows = limits.MaxResultRows
		output, err := sqlconnector.RetainQueryFile(ctx, limits, func(ctx context.Context, dst io.Writer) (sqlconnector.StreamResult, error) {
			var stream *sqlconnector.RowStream
			err := c.client.QueryPages(ctx, request, func(page *QueryResult) error {
				normalized, err := sqlResultFromQuery(page, limits.MaxResponseBytes)
				if err != nil {
					return err
				}
				if stream == nil {
					stream, err = sqlconnector.NewRowStream(ctx, dst, normalized.ColumnNames, limits)
					if err != nil {
						return err
					}
				}
				for _, row := range normalized.Rows {
					if err := stream.WriteRow(row); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return sqlconnector.StreamResult{}, err
			}
			if stream == nil {
				return sqlconnector.StreamResult{}, sqlconnector.ErrInvalidConfig.WithPublicDetail("BigQuery did not return query result metadata.")
			}
			return stream.Finish()
		})
		return sqlconnector.NewExecutionResultData("bigquery", start, output, err)
	}
	queryResult, err := c.client.Query(ctx, request)
	if err != nil {
		return bigQueryFailureResult(start, err)
	}

	if limits.MaxResultRows > 0 && len(queryResult.Rows) > limits.MaxResultRows {
		return bigQueryFailureResult(
			start,
			xerror.ExecutionRowLimitExceeded().Detailf(
				"BigQuery operation returned more than %d rows",
				limits.MaxResultRows,
			),
		)
	}

	sqlResult, err := sqlResultFromQuery(queryResult, effectiveResponseBytes(limits))
	if err != nil {
		return bigQueryFailureResult(start, err)
	}

	return bigQuerySuccessResult(start, sqlResult)
}

func (c *Connector) executeInsertRows(
	ctx context.Context,
	start time.Time,
	stmt statement,
	params map[string]any,
	limits types.ExecutionLimits,
) *result.ExecutionResult {
	datasetID := strings.TrimSpace(stmt.Dataset)
	if datasetID == "" {
		datasetID = strings.TrimSpace(c.config.DefaultDataset)
	}
	if datasetID == "" {
		return bigQueryFailureResult(
			start,
			xerror.InvalidInput(xerror.Query).Detail("bigquery insert_rows dataset is required"),
		)
	}
	tableID := strings.TrimSpace(stmt.Table)
	if tableID == "" {
		return bigQueryFailureResult(
			start,
			xerror.InvalidInput(xerror.Query).Detail("bigquery insert_rows table is required"),
		)
	}

	rows, err := resolveRows(stmt.Rows, params)
	if err != nil {
		return bigQueryFailureResult(start, err)
	}
	if limits.MaxResultRows > 0 && len(rows) > limits.MaxResultRows {
		return bigQueryFailureResult(
			start,
			xerror.ExecutionRowLimitExceeded().Detailf(
				"BigQuery insert_rows received more than %d rows",
				limits.MaxResultRows,
			),
		)
	}

	insertIDPrefix, err := resolveOptionalString(stmt.InsertIDPrefix, params)
	if err != nil {
		return bigQueryFailureResult(start, err)
	}

	insertResult, err := c.client.InsertRows(ctx, InsertRowsRequest{
		ProjectID:        c.config.ProjectID,
		DatasetID:        datasetID,
		TableID:          tableID,
		InsertIDPrefix:   insertIDPrefix,
		Rows:             rows,
		MaxRequestBytes:  limits.HTTPMaxRequestBytes,
		MaxResponseBytes: effectiveResponseBytes(limits),
	})
	if err != nil {
		return bigQueryFailureResult(start, err)
	}

	return bigQuerySuccessResult(start, &result.SQLResult{
		Rows:        []map[string]any{},
		ColumnNames: []string{},
		RowCount:    insertResult.InsertedRows,
	})
}

func columnsFromFields(fields []Field) []sqlconnector.Column {
	columns := make([]sqlconnector.Column, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, sqlconnector.Column{
			Name:     field.Name,
			Type:     bigQueryColumnType(field),
			Nullable: strings.ToUpper(strings.TrimSpace(field.Mode)) != "REQUIRED",
		})
	}
	return columns
}

func bigQueryColumnType(field Field) string {
	typeName := strings.ToUpper(strings.TrimSpace(field.Type))
	if typeName != "NUMERIC" && typeName != "BIGNUMERIC" {
		return field.Type
	}
	precision, scale := strings.TrimSpace(field.Precision), strings.TrimSpace(field.Scale)
	if precision == "" && scale == "" {
		if typeName == "NUMERIC" {
			return "NUMERIC(38,9)"
		}
		return field.Type
	}
	if precision != "" && scale != "" {
		return fmt.Sprintf("%s(%s,%s)", typeName, precision, scale)
	}
	return field.Type
}

func bindQueryParameters(
	sqlText string,
	params map[string]types.TypedParam,
) (string, []QueryParameter, error) {
	if strings.TrimSpace(sqlText) == "" {
		return "", nil, xerror.InvalidInput(xerror.Query).Detail("bigquery query sql is required")
	}

	names := param.ExtractParamNames(sqlText)
	if len(names) == 0 {
		return sqlText, nil, nil
	}

	boundSQL := sqlText
	queryParams := make([]QueryParameter, 0, len(names))
	usedNames := make(map[string]string, len(names))
	for _, name := range names {
		typed, ok := params[name]
		if !ok {
			return "", nil, xerror.InvalidInput(xerror.Query).Detailf(
				"missing required parameter %q",
				name,
			)
		}

		bigQueryName := bigQueryParamName(name)
		if existing, ok := usedNames[bigQueryName]; ok && existing != name {
			return "", nil, xerror.InvalidInput(xerror.Query).Detailf(
				"parameters %q and %q collide after BigQuery name normalization",
				existing,
				name,
			)
		}
		usedNames[bigQueryName] = name

		queryParam, err := queryParameterFromTyped(bigQueryName, typed)
		if err != nil {
			return "", nil, err
		}
		queryParams = append(queryParams, queryParam)
		boundSQL = strings.ReplaceAll(boundSQL, "{{"+name+"}}", "@"+bigQueryName)
	}

	return boundSQL, queryParams, nil
}

func queryParameterFromTyped(name string, typed types.TypedParam) (QueryParameter, error) {
	switch typed.DataType {
	case "integer":
		value, err := types.CoerceIntegerParam(typed.Value)
		if err != nil {
			return QueryParameter{}, xerror.InvalidInput(xerror.Query).Detailf(
				"invalid BigQuery integer parameter %q: %w",
				name,
				err,
			)
		}
		return QueryParameter{Name: name, Type: "INT64", Value: value}, nil
	case "boolean":
		value, err := coerceBoolParam(typed.Value)
		if err != nil {
			return QueryParameter{}, xerror.InvalidInput(xerror.Query).Detailf(
				"invalid BigQuery boolean parameter %q: %w",
				name,
				err,
			)
		}
		return QueryParameter{Name: name, Type: "BOOL", Value: value}, nil
	case "date":
		return QueryParameter{Name: name, Type: "DATE", Value: fmt.Sprintf("%v", typed.Value)}, nil
	case "uuid", "text", "":
		return QueryParameter{Name: name, Type: "STRING", Value: fmt.Sprintf("%v", typed.Value)}, nil
	default:
		return QueryParameter{Name: name, Type: "STRING", Value: fmt.Sprintf("%v", typed.Value)}, nil
	}
}

func coerceBoolParam(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return false, xerror.Unexpected().With(err)
		}
		return parsed, nil
	default:
		return false, xerror.Unexpected().Detailf("unsupported type %T for boolean parameter", value)
	}
}

func bigQueryParamName(name string) string {
	var builder strings.Builder
	for idx, r := range name {
		valid := r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
		if idx == 0 && unicode.IsDigit(r) {
			builder.WriteByte('_')
		}
		if valid {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "param"
	}
	return builder.String()
}

func sqlResultFromQuery(queryResult *QueryResult, maxBytes int64) (*result.SQLResult, error) {
	if queryResult == nil {
		queryResult = &QueryResult{}
	}

	columnNames := make([]string, 0, len(queryResult.Schema))
	for _, field := range queryResult.Schema {
		columnNames = append(columnNames, field.Name)
	}

	sqlResult := &result.SQLResult{ColumnNames: columnNames, RowCount: len(queryResult.Rows)}
	metadata, err := json.Marshal(sqlResult)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	budget := result.NewResponseBudget(maxBytes)
	// Replace the metadata's null rows with array brackets.
	if err := budget.Charge(int64(len(metadata) - 2)); err != nil {
		return nil, err
	}
	sqlResult.Rows = []map[string]any{}
	lastField := make(map[string]int, len(queryResult.Schema))
	for i, field := range queryResult.Schema {
		lastField[field.Name] = i
	}
	keyBytes := int64(2)
	for name := range lastField {
		encoded, err := json.Marshal(name)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		keyBytes += int64(len(encoded) + 1)
	}
	if len(lastField) > 1 {
		keyBytes += int64(len(lastField) - 1)
	}
	for _, providerRow := range queryResult.Rows {
		if len(sqlResult.Rows) > 0 {
			if err := budget.Charge(1); err != nil {
				return nil, err
			}
		}
		if err := budget.Check(keyBytes + int64(len(lastField))); err != nil {
			return nil, err
		}
		if err := budget.Charge(keyBytes); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(lastField))
		for _, i := range lastField {
			field := queryResult.Schema[i]
			value := normalizeValue(providerRow[field.Name], field)
			if err := budget.ChargeJSON(value); err != nil {
				return nil, err
			}
			row[field.Name] = value
		}
		sqlResult.Rows = append(sqlResult.Rows, row)
	}
	return sqlResult, nil
}

func normalizeValue(value any, field Field) any {
	if value == nil {
		return nil
	}
	if strings.EqualFold(field.Mode, "REPEATED") {
		return normalizeJSONCompatible(value)
	}

	switch strings.ToUpper(strings.TrimSpace(field.Type)) {
	case "STRING", "DATE", "DATETIME", "TIME", "TIMESTAMP", "GEOGRAPHY", "BYTES":
		return fmt.Sprintf("%v", value)
	case "INT64", "INTEGER":
		return normalizeIntegerValue(value)
	case "FLOAT64", "FLOAT":
		return normalizeFloatValue(value)
	case "BOOL", "BOOLEAN":
		return normalizeBoolValue(value)
	case "NUMERIC", "BIGNUMERIC":
		return fmt.Sprintf("%v", value)
	case "JSON":
		return normalizeJSONCompatible(value)
	case "RECORD", "STRUCT":
		return normalizeJSONCompatible(value)
	default:
		return value
	}
}

func normalizeIntegerValue(value any) any {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return value
}

func normalizeFloatValue(value any) any {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return parsed
		}
	}
	return value
}

func normalizeBoolValue(value any) any {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed
		}
	}
	return value
}

func normalizeJSONCompatible(value any) any {
	switch v := value.(type) {
	case string:
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(v))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err == nil {
			return decoded
		}
		return v
	default:
		return value
	}
}

func resolveRows(raw json.RawMessage, params map[string]any) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, xerror.InvalidInput(xerror.Query).Detail("bigquery insert_rows rows are required")
	}

	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, xerror.InvalidInput(xerror.Query).Detailf("invalid bigquery rows payload: %w", err)
	}

	resolved, err := param.SubstituteValue(value, params)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.Query).Detailf("resolve bigquery rows: %w", err)
	}

	rows, ok := coerceRows(resolved)
	if !ok {
		return nil, xerror.InvalidInput(xerror.Query).Detail("bigquery insert_rows rows must resolve to row objects")
	}
	return rows, nil
}

func coerceRows(value any) ([]map[string]any, bool) {
	switch v := value.(type) {
	case []map[string]any:
		return append([]map[string]any(nil), v...), true
	case []any:
		rows := make([]map[string]any, 0, len(v))
		for _, item := range v {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			rows = append(rows, row)
		}
		return rows, true
	case map[string]any:
		return []map[string]any{v}, true
	default:
		return nil, false
	}
}

func resolveOptionalString(value string, params map[string]any) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}

	resolved, err := param.SubstituteString(value, params)
	if err != nil {
		return "", xerror.InvalidInput(xerror.Query).Detailf(
			"resolve bigquery insert_id_prefix: %w",
			err,
		)
	}
	return resolved, nil
}

func normalizedOperation(operation string) string {
	trimmed := strings.TrimSpace(operation)
	if trimmed == "" {
		return operationQuery
	}
	return trimmed
}

func providerRowLimit(maxRows int) int {
	if maxRows <= 0 {
		return 0
	}
	return maxRows + 1
}

func effectiveTimeout(
	configTimeoutSeconds int,
	operationTimeoutSeconds *int,
	limits types.ExecutionLimits,
) time.Duration {
	candidates := []time.Duration{}
	if configTimeoutSeconds > 0 {
		candidates = append(candidates, time.Duration(configTimeoutSeconds)*time.Second)
	}
	if operationTimeoutSeconds != nil && *operationTimeoutSeconds > 0 {
		candidates = append(candidates, time.Duration(*operationTimeoutSeconds)*time.Second)
	}
	if limits.ConnectorTimeout > 0 {
		candidates = append(candidates, limits.ConnectorTimeout)
	}
	return minDuration(candidates...)
}

func effectiveMaximumBytesBilled(configValue int64, operationValue *int64) int64 {
	candidates := []int64{}
	if configValue > 0 {
		candidates = append(candidates, configValue)
	}
	if operationValue != nil && *operationValue > 0 {
		candidates = append(candidates, *operationValue)
	}
	return minInt64(candidates...)
}

func effectiveResponseBytes(limits types.ExecutionLimits) int64 {
	return minInt64(limits.MaxResponseBytes, limits.HTTPMaxResponseBytes)
}

func minDuration(values ...time.Duration) time.Duration {
	var result time.Duration
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}

func minInt64(values ...int64) int64 {
	var result int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}

func bigQuerySuccessResult(start time.Time, sqlResult *result.SQLResult) *result.ExecutionResult {
	return sqlconnector.NewExecutionResultData("bigquery", start, sqlResult, nil)
}

func bigQueryFailureResult(start time.Time, err error) *result.ExecutionResult {
	return sqlconnector.NewExecutionResultData("bigquery", start, sqlconnector.EmptyResultData(), err)
}
