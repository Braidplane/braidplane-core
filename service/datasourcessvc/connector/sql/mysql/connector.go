package mysql

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

var (
	nativeInputRelationPattern = regexp.MustCompile(`(?i)\b(from|join)\s+input\b`)
	selectKeywordPattern       = regexp.MustCompile(`(?i)\bselect\b`)
	leadingWithPattern         = regexp.MustCompile(`(?i)^WITH\s+(RECURSIVE\s+)?`)
)

const mysqlInputColumnTypeBigInt = "BIGINT"

const mysqlPublicTCPNetwork = "braidplane_public_tcp"

// init registers the guarded MySQL network used when private hosts are not allowed.
func init() {
	mysqldriver.RegisterDialContext(mysqlPublicTCPNetwork, func(ctx context.Context, addr string) (net.Conn, error) {
		return hostguard.DialPublicHost(ctx, "tcp", addr)
	})
}

// Config holds configuration for MySQL/MariaDB connections.
type Config struct {
	Host              string `json:"host"                      validate:"required,hostname|ip"`
	Port              int    `json:"port"                      validate:"required,min=1,max=65535"`
	Database          string `json:"database"                  validate:"required"`
	User              string `json:"user"                      validate:"required"`
	Password          string `json:"password"`
	ConnectTimeout    int    `json:"connect_timeout,omitempty"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds MySQL authentication values.
type Credentials struct {
	Username string  `json:"username" validate:"required"`
	Password *string `json:"password" validate:"required"`
}

// Connector connects to MySQL/MariaDB databases.
type Connector struct {
	db                    *stdsql.DB
	connectTimeoutSeconds int
}

// SQLPoolStats returns a safe point-in-time snapshot of the underlying pool.
func (c *Connector) SQLPoolStats() stdsql.DBStats {
	return c.db.Stats()
}

// New creates a new MySQL connector with the provided configuration.
func New(ctx context.Context, config Config) (*Connector, error) {
	if config.Host == "" {
		return nil, sqlconnector.ErrInvalidConfig.Detail("host is required")
	}
	if config.Port == 0 {
		return nil, sqlconnector.ErrInvalidConfig.Detail("port is required")
	}
	if config.Database == "" {
		return nil, sqlconnector.ErrInvalidConfig.Detail("database is required")
	}
	if config.User == "" {
		return nil, sqlconnector.ErrInvalidConfig.Detail("user is required")
	}
	config.ConnectTimeout = sqlconnector.ResolveConnectTimeoutSeconds(config.ConnectTimeout)

	network := "tcp"
	if !config.AllowPrivateHosts {
		network = mysqlPublicTCPNetwork
	}
	driverConfig := mysqldriver.NewConfig()
	driverConfig.User = config.User
	driverConfig.Passwd = config.Password
	driverConfig.Net = network
	driverConfig.Addr = net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	driverConfig.DBName = config.Database
	driverConfig.Timeout = time.Duration(config.ConnectTimeout) * time.Second
	driverConfig.ParseTime = true
	if !transportsecurity.PlaintextAllowed(ctx) {
		driverConfig.TLS = transportsecurity.TLS(config.Host)
	}
	driverConnector, err := mysqldriver.NewConnector(driverConfig)
	if err != nil {
		return nil, sqlconnector.ErrInvalidConfig.With(err)
	}
	db := stdsql.OpenDB(driverConnector)
	db, err = sqlconnector.ConfigureAndPingDB(ctx, db, sqlconnector.OpenDBConfig{
		DriverName:            "mysql",
		ConnectTimeoutSeconds: config.ConnectTimeout,
		StartupErrorMapper:    mapStartupError,
	})
	if err != nil {
		return nil, err
	}

	return &Connector{
		db:                    db,
		connectTimeoutSeconds: config.ConnectTimeout,
	}, nil
}

func mapStartupError(err error) error {
	var mysqlErr *mysqldriver.MySQLError
	if xerror.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1045:
			return xerror.InvalidCredentials().With(err)
		case 1044:
			return xerror.Forbidden(xerror.DataSource).With(err)
		case 1049:
			return xerror.NotFound(xerror.DataSource).With(err)
		}
	}

	return xerror.ConnectionFailed(xerror.DataSource).With(err)
}

// TestConnection verifies that a connection to MySQL can be established.
func (c *Connector) TestConnection(ctx context.Context) error {
	if err := sqlconnector.TestDBConnection(ctx, c.db, c.connectTimeoutSeconds); err != nil {
		return mapStartupError(err)
	}
	return nil
}

// Execute runs the SQL query described by queryData and returns the results.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	start := time.Now()
	execLimits := types.ResolveExecutionLimits(limits...)

	// Normalize typed parameters to appropriate SQL types (e.g., float64 -> int64 for integer
	// params)
	normalized, err := sqlconnector.NormalizeParams(params)
	if err != nil {
		return sqlconnector.NewFailedExecutionResult("mysql", start, err), nil
	}

	stmt, args, err := prepareStatement(queryData, normalized)
	if err != nil {
		return sqlconnector.NewFailedExecutionResult("mysql", start, err), nil
	}

	sqlResult, execErr := executeStatementWithLimits(c, ctx, stmt, args, execLimits)

	return sqlconnector.NewExecutionResult("mysql", start, sqlResult, execErr), nil
}

// ExecuteStatement runs a parameterized SQL statement with ordered positional arguments.
// Placeholders in stmt must use MySQL ? notation matching the order of args.
func (c *Connector) ExecuteStatement(
	ctx context.Context,
	stmt string,
	args []any,
) (sqlconnector.Result, error) {
	return executeStatementWithLimits(c, ctx, stmt, args, types.ExecutionLimits{})
}

func executeStatementWithLimits(
	c *Connector,
	ctx context.Context,
	stmt string,
	args []any,
	limits types.ExecutionLimits,
) (sqlconnector.Result, error) {
	return sqlconnector.StatementExecutor{
		DB:               c.db,
		IsQueryStatement: isQueryStatement,
		NormalizeValue:   sqlconnector.BytesToStringValue,
	}.Execute(ctx, stmt, args, limits)
}

func prepareStatement(
	queryData json.RawMessage,
	params map[string]any,
) (string, []any, error) {
	stmt, err := parseStatement(queryData)
	if err != nil {
		return "", nil, err
	}

	processedNative, nativeArgs, handledNative, err := prepareNativeInputRelationStatement(
		stmt,
		params,
	)
	if err != nil {
		return "", nil, err
	}
	if handledNative {
		return processedNative, nativeArgs, nil
	}
	if _, hasInputRows := params[sinkbinding.InternalInputRows]; hasInputRows {
		return "", nil, xerror.InvalidInput(xerror.Query).Detail(
			"mysql pipeline sink statement must select rows from input",
		)
	}

	processed, paramOrder, occurrenceCounts := extractAndReplace(stmt)
	args := make([]any, 0, len(paramOrder))

	for _, name := range paramOrder {
		value, ok := params[name]
		if !ok {
			return "", nil, xerror.Unexpected().Detailf("missing required parameter %q", name)
		}

		for i := 0; i < occurrenceCounts[name]; i++ {
			args = append(args, value)
		}
	}

	return processed, args, nil
}

func prepareNativeInputRelationStatement(
	stmt string,
	params map[string]any,
) (string, []any, bool, error) {
	if !nativeInputRelationPattern.MatchString(stmt) {
		return "", nil, false, nil
	}

	rowsValue, ok := params[sinkbinding.InternalInputRows]
	if !ok {
		return "", nil, false, nil
	}

	rows, ok := rowsValue.([]map[string]any)
	if !ok {
		return "", nil, false, xerror.Unexpected().Detailf(
			"parameter %q must contain runtime rows",
			sinkbinding.InternalInputRows,
		)
	}

	columns, err := nativeInputColumns(params, rows)
	if err != nil {
		return "", nil, false, err
	}
	columnTypes, err := mysqlInputColumnTypes(rows, columns)
	if err != nil {
		return "", nil, false, err
	}

	positions := make(map[string]string, len(columns))
	for i, column := range columns {
		positions[column] = "c" + strconv.Itoa(i)
	}
	var jsonRows []map[string]any
	if rows != nil {
		jsonRows = make([]map[string]any, len(rows))
	}
	for i, row := range rows {
		if row == nil {
			continue
		}
		jsonRows[i] = make(map[string]any)
		for column, value := range row {
			position, selected := positions[column]
			if !selected {
				continue
			}
			if value != nil && strings.HasPrefix(columnTypes[column], "DECIMAL(") {
				// MySQL's JSON numeric parsing can round before JSON_TABLE converts to DECIMAL.
				value = fmt.Sprint(value)
			}
			jsonRows[i][position] = value
		}
	}
	rowsJSON, err := json.Marshal(jsonRows)
	if err != nil {
		return "", nil, false, xerror.Unexpected().With(err)
	}

	inputRelationSQL := buildMySQLInputRelationSQL(columns, columnTypes)
	processed, paramOrder, occurrenceCounts := extractAndReplace(stmt)
	processed = injectMySQLInputRelation(processed, inputRelationSQL)
	args := make([]any, 0, len(paramOrder)+1)
	args = append(args, string(rowsJSON))

	for _, name := range paramOrder {
		value, ok := params[name]
		if !ok {
			return "", nil, false, xerror.Unexpected().Detailf(
				"missing required parameter %q",
				name,
			)
		}

		for i := 0; i < occurrenceCounts[name]; i++ {
			args = append(args, value)
		}
	}

	return processed, args, true, nil
}

func parseStatement(queryData json.RawMessage) (string, error) {
	// Parse polymorphic MySQLStatement: {"type": "mysql", "sql": "..."}
	var mysqlStmt struct {
		Type string `json:"type"`
		SQL  string `json:"sql"`
	}
	if err := json.Unmarshal(queryData, &mysqlStmt); err != nil {
		return "", sqlconnector.ErrInvalidConfig.With(err)
	}

	if mysqlStmt.SQL == "" {
		return "", sqlconnector.ErrInvalidConfig.Detail("SQL statement is empty")
	}

	return mysqlStmt.SQL, nil
}

func extractAndReplace(stmt string) (string, []string, map[string]int) {
	ordered := param.ExtractParamNames(stmt)
	occurrenceCounts := make(map[string]int, len(ordered))
	result := stmt

	for _, name := range ordered {
		placeholder := fmt.Sprintf("{{%s}}", name)
		occurrenceCounts[name] = strings.Count(result, placeholder)
		result = strings.ReplaceAll(result, placeholder, "?")
	}

	return result, ordered, occurrenceCounts
}

func nativeInputColumns(params map[string]any, rows []map[string]any) ([]string, error) {
	if columnsValue, ok := params[sinkbinding.InternalInputColumns]; ok {
		switch typed := columnsValue.(type) {
		case []string:
			if len(typed) > 0 {
				return append([]string(nil), typed...), nil
			}
		case []any:
			columns := make([]string, 0, len(typed))
			for _, value := range typed {
				name, ok := value.(string)
				if !ok {
					return nil, xerror.Unexpected().Detailf(
						"parameter %q must contain only string column names",
						sinkbinding.InternalInputColumns,
					)
				}
				columns = append(columns, name)
			}

			if len(columns) > 0 {
				return columns, nil
			}
		default:
			return nil, xerror.Unexpected().Detailf(
				"parameter %q must contain runtime column names",
				sinkbinding.InternalInputColumns,
			)
		}
	}

	columnsMap := make(map[string]struct{})
	for _, row := range rows {
		for column := range row {
			columnsMap[column] = struct{}{}
		}
	}

	columns := make([]string, 0, len(columnsMap))
	for column := range columnsMap {
		columns = append(columns, column)
	}
	sort.Strings(columns)

	if len(columns) == 0 {
		return nil, xerror.Unexpected().Detail("native input relation requires at least one column")
	}

	return columns, nil
}

func buildMySQLInputRelationSQL(columns []string, columnTypes map[string]string) string {
	columnDefs := make([]string, 0, len(columns))
	for i, column := range columns {
		columnDefs = append(
			columnDefs,
			fmt.Sprintf(
				"%s %s PATH '$.c%d'",
				quoteMySQLIdentifier(column),
				columnTypes[column],
				i,
			),
		)
	}

	return fmt.Sprintf(
		"SELECT * FROM JSON_TABLE(CAST(? AS JSON), '$[*]' COLUMNS (%s)) AS src",
		strings.Join(columnDefs, ", "),
	)
}

func injectMySQLInputRelation(stmt, inputRelationSQL string) string {
	trimmed := strings.TrimSpace(stmt)
	upperTrimmed := strings.ToUpper(trimmed)

	if strings.HasPrefix(upperTrimmed, "INSERT ") {
		location := selectKeywordPattern.FindStringIndex(trimmed)
		if location != nil {
			return trimmed[:location[0]] + "WITH input AS (" + inputRelationSQL + ") " +
				trimmed[location[0]:]
		}
	}

	return prependInputRelationCTE(stmt, inputRelationSQL)
}

func prependInputRelationCTE(stmt, inputRelationSQL string) string {
	trimmed := strings.TrimSpace(stmt)
	withPrefixLocation := leadingWithPattern.FindStringSubmatchIndex(trimmed)
	if withPrefixLocation != nil {
		restStatement := strings.TrimSpace(trimmed[withPrefixLocation[1]:])
		if withPrefixLocation[2] != -1 {
			return "WITH RECURSIVE input AS (" + inputRelationSQL + "), " + restStatement
		}

		return "WITH input AS (" + inputRelationSQL + "), " + restStatement
	}

	return "WITH input AS (" + inputRelationSQL + ") " + stmt
}

func quoteMySQLIdentifier(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

func mysqlInputColumnType(column string, rows []map[string]any) string {
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case bool:
			return "BOOLEAN"
		case float32:
			if float64(int64(typed)) == float64(typed) {
				return mysqlInputColumnTypeBigInt
			}
			return "DOUBLE"
		case float64:
			if float64(int64(typed)) == typed {
				return mysqlInputColumnTypeBigInt
			}
			return "DOUBLE"
		case int, int8, int16, int32, int64:
			return mysqlInputColumnTypeBigInt
		case uint, uint8, uint16, uint32, uint64:
			return mysqlInputColumnTypeBigInt
		case decimal.Value, json.Number:
			return "DECIMAL"
		case []any, map[string]any, json.RawMessage:
			return "JSON"
		default:
			_ = typed
			return "TEXT"
		}
	}

	return "TEXT"
}

func mysqlInputColumnTypes(rows []map[string]any, columns []string) (map[string]string, error) {
	result := make(map[string]string, len(columns))
	for _, column := range columns {
		containsDecimal := false
		for _, row := range rows {
			switch row[column].(type) {
			case decimal.Value, json.Number:
				containsDecimal = true
			}
		}
		if !containsDecimal {
			result[column] = mysqlInputColumnType(column, rows)
			continue
		}

		maxIntegerDigits, maxScale := 1, 0
		for _, row := range rows {
			value, ok := row[column]
			if !ok || value == nil {
				continue
			}
			var text string
			switch typed := value.(type) {
			case decimal.Value:
				text = typed.String()
			case json.Number:
				text = typed.String()
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
				text = fmt.Sprint(typed)
			case float32, float64:
				return nil, xerror.InvalidInput(xerror.Query).Detailf("MySQL input column %q mixes decimal and floating-point values", column)
			default:
				return nil, xerror.InvalidInput(xerror.Query).Detailf("MySQL input column %q mixes decimal and non-numeric values", column)
			}
			integerDigits, scale, err := mysqlDecimalBounds(text)
			if err != nil {
				return nil, xerror.InvalidInput(xerror.Query).Detailf("MySQL input column %q has an invalid decimal", column)
			}
			maxIntegerDigits = max(maxIntegerDigits, integerDigits)
			maxScale = max(maxScale, scale)
		}
		precision := maxIntegerDigits + maxScale
		if precision > 65 || maxScale > 30 {
			return nil, xerror.InvalidInput(xerror.Query).Detailf("MySQL input column %q exceeds DECIMAL(65,30)", column)
		}
		result[column] = fmt.Sprintf("DECIMAL(%d,%d)", precision, maxScale)
	}
	return result, nil
}

func mysqlDecimalBounds(text string) (int, int, error) {
	if len(text) == 0 || len(text) > 128 {
		return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
	}
	position := 0
	if text[position] == '-' {
		position++
		if position == len(text) {
			return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
		}
	}
	integerStart := position
	switch {
	case text[position] == '0':
		position++
	case text[position] >= '1' && text[position] <= '9':
		for position < len(text) && text[position] >= '0' && text[position] <= '9' {
			position++
		}
	default:
		return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
	}
	integerDigits := position - integerStart
	if text[integerStart] == '0' && integerDigits > 1 {
		return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
	}
	fractionDigits := 0
	if position < len(text) && text[position] == '.' {
		position++
		fractionStart := position
		for position < len(text) && text[position] >= '0' && text[position] <= '9' {
			position++
		}
		fractionDigits = position - fractionStart
		if fractionDigits == 0 {
			return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
		}
	}
	mantissaEnd := position
	exponent := 0
	if position < len(text) && (text[position] == 'e' || text[position] == 'E') {
		position++
		exponentStart := position
		if position < len(text) && (text[position] == '+' || text[position] == '-') {
			position++
		}
		digitsStart := position
		for position < len(text) && text[position] >= '0' && text[position] <= '9' {
			position++
		}
		if position != len(text) || position == digitsStart || position-exponentStart > 4 {
			return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
		}
		parsed, err := strconv.Atoi(text[exponentStart:position])
		if err != nil || parsed < -308 || parsed > 308 {
			return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
		}
		exponent = parsed
	}
	if position != len(text) {
		return 0, 0, xerror.InvalidInput(xerror.Query).Detail("invalid decimal")
	}
	allZero := true
	for i := integerStart; i < mantissaEnd; i++ {
		if text[i] >= '1' && text[i] <= '9' {
			allZero = false
			break
		}
	}
	if allZero {
		return 1, 0, nil
	}
	integerDigits += exponent
	scale := fractionDigits - exponent
	if integerDigits < 1 {
		integerDigits = 1
	}
	if scale < 0 {
		scale = 0
	}
	return integerDigits, scale, nil
}

// IntrospectSchema retrieves the schema of the MySQL database.
func (c *Connector) IntrospectSchema(ctx context.Context) (*sqlconnector.Schema, error) {
	query := `
		SELECT TABLE_NAME
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_TYPE = 'BASE TABLE'
		AND TABLE_SCHEMA NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')
		ORDER BY TABLE_NAME
	`

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, sqlconnector.ErrSchemaIntrospection.With(err)
	}
	defer func() { _ = rows.Close() }()

	tables := make([]sqlconnector.Table, 0)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, sqlconnector.ErrSchemaIntrospection.With(err)
		}
		if err := schemaquota.Charge(ctx, 1, len(tableName)); err != nil {
			return nil, err
		}

		colQuery := `
			SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE()
			AND TABLE_NAME = ?
			ORDER BY ORDINAL_POSITION
		`
		colRows, err := c.db.QueryContext(ctx, colQuery, tableName)
		if err != nil {
			return nil, sqlconnector.ErrSchemaIntrospection.With(err)
		}
		defer func() { _ = colRows.Close() }()

		columns := make([]sqlconnector.Column, 0)
		for colRows.Next() {
			var colName, colType, isNullable string
			if err := colRows.Scan(&colName, &colType, &isNullable); err != nil {
				return nil, sqlconnector.ErrSchemaIntrospection.With(err)
			}
			if err := schemaquota.Charge(ctx, 0, len(colName)+len(colType)+len(isNullable)); err != nil {
				return nil, err
			}

			columns = append(columns, sqlconnector.Column{
				Name:     colName,
				Type:     colType,
				Nullable: isNullable == "YES",
			})
		}

		if err = colRows.Err(); err != nil {
			return nil, sqlconnector.ErrSchemaIntrospection.With(err)
		}

		tables = append(tables, sqlconnector.Table{
			Name:    tableName,
			Columns: columns,
		})
	}

	if err = rows.Err(); err != nil {
		return nil, sqlconnector.ErrSchemaIntrospection.With(err)
	}

	return &sqlconnector.Schema{Tables: tables}, nil
}

// GetCapabilities returns the capabilities supported by MySQL connector.
func (c *Connector) GetCapabilities() sqlconnector.Capabilities {
	return sqlconnector.Capabilities{
		SupportsSchemaIntrospection: true,
		SupportsPagination:          true,
		SupportsTransactions:        true,
	}
}

// Close closes the database connection pool.
func (c *Connector) Close() error {
	return sqlconnector.CloseDB(c.db)
}
