package mysql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func connectorPort() int {
	return depsports.MySQLConnectorPort()
}

func getTestConfig() mysql.Config {
	return mysql.Config{
		Host:              "127.0.0.1",
		Port:              connectorPort(),
		Database:          "braidplane",
		User:              "braidplane",
		Password:          "braidplanepass",
		ConnectTimeout:    5,
		AllowPrivateHosts: true,
	}
}

func executeSQL(
	ctx context.Context,
	conn *mysql.Connector,
	statement string,
	params map[string]any,
) (*result.SQLResult, error) {
	return executeSQLWithLimits(ctx, conn, statement, params, types.ExecutionLimits{})
}

func executeSQLWithLimits(
	ctx context.Context,
	conn *mysql.Connector,
	statement string,
	params map[string]any,
	limits types.ExecutionLimits,
) (*result.SQLResult, error) {
	// Create polymorphic MySQLStatement: {"type": "mysql", "sql": "..."}
	queryData, err := json.Marshal(map[string]string{"type": "mysql", "sql": statement})
	if err != nil {
		return nil, err
	}

	// Convert plain params to TypedParam map (use "text" for E2E tests)
	typedParams := make(map[string]types.TypedParam, len(params))
	for name, value := range params {
		typedParams[name] = types.TypedParam{
			Value:    value,
			DataType: "text", // E2E tests don't test normalization
		}
	}

	execResult, err := conn.Execute(ctx, queryData, typedParams, limits)
	if err != nil {
		return nil, err
	}

	// Check for execution error
	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	// Extract SQL result
	sqlResult, ok := execResult.SQL()
	if !ok {
		return nil, fmt.Errorf("expected SQL result, got: %s", execResult.Type())
	}

	return sqlResult, nil
}

func TestNew(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	tests := []struct {
		name      string
		config    mysql.Config
		wantErr   bool
		errString string
		errIs     error
	}{
		{name: "ValidConfig", config: getTestConfig(), wantErr: false},
		{
			name: "MissingHost",
			config: mysql.Config{
				Port:           connectorPort(),
				Database:       "braidplane",
				User:           "braidplane",
				Password:       "braidplanepass",
				ConnectTimeout: 5,
			},
			wantErr:   true,
			errString: "host is required",
		},
		{
			name: "MissingPort",
			config: mysql.Config{
				Host:              "localhost",
				Database:          "braidplane",
				User:              "braidplane",
				Password:          "braidplanepass",
				ConnectTimeout:    5,
				AllowPrivateHosts: true,
			},
			wantErr:   true,
			errString: "port is required",
		},
		{
			name: "MissingDatabase",
			config: mysql.Config{
				Host:              "localhost",
				Port:              connectorPort(),
				User:              "braidplane",
				Password:          "braidplanepass",
				ConnectTimeout:    5,
				AllowPrivateHosts: true,
			},
			wantErr:   true,
			errString: "database is required",
		},
		{
			name: "MissingUser",
			config: mysql.Config{
				Host:              "localhost",
				Port:              connectorPort(),
				Database:          "braidplane",
				Password:          "braidplanepass",
				ConnectTimeout:    5,
				AllowPrivateHosts: true,
			},
			wantErr:   true,
			errString: "user is required",
		},
		{
			name: "PasswordRequiredByServer",
			config: mysql.Config{
				Host:              "localhost",
				Port:              connectorPort(),
				Database:          "braidplane",
				User:              "braidplane",
				ConnectTimeout:    5,
				AllowPrivateHosts: true,
			},
			wantErr: true,
			errIs:   xerror.InvalidCredentials(),
		},
		{
			name: "InvalidHost",
			config: mysql.Config{
				Host:              "invalid-host-12345",
				Port:              connectorPort(),
				Database:          "braidplane",
				User:              "braidplane",
				Password:          "braidplanepass",
				ConnectTimeout:    1,
				AllowPrivateHosts: true,
			},
			wantErr: true,
			errIs:   xerror.ConnectionFailed(xerror.DataSource),
		},
		{
			name: "InvalidCredentials",
			config: mysql.Config{
				Host:              "localhost",
				Port:              connectorPort(),
				Database:          "braidplane",
				User:              "invalid_user",
				Password:          "invalid_password",
				ConnectTimeout:    1,
				AllowPrivateHosts: true,
			},
			wantErr: true,
			errIs:   xerror.InvalidCredentials(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), tt.config)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errIs != nil {
					require.ErrorIs(t, err, tt.errIs)
				} else {
					assert.Contains(t, err.Error(), tt.errString)
				}
				assert.Nil(t, conn)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, conn)
			defer func() {
				closeErr := conn.Close()
				assert.NoError(t, closeErr)
			}()
		})
	}
}

