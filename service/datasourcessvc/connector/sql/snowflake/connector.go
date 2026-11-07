package snowflake

import (
	"compress/gzip"
	"context"
	stdsql "database/sql"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	snowflakeclient "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

var nativeInputRelationPattern = regexp.MustCompile(`(?i)\b(from|join)\s+input\b`)

const (
	maxSnowflakeCompressedInputBytes   = 64 * 1024 * 1024
	maxSnowflakeUncompressedInputBytes = 128 * 1024 * 1024
)

// Config contains non-sensitive Snowflake session configuration owned by the
// Braidplane connector boundary.
type Config struct {
	WIFObjects     []snowflakewif.ObjectGrant            `json:"wif_objects,omitempty"`
	Account        string                                `json:"account" validate:"required"`
	Warehouse      string                                `json:"warehouse" validate:"required"`
	Database       string                                `json:"database" validate:"required"`
	Schema         string                                `json:"schema" validate:"required"`
	Role           string                                `json:"role,omitempty"`
	ConnectTimeout int                                   `json:"connect_timeout,omitempty"`
	WIFTokenFile   string                                `json:"_wif_token_file,omitempty"`
	WIFTokenSource func(context.Context) (string, error) `json:"-"`
}

// Credentials selects password or deployment-owned workload identity authentication.
type Credentials struct {
	AuthType string `json:"auth_type" validate:"required"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

const (
	AuthTypePassword         = snowflakeclient.AuthTypePassword
	AuthTypeWorkloadIdentity = snowflakeclient.AuthTypeWorkloadIdentity
)

type providerClient interface {
	Close() error
	TestConnection(context.Context) error
	Execute(context.Context, string, []any, bool, snowflakeclient.RowScanner) (snowflakeclient.Result, error)
	ListColumns(context.Context) ([]snowflakeclient.Column, error)
}

// Connector executes Snowflake SQL through the official database/sql driver.
type Connector struct {
	client providerClient
	clock  clock.Clock
}

// New opens and verifies a Snowflake connection pool.
func New(ctx context.Context, config Config, credentials Credentials) (*Connector, error) {
	client, err := snowflakeclient.New(ctx, config.clientConfig(), credentials.clientCredentials())
	if err != nil {
		return nil, err
	}
	return &Connector{client: client, clock: clock.New()}, nil
}

// ValidateCredentials enforces the public Snowflake authentication contract without opening a session.
func ValidateCredentials(credentials Credentials) error {
	return snowflakeclient.ValidateCredentials(credentials.clientCredentials())
}

func (config Config) clientConfig() snowflakeclient.Config {
	return snowflakeclient.Config{
		WIFObjects:     config.WIFObjects,
		Account:        config.Account,
		Warehouse:      config.Warehouse,
		Database:       config.Database,
		Schema:         config.Schema,
		Role:           config.Role,
		ConnectTimeout: config.ConnectTimeout,
		WIFTokenFile:   config.WIFTokenFile,
		WIFTokenSource: config.WIFTokenSource,
	}
}

func (credentials Credentials) clientCredentials() snowflakeclient.Credentials {
	return snowflakeclient.Credentials{
		AuthType: credentials.AuthType,
		Username: credentials.Username,
		Password: credentials.Password,
	}
}

// Close releases the Snowflake connection pool.
func (c *Connector) Close() error {
	return c.client.Close()
}

// TestConnection verifies the configured account and warehouse session.
func (c *Connector) TestConnection(ctx context.Context) error {
	return c.client.TestConnection(ctx)
}

// GetCapabilities reports the initial Snowflake SQL connector capabilities.
func (c *Connector) GetCapabilities() sqlconnector.Capabilities {
	return sqlconnector.Capabilities{
		SupportsSchemaIntrospection: true,
		SupportsPagination:          false,
		SupportsTransactions:        false,
	}
}

// Execute runs a parameterized source query or explicitly authored INSERT sink statement.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	start := c.clock.Now()
	normalized, err := sqlconnector.NormalizeParams(params)
	if err != nil {
		return sqlconnector.NewFailedExecutionResult("snowflake", start, err), nil
	}
	stmt, args, err := prepareStatement(queryData, normalized)
	if err != nil {
		return sqlconnector.NewFailedExecutionResult("snowflake", start, err), nil
	}
	execLimits := types.ResolveExecutionLimits(limits...)
	queryStatement := isQueryStatement(stmt)
	if _, hasPipelineInput := normalized[sinkbinding.InternalInputRows]; hasPipelineInput {
		queryStatement = false
	}
	providerResult, execErr := c.client.Execute(
		ctx, stmt, args, queryStatement, func(rows *stdsql.Rows) (snowflakeclient.Result, error) {
			scanned, err := sqlconnector.ScanRows(rows, execLimits, sqlconnector.BytesToStringValue)
			if err != nil {
				return snowflakeclient.Result{}, err
			}
			return snowflakeclient.Result{Rows: scanned.Rows, ColumnNames: scanned.ColumnNames, RowCount: scanned.RowCount}, nil
		},
	)
	for _, row := range providerResult.Rows {
		for name, value := range row {
			row[name] = sqlconnector.BytesToStringValue(value, "")
		}
	}
	sqlResult := sqlconnector.Result{
		Rows: providerResult.Rows, ColumnNames: providerResult.ColumnNames, RowCount: providerResult.RowCount,
	}
	if execErr == nil {
		execErr = enforceNormalizedResultSize(sqlResult, execLimits.MaxResponseBytes)
		if execErr != nil {
			sqlResult = sqlconnector.Result{}
		}
	}
	return sqlconnector.NewExecutionResult("snowflake", start, sqlResult, execErr), nil
}

func enforceNormalizedResultSize(sqlResult sqlconnector.Result, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	payload, err := json.Marshal(sqlResult)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if int64(len(payload)) <= maxBytes {
		return nil
	}
	return xerror.ExecutionResponseSizeExceeded().Detailf(
		"Snowflake normalized result exceeds %d byte limit",
		maxBytes,
	)
}

func prepareStatement(queryData json.RawMessage, params map[string]any) (string, []any, error) {
	var statement struct {
		Type string `json:"type"`
		SQL  string `json:"sql"`
	}
	if err := json.Unmarshal(queryData, &statement); err != nil {
		return "", nil, sqlconnector.ErrInvalidConfig.With(err)
	}
	if strings.TrimSpace(statement.SQL) == "" {
		return "", nil, sqlconnector.ErrInvalidConfig.Detail("SQL statement is empty")
	}
	if nativeInputRelationPattern.MatchString(statement.SQL) {
		if rowsValue, ok := params[sinkbinding.InternalInputRows]; ok {
			rows, rowsOK := rowsValue.([]map[string]any)
			if !rowsOK {
				return "", nil, xerror.InvalidInput(xerror.Query).Detail("snowflake input rows are invalid")
			}
			columns := make([]string, 0)
			if columnValue, ok := params[sinkbinding.InternalInputColumns].([]string); ok {
				columns = append(columns, columnValue...)
			}
			if len(columns) == 0 {
				columnSet := make(map[string]struct{})
				for _, row := range rows {
					for name := range row {
						columnSet[name] = struct{}{}
					}
				}
				for name := range columnSet {
					columns = append(columns, name)
				}
				sort.Strings(columns)
			}
			if len(columns) == 0 {
				return "", nil, xerror.InvalidInput(xerror.Query).Detail(
					"snowflake input rows require at least one column",
				)
			}
			rowsJSON, err := json.Marshal(rows)
			if err != nil {
				return "", nil, xerror.Unexpected().With(err)
			}
			if err := validateSnowflakeInputPayload(rowsJSON); err != nil {
				return "", nil, err
			}
			projections := make([]string, 0, len(columns))
			for _, column := range columns {
				escaped := strings.ReplaceAll(column, `"`, `""`)
				projection := `value:"` + escaped + `"`
				if cast := snowflakeInputCast(rows, column); cast != "" {
					projection += "::" + cast
				}
				projections = append(projections, projection+` AS "`+escaped+`"`)
			}
			inputRelationSQL := "SELECT " + strings.Join(projections, ", ") +
				" FROM TABLE(FLATTEN(INPUT => PARSE_JSON(?)))"
			stmtWithInput, err := injectSnowflakeInputRelation(statement.SQL, inputRelationSQL)
			if err != nil {
				return "", nil, err
			}
			processed, args, err := bindNamedParams(stmtWithInput, params)
			if err != nil {
				return "", nil, err
			}
			return processed, append([]any{string(rowsJSON)}, args...), nil
		}
	}
	if _, hasInputRows := params[sinkbinding.InternalInputRows]; hasInputRows {
		return "", nil, xerror.InvalidInput(xerror.Query).Detail(
			"snowflake pipeline sink statement must select rows from input",
		)
	}

	return bindNamedParams(statement.SQL, params)
}

