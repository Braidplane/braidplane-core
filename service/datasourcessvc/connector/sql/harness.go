package sql

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const (
	// DefaultConnectTimeoutSeconds is the shared default for TCP SQL connector startup.
	DefaultConnectTimeoutSeconds = 5
	// DefaultMaxOpenConns is the shared conservative pool size for SQL connectors.
	DefaultMaxOpenConns = 5
	// DefaultMaxIdleConns is the shared idle pool size for SQL connectors.
	DefaultMaxIdleConns = 2
	// DefaultConnMaxIdleTime retires connections that remain unused.
	DefaultConnMaxIdleTime = 5 * time.Minute
	// DefaultConnMaxLifetime bounds how long a connection can retain credentials.
	DefaultConnMaxLifetime = 30 * time.Minute
)

// Credentials carries the username/password pair shared by TCP SQL connectors.
type Credentials struct {
	Username string
	Password string
}

// OpenDBConfig describes the driver-owned inputs needed for shared database lifecycle setup.
type OpenDBConfig struct {
	DriverName            string
	DSN                   string
	ConnectTimeoutSeconds int
	MaxOpenConns          int
	MaxIdleConns          int
	ConnMaxIdleTime       time.Duration
	ConnMaxLifetime       time.Duration
	StartupErrorMapper    func(error) error
}

// ValueNormalizer adapts database driver values into Braidplane's normalized SQL row shape.
type ValueNormalizer func(any, string) any

// StatementExecutor runs already-prepared SQL statements through a shared database/sql path.
type StatementExecutor struct {
	DB               *stdsql.DB
	IsQueryStatement func(string) bool
	NormalizeValue   ValueNormalizer
}

// ApplyCredentialDefaults merges resolved credentials and shared connection defaults into config fields.
func ApplyCredentialDefaults(
	user *string,
	password *string,
	connectTimeoutSeconds *int,
	credentials Credentials,
) {
	if user != nil {
		*user = credentials.Username
	}
	if password != nil {
		*password = credentials.Password
	}
	if connectTimeoutSeconds != nil {
		*connectTimeoutSeconds = ResolveConnectTimeoutSeconds(*connectTimeoutSeconds)
	}
}

// ResolveConnectTimeoutSeconds applies the shared SQL startup timeout only when unset.
func ResolveConnectTimeoutSeconds(configured int) int {
	if configured == 0 {
		return DefaultConnectTimeoutSeconds
	}
	return configured
}

// OpenDB opens, configures, and verifies a database/sql pool for a dialect-owned driver and DSN.
func OpenDB(ctx context.Context, config OpenDBConfig) (*stdsql.DB, error) {
	db, err := stdsql.Open(config.DriverName, config.DSN)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return ConfigureAndPingDB(ctx, db, config)
}

