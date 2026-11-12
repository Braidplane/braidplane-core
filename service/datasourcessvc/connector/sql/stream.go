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
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
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
	if err := validateFileLimits(limits); err != nil {
		return StreamResult{}, err
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
	output, err := StreamRows(ctx, rows, limits, executor.NormalizeValue, dst)
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

// StreamRows serializes database/sql rows without collecting the result.
func StreamRows(ctx context.Context, rows *stdsql.Rows, limits types.ExecutionLimits, normalize ValueNormalizer, dst io.Writer) (StreamResult, error) {
	names, err := rows.Columns()
	if err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return StreamResult{}, ErrQueryExecutionFailed.With(err)
	}
	stream, err := NewRowStream(ctx, dst, names, limits)
	if err != nil {
		return StreamResult{}, err
	}
	for rows.Next() {
		if stream.count >= limits.MaxResultRows {
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
			pointers[i] = &budgetScanner{budget: budget, normalize: normalize, databaseType: columnTypes[i].DatabaseTypeName(), row: row, name: name}
		}
		if err := rows.Scan(pointers...); err != nil {
			if xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
				return StreamResult{}, xerror.ExecutionResponseSizeExceeded().With(err)
			}
			return StreamResult{}, ErrQueryExecutionFailed.With(err)
		}
		if err := stream.WriteRow(row); err != nil {
			return StreamResult{}, err
		}
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
	return stream.Finish()
}

// RowStream writes one bounded row at a time from SQL drivers or provider pages.
type RowStream struct {
	writer *sqlStreamWriter
	names  []string
	limits types.ExecutionLimits
	count  int
}

func NewRowStream(ctx context.Context, dst io.Writer, names []string, limits types.ExecutionLimits) (*RowStream, error) {
	if err := validateFileLimits(limits); err != nil {
		return nil, err
	}
	if dst == nil {
		return nil, ErrInvalidConfig.WithPublicDetail("SQL file output requires a destination.")
	}
	// Duplicate names would silently lose values in the serialized row map.
	seen := make(map[string]bool, len(names))
	columnBudget := result.NewResponseBudget(limits.MaxResponseBytes)
	if err := columnBudget.Charge(2); err != nil {
		return nil, err
	}
	for i, name := range names {
		if i > 0 {
			if err := columnBudget.Charge(1); err != nil {
				return nil, err
			}
		}
		if seen[name] {
			return nil, ErrInvalidConfig.WithPublicDetail("SQL file exports require unique column names. Add explicit aliases for duplicate columns.")
		}
		seen[name] = true
		if err := columnBudget.ChargeJSON(name); err != nil {
			return nil, err
		}
	}
	writer := &sqlStreamWriter{ctx: ctx, dst: dst, digest: sha256.New(), limit: limits.MaxFileBytes}
	encoder := json.NewEncoder(writer)
	if _, err := io.WriteString(writer, `{"column_names":`); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if err := encoder.Encode(names); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if _, err := io.WriteString(writer, `,"rows":[`); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &RowStream{writer: writer, names: append([]string{}, names...), limits: limits}, nil
}

func (s *RowStream) WriteRow(row map[string]any) error {
	if s.count >= s.limits.MaxResultRows {
		return xerror.ExecutionRowLimitExceeded()
	}
	if len(row) != len(s.names) {
		return ErrInvalidConfig.WithPublicDetail("SQL result columns changed during streaming.")
	}
	for _, name := range s.names {
		if _, ok := row[name]; !ok {
			return ErrInvalidConfig.WithPublicDetail("SQL result columns changed during streaming.")
		}
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if int64(len(encoded)) > s.limits.MaxResponseBytes {
		return xerror.ExecutionResponseSizeExceeded()
	}
	if s.count > 0 {
		if _, err := io.WriteString(s.writer, ","); err != nil {
			return xerror.Unexpected().With(err)
		}
	}
	if _, err := s.writer.Write(encoded); err != nil {
		return xerror.Unexpected().With(err)
	}
	if _, err := io.WriteString(s.writer, "\n"); err != nil {
		return xerror.Unexpected().With(err)
	}
	s.count++
	return nil
}

func (s *RowStream) Finish() (StreamResult, error) {
	if _, err := fmt.Fprintf(s.writer, `],"row_count":%d}`, s.count); err != nil {
		return StreamResult{}, xerror.Unexpected().With(err)
	}
	return StreamResult{ColumnNames: s.names, RowCount: s.count, Metadata: &filestream.Metadata{SizeBytes: s.writer.written, SHA256: hex.EncodeToString(s.writer.digest.Sum(nil)), ContentType: "application/json"}}, nil
}

func validateFileLimits(limits types.ExecutionLimits) error {
	if limits.MaxResultRows <= 0 || limits.MaxFileBytes <= 0 || limits.MaxResponseBytes <= 0 {
		return ErrInvalidConfig.WithPublicDetail("SQL file exports require positive row, transfer byte and per-row byte limits.")
	}
	return nil
}

// FileOutputRequested decodes the common SQL result-format contract.
func FileOutputRequested(raw json.RawMessage) (bool, error) {
	var options struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return false, ErrInvalidConfig.With(err)
	}
	switch options.Format {
	case "", "rows":
		return false, nil
	case "file":
		return true, nil
	default:
		return false, ErrInvalidConfig.WithPublicDetail("SQL result format must be rows or file.")
	}
}

// RetainQueryFile stages a provider's complete serialized result before exposing it.
func RetainQueryFile(ctx context.Context, limits types.ExecutionLimits, produce func(context.Context, io.Writer) (StreamResult, error)) (*result.SQLResult, error) {
	if err := validateFileLimits(limits); err != nil {
		return nil, err
	}
	store, ok := filestream.FromContext(ctx)
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	var output StreamResult
	ref, metadata, err := store.Retain(ctx, limits.MaxFileBytes, func(dst io.Writer) (*filestream.Metadata, error) {
		var err error
		output, err = produce(ctx, dst)
		return output.Metadata, err
	})
	if err != nil {
		return nil, err
	}
	return &result.SQLResult{Rows: []map[string]any{}, ColumnNames: output.ColumnNames, RowCount: output.RowCount, Reference: ref, File: &filepayload.FilePayloadMetadata{Filename: "query-result.json", MIMEType: "application/json", SizeBytes: metadata.SizeBytes, DigestAlgorithm: filepayload.FilePayloadDigestSHA256, Digest: metadata.SHA256}}, nil
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
