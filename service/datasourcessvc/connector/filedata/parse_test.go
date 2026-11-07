package filedata

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestParseCSV_WithHeader(t *testing.T) {
	t.Parallel()

	content := []byte("name,age\nalice,30\nbob,25\n")

	r, err := ParseCSV(content, ParseCSVOptions{})

	require.NoError(t, err)
	require.Equal(t, 2, r.RowCount)
	require.Equal(t, []string{"name", "age"}, r.ColumnNames)
	require.Equal(t, "alice", r.Rows[0]["name"])
	require.Equal(t, "30", r.Rows[0]["age"])
	require.False(t, r.IsHeadlessCSV)
}

func TestParseCSV_HeadlessDetection(t *testing.T) {
	t.Parallel()

	content := []byte("1.0,2.0,3.0\n4.0,5.0,6.0\n")

	r, err := ParseCSV(content, ParseCSVOptions{})

	require.NoError(t, err)
	require.True(t, r.IsHeadlessCSV)
	require.Contains(t, r.ColumnNames, "col_0")
	require.Contains(t, r.Rows[0], "csv_row")
}

func TestParseCSV_EmptyContent(t *testing.T) {
	t.Parallel()

	content := []byte("")

	r, err := ParseCSV(content, ParseCSVOptions{})

	require.NoError(t, err)
	require.Equal(t, 0, r.RowCount)
	require.Empty(t, r.Rows)
}

func TestParseCSV_CustomDelimiter(t *testing.T) {
	t.Parallel()

	hasHeader := true
	content := []byte("name;age\nalice;30\n")

	r, err := ParseCSV(content, ParseCSVOptions{Delimiter: ";", HasHeader: &hasHeader})

	require.NoError(t, err)
	require.Equal(t, 1, r.RowCount)
	require.Equal(t, "alice", r.Rows[0]["name"])
}

func TestParseCSV_ReturnsRowLimitViolation(t *testing.T) {
	t.Parallel()

	content := []byte("name\nalice\nbob\n")

	_, err := ParseCSV(content, ParseCSVOptions{MaxRows: 1})

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

func TestParseCSV_ReturnsResponseSizeViolation(t *testing.T) {
	t.Parallel()

	content := []byte("name\nalice\n")

	_, err := ParseCSV(content, ParseCSVOptions{MaxBytes: 4})

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestParseJSON_Array(t *testing.T) {
	t.Parallel()

	content := []byte(`[{"id":1,"name":"alice"},{"id":2,"name":"bob"}]`)

	r, err := ParseJSON(content)

	require.NoError(t, err)
	require.Equal(t, 2, r.RowCount)
	require.Equal(t, []string{"id", "name"}, r.ColumnNames)
}

func TestParseJSON_PreservesLargeDecimalToken(t *testing.T) {
	t.Parallel()

	r, err := ParseJSON([]byte(`[{"amount":12345678901234567890.123456789}]`))

	require.NoError(t, err)
	require.Equal(t, json.Number("12345678901234567890.123456789"), r.Rows[0]["amount"])
}

func TestParseJSONWithLimits_ReturnsRowLimitViolation(t *testing.T) {
	t.Parallel()

	content := []byte(`[{"id":1},{"id":2}]`)

	_, err := ParseJSONWithLimits(content, ParseJSONOptions{MaxRows: 1})

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

func TestParseJSONWithLimits_ReturnsResponseSizeViolation(t *testing.T) {
	t.Parallel()

	content := []byte(`[{"id":1}]`)

	_, err := ParseJSONWithLimits(content, ParseJSONOptions{MaxBytes: 4})

	require.Error(t, err)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestParseJSON_Object(t *testing.T) {
	t.Parallel()

	content := []byte(`{"key":"value","count":42}`)

	r, err := ParseJSON(content)

	require.NoError(t, err)
	require.Equal(t, 1, r.RowCount)
	require.Equal(t, []string{"count", "key"}, r.ColumnNames)
}

func TestParseJSON_Invalid(t *testing.T) {
	t.Parallel()

	content := []byte(`not json`)

	_, err := ParseJSON(content)

	require.Error(t, err)
	require.ErrorContains(t, err, "parse JSON content")
}
