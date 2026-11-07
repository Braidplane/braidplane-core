package postgres

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

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

var nativeInputRelationPattern = regexp.MustCompile(`(?i)\b(from|join)\s+input\b`)

const postgresInputColumnTypeBigInt = "bigint"

const (
	postgresErrCodeInvalidAuthorizationSpecification = "28000"
	postgresErrCodeInvalidPassword                   = "28P01"
)

// Config holds configuration for PostgreSQL connections.
type Config struct {
	// DialFunc is supplied only by trusted runtime wiring, never by a source payload.
	Authorize         func(context.Context) error `json:"-"`
	MaxConns          int                         `json:"-"`
	DialFunc          pgconn.DialFunc             `json:"-"`
	Host              string                      `json:"host"                      validate:"required,hostname|ip"`
	Port              int                         `json:"port"                      validate:"required,min=1,max=65535"`
	Database          string                      `json:"database"                  validate:"required"`
	SSLMode           string                      `json:"ssl_mode,omitempty"`
	User              string                      `json:"user"                      validate:"required"`
	Password          string                      `json:"password"                  validate:"required"`
	ConnectTimeout    int                         `json:"connect_timeout,omitempty"`
	AllowPrivateHosts bool                        `json:"allow_private_hosts,omitempty"`
}

// Credentials holds PostgreSQL authentication values.
type Credentials struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// Connector connects to PostgreSQL databases via pgx.
type Connector struct {
	authorize             func(context.Context) error
	db                    *stdsql.DB
	connectTimeoutSeconds int
}

// SQLPoolStats returns a safe point-in-time snapshot of the underlying pool.
func (c *Connector) SQLPoolStats() stdsql.DBStats {
	return c.db.Stats()
}

// New creates a new PostgreSQL connector with the provided configuration.
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
	if config.Password == "" {
		return nil, sqlconnector.ErrInvalidConfig.Detail("password is required")
	}

	config.ConnectTimeout = sqlconnector.ResolveConnectTimeoutSeconds(config.ConnectTimeout)
	if strings.TrimSpace(config.SSLMode) == "" {
		config.SSLMode = "verify-full"
	}

	if !transportsecurity.PlaintextAllowed(ctx) && config.SSLMode != "verify-full" {
		return nil, sqlconnector.ErrInvalidConfig.Detail("PostgreSQL requires ssl_mode=verify-full")
	}
	dsn := buildDSN(config)
	db, err := openPostgresDB(ctx, dsn, config)
	if err != nil {
		return nil, err
	}

	return &Connector{
		authorize:             config.Authorize,
		db:                    db,
		connectTimeoutSeconds: config.ConnectTimeout,
	}, nil
}

// openPostgresDB opens a database/sql pool with pgx-specific startup hooks.
func openPostgresDB(ctx context.Context, dsn string, config Config) (*stdsql.DB, error) {
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if config.DialFunc != nil {
		connConfig.DialFunc = config.DialFunc
	} else if !config.AllowPrivateHosts {
		connConfig.DialFunc = hostguard.DialPublicHost
	}

	db := stdlib.OpenDB(*connConfig)
	db, err = sqlconnector.ConfigureAndPingDB(ctx, db, sqlconnector.OpenDBConfig{
		MaxOpenConns:          config.MaxConns,
		MaxIdleConns:          config.MaxConns,
		DriverName:            "pgx",
		DSN:                   dsn,
		ConnectTimeoutSeconds: config.ConnectTimeout,
		StartupErrorMapper:    mapPostgresError,
	})
	if err != nil {
		return nil, err
	}
	if config.Authorize != nil {
		// Idle per-source pools must not consume the shared demo fixture's connection budget.
		db.SetMaxIdleConns(0)
	}
	return db, nil
}