// ConfigureAndPingDB applies shared pool settings and verifies startup for an
// already-opened database/sql pool.
func ConfigureAndPingDB(
	ctx context.Context,
	db *stdsql.DB,
	config OpenDBConfig,
) (*stdsql.DB, error) {
	maxOpenConns := config.MaxOpenConns
	if maxOpenConns == 0 {
		maxOpenConns = DefaultMaxOpenConns
	}
	maxIdleConns := config.MaxIdleConns
	if maxIdleConns == 0 {
		maxIdleConns = DefaultMaxIdleConns
	}
	connMaxIdleTime := config.ConnMaxIdleTime
	if connMaxIdleTime == 0 {
		connMaxIdleTime = DefaultConnMaxIdleTime
	}
	connMaxLifetime := config.ConnMaxLifetime
	if connMaxLifetime == 0 {
		connMaxLifetime = DefaultConnMaxLifetime
	}
	if maxOpenConns < 0 ||
		maxIdleConns < 0 ||
		maxIdleConns > maxOpenConns ||
		connMaxIdleTime < 0 ||
		connMaxLifetime < 0 {
		_ = db.Close()
		return nil, ErrInvalidConfig.Detail("invalid SQL pool lifecycle settings")
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(connMaxIdleTime)
	db.SetConnMaxLifetime(connMaxLifetime)

	connectTimeout := time.Duration(
		ResolveConnectTimeoutSeconds(config.ConnectTimeoutSeconds),
	) * time.Second
	timeoutCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if err := db.PingContext(timeoutCtx); err != nil {
		logging.LogError(ctx, "SQL connector startup failed", err, config.DSN)
		_ = db.Close()
		if config.StartupErrorMapper != nil {
			if mappedErr := config.StartupErrorMapper(err); mappedErr != nil {
				return nil, mappedErr
			}
		}
		return nil, xerror.Unexpected().Detail("SQL connector startup failed")
	}

	return db, nil
}

// TestDBConnection verifies an existing SQL pool with the shared connector error mapping.
func TestDBConnection(ctx context.Context, db *stdsql.DB, connectTimeoutSeconds int) error {
	connectTimeout := time.Duration(
		ResolveConnectTimeoutSeconds(connectTimeoutSeconds),
	) * time.Second
	timeoutCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if err := db.PingContext(timeoutCtx); err != nil {
		return ErrConnectionFailed.With(err)
	}
	return nil
}

// CloseDB closes an existing SQL pool with the shared connector error mapping.
func CloseDB(db *stdsql.DB) error {
	if err := db.Close(); err != nil {
		return ErrConnectionFailed.With(err)
	}
	return nil
}

// Execute runs a statement as either QueryContext or ExecContext using the dialect classifier.
func (executor StatementExecutor) Execute(
	ctx context.Context,
	stmt string,
	args []any,
	limits types.ExecutionLimits,
) (Result, error) {
	if stmt == "" {
		return Result{}, ErrInvalidConfig.Detail("statement is empty")
	}
	if executor.DB == nil {
		return Result{}, ErrInvalidConfig.Detail("database pool is required")
	}
	if executor.IsQueryStatement == nil {
		return Result{}, ErrInvalidConfig.Detail("query statement classifier is required")
	}

	if limits.ReadOnly {
		return executor.executeReadOnly(ctx, stmt, args, limits)
	}
	if executor.IsQueryStatement(stmt) {
		return executor.Query(ctx, stmt, args, limits)
	}
	return executor.Exec(ctx, stmt, args)
}

// Query executes a row-returning statement and scans rows into the shared SQL result shape.
func (executor StatementExecutor) Query(
	ctx context.Context,
	stmt string,
	args []any,
	limits types.ExecutionLimits,
) (Result, error) {
	if executor.DB == nil {
		return Result{}, ErrInvalidConfig.Detail("database pool is required")
	}

	return executor.queryStatement(ctx, executor.DB, stmt, args, limits)
}

// Exec executes a non-row statement and normalizes affected rows and insert identity metadata.
func (executor StatementExecutor) Exec(ctx context.Context, stmt string, args []any) (Result, error) {
	if executor.DB == nil {
		return Result{}, ErrInvalidConfig.Detail("database pool is required")
	}

	return execStatement(ctx, executor.DB, stmt, args)
}

// ScanRows consumes database/sql rows into maps while enforcing Braidplane row and response byte limits.
func ScanRows(rows *stdsql.Rows, limits types.ExecutionLimits, normalize ValueNormalizer) (Result, error) {
	columnNames, err := rows.Columns()
	if err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}

	queryResult := Result{
		ColumnNames: columnNames,
		Rows:        make([]map[string]any, 0),
	}

	budget := result.NewResponseBudget(limits.MaxResponseBytes)
	if err := budget.Charge(4); err != nil {
		return Result{}, err
	}
	for i, name := range columnNames {
		if i > 0 {
			if err := budget.Charge(1); err != nil {
				return Result{}, err
			}
		}
		if err := budget.ChargeJSON(name); err != nil {
			return Result{}, err
		}
	}
	lastColumn := make(map[string]int, len(columnNames))
	for i, name := range columnNames {
		lastColumn[name] = i
	}

	for rows.Next() {
		if limits.MaxResultRows > 0 && len(queryResult.Rows) >= limits.MaxResultRows {
			return Result{}, xerror.ExecutionRowLimitExceeded().Detailf(
				"query returned more than %d rows",
				limits.MaxResultRows,
			)
		}

		row := make(map[string]any, len(lastColumn))
		if len(queryResult.Rows) > 0 {
			if err := budget.Charge(1); err != nil {
				return Result{}, err
			}
		}
		if err := budget.Charge(2); err != nil {
			return Result{}, err
		}
		valuePtrs := make([]any, len(columnNames))
		fields := 0
		for i, name := range columnNames {
			// SQL row maps preserve only the last occurrence of a column name.
			discard := lastColumn[name] != i
			valuePtrs[i] = &budgetScanner{budget: budget, normalize: normalize, databaseType: columnTypes[i].DatabaseTypeName(), row: row, name: name, discard: discard}
			if discard {
				continue
			}
			if err := budget.ChargeJSON(name); err != nil {
				return Result{}, err
			}
			if err := budget.Charge(1); err != nil {
				return Result{}, err
			}
			if fields > 0 {
				if err := budget.Charge(1); err != nil {
					return Result{}, err
				}
			}
			fields++
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			if xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
				return Result{}, xerror.ExecutionResponseSizeExceeded().With(err)
			}
			return Result{}, ErrQueryExecutionFailed.With(err)
		}

		queryResult.Rows = append(queryResult.Rows, row)
	}

	if err := rows.Err(); err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}
	queryResult.RowCount = len(queryResult.Rows)

	return queryResult, nil
}

// ResultFromExec maps database/sql exec metadata into the shared SQL result shape.
func ResultFromExec(execResult stdsql.Result) Result {
	queryResult := Result{
		ColumnNames: []string{},
		Rows:        []map[string]any{},
	}

	if rowsAffected, err := execResult.RowsAffected(); err == nil {
		queryResult.RowCount = int(rowsAffected)
	}
	if lastID, err := execResult.LastInsertId(); err == nil && lastID > 0 {
		queryResult.LastInsertID = &lastID
	}

	return queryResult
}