func TestConnector_TestConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx := context.Background()
	config := getTestConfig()

	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), config)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer func() {
		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	err = conn.TestConnection(ctx)
	assert.NoError(t, err)
}

func TestConnector_Execute(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx := context.Background()
	config := getTestConfig()

	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), config)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer func() {
		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	tableName := fmt.Sprintf("test_execute_%d", time.Now().UnixNano())
	createTableQuery := fmt.Sprintf(`
		CREATE TEMPORARY TABLE %s (
			id INT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(100) NOT NULL,
			value INT
		)
	`, tableName)

	_, err = executeSQL(ctx, conn, createTableQuery, nil)
	require.NoError(t, err)

	tests := []struct {
		name         string
		query        string
		assertErr    func(t *testing.T, err error)
		assertResult func(t *testing.T, result *result.SQLResult)
	}{
		{
			name:  "InsertSingleRow",
			query: fmt.Sprintf("INSERT INTO %s (name, value) VALUES ('test1', 100)", tableName),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 1, result.RowCount, "should affect 1 row")
				require.NotNil(t, result.LastInsertID, "MySQL INSERT should have insert ID")
				assert.Positive(t, *result.LastInsertID, "insert ID should be positive")
				assert.Empty(t, result.Rows, "exec should not return rows")
				assert.Empty(t, result.ColumnNames, "exec should not have columns")
			},
		},
		{
			name: "InsertMultipleRows",
			query: fmt.Sprintf(
				"INSERT INTO %s (name, value) VALUES ('test2', 200), ('test3', 300)",
				tableName,
			),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 2, result.RowCount, "should affect 2 rows")
				require.NotNil(t, result.LastInsertID, "MySQL INSERT should have insert ID")
				assert.Positive(t, *result.LastInsertID, "insert ID should be positive")
				assert.Empty(t, result.Rows, "exec should not return rows")
				assert.Empty(t, result.ColumnNames, "exec should not have columns")
			},
		},
		{
			name:  "SelectAllRows",
			query: fmt.Sprintf("SELECT * FROM %s ORDER BY id", tableName),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 3, result.RowCount, "should return 3 rows")
				assert.Len(t, result.Rows, 3, "rows slice should have 3 elements")
				assert.Nil(t, result.LastInsertID, "SELECT has no insert ID")
				assert.Equal(
					t,
					[]string{"id", "name", "value"},
					result.ColumnNames,
					"should have id, name, value columns",
				)
			},
		},
		{
			name:  "SelectWithFilter",
			query: fmt.Sprintf("SELECT * FROM %s WHERE value > 150", tableName),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 2, result.RowCount, "should return 2 rows with value > 150")
				assert.Len(t, result.Rows, 2, "rows slice should have 2 elements")
				assert.Nil(t, result.LastInsertID, "SELECT has no insert ID")
				assert.Equal(
					t,
					[]string{"id", "name", "value"},
					result.ColumnNames,
					"should have id, name, value columns",
				)
			},
		},
		{
			name:  "UpdateRows",
			query: fmt.Sprintf("UPDATE %s SET value = 999 WHERE name = 'test1'", tableName),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 1, result.RowCount, "should affect 1 row")
				assert.Nil(t, result.LastInsertID, "UPDATE has no insert ID")
				assert.Empty(t, result.Rows, "exec should not return rows")
				assert.Empty(t, result.ColumnNames, "exec should not have columns")
			},
		},
		{
			name:  "DeleteRows",
			query: fmt.Sprintf("DELETE FROM %s WHERE value = 200", tableName),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 1, result.RowCount, "should delete 1 row")
				assert.Nil(t, result.LastInsertID, "DELETE has no insert ID")
				assert.Empty(t, result.Rows, "exec should not return rows")
				assert.Empty(t, result.ColumnNames, "exec should not have columns")
			},
		},
		{
			name:  "CountRows",
			query: fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName),
			assertResult: func(t *testing.T, result *result.SQLResult) {
				assert.Equal(t, 1, result.RowCount, "COUNT query should return 1 row")
				assert.Len(t, result.Rows, 1, "rows slice should have 1 element")
				assert.Nil(t, result.LastInsertID, "SELECT has no insert ID")
				assert.Len(t, result.ColumnNames, 1, "COUNT query should have 1 column")
			},
		},
		{
			name:  "InvalidSQL",
			query: "SELECT * FROM non_existent_table",
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "doesn't exist")
			},
		},
		{
			name:  "SyntaxError",
			query: "SELCT * FRM invalid syntax",
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "syntax")
			},
		},
		{
			name:  "EmptyQuery",
			query: "",
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "statement is empty")
			},
		},
	}

	// Subtests share connection and temp table, must run sequentially
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, execErr := executeSQL(ctx, conn, tt.query, nil)

			if tt.assertErr != nil {
				tt.assertErr(t, execErr)
				return
			}

			require.NoError(t, execErr)
			if tt.assertResult != nil {
				tt.assertResult(t, result)
			}
		})
	}

	// Verify final state with comprehensive assertions
	result, err := executeSQL(
		ctx,
		conn,
		fmt.Sprintf("SELECT * FROM %s ORDER BY id", tableName),
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, 2, result.RowCount, "Should have 2 rows remaining (test1 updated, test3)")
	assert.Len(t, result.Rows, 2, "Rows slice length should match RowCount")
	assert.ElementsMatch(
		t,
		[]string{"id", "name", "value"},
		result.ColumnNames,
		"Column names should match table schema",
	)

	// Verify row contents
	require.Len(t, result.Rows, 2)

	// Check name field (handle potential encoding issues)
	name1 := result.Rows[0]["name"]
	if nameStr, ok := name1.(string); ok {
		assert.Equal(t, "test1", nameStr)
	} else {
		t.Fatalf("unexpected type for name: %T", name1)
	}

	// Values may be int64 or float64 depending on JSON marshaling
	value999 := result.Rows[0]["value"]
	switch v := value999.(type) {
	case int64:
		assert.Equal(t, int64(999), v, "test1 value should be updated to 999")
	case float64:
		assert.InEpsilon(t, 999.0, v, 0.000001, "test1 value should be updated to 999")
	default:
		t.Fatalf("unexpected type for value: %T", v)
	}

	name2 := result.Rows[1]["name"]
	if nameStr, ok := name2.(string); ok {
		assert.Equal(t, "test3", nameStr)
	} else {
		t.Fatalf("unexpected type for name: %T", name2)
	}

	value300 := result.Rows[1]["value"]
	switch v := value300.(type) {
	case int64:
		assert.Equal(t, int64(300), v)
	case float64:
		assert.InEpsilon(t, 300.0, v, 0.000001)
	default:
		t.Fatalf("unexpected type for value: %T", v)
	}
}