// mapPostgresError classifies native causes before the worker discards provider text.
func mapPostgresError(err error) error {
	if err == nil {
		return nil
	}
	if isPostgresCredentialFailure(err) {
		return xerror.InvalidCredentials().
			WithPublicDetail("PostgreSQL rejected the configured username or password. Check the saved credentials and recheck access.").
			Detail("PostgreSQL credentials were rejected")
	}

	var pgErr *pgconn.PgError
	if xerror.As(err, &pgErr) {
		if strings.HasPrefix(pgErr.Code, "23") {
			// Execution history serializes errors; provider causes can include row values.
			return xerror.DestinationConstraintViolation().WithPublicDetail("Review the destination constraints and the values produced for this write. Verify destination state before repeating a write.")
		}
		switch pgErr.Code {
		case "42501":
			return xerror.Forbidden(xerror.DataSource).With(err)
		case "42703", "42P01", "3F000":
			return xerror.SourceSchemaReferenceInvalid().With(err)
		case "42601", "42702", "42P09", "42803", "42804", "42883", "42P10":
			return xerror.SourceQueryInvalid().With(err)
		}
	}
	var networkErr net.Error
	if xerror.Is(err, context.DeadlineExceeded) || (xerror.As(err, &networkErr) && networkErr.Timeout()) {
		return xerror.Timeout(xerror.DataSource).With(err)
	}
	if networkErr != nil {
		return xerror.ConnectionFailed(xerror.DataSource).With(err)
	}
	return xerror.Unexpected().With(err)
}

// isPostgresCredentialFailure reports whether a connection startup error is a
// PostgreSQL authentication failure.
func isPostgresCredentialFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !xerror.As(err, &pgErr) {
		return false
	}

	switch pgErr.Code {
	case postgresErrCodeInvalidAuthorizationSpecification, postgresErrCodeInvalidPassword:
		return true
	default:
		return false
	}
}

func buildDSN(config Config) string {
	dsnURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.User, config.Password),
		Host:   fmt.Sprintf("%s:%d", config.Host, config.Port),
		Path:   config.Database,
	}
	query := dsnURL.Query()
	query.Set("sslmode", config.SSLMode)
	query.Set("connect_timeout", fmt.Sprintf("%d", config.ConnectTimeout))
	dsnURL.RawQuery = query.Encode()

	return dsnURL.String()
}

// TestConnection verifies that a connection to PostgreSQL can be established.
func (c *Connector) TestConnection(ctx context.Context) error {
	if c.authorize != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := c.authorize(ctx); err != nil {
			return err
		}
	}
	return sqlconnector.TestDBConnection(ctx, c.db, c.connectTimeoutSeconds)
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
		return sqlconnector.NewFailedExecutionResult("postgres", start, err), nil
	}

	stmt, args, err := prepareStatement(queryData, normalized)
	if err != nil {
		return sqlconnector.NewFailedExecutionResult("postgres", start, err), nil
	}

	authoredStatement, err := parseStatement(queryData)
	if err != nil {
		return sqlconnector.NewFailedExecutionResult("postgres", start, err), nil
	}
	authoredStatement, _ = extractAndReplace(authoredStatement, 1)
	sqlResult, execErr := executeStatementWithLimits(c, ctx, stmt, args, execLimits, authoredStatement)

	return sqlconnector.NewExecutionResult("postgres", start, sqlResult, execErr), nil
}

// ExecuteStatement runs a parameterized SQL statement with ordered positional arguments.
// Placeholders in stmt must use PostgreSQL $N notation matching the order of args.
func (c *Connector) ExecuteStatement(
	ctx context.Context,
	stmt string,
	args []any,
) (sqlconnector.Result, error) {
	return executeStatementWithLimits(c, ctx, stmt, args, types.ExecutionLimits{}, stmt)
}