// BytesToStringValue normalizes byte slices returned by some SQL drivers into strings.
func BytesToStringValue(value any, _ string) any {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return value
}

// NumericBytesToStringValue preserves text-encoded PostgreSQL numerics without
// converting bytea values to invalid UTF-8 strings.
func NumericBytesToStringValue(value any, databaseType string) any {
	if strings.EqualFold(databaseType, "NUMERIC") || strings.EqualFold(databaseType, "DECIMAL") {
		return BytesToStringValue(value, databaseType)
	}
	return value
}

// ResultData converts the harness result shape into the stored connector SQL payload.
func ResultData(queryResult Result) *result.SQLResult {
	return &result.SQLResult{
		Rows:         queryResult.Rows,
		ColumnNames:  queryResult.ColumnNames,
		RowCount:     queryResult.RowCount,
		LastInsertID: queryResult.LastInsertID,
	}
}

// EmptyResultData returns a non-nil empty SQL payload for connectors that retain payloads on errors.
func EmptyResultData() *result.SQLResult {
	return &result.SQLResult{
		Rows:        []map[string]any{},
		ColumnNames: []string{},
		RowCount:    0,
	}
}

// NewExecutionResult wraps a harness SQL result with shared duration and vendor metadata.
func NewExecutionResult(
	vendorType string,
	start time.Time,
	queryResult Result,
	err error,
) *result.ExecutionResult {
	return NewExecutionResultData(vendorType, start, ResultData(queryResult), err)
}

// NewExecutionResultData wraps an already-normalized SQL payload with shared duration metadata.
func NewExecutionResultData(
	vendorType string,
	start time.Time,
	sqlResult *result.SQLResult,
	err error,
) *result.ExecutionResult {
	return result.NewSQLResult(vendorType, sqlResult, time.Since(start).Milliseconds(), err)
}

// NewFailedExecutionResult wraps an execution failure without inventing a result payload.
func NewFailedExecutionResult(
	vendorType string,
	start time.Time,
	err error,
) *result.ExecutionResult {
	return NewExecutionResultData(vendorType, start, nil, err)
}

type statementRunner interface {
	QueryContext(context.Context, string, ...any) (*stdsql.Rows, error)
	ExecContext(context.Context, string, ...any) (stdsql.Result, error)
}

// executeReadOnly runs one statement inside a driver-native read-only transaction.
func (executor StatementExecutor) executeReadOnly(
	ctx context.Context,
	stmt string,
	args []any,
	limits types.ExecutionLimits,
) (Result, error) {
	tx, err := executor.DB.BeginTx(ctx, &stdsql.TxOptions{ReadOnly: true})
	if err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}
	defer func() { _ = tx.Rollback() }()

	var queryResult Result
	if executor.IsQueryStatement(stmt) {
		queryResult, err = executor.queryStatement(ctx, tx, stmt, args, limits)
	} else {
		queryResult, err = execStatement(ctx, tx, stmt, args)
	}
	if err != nil {
		return Result{}, err
	}
	if err := tx.Rollback(); err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}
	return queryResult, nil
}

func (executor StatementExecutor) queryStatement(
	ctx context.Context,
	runner statementRunner,
	stmt string,
	args []any,
	limits types.ExecutionLimits,
) (Result, error) {
	rows, err := runner.QueryContext(ctx, stmt, args...)
	if err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}
	defer func() { _ = rows.Close() }()

	return ScanRows(rows, limits, executor.NormalizeValue)
}

func execStatement(
	ctx context.Context,
	runner statementRunner,
	stmt string,
	args []any,
) (Result, error) {
	execResult, err := runner.ExecContext(ctx, stmt, args...)
	if err != nil {
		return Result{}, ErrQueryExecutionFailed.With(err)
	}

	return ResultFromExec(execResult), nil
}

// A Scanner receives driver-owned bytes before database/sql clones them for *any.
type budgetScanner struct {
	budget       *result.ResponseBudget
	normalize    ValueNormalizer
	databaseType string
	row          map[string]any
	name         string
	discard      bool
}

func (s *budgetScanner) Scan(value any) error {
	if s.discard {
		return nil
	}
	// Reject before normalization, JSON encoding, or retaining driver-owned bytes.
	var size int
	switch v := value.(type) {
	case string:
		size = len(v)
	case []byte:
		size = len(v)
	}
	if err := s.budget.Check(int64(size)); err != nil {
		return err
	}
	if s.normalize != nil {
		value = s.normalize(value, s.databaseType)
	}
	if err := s.budget.ChargeJSON(value); err != nil {
		return err
	}
	if v, ok := value.([]byte); ok {
		value = bytes.Clone(v)
	}
	s.row[s.name] = value
	return nil
}
