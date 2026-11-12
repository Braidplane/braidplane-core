package sql

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const harnessDriverName = "braidplane-sql-harness-test"

var (
	harnessDriverOnce   sync.Once
	harnessDriverSeq    atomic.Uint64
	harnessDriverStates sync.Map
)

type harnessDriverState struct {
	pingErr         error
	pingHadDeadline bool
	queryRows       [][]driver.Value
	queryNextRows   [][]driver.Value
	queryColumns    []string
	queryErr        error
	queryCount      int
	execResult      driver.Result
	execErr         error
	execCount       int
	beginErr        error
	beginCount      int
	beginReadOnly   bool
	beginIsolation  driver.IsolationLevel
	commitCount     int
	rollbackErr     error
	rollbackCount   int
}

type harnessDriver struct{}

type harnessConn struct {
	state *harnessDriverState
}

type harnessRows struct {
	columns  []string
	values   [][]driver.Value
	index    int
	nextRows [][]driver.Value
}

type harnessExecResult struct {
	lastInsertID int64
	rowsAffected int64
}

type harnessTx struct {
	state *harnessDriverState
}

func TestApplyCredentialDefaultsMergesCredentialsAndTimeout(t *testing.T) {
	t.Parallel()

	user := "config-user"
	password := "config-password"
	timeoutSeconds := 0

	ApplyCredentialDefaults(&user, &password, &timeoutSeconds, Credentials{
		Username: "resolved-user",
		Password: "resolved-password",
	})

	require.Equal(t, "resolved-user", user)
	require.Equal(t, "resolved-password", password)
	require.Equal(t, DefaultConnectTimeoutSeconds, timeoutSeconds)
}

func TestNumericBytesToStringValuePreservesBinaryColumns(t *testing.T) {
	t.Parallel()

	numeric := NumericBytesToStringValue([]byte("1234567890.123456789"), "NUMERIC")
	require.Equal(t, "1234567890.123456789", numeric)
	bytea := NumericBytesToStringValue([]byte{0xff, 0x00}, "BYTEA")
	require.Equal(t, []byte{0xff, 0x00}, bytea)
}

func TestResolveConnectTimeoutSecondsKeepsExplicitValue(t *testing.T) {
	t.Parallel()

	require.Equal(t, 19, ResolveConnectTimeoutSeconds(19))
}