func TestConnector_ExecutePreservesDecimalInputRelation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping MySQL decimal relation E2E test in short mode")
	}
	t.Parallel()

	conn, err := mysql.New(transportsecurity.WithPlaintext(t.Context()), getTestConfig())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, conn.Close()) })
	refund, err := decimal.Parse("-2.675")
	require.NoError(t, err)
	rows := []map[string]any{
		{"id": int64(1), "amount": int64(2)},
		{"id": int64(2), "amount": json.Number("9007199254740993.10")},
		{"id": int64(3), "amount": refund},
		{"id": int64(4), "amount": json.Number("1e-3")},
		{"id": int64(5), "amount": nil},
	}
	params := map[string]any{
		sinkbinding.InternalInputRows:    rows,
		sinkbinding.InternalInputColumns: []string{"id", "amount"},
	}
	got, err := executeSQL(t.Context(), conn, "SELECT id, amount FROM input ORDER BY id", params)
	require.NoError(t, err)
	require.Len(t, got.Rows, len(rows))
	for i, expected := range []any{"2.000", "9007199254740993.100", "-2.675", "0.001", nil} {
		require.Equal(t, expected, got.Rows[i]["amount"])
	}
	require.Equal(t, json.Number("9007199254740993.10"), rows[1]["amount"])

	rows[0]["amount"] = 2.5
	_, err = executeSQL(t.Context(), conn, "SELECT amount FROM input", params)
	require.ErrorContains(t, err, "mixes decimal and floating-point values")
}