func executeStatementWithLimits(
	c *Connector,
	ctx context.Context,
	stmt string,
	args []any,
	limits types.ExecutionLimits,
	authoredStatement string,
) (sqlconnector.Result, error) {
	if c.authorize != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := c.authorize(ctx); err != nil {
			return sqlconnector.Result{}, err
		}
	}
	queryResult, err := (sqlconnector.StatementExecutor{
		DB: c.db,
		// The generated input CTE must not turn a non-returning write into a query.
		IsQueryStatement: func(string) bool { return isQueryStatement(authoredStatement) },
		NormalizeValue:   sqlconnector.NumericBytesToStringValue,
	}).Execute(ctx, stmt, args, limits)
	if err != nil {
		return queryResult, mapPostgresError(err)
	}
	return queryResult, nil
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
		return "", nil, sqlconnector.ErrQueryExecutionFailed.With(
			xerror.InvalidInput(xerror.Query).Detail(
				"postgres pipeline sink statement must select rows from input",
			),
		)
	}

	processed, paramOrder := extractAndReplace(stmt, 1)
	args := make([]any, 0, len(paramOrder))

	for _, name := range paramOrder {
		value, ok := params[name]
		if !ok {
			return "", nil, sqlconnector.ErrQueryExecutionFailed.With(
				xerror.Unexpected().Detailf("missing required parameter %q", name),
			)
		}

		args = append(args, value)
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
		return "", nil, false, sqlconnector.ErrQueryExecutionFailed.With(
			xerror.Unexpected().Detailf(
				"parameter %q must contain runtime rows",
				sinkbinding.InternalInputRows,
			),
		)
	}

	columns, err := nativeInputColumns(params, rows)
	if err != nil {
		return "", nil, false, sqlconnector.ErrQueryExecutionFailed.With(err)
	}

	rowsJSON, err := json.Marshal(rows)
	if err != nil {
		return "", nil, false, sqlconnector.ErrQueryExecutionFailed.With(
			xerror.Unexpected().With(err),
		)
	}

	inputRelationSQL := buildPostgresInputRelationSQL(columns, rows)
	stmtWithInput := prependInputRelationCTE(stmt, inputRelationSQL)
	processed, paramOrder := extractAndReplace(stmtWithInput, 2)
	args := make([]any, 0, len(paramOrder)+1)
	args = append(args, string(rowsJSON))

	for _, name := range paramOrder {
		value, ok := params[name]
		if !ok {
			return "", nil, false, sqlconnector.ErrQueryExecutionFailed.With(
				xerror.Unexpected().Detailf("missing required parameter %q", name),
			)
		}

		args = append(args, value)
	}

	return processed, args, true, nil
}

func parseStatement(queryData json.RawMessage) (string, error) {
	// Parse polymorphic PostgresStatement: {"type": "postgres", "sql": "..."}
	var pgStmt struct {
		Type string `json:"type"`
		SQL  string `json:"sql"`
	}
	if err := json.Unmarshal(queryData, &pgStmt); err != nil {
		return "", sqlconnector.ErrInvalidConfig.With(err)
	}

	if pgStmt.SQL == "" {
		return "", sqlconnector.ErrInvalidConfig.Detail("SQL statement is empty")
	}

	return pgStmt.SQL, nil
}