func TestOpenDBAppliesLifecycleDefaultsAndStartupMapper(t *testing.T) {
	t.Parallel()

	state := &harnessDriverState{}
	db, err := OpenDB(t.Context(), OpenDBConfig{
		DriverName: harnessDriverName,
		DSN:        registerHarnessState(t, state),
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	require.True(t, state.pingHadDeadline)
	require.Equal(t, DefaultMaxOpenConns, db.Stats().MaxOpenConnections)

	secretErr := xerror.Unexpected().Detail("provider error containing secret-password")
	_, err = OpenDB(t.Context(), OpenDBConfig{
		DriverName: harnessDriverName,
		DSN: registerHarnessState(t, &harnessDriverState{
			pingErr: secretErr,
		}),
		StartupErrorMapper: func(error) error {
			return xerror.InvalidCredentials().Detail("credentials rejected")
		},
	})

	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.NotContains(t, err.Error(), "secret-password")

	_, err = OpenDB(t.Context(), OpenDBConfig{
		DriverName: harnessDriverName,
		DSN: registerHarnessState(t, &harnessDriverState{
			pingErr: secretErr,
		}),
		StartupErrorMapper: func(error) error {
			return nil
		},
	})

	require.ErrorIs(t, err, xerror.Unexpected())
	require.NotContains(t, err.Error(), "secret-password")
}

func TestOpenDBAppliesExplicitPoolBounds(t *testing.T) {
	t.Parallel()

	db, err := OpenDB(t.Context(), OpenDBConfig{
		DriverName:      harnessDriverName,
		DSN:             registerHarnessState(t, &harnessDriverState{}),
		MaxOpenConns:    3,
		MaxIdleConns:    1,
		ConnMaxIdleTime: time.Minute,
		ConnMaxLifetime: 2 * time.Minute,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	require.Equal(t, 3, db.Stats().MaxOpenConnections)
}

func TestOpenDBRejectsInvalidPoolBoundsAndClosesDB(t *testing.T) {
	t.Parallel()

	_, err := OpenDB(t.Context(), OpenDBConfig{
		DriverName:   harnessDriverName,
		DSN:          registerHarnessState(t, &harnessDriverState{}),
		MaxOpenConns: 1,
		MaxIdleConns: 2,
	})

	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestTestDBConnectionUsesTimeoutAndMapsPingFailures(t *testing.T) {
	t.Parallel()

	successState := &harnessDriverState{}
	successDB := openHarnessDB(t, successState)
	require.NoError(t, TestDBConnection(context.Background(), successDB, 0))
	require.True(t, successState.pingHadDeadline)
	require.NoError(t, successDB.Close())

	failureState := &harnessDriverState{
		pingErr: xerror.Unexpected().Detail("ping failed"),
	}
	failureDB := openHarnessDB(t, failureState)
	defer func() { require.NoError(t, failureDB.Close()) }()

	err := TestDBConnection(context.Background(), failureDB, 2)
	require.ErrorIs(t, err, ErrConnectionFailed)
	require.True(t, failureState.pingHadDeadline)
}

func TestCloseDBClosesOpenPool(t *testing.T) {
	t.Parallel()

	db := openHarnessDB(t, &harnessDriverState{})

	require.NoError(t, CloseDB(db))
}

func TestStatementExecutorValidatesRequiredInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		executor func(*testing.T) StatementExecutor
		stmt     string
		want     string
	}{
		{
			name: "EmptyStatement",
			executor: func(t *testing.T) StatementExecutor {
				t.Helper()

				db := openHarnessDB(t, &harnessDriverState{})
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				return StatementExecutor{
					DB:               db,
					IsQueryStatement: func(string) bool { return true },
				}
			},
			want: "statement is empty",
		},
		{
			name: "MissingDB",
			executor: func(t *testing.T) StatementExecutor {
				t.Helper()

				return StatementExecutor{
					IsQueryStatement: func(string) bool { return true },
				}
			},
			stmt: "SELECT 1",
			want: "database pool is required",
		},
		{
			name: "MissingClassifier",
			executor: func(t *testing.T) StatementExecutor {
				t.Helper()

				db := openHarnessDB(t, &harnessDriverState{})
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				return StatementExecutor{DB: db}
			},
			stmt: "SELECT 1",
			want: "query statement classifier is required",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := testCase.executor(t).Execute(
				t.Context(),
				testCase.stmt,
				nil,
				types.ExecutionLimits{},
			)

			require.ErrorIs(t, err, ErrInvalidConfig)
			require.Contains(t, err.Error(), testCase.want)
			require.Equal(t, Result{}, result)
		})
	}
}

func TestStatementExecutorScansRowsWithDialectNormalizer(t *testing.T) {
	t.Parallel()

	db := openHarnessDB(t, &harnessDriverState{
		queryRows: [][]driver.Value{
			{int64(1), []byte("alice")},
			{int64(2), []byte("bob")},
		},
	})
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return true },
		NormalizeValue:   BytesToStringValue,
	}

	result, err := executor.Execute(
		t.Context(),
		"SELECT id, name FROM users",
		nil,
		types.ExecutionLimits{},
	)

	require.NoError(t, err)
	require.Equal(t, []string{"id", "name"}, result.ColumnNames)
	require.Equal(t, 2, result.RowCount)
	require.Equal(t, []map[string]any{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
	}, result.Rows)
}

func TestStatementExecutorReturnsRowLimitViolation(t *testing.T) {
	t.Parallel()

	db := openHarnessDB(t, &harnessDriverState{
		queryRows: [][]driver.Value{
			{int64(1), []byte("alice")},
			{int64(2), []byte("bob")},
		},
	})
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return true },
	}

	result, err := executor.Execute(
		t.Context(),
		"SELECT id, name FROM users",
		nil,
		types.ExecutionLimits{MaxResultRows: 1},
	)

	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
	require.Equal(t, Result{}, result)
}

func TestStatementExecutorNormalizesExecMetadata(t *testing.T) {
	t.Parallel()

	db := openHarnessDB(t, &harnessDriverState{
		execResult: harnessExecResult{lastInsertID: 42, rowsAffected: 3},
	})
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return false },
	}

	result, err := executor.Execute(
		t.Context(),
		"UPDATE users SET active = ?",
		[]any{true},
		types.ExecutionLimits{},
	)

	require.NoError(t, err)
	require.Empty(t, result.ColumnNames)
	require.Empty(t, result.Rows)
	require.Equal(t, 3, result.RowCount)
	require.NotNil(t, result.LastInsertID)
	require.Equal(t, int64(42), *result.LastInsertID)
}