func TestConnector_ExecuteReadOnlyUsesNativeTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx := context.Background()
	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), getTestConfig())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	tableName := fmt.Sprintf("test_read_only_%d", time.Now().UnixNano())
	_, err = executeSQL(
		ctx,
		conn,
		fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY, value INT NOT NULL)", tableName),
		nil,
	)
	require.NoError(t, err)
	defer func() {
		_, cleanupErr := executeSQL(ctx, conn, "DROP TABLE "+tableName, nil)
		assert.NoError(t, cleanupErr)
	}()

	_, err = executeSQL(
		ctx,
		conn,
		fmt.Sprintf("INSERT INTO %s (id, value) VALUES (1, 10)", tableName),
		nil,
	)
	require.NoError(t, err)

	readResult, err := executeSQLWithLimits(
		ctx,
		conn,
		"SELECT value FROM "+tableName+" WHERE id = 1",
		nil,
		types.ExecutionLimits{ReadOnly: true},
	)
	require.NoError(t, err)
	require.EqualValues(t, 10, readResult.Rows[0]["value"])

	_, err = executeSQLWithLimits(
		ctx,
		conn,
		"UPDATE "+tableName+" SET value = 20 WHERE id = 1",
		nil,
		types.ExecutionLimits{ReadOnly: true},
	)
	var mysqlErr *mysqldriver.MySQLError
	require.ErrorAs(t, err, &mysqlErr)
	require.EqualValues(t, 1792, mysqlErr.Number)
	require.Equal(t, [5]byte{'2', '5', '0', '0', '6'}, mysqlErr.SQLState)

	verifyResult, err := executeSQL(
		ctx,
		conn,
		"SELECT value FROM "+tableName+" WHERE id = 1",
		nil,
	)
	require.NoError(t, err)
	require.EqualValues(t, 10, verifyResult.Rows[0]["value"])
}

