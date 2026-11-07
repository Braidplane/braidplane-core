package snowflake

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	snowflakeclient "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestExecuteStopsScanningAtResponseBudget(t *testing.T) {
	for _, value := range []driver.Value{strings.Repeat("x", 256), []byte(strings.Repeat("x", 256))} {
		fixture := &budgetRows{columns: []string{"payload"}, values: [][]driver.Value{{value}, {"unread"}}}
		connector := newScanningConnector(t, fixture)
		got, err := connector.Execute(t.Context(), json.RawMessage(`{"sql":"SELECT payload"}`), nil, types.ExecutionLimits{MaxResponseBytes: 64})
		require.NoError(t, err)
		require.ErrorIs(t, got.Error(), xerror.ExecutionResponseSizeExceeded())
		require.Equal(t, 1, fixture.nextCalls, "stop before reading the next provider row")
		require.True(t, fixture.closed)
		payload, ok := got.SQL()
		require.True(t, ok)
		require.Empty(t, payload.Rows)
	}
}

func TestExecuteEnforcesCumulativeBytesAndRowLimit(t *testing.T) {
	for _, limits := range []types.ExecutionLimits{{MaxResponseBytes: 60}, {MaxResponseBytes: 1024, MaxResultRows: 1}} {
		fixture := &budgetRows{columns: []string{"p"}, values: [][]driver.Value{{strings.Repeat("x", 20)}, {strings.Repeat("y", 20)}, {"unread"}}}
		connector := newScanningConnector(t, fixture)
		got, err := connector.Execute(t.Context(), json.RawMessage(`{"sql":"SELECT p"}`), nil, limits)
		require.NoError(t, err)
		if limits.MaxResultRows > 0 {
			require.ErrorIs(t, got.Error(), xerror.ExecutionRowLimitExceeded())
		} else {
			require.ErrorIs(t, got.Error(), xerror.ExecutionResponseSizeExceeded())
		}
		require.Equal(t, 2, fixture.nextCalls)
		require.True(t, fixture.closed)
	}
}

func TestExecutePreservesExactNormalizedBoundaryAndDuplicateNames(t *testing.T) {
	const name = "<escaped>\u2028"
	value := string([]byte{0xff, 0})
	want := sqlconnector.Result{ColumnNames: []string{name, name}, Rows: []map[string]any{{name: value}}, RowCount: 1}
	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	for _, limit := range []int64{int64(len(encoded) - 1), int64(len(encoded)), 0, -1} {
		fixture := &budgetRows{columns: []string{name, name}, values: [][]driver.Value{{strings.Repeat("ignored", 100), []byte(value)}}}
		connector := newScanningConnector(t, fixture)
		got, err := connector.Execute(t.Context(), json.RawMessage(`{"sql":"SELECT p"}`), nil, types.ExecutionLimits{MaxResponseBytes: limit})
		require.NoError(t, err)
		payload, ok := got.SQL()
		require.True(t, ok)
		if limit > 0 && limit < int64(len(encoded)) {
			require.ErrorIs(t, got.Error(), xerror.ExecutionResponseSizeExceeded())
			require.Empty(t, payload.Rows)
		} else {
			require.NoError(t, got.Error())
			require.Equal(t, want.Rows, payload.Rows)
			require.Equal(t, want.ColumnNames, payload.ColumnNames)
			require.Equal(t, want.RowCount, payload.RowCount)
		}
		require.True(t, fixture.closed)
	}
}

func TestExecuteOwnsReusedDriverBytesAndRejectsOversizedMetadata(t *testing.T) {
	buffer := []byte("owned")
	fixture := &budgetRows{columns: []string{"p"}, values: [][]driver.Value{{buffer}}, buffer: buffer}
	connector := newScanningConnector(t, fixture)
	got, err := connector.Execute(t.Context(), json.RawMessage(`{"sql":"SELECT p"}`), nil, types.ExecutionLimits{MaxResponseBytes: 1024})
	require.NoError(t, err)
	require.NoError(t, got.Error())
	payload, ok := got.SQL()
	require.True(t, ok)
	require.Equal(t, "owned", payload.Rows[0]["p"])
	require.Equal(t, []byte("#####"), buffer, "the driver reused its buffer on Close")
	fixture = &budgetRows{columns: []string{strings.Repeat("column", 32)}, values: [][]driver.Value{{"unread"}}}
	connector = newScanningConnector(t, fixture)
	got, err = connector.Execute(t.Context(), json.RawMessage(`{"sql":"SELECT p"}`), nil, types.ExecutionLimits{MaxResponseBytes: 64})
	require.NoError(t, err)
	require.ErrorIs(t, got.Error(), xerror.ExecutionResponseSizeExceeded())
	require.Zero(t, fixture.nextCalls)
	require.True(t, fixture.closed)
}

type scanningProvider struct {
	fakeProviderClient
	rows *stdsql.Rows
}

func (p scanningProvider) Execute(_ context.Context, _ string, _ []any, _ bool, scan snowflakeclient.RowScanner) (snowflakeclient.Result, error) {
	defer func() { _ = p.rows.Close() }()
	return scan(p.rows)
}

func newScanningConnector(t *testing.T, fixture *budgetRows) *Connector {
	t.Helper()
	db := stdsql.OpenDB(budgetDriverConnector{rows: fixture})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rows, err := db.QueryContext(t.Context(), "SELECT fixture")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = rows.Close()
		require.NoError(t, rows.Err())
	})
	return newTestConnector(scanningProvider{rows: rows})
}

type budgetDriverConnector struct {
	rows *budgetRows
}

type budgetDriver struct{}

type budgetConnection struct {
	rows *budgetRows
}

func (c budgetDriverConnector) Connect(context.Context) (driver.Conn, error) {
	return budgetConnection(c), nil
}
func (budgetDriverConnector) Driver() driver.Driver { return budgetDriver{} }
func (budgetDriver) Open(string) (driver.Conn, error) {
	return nil, xerror.Unexpected().Detail("fixture Open is unused")
}

func (budgetConnection) Prepare(string) (driver.Stmt, error) {
	return nil, xerror.Unexpected().Detail("fixture Prepare is unused")
}
func (budgetConnection) Close() error { return nil }
func (budgetConnection) Begin() (driver.Tx, error) {
	return nil, xerror.Unexpected().Detail("fixture Begin is unused")
}

func (c budgetConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.rows, nil
}

type budgetRows struct {
	columns   []string
	values    [][]driver.Value
	buffer    []byte
	nextCalls int
	closed    bool
}

func (r *budgetRows) Columns() []string { return r.columns }
func (r *budgetRows) Close() error {
	r.closed = true
	for i := range r.buffer {
		r.buffer[i] = '#'
	}
	return nil
}

func (r *budgetRows) Next(values []driver.Value) error {
	r.nextCalls++
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(values, r.values[0])
	r.values = r.values[1:]
	return nil
}