func TestStatementExecutorUsesDriverNativeReadOnlyTransaction(t *testing.T) {
	t.Parallel()

	state := &harnessDriverState{
		queryRows: [][]driver.Value{{int64(1), []byte("alice")}},
	}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return true },
		NormalizeValue:   BytesToStringValue,
	}

	result, err := executor.Execute(
		t.Context(),
		"SELECT id, name FROM users",
		nil,
		types.ExecutionLimits{ReadOnly: true},
	)

	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.Equal(t, 1, state.beginCount)
	require.True(t, state.beginReadOnly)
	require.Zero(t, state.commitCount)
	require.Equal(t, 1, state.rollbackCount)
}

func TestStatementExecutorMapsReadOnlyTransactionBeginFailure(t *testing.T) {
	t.Parallel()

	state := &harnessDriverState{
		beginErr: fmt.Errorf("begin failed"),
	}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return true },
	}

	result, err := executor.Execute(
		t.Context(),
		"SELECT id, name FROM users",
		nil,
		types.ExecutionLimits{ReadOnly: true},
	)

	require.ErrorIs(t, err, ErrQueryExecutionFailed)
	require.Contains(t, err.Error(), "begin failed")
	require.Equal(t, Result{}, result)
	require.Equal(t, 1, state.beginCount)
	require.True(t, state.beginReadOnly)
	require.Zero(t, state.queryCount)
	require.Zero(t, state.execCount)
	require.Zero(t, state.commitCount)
	require.Zero(t, state.rollbackCount)
}

func TestStatementExecutorRollsBackReadOnlyTransactionAfterQueryFailure(t *testing.T) {
	t.Parallel()

	state := &harnessDriverState{
		queryErr: fmt.Errorf("query failed"),
	}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return true },
	}

	result, err := executor.Execute(
		t.Context(),
		"SELECT id, name FROM users",
		nil,
		types.ExecutionLimits{ReadOnly: true},
	)

	require.ErrorIs(t, err, ErrQueryExecutionFailed)
	require.Contains(t, err.Error(), "query failed")
	require.Equal(t, Result{}, result)
	require.Equal(t, 1, state.beginCount)
	require.True(t, state.beginReadOnly)
	require.Equal(t, 1, state.queryCount)
	require.Zero(t, state.execCount)
	require.Zero(t, state.commitCount)
	require.Equal(t, 1, state.rollbackCount)
}

func TestStatementExecutorRollsBackReadOnlyTransactionAfterExecFailure(t *testing.T) {
	t.Parallel()

	state := &harnessDriverState{
		execErr: fmt.Errorf("exec failed"),
	}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return false },
	}

	result, err := executor.Execute(
		t.Context(),
		"UPDATE users SET active = ?",
		[]any{true},
		types.ExecutionLimits{ReadOnly: true},
	)

	require.ErrorIs(t, err, ErrQueryExecutionFailed)
	require.Contains(t, err.Error(), "exec failed")
	require.Equal(t, Result{}, result)
	require.Equal(t, 1, state.beginCount)
	require.True(t, state.beginReadOnly)
	require.Zero(t, state.queryCount)
	require.Equal(t, 1, state.execCount)
	require.Zero(t, state.commitCount)
	require.Equal(t, 1, state.rollbackCount)
}

func TestStatementExecutorMapsExplicitReadOnlyTransactionRollbackFailure(t *testing.T) {
	t.Parallel()

	state := &harnessDriverState{
		queryRows:   [][]driver.Value{{int64(1), []byte("alice")}},
		rollbackErr: fmt.Errorf("rollback failed"),
	}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()

	executor := StatementExecutor{
		DB:               db,
		IsQueryStatement: func(string) bool { return true },
		NormalizeValue:   BytesToStringValue,
	}

	result, err := executor.Execute(
		t.Context(),
		"SELECT id, name FROM users",
		nil,
		types.ExecutionLimits{ReadOnly: true},
	)

	require.ErrorIs(t, err, ErrQueryExecutionFailed)
	require.Contains(t, err.Error(), "rollback failed")
	require.Equal(t, Result{}, result)
	require.Equal(t, 1, state.beginCount)
	require.True(t, state.beginReadOnly)
	require.Equal(t, 1, state.queryCount)
	require.Zero(t, state.execCount)
	require.Zero(t, state.commitCount)
	require.Equal(t, 1, state.rollbackCount)
}