func TestConnector_IntrospectSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx := context.Background()
	config := getTestConfig()

	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), config)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer func() {
		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	// Introspect schema (should contain seeded tables from init.sql)
	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.NotNil(t, schema)
	require.NotNil(t, schema.Tables)

	// Build table map for easier lookup
	tableMap := make(map[string]*sqlconnector.Table)
	for i := range schema.Tables {
		tableMap[schema.Tables[i].Name] = &schema.Tables[i]
	}

	// Verify expected tables from init.sql exist
	expectedTables := []string{"users", "products", "orders", "order_items"}
	for _, tableName := range expectedTables {
		table, exists := tableMap[tableName]
		require.True(t, exists, "Expected table %s to exist", tableName)
		require.NotEmpty(t, table.Columns, "Table %s should have columns", tableName)
	}

	// Verify users table structure
	usersTable := tableMap["users"]
	require.NotNil(t, usersTable)
	userColumns := make(map[string]string)
	for _, col := range usersTable.Columns {
		userColumns[col.Name] = col.Type
	}
	assert.Contains(t, userColumns, "id")
	assert.Contains(t, userColumns, "email")
	assert.Contains(t, userColumns, "full_name")
	assert.Contains(t, userColumns, "age")
	assert.Contains(t, userColumns, "is_active")
	assert.Contains(t, userColumns, "balance")
	assert.Contains(t, userColumns, "created_at")

	// Verify products table structure
	productsTable := tableMap["products"]
	require.NotNil(t, productsTable)
	productColumns := make(map[string]string)
	for _, col := range productsTable.Columns {
		productColumns[col.Name] = col.Type
	}
	assert.Contains(t, productColumns, "id")
	assert.Contains(t, productColumns, "sku")
	assert.Contains(t, productColumns, "name")
	assert.Contains(t, productColumns, "price")
	assert.Contains(t, productColumns, "stock_quantity")

	// Verify all tables have proper structure
	for _, table := range schema.Tables {
		assert.NotEmpty(t, table.Name)
		assert.NotNil(t, table.Columns)
		for _, col := range table.Columns {
			assert.NotEmpty(t, col.Name)
			assert.NotEmpty(t, col.Type)
		}
	}
}

func TestConnector_ExecuteReturnsRowLimitViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), getTestConfig())
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer func() {
		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	_, err = executeSQLWithLimits(
		ctx,
		conn,
		"SELECT 1 AS id UNION ALL SELECT 2 AS id",
		nil,
		types.ExecutionLimits{MaxResultRows: 1},
	)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

func TestConnector_Close(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx := context.Background()
	config := getTestConfig()

	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), config)
	require.NoError(t, err)
	require.NotNil(t, conn)

	err = conn.Close()
	require.NoError(t, err)

	err = conn.TestConnection(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestConnector_ConcurrentQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx := context.Background()
	config := getTestConfig()

	conn, err := mysql.New(transportsecurity.WithPlaintext(ctx), config)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer func() {
		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	tableName := fmt.Sprintf("test_concurrent_%d", time.Now().UnixNano())
	createTableQuery := fmt.Sprintf(`
		CREATE TABLE %s (
			id INT AUTO_INCREMENT PRIMARY KEY,
			value INT NOT NULL
		)
	`, tableName)

	_, err = executeSQL(ctx, conn, createTableQuery, nil)
	require.NoError(t, err)

	defer func() {
		dropQuery := fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)
		_, _ = executeSQL(ctx, conn, dropQuery, nil)
	}()

	const numConcurrent = 10
	var wg sync.WaitGroup
	errChan := make(chan error, numConcurrent)

	for i := range numConcurrent {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			query := fmt.Sprintf("INSERT INTO %s (value) VALUES (%d)", tableName, value)
			_, execErr := executeSQL(ctx, conn, query, nil)
			if execErr != nil {
				errChan <- execErr
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for execErr := range errChan {
		t.Errorf("Concurrent query failed: %v", execErr)
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)
	result, err := executeSQL(ctx, conn, countQuery, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.NotEmpty(t, result.Rows)

	count := 0
	for _, v := range result.Rows[0] {
		switch val := v.(type) {
		case float64:
			count = int(val)
		case int64:
			count = int(val)
		case int:
			count = val
		case []uint8:
			_, _ = fmt.Sscanf(string(val), "%d", &count)
		case string:
			_, _ = fmt.Sscanf(val, "%d", &count)
		default:
			if scanner, ok := val.(sql.Scanner); ok {
				var scanVal int64
				err := scanner.Scan(&scanVal)
				require.NoError(t, err)
				count = int(scanVal)
			}
		}
		break
	}

	assert.Equal(t, numConcurrent, count)
}