func extractAndReplace(stmt string, startIndex int) (string, []string) {
	ordered := param.ExtractParamNames(stmt)
	result := stmt

	for i, name := range ordered {
		placeholder := fmt.Sprintf("{{%s}}", name)
		replacement := fmt.Sprintf("$%d", startIndex+i)

		result = strings.ReplaceAll(result, placeholder, replacement)
	}

	return result, ordered
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

func buildPostgresInputRelationSQL(columns []string, rows []map[string]any) string {
	columnDefs := make([]string, 0, len(columns))
	for _, column := range columns {
		columnDefs = append(
			columnDefs,
			fmt.Sprintf(
				"%s %s",
				quotePostgresIdentifier(column),
				postgresInputColumnType(column, rows),
			),
		)
	}

	return fmt.Sprintf(
		"SELECT * FROM jsonb_to_recordset($1::jsonb) AS src(%s)",
		strings.Join(columnDefs, ", "),
	)
}

func prependInputRelationCTE(stmt, inputRelationSQL string) string {
	trimmed := strings.TrimSpace(stmt)
	upperTrimmed := strings.ToUpper(trimmed)

	const withKeyword = "WITH "
	const withRecursiveKeyword = "WITH RECURSIVE "

	if strings.HasPrefix(upperTrimmed, withRecursiveKeyword) {
		return "WITH RECURSIVE input AS (" + inputRelationSQL + "), " +
			strings.TrimSpace(trimmed[len(withRecursiveKeyword):])
	}

	if strings.HasPrefix(upperTrimmed, withKeyword) {
		return "WITH input AS (" + inputRelationSQL + "), " +
			strings.TrimSpace(trimmed[len(withKeyword):])
	}

	return "WITH input AS (" + inputRelationSQL + ") " + stmt
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func postgresInputColumnType(column string, rows []map[string]any) string {
	for _, row := range rows {
		switch typed := row[column].(type) {
		case decimal.Value:
			return "numeric"
		case json.Number:
			if _, err := typed.Int64(); err != nil {
				return "numeric"
			}
		}
	}
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}

		switch typed := value.(type) {
		case bool:
			return "boolean"
		case float32:
			if float64(int64(typed)) == float64(typed) {
				return postgresInputColumnTypeBigInt
			}
			return "double precision"
		case float64:
			if float64(int64(typed)) == typed {
				return postgresInputColumnTypeBigInt
			}
			return "double precision"
		case int, int8, int16, int32, int64:
			return postgresInputColumnTypeBigInt
		case uint, uint8, uint16, uint32, uint64:
			return postgresInputColumnTypeBigInt
		case decimal.Value, json.Number:
			return "numeric"
		case []any, map[string]any, json.RawMessage:
			return "jsonb"
		default:
			_ = typed
			return "text"
		}
	}

	return "text"
}

// IntrospectSchema retrieves the schema of the PostgreSQL database.
func (c *Connector) IntrospectSchema(ctx context.Context) (*sqlconnector.Schema, error) {
	if c.authorize != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := c.authorize(ctx); err != nil {
			return nil, err
		}
	}
	query := `
		SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
		ORDER BY table_schema, table_name
	`

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, sqlconnector.ErrSchemaIntrospection.With(err)
	}
	defer func() { _ = rows.Close() }()

	tables := make([]sqlconnector.Table, 0)
	for rows.Next() {
		var schema, tableName string
		if err := rows.Scan(&schema, &tableName); err != nil {
			return nil, sqlconnector.ErrSchemaIntrospection.With(err)
		}
		if err := schemaquota.Charge(ctx, 1, len(schema)+len(tableName)); err != nil {
			return nil, err
		}

		columnsQuery := `
			SELECT column_name, data_type, is_nullable, numeric_precision, numeric_scale
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2
			ORDER BY ordinal_position
		`
		colRows, err := c.db.QueryContext(ctx, columnsQuery, schema, tableName)
		if err != nil {
			return nil, sqlconnector.ErrSchemaIntrospection.With(err)
		}
		defer func() { _ = colRows.Close() }()

		columns := make([]sqlconnector.Column, 0)
		for colRows.Next() {
			var colName, dataType, isNullable string
			var precision, scale stdsql.NullInt64
			if err := colRows.Scan(&colName, &dataType, &isNullable, &precision, &scale); err != nil {
				return nil, sqlconnector.ErrSchemaIntrospection.With(err)
			}
			if err := schemaquota.Charge(ctx, 0, len(colName)+len(dataType)+len(isNullable)); err != nil {
				return nil, err
			}

			columns = append(columns, sqlconnector.Column{
				Name:     colName,
				Type:     postgresColumnType(dataType, precision, scale),
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

func postgresColumnType(dataType string, precision, scale stdsql.NullInt64) string {
	if strings.EqualFold(dataType, "numeric") && precision.Valid && scale.Valid {
		return fmt.Sprintf("numeric(%d,%d)", precision.Int64, scale.Int64)
	}
	return dataType
}

// GetCapabilities returns the capabilities supported by PostgreSQL connector.
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