func snowflakeInputCast(rows []map[string]any, column string) string {
	for _, row := range rows {
		switch row[column].(type) {
		case nil:
			continue
		case string, []byte:
			return "STRING"
		case bool:
			return "BOOLEAN"
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return "NUMBER"
		case float32, float64:
			return "FLOAT"
		default:
			return ""
		}
	}
	return ""
}

func validateSnowflakeInputPayload(payload []byte) error {
	return validateSnowflakeInputPayloadLimits(
		payload,
		maxSnowflakeCompressedInputBytes,
		maxSnowflakeUncompressedInputBytes,
	)
}

func validateSnowflakeInputPayloadLimits(payload []byte, compressedLimit, uncompressedLimit int64) error {
	if int64(len(payload)) > uncompressedLimit {
		return xerror.InvalidInput(xerror.Query).Detailf(
			"Snowflake input payload is %d uncompressed bytes; maximum is %d bytes",
			len(payload), uncompressedLimit,
		)
	}

	compressedSize := &byteCounter{}
	compressor, err := gzip.NewWriterLevel(compressedSize, gzip.BestSpeed)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if _, err := compressor.Write(payload); err != nil {
		return xerror.Unexpected().With(err)
	}
	if err := compressor.Close(); err != nil {
		return xerror.Unexpected().With(err)
	}
	if compressedSize.total > compressedLimit {
		return xerror.InvalidInput(xerror.Query).Detailf(
			"Snowflake input payload compresses to %d bytes; maximum is %d bytes",
			compressedSize.total, compressedLimit,
		)
	}
	return nil
}

