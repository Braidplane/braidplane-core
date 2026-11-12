package e2e

import (
	"context"
	"crypto/md5" // #nosec G501 -- MD5 defines synthetic fixture bytes, not an integrity check.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func sampleSQLStreamHeap(ctx context.Context) func() uint64 {
	ctx, cancel := context.WithCancel(ctx)
	finished := make(chan uint64, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		var peak uint64
		for {
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			peak = max(peak, stats.HeapAlloc)
			select {
			case <-ctx.Done():
				finished <- peak
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	var peak uint64
	return func() uint64 { once.Do(func() { cancel(); peak = <-finished }); return peak }
}

type sqlOracleReader struct {
	io.Reader
	bytes int64
}

func (r *sqlOracleReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	r.bytes += int64(n)
	return n, err
}

// verifySQLSnapshot uses the frozen business formula independently of SQL and
// checks one record at a time; neither the source nor result is materialized.
func verifySQLSnapshot(source io.Reader, rows int) (int64, string, error) {
	hash := sha256.New()
	reader := &sqlOracleReader{Reader: io.TeeReader(source, hash)}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	expect := func(want any) error {
		got, err := decoder.Token()
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		if got != want {
			return xerror.Unexpected().Detailf("SQL snapshot token mismatch: expected %v", want)
		}
		return nil
	}
	for _, token := range []any{json.Delim('{'), "column_names"} {
		if err := expect(token); err != nil {
			return 0, "", err
		}
	}
	var columns []string
	if err := decoder.Decode(&columns); err != nil {
		return 0, "", xerror.Unexpected().With(err)
	}
	if strings.Join(columns, ",") != "order_id,amount_cents,payload" {
		return 0, "", xerror.Unexpected().Detail("SQL snapshot columns differ")
	}
	for _, token := range []any{"rows", json.Delim('[')} {
		if err := expect(token); err != nil {
			return 0, "", err
		}
	}
	count := 0
	for decoder.More() {
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			return 0, "", xerror.Unexpected().With(err)
		}
		count++
		id := strconv.Itoa(count)
		// #nosec G401 -- fixture definition only; SHA-256 verifies the complete object.
		digest := md5.Sum([]byte(id))
		if count > rows || len(row) != 3 || row["order_id"] != json.Number(id) || row["amount_cents"] != json.Number(strconv.Itoa(count%100000)) || row["payload"] != strings.Repeat(hex.EncodeToString(digest[:]), 32) {
			return 0, "", xerror.Unexpected().Detailf("SQL snapshot record %d differs in identity, type or payload", count)
		}
	}
	if count != rows {
		return 0, "", xerror.Unexpected().Detail("SQL snapshot has missing records")
	}
	for _, token := range []any{json.Delim(']'), "row_count", json.Number(strconv.Itoa(rows)), json.Delim('}')} {
		if err := expect(token); err != nil {
			return 0, "", err
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return 0, "", xerror.Unexpected().With(err).Detail("SQL snapshot has trailing data or an incomplete body")
	}
	return reader.bytes, hex.EncodeToString(hash.Sum(nil)), nil
}

func TestSQLSnapshotOracleRejectsCorruption(t *testing.T) {
	// Known fixture for order 1, independent of the serializer and database.
	payload := strings.Repeat("c4ca4238a0b923820dcc509a6f75849b", 32)
	valid := fmt.Sprintf(`{"column_names":["order_id","amount_cents","payload"],"rows":[{"order_id":1,"amount_cents":1,"payload":%q}],"row_count":1}`, payload)
	n, digest, err := verifySQLSnapshot(strings.NewReader(valid), 1)
	require.NoError(t, err)
	require.EqualValues(t, len(valid), n)
	expected := sha256.Sum256([]byte(valid))
	require.Equal(t, hex.EncodeToString(expected[:]), digest)
	for _, invalid := range []string{
		valid[:len(valid)-1], valid + "{}",
		strings.Replace(valid, `"order_id":1`, `"order_id":"1"`, 1),
		strings.Replace(valid, `"amount_cents":1`, `"amount_cents":null`, 1),
		strings.Replace(valid, `"amount_cents":1`, `"extra":0,"amount_cents":1`, 1),
		strings.Replace(valid, payload, strings.Repeat("x", 1024), 1),
		strings.Replace(valid, `"row_count":1`, `"row_count":2`, 1),
	} {
		_, _, err := verifySQLSnapshot(strings.NewReader(invalid), 1)
		require.Error(t, err)
	}
}