func TestNewExecutionResultWrapsSQLPayload(t *testing.T) {
	t.Parallel()

	start := time.Now()

	execResult := NewExecutionResult("sqlserver", start, Result{
		ColumnNames: []string{"id"},
		Rows:        []map[string]any{{"id": int64(1)}},
		RowCount:    1,
	}, nil)

	require.Equal(t, "sqlserver", execResult.VendorType())
	require.NoError(t, execResult.Error())
	sqlResult, ok := execResult.SQL()
	require.True(t, ok)
	require.Equal(t, []string{"id"}, sqlResult.ColumnNames)
	require.Equal(t, []map[string]any{{"id": int64(1)}}, sqlResult.Rows)
	require.Equal(t, 1, sqlResult.RowCount)
}

func TestNewFailedExecutionResultPreservesNilSQLPayload(t *testing.T) {
	t.Parallel()

	execResult := NewFailedExecutionResult(
		"postgres",
		time.Now(),
		ErrQueryExecutionFailed.Detail("statement failed"),
	)

	require.ErrorIs(t, execResult.Error(), ErrQueryExecutionFailed)
	sqlResult, ok := execResult.SQL()
	require.True(t, ok)
	require.Nil(t, sqlResult)
}

func openHarnessDB(t *testing.T, state *harnessDriverState) *stdsql.DB {
	t.Helper()

	db, err := stdsql.Open(harnessDriverName, registerHarnessState(t, state))
	require.NoError(t, err)
	return db
}

func registerHarnessState(t *testing.T, state *harnessDriverState) string {
	t.Helper()

	harnessDriverOnce.Do(func() {
		stdsql.Register(harnessDriverName, harnessDriver{})
	})

	dsn := fmt.Sprintf("%s/%d", t.Name(), harnessDriverSeq.Add(1))
	harnessDriverStates.Store(dsn, state)
	t.Cleanup(func() {
		harnessDriverStates.Delete(dsn)
	})

	return dsn
}

func (harnessDriver) Open(name string) (driver.Conn, error) {
	raw, ok := harnessDriverStates.Load(name)
	if !ok {
		return nil, fmt.Errorf("missing harness state for %s", name)
	}

	state, ok := raw.(*harnessDriverState)
	if !ok {
		return nil, fmt.Errorf("invalid harness state for %s", name)
	}

	return &harnessConn{state: state}, nil
}

func (c *harnessConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("harness driver does not support prepared statements")
}

func (c *harnessConn) Close() error {
	return nil
}

func (c *harnessConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("harness driver does not support transactions")
}

func (c *harnessConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.state.beginCount++
	c.state.beginReadOnly = opts.ReadOnly
	c.state.beginIsolation = opts.Isolation
	if c.state.beginErr != nil {
		return nil, c.state.beginErr
	}
	return &harnessTx{state: c.state}, nil
}

func (c *harnessConn) Ping(ctx context.Context) error {
	if _, ok := ctx.Deadline(); ok {
		c.state.pingHadDeadline = true
	}
	return c.state.pingErr
}

func (c *harnessConn) QueryContext(
	_ context.Context,
	_ string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	c.state.queryCount++
	if c.state.queryErr != nil {
		return nil, c.state.queryErr
	}

	columns := c.state.queryColumns
	if columns == nil {
		columns = []string{"id", "name"}
	}
	return &harnessRows{
		columns:  columns,
		values:   c.state.queryRows,
		nextRows: c.state.queryNextRows,
	}, nil
}

func (c *harnessConn) ExecContext(
	_ context.Context,
	_ string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	c.state.execCount++
	if c.state.execErr != nil {
		return nil, c.state.execErr
	}
	if c.state.execResult != nil {
		return c.state.execResult, nil
	}
	return driver.RowsAffected(0), nil
}

func (r *harnessRows) Columns() []string {
	return r.columns
}

func (r *harnessRows) Close() error {
	return nil
}

func (r *harnessRows) HasNextResultSet() bool { return r.nextRows != nil }

func (r *harnessRows) NextResultSet() error {
	if r.nextRows == nil {
		return io.EOF
	}
	r.values, r.nextRows, r.index = r.nextRows, nil, 0
	return nil
}