type byteCounter struct {
	total int64
}

func (c *byteCounter) Write(payload []byte) (int, error) {
	c.total += int64(len(payload))
	return len(payload), nil
}

func injectSnowflakeInputRelation(stmt, inputRelationSQL string) (string, error) {
	trimmed := strings.TrimSpace(stmt)
	if !strings.HasPrefix(strings.ToUpper(trimmed), "INSERT ") {
		return "", xerror.InvalidInput(xerror.Query).Detail(
			"snowflake pipeline sink statement must be an INSERT operation",
		)
	}

	selectIndex := unquotedSQLKeywordIndex(trimmed, "SELECT")
	if selectIndex < 0 {
		return "", xerror.InvalidInput(xerror.Query).Detail(
			"snowflake pipeline sink INSERT must select rows from input",
		)
	}

	// Snowflake places a query's WITH clause after the INSERT target and before SELECT.
	return trimmed[:selectIndex] + "WITH input AS (" + inputRelationSQL + ") " +
		trimmed[selectIndex:], nil
}

func unquotedSQLKeywordIndex(statement, keyword string) int {
	for index := 0; index < len(statement); {
		switch statement[index] {
		case '\'', '"':
			quote := statement[index]
			index++
			for index < len(statement) {
				if statement[index] != quote {
					index++
					continue
				}
				if index+1 < len(statement) && statement[index+1] == quote {
					index += 2
					continue
				}
				index++
				break
			}
		case '-':
			if index+1 < len(statement) && statement[index+1] == '-' {
				index += 2
				for index < len(statement) && statement[index] != '\n' {
					index++
				}
				continue
			}
			index++
		case '/':
			if index+1 < len(statement) && statement[index+1] == '*' {
				index += 2
				for index+1 < len(statement) && (statement[index] != '*' || statement[index+1] != '/') {
					index++
				}
				if index+1 < len(statement) {
					index += 2
				}
				continue
			}
			index++
		default:
			end := index + len(keyword)
			if end <= len(statement) && strings.EqualFold(statement[index:end], keyword) &&
				(index == 0 || !isSQLIdentifierByte(statement[index-1])) &&
				(end == len(statement) || !isSQLIdentifierByte(statement[end])) {
				return index
			}
			index++
		}
	}
	return -1
}

func isSQLIdentifierByte(value byte) bool {
	return value == '_' || value == '$' ||
		value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}

func bindNamedParams(statement string, params map[string]any) (string, []any, error) {
	return param.BindParams(statement, params, "?")
}

func isQueryStatement(statement string) bool {
	upper := strings.ToUpper(strings.TrimSpace(statement))
	for _, prefix := range []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "WITH", "EXPLAIN"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// IntrospectSchema reads visible tables and columns from Snowflake information_schema.
func (c *Connector) IntrospectSchema(ctx context.Context) (*sqlconnector.Schema, error) {
	columns, err := c.client.ListColumns(ctx)
	if err != nil {
		return nil, err
	}
	tables := make([]sqlconnector.Table, 0)
	tableIndexes := make(map[string]int)
	for _, column := range columns {
		fullName := column.Schema + "." + column.Table
		if err := schemaquota.Charge(ctx, 1, len(fullName)+len(column.Name)+len(column.DataType)); err != nil {
			return nil, err
		}
		index, ok := tableIndexes[fullName]
		if !ok {
			index = len(tables)
			tableIndexes[fullName] = index
			tables = append(tables, sqlconnector.Table{Name: fullName})
		}
		tables[index].Columns = append(tables[index].Columns, sqlconnector.Column{
			Name: column.Name, Type: column.DataType, Nullable: strings.EqualFold(column.IsNullable, "YES"),
		})
	}
	return &sqlconnector.Schema{Tables: tables}, nil
}

func (config Config) ValidateOnboarding() error {
	return (snowflakewif.Setup{Warehouse: config.Warehouse, Database: config.Database, Schema: config.Schema, Role: config.Role, Objects: config.WIFObjects}).Validate()
}
