package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestPostgresRetainedSQLStream(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable database fixture")
	}
	store := filestreamfixture.New()
	ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), store)
	connector, err := postgres.New(ctx, getTestConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	limits := types.ExecutionLimits{MaxResultRows: 3, MaxResponseBytes: 1024, MaxFileBytes: 4096}
	execution, err := connector.Execute(ctx, json.RawMessage(`{"type":"postgres","format":"file","sql":"SELECT i::bigint AS id, 123456789.123456789::numeric AS decimal, NULL::text AS nullable, decode('00ff','hex') AS binary FROM generate_series(1,3) i ORDER BY i"}`), nil, limits)
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	output, ok := execution.SQL()
	require.True(t, ok)
	require.Empty(t, output.Rows)
	require.Equal(t, 3, output.RowCount)
	require.NotNil(t, output.Reference)
	body, err := store.Open(ctx, *output.Reference, limits.MaxFileBytes)
	require.NoError(t, err)
	defer func() { require.NoError(t, body.Close()) }()
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	var envelope struct {
		Rows     []map[string]any `json:"rows"`
		RowCount int              `json:"row_count"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&envelope))
	require.Len(t, envelope.Rows, 3)
	require.Equal(t, 3, envelope.RowCount)
	for i, row := range envelope.Rows {
		require.Equal(t, json.Number(strconv.Itoa(i+1)), row["id"])
		require.Equal(t, "123456789.123456789", row["decimal"])
		require.Nil(t, row["nullable"])
		require.Equal(t, "AP8=", row["binary"])
	}
	for _, statement := range []string{
		`{"type":"postgres","format":"file","sql":"SELECT i FROM generate_series(1,4) i"}`,
		`{"type":"postgres","format":"file","sql":"CREATE TEMP TABLE forbidden_stream_write(id int)"}`,
		`{"type":"postgres","format":"file","sql":"SELECT 1 AS duplicate, 2 AS duplicate"}`,
	} {
		execution, err := connector.Execute(ctx, json.RawMessage(statement), nil, limits)
		require.NoError(t, err)
		require.Error(t, execution.Error())
		output, _ := execution.SQL()
		require.Nil(t, output)
	}
}

type snapshotGateStore struct {
	filestream.Store
	entered chan struct{}
	release chan struct{}
}

func (s snapshotGateStore) Retain(ctx context.Context, maxBytes int64, producer filestream.Producer) (*filepayload.Reference, *filestream.Metadata, error) {
	return s.Store.Retain(ctx, maxBytes, func(dst io.Writer) (*filestream.Metadata, error) {
		return producer(&snapshotGateWriter{ctx: ctx, dst: dst, entered: s.entered, release: s.release})
	})
}

type snapshotGateWriter struct {
	ctx              context.Context
	dst              io.Writer
	entered, release chan struct{}
	once             sync.Once
}

func (w *snapshotGateWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		select {
		case <-w.release:
		case <-w.ctx.Done():
		}
	})
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.dst.Write(data)
}

func TestPostgresStreamSnapshotAndCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable database fixture")
	}
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 15*time.Second)
	defer cancel()
	connector, err := postgres.New(ctx, getTestConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	_, err = connector.ExecuteStatement(ctx, "CREATE TABLE fixture_sql_stream_snapshot(id integer PRIMARY KEY, value text); INSERT INTO fixture_sql_stream_snapshot VALUES (1,'original')", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		_, err := connector.ExecuteStatement(cleanupCtx, "DROP TABLE fixture_sql_stream_snapshot", nil)
		require.NoError(t, err)
	})
	store := filestreamfixture.New()
	gate := snapshotGateStore{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	limits := types.ExecutionLimits{MaxResultRows: 2, MaxResponseBytes: 4096, MaxFileBytes: 4096}
	type executionOutcome struct {
		result *result.ExecutionResult
		err    error
	}
	completed := make(chan executionOutcome, 1)
	go func() {
		result, err := connector.Execute(filestream.WithStore(ctx, gate), json.RawMessage(`{"type":"postgres","format":"file","sql":"SELECT id,value FROM fixture_sql_stream_snapshot ORDER BY id"}`), nil, limits)
		completed <- executionOutcome{result, err}
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("source did not reach the serialization gate")
	}
	_, err = connector.ExecuteStatement(ctx, "UPDATE fixture_sql_stream_snapshot SET value='changed'", nil)
	require.NoError(t, err)
	close(gate.release)
	outcome := <-completed
	require.NoError(t, outcome.err)
	require.NoError(t, outcome.result.Error())
	output, _ := outcome.result.SQL()
	body, err := store.Open(ctx, *output.Reference, limits.MaxFileBytes)
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Contains(t, string(raw), `"value":"original"`)
	require.NotContains(t, string(raw), "changed")

	baseCtx := filestream.WithStore(ctx, store)
	execution, err := connector.Execute(baseCtx, json.RawMessage(`{"type":"postgres","format":"file","sql":"WITH changed AS (UPDATE fixture_sql_stream_snapshot SET value='forbidden' RETURNING id) SELECT id FROM changed"}`), nil, limits)
	require.NoError(t, err)
	require.Error(t, execution.Error(), "database must enforce read-only SQL, including write CTEs")
	current, err := connector.ExecuteStatement(ctx, "SELECT value FROM fixture_sql_stream_snapshot", nil)
	require.NoError(t, err)
	require.Equal(t, "changed", current.Rows[0]["value"])
	started := time.Now()
	execution, err = connector.Execute(baseCtx, json.RawMessage(`{"type":"postgres","format":"file","sql":"SELECT i AS id, repeat('x',1024) AS payload, CASE WHEN i=2000 THEN pg_sleep(5) END AS paused FROM generate_series(1,4000) i"}`), nil, limits)
	require.NoError(t, err)
	require.Error(t, execution.Error())
	require.Less(t, time.Since(started), 2*time.Second, "a row-limit refusal must cancel the remaining SQL work rather than drain it")

	cancelCtx, cancelQuery := context.WithTimeout(baseCtx, 50*time.Millisecond)
	defer cancelQuery()
	execution, err = connector.Execute(cancelCtx, json.RawMessage(`{"type":"postgres","format":"file","sql":"SELECT pg_sleep(5) AS pause"}`), nil, limits)
	require.NoError(t, err)
	require.Error(t, execution.Error())
	require.Eventually(t, func() bool { return connector.SQLPoolStats().InUse == 0 }, time.Second, 10*time.Millisecond)
	timeLimits := limits
	timeLimits.ConnectorTimeout = 50 * time.Millisecond
	execution, err = connector.Execute(baseCtx, json.RawMessage(`{"type":"postgres","format":"file","sql":"SELECT pg_sleep(5) AS pause"}`), nil, timeLimits)
	require.NoError(t, err)
	require.Error(t, execution.Error(), "the configured connector timeout must bound SQL scanning")
	require.Eventually(t, func() bool { return connector.SQLPoolStats().InUse == 0 }, time.Second, 10*time.Millisecond)
	execution, err = connector.Execute(baseCtx, json.RawMessage(`{"type":"postgres","format":"file","sql":"SELECT 1 AS id"}`), nil, limits)
	require.NoError(t, err)
	require.NoError(t, execution.Error(), "cancelled SQL must release its connection for the next execution")
}
