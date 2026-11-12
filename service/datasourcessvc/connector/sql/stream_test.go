package sql

import (
	"bytes"
	"context"
	"crypto/sha256"
	stdsql "database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func streamTestLimits() types.ExecutionLimits {
	return types.ExecutionLimits{MaxResultRows: 10, MaxFileBytes: 4096, MaxResponseBytes: 1024}
}

func TestQueryToPreservesValuesAndSnapshot(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123, time.UTC)
	state := &harnessDriverState{
		queryColumns: []string{"id", "null", "bool", "binary", "timestamp", "decimal"},
		queryRows:    [][]driver.Value{{int64(9007199254740993), nil, true, []byte{0, 255}, stamp, "123456789.123456789"}},
	}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()
	var dst bytes.Buffer
	output, err := (StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}).QueryTo(t.Context(), "SELECT values", nil, streamTestLimits(), &dst)
	require.NoError(t, err)
	require.Equal(t, 1, output.RowCount)
	require.True(t, state.beginReadOnly)
	require.Equal(t, driver.IsolationLevel(stdsql.LevelRepeatableRead), state.beginIsolation)
	require.Equal(t, 1, state.rollbackCount)
	require.Zero(t, state.commitCount)
	digest := sha256.Sum256(dst.Bytes())
	require.Equal(t, hex.EncodeToString(digest[:]), output.Metadata.SHA256)
	require.EqualValues(t, dst.Len(), output.Metadata.SizeBytes)
	var envelope struct {
		ColumnNames []string         `json:"column_names"`
		Rows        []map[string]any `json:"rows"`
		RowCount    int              `json:"row_count"`
	}
	decoder := json.NewDecoder(&dst)
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&envelope))
	require.Equal(t, state.queryColumns, envelope.ColumnNames)
	require.Equal(t, 1, envelope.RowCount)
	require.Equal(t, map[string]any{"id": json.Number("9007199254740993"), "null": nil, "bool": true, "binary": "AP8=", "timestamp": stamp.Format(time.RFC3339Nano), "decimal": "123456789.123456789"}, envelope.Rows[0])
}

func TestQueryToRowAndByteBoundaries(t *testing.T) {
	t.Parallel()
	state := &harnessDriverState{queryRows: [][]driver.Value{{int64(1), "a"}, {int64(2), "b"}}}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()
	executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}
	var baseline bytes.Buffer
	_, err := executor.QueryTo(t.Context(), "SELECT values", nil, streamTestLimits(), &baseline)
	require.NoError(t, err)
	for _, limit := range []int{1, 2, 3} {
		limits := streamTestLimits()
		limits.MaxResultRows = limit
		output, err := executor.QueryTo(t.Context(), "SELECT values", nil, limits, io.Discard)
		if limit < 2 {
			require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
			require.Nil(t, output.Metadata)
		} else {
			require.NoError(t, err)
		}
	}
	for _, allowance := range []int{baseline.Len() - 1, baseline.Len(), baseline.Len() + 1} {
		limits := streamTestLimits()
		limits.MaxFileBytes = int64(allowance)
		var dst bytes.Buffer
		output, err := executor.QueryTo(t.Context(), "SELECT values", nil, limits, &dst)
		require.LessOrEqual(t, dst.Len(), allowance)
		if allowance < baseline.Len() {
			require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
			require.Nil(t, output.Metadata)
		} else {
			require.NoError(t, err)
			require.Equal(t, baseline.String(), dst.String())
		}
	}
}

func TestQueryToRefusesLossyColumnsAndOversizedRows(t *testing.T) {
	t.Parallel()
	for _, columns := range [][]string{{"id", "id"}, {"id", "name"}} {
		state := &harnessDriverState{queryColumns: columns, queryRows: [][]driver.Value{{int64(1), string(bytes.Repeat([]byte("x"), 2048))}}}
		db := openHarnessDB(t, state)
		executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}
		output, err := executor.QueryTo(t.Context(), "SELECT values", nil, streamTestLimits(), io.Discard)
		require.Error(t, err)
		require.Nil(t, output.Metadata)
		require.NoError(t, db.Close())
	}
}

type streamWriterFunc func([]byte) (int, error)

func TestQueryToRefusesMultipleResultsAndShortWrites(t *testing.T) {
	t.Parallel()
	state := &harnessDriverState{queryRows: [][]driver.Value{{int64(1), "a"}}, queryNextRows: [][]driver.Value{{int64(2), "b"}}}
	db := openHarnessDB(t, state)
	defer func() { require.NoError(t, db.Close()) }()
	executor := StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}
	output, err := executor.QueryTo(t.Context(), "SELECT values", nil, streamTestLimits(), io.Discard)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Nil(t, output.Metadata)
	state.queryNextRows = nil
	output, err = executor.QueryTo(t.Context(), "SELECT values", nil, streamTestLimits(), streamWriterFunc(func(data []byte) (int, error) { return len(data) - 1, nil }))
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.Nil(t, output.Metadata)
	state.queryRows = nil
	var dst bytes.Buffer
	output, err = executor.QueryTo(t.Context(), "SELECT values", nil, streamTestLimits(), &dst)
	require.NoError(t, err)
	require.Zero(t, output.RowCount)
	require.JSONEq(t, `{"column_names":["id","name"],"rows":[],"row_count":0}`, dst.String())
}

func (f streamWriterFunc) Write(data []byte) (int, error) { return f(data) }

func TestQueryToInvalidatesOutputOnWriteFailureOrCancellation(t *testing.T) {
	t.Parallel()
	for _, cancelWrite := range []bool{false, true} {
		state := &harnessDriverState{queryRows: [][]driver.Value{{int64(1), "a"}}}
		db := openHarnessDB(t, state)
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		writer := streamWriterFunc(func(data []byte) (int, error) {
			calls++
			if cancelWrite {
				cancel()
				return len(data), nil
			}
			return 0, io.ErrClosedPipe
		})
		output, err := (StatementExecutor{DB: db, IsQueryStatement: func(string) bool { return true }}).QueryTo(ctx, "SELECT values", nil, streamTestLimits(), writer)
		require.Error(t, err)
		require.Nil(t, output.Metadata)
		require.Equal(t, 1, calls)
		require.True(t, xerror.Is(err, context.Canceled) || xerror.Is(err, io.ErrClosedPipe))
		cancel()
		require.NoError(t, db.Close())
	}
}