func (r *harnessRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}

	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func (r harnessExecResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

func (r harnessExecResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

func (tx *harnessTx) Commit() error {
	tx.state.commitCount++
	return nil
}

func (tx *harnessTx) Rollback() error {
	tx.state.rollbackCount++
	return tx.state.rollbackErr
}

var (
	_ driver.Pinger         = (*harnessConn)(nil)
	_ driver.QueryerContext = (*harnessConn)(nil)
	_ driver.ExecerContext  = (*harnessConn)(nil)
	_ driver.ConnBeginTx    = (*harnessConn)(nil)
)

func TestStatementExecutorRejectsOversizedValueBeforeNormalization(t *testing.T) {
	t.Parallel()
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(readOnly), func(t *testing.T) {
			for _, value := range []driver.Value{bytes.Repeat([]byte("x"), 1<<20), strings.Repeat("x", 1<<20)} {
				db := openHarnessDB(t, &harnessDriverState{queryRows: [][]driver.Value{{int64(1), value}}})
				normalized := false
				executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }, NormalizeValue: func(v any, _ string) any {
					if _, ok := v.(int64); !ok {
						normalized = true
					}
					return v
				}}
				got, err := executor.Execute(t.Context(), "SELECT id, name", nil, types.ExecutionLimits{ReadOnly: readOnly, MaxResponseBytes: 128})
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Equal(t, Result{}, got)
				require.False(t, normalized, "oversized driver value reached normalization")
				require.NoError(t, db.Close())
			}
		})
	}
}

func TestStatementExecutorChargesCumulativeAndEncodedBytes(t *testing.T) {
	t.Parallel()
	for _, value := range []driver.Value{strings.Repeat("x", 40), strings.Repeat("<", 10), bytes.Repeat([]byte{0xff}, 40)} {
		db := openHarnessDB(t, &harnessDriverState{queryRows: [][]driver.Value{{int64(1), value}, {int64(2), value}, {int64(3), value}}})
		executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}
		got, err := executor.Execute(t.Context(), "SELECT id, name", nil, types.ExecutionLimits{MaxResponseBytes: 128})
		require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
		require.Equal(t, Result{}, got)
		require.NoError(t, db.Close())
	}
}

func TestStatementExecutorOwnsAcceptedBinaryValues(t *testing.T) {
	t.Parallel()
	source := []byte{0xff, 0x00}
	db := openHarnessDB(t, &harnessDriverState{queryRows: [][]driver.Value{{int64(1), source}}})
	defer func() { require.NoError(t, db.Close()) }()
	executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }, NormalizeValue: NumericBytesToStringValue}
	got, err := executor.Execute(t.Context(), "SELECT id, name", nil, types.ExecutionLimits{MaxResponseBytes: 1024})
	require.NoError(t, err)
	source[0] = 0
	require.Equal(t, []byte{0xff, 0x00}, got.Rows[0]["name"])
}

func TestStatementExecutorAcceptsManySmallRowsAtEnvelopeLimit(t *testing.T) {
	t.Parallel()
	rows := make([][]driver.Value, 1000)
	for i := range rows {
		rows[i] = []driver.Value{int64(0), int64(0)}
	}
	db := openHarnessDB(t, &harnessDriverState{queryRows: rows})
	defer func() { require.NoError(t, db.Close()) }()
	executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}
	expected, err := executor.Execute(t.Context(), "SELECT id, name", nil, types.ExecutionLimits{})
	require.NoError(t, err)
	encoded, err := json.Marshal(result.NewSQLResult("postgres", ResultData(expected), 0, nil))
	require.NoError(t, err)
	got, err := executor.Execute(t.Context(), "SELECT id, name", nil, types.ExecutionLimits{MaxResponseBytes: int64(len(encoded))})
	require.NoError(t, err)
	require.Equal(t, expected, got)
}

func TestStatementExecutorCountsDuplicateColumnsOncePerRow(t *testing.T) {
	t.Parallel()
	rows := make([][]driver.Value, 1000)
	for i := range rows {
		rows[i] = []driver.Value{int64(1), int64(2)}
	}
	db := openHarnessDB(t, &harnessDriverState{queryRows: rows, queryColumns: []string{"id", "id"}})
	defer func() { require.NoError(t, db.Close()) }()
	executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}
	expected, err := executor.Execute(t.Context(), "SELECT 1 AS id, 2 AS id", nil, types.ExecutionLimits{})
	require.NoError(t, err)
	require.Equal(t, int64(2), expected.Rows[0]["id"])
	encoded, err := json.Marshal(result.NewSQLResult("postgres", ResultData(expected), 0, nil))
	require.NoError(t, err)
	got, err := executor.Execute(t.Context(), "SELECT 1 AS id, 2 AS id", nil, types.ExecutionLimits{MaxResponseBytes: int64(len(encoded))})
	require.NoError(t, err)
	require.Equal(t, expected, got)
}
