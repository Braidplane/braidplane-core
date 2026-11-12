package sql

import (
	"context"
	"crypto/sha256"
	stdsql "database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// StreamResult describes a complete serialized SQL result without retaining rows.
type StreamResult struct {
	Metadata    *filestream.Metadata
	ColumnNames []string
	RowCount    int
}

// QueryTo serializes a finite query in one read-only repeatable-read snapshot.
// Any error invalidates all bytes written; callers must discard partial output.
func (executor StatementExecutor) QueryTo(ctx context.Context, stmt string, args []any, limits types.ExecutionLimits, dst io.Writer) (StreamResult, error) {
	if executor.DB == nil || executor.IsQueryStatement == nil || stmt == "" || !executor.IsQueryStatement(stmt) || dst == nil {
		return StreamResult{}, ErrInvalidConfig.WithPublicDetail("SQL file exports require a row-returning query, database and destination.")
	}
	if limits.MaxResultRows <= 0 || limits.MaxFileBytes <= 0 || limits.MaxResponseBytes <= 0 {
		return StreamResult{}, ErrInvalidConfig.WithPublicDetail("SQL file exports require positive row, transfer byte and per-row byte limits.")
	}
	var cancel context.CancelFunc
	if limits.ConnectorTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, limits.ConnectorTimeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	tx, err := executor.DB.BeginTx(ctx, &stdsql.TxOptions{ReadOnly: true, Isolation: stdsql.LevelRepeatableRead})
	if err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, stmt, args...)
	if err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	defer func() { _ = rows.Close() }()
	output, err := executor.writeRows(ctx, rows, limits, dst)
	if err != nil {
		cancel() // Stop the source before Close can drain the remaining result.
		return StreamResult{}, err
	}
	if err := rows.Close(); err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	if err := tx.Rollback(); err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	return output, nil
}

func (executor StatementExecutor) writeRows(ctx context.Context, rows *stdsql.Rows, limits types.ExecutionLimits, dst io.Writer) (StreamResult, error) {
	names, err := rows.Columns()
	if err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	// Duplicate names would silently lose values in the serialized row map.
	seen := make(map[string]bool, len(names))
	columnBudget := result.NewResponseBudget(limits.MaxResponseBytes)
	for _, name := range names {
		if seen[name] {
			return StreamResult{}, ErrInvalidConfig.WithPublicDetail("SQL file exports require unique column names. Add explicit aliases for duplicate columns.")
		}
		seen[name] = true
		if err := columnBudget.ChargeJSON(name); err != nil {
			return StreamResult{}, err
		}
	}
	writer := &sqlStreamWriter{ctx: ctx, dst: dst, digest: sha256.New(), limit: limits.MaxFileBytes}
	encoder := json.NewEncoder(writer)
	if _, err := io.WriteString(writer, `{"column_names":`); err != nil {
		return StreamResult{}, xerror.Unexpected().With(err)
	}
	if err := encoder.Encode(names); err != nil {
		return StreamResult{}, xerror.Unexpected().With(err)
	}
	if _, err := io.WriteString(writer, `,"rows":[`); err != nil {
		return StreamResult{}, xerror.Unexpected().With(err)
	}
	count := 0
	for rows.Next() {
		if count >= limits.MaxResultRows {
			return StreamResult{}, xerror.ExecutionRowLimitExceeded()
		}
		row := make(map[string]any, len(names))
		budget := result.NewResponseBudget(limits.MaxResponseBytes)
		if err := budget.Charge(2); err != nil {
			return StreamResult{}, err
		}
		pointers := make([]any, len(names))
		for i, name := range names {
			if err := budget.ChargeJSON(name); err != nil {
				return StreamResult{}, err
			}
			punctuation := int64(1)
			if i > 0 {
				punctuation++
			}
			if err := budget.Charge(punctuation); err != nil {
				return StreamResult{}, err
			}
			pointers[i] = &budgetScanner{budget: budget, normalize: executor.NormalizeValue, databaseType: columnTypes[i].DatabaseTypeName(), row: row, name: name}
		}
		if err := rows.Scan(pointers...); err != nil {
			if xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
				return StreamResult{}, xerror.ExecutionResponseSizeExceeded().With(err)
			}
			return StreamResult{}, ErrQueryExecutionFailed.With(err)
		}
		if count > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return StreamResult{}, xerror.Unexpected().With(err)
			}
		}
		if err := encoder.Encode(row); err != nil {
			return StreamResult{}, xerror.Unexpected().With(err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	if rows.NextResultSet() {
		return StreamResult{}, ErrInvalidConfig.WithPublicDetail("SQL file exports require a single result set.")
	}
	if err := rows.Err(); err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	if _, err := fmt.Fprintf(writer, `],"row_count":%d}`, count); err != nil {
		return StreamResult{}, xerror.Unexpected().With(err)
	}
	return StreamResult{ColumnNames: names, RowCount: count, Metadata: &filestream.Metadata{SizeBytes: writer.written, SHA256: hex.EncodeToString(writer.digest.Sum(nil)), ContentType: "application/json"}}, nil
}

type sqlStreamWriter struct {
	ctx     context.Context
	dst     io.Writer
	digest  hash.Hash
	limit   int64
	written int64
}

func (w *sqlStreamWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.limit-w.written {
		return 0, xerror.ExecutionResponseSizeExceeded()
	}
	n, err := w.dst.Write(data)
	w.written += int64(n)
	_, _ = w.digest.Write(data[:n])
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.ctx.Err()
	}
	return n, err
}
