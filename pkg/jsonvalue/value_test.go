package jsonvalue

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnmarshalPreservesNumbersAndRejectsTrailingValues(t *testing.T) {
	t.Parallel()
	var value any
	require.NoError(t, Unmarshal([]byte(`{"values":[9007199254740993,9223372036854775808,-3,1.25,1e2]}`), &value))
	require.Equal(t, map[string]any{"values": []any{
		9007199254740993, json.Number("9223372036854775808"), -3, json.Number("1.25"), json.Number("1e2"),
	}}, Normalize(value))
	require.ErrorContains(t, Unmarshal([]byte(`{} {}`), &value), "expected one JSON value")
	require.ErrorContains(t, Unmarshal([]byte(`{} invalid`), &value), "expected one JSON value")
}

func TestNormalizePreservesNonIntegerNumericLexemes(t *testing.T) {
	t.Parallel()
	for _, literal := range []string{"0.10", "2.675", "-2.675", "0.12345678901234567890123456789012345678", "1e-308", "9007199254740993e0", "-9007199254740993.0", "9223372036854775807e0", "1e400"} {
		value := map[string]any{"nested": []any{json.Number(literal)}}
		Normalize(value)
		require.Equal(t, json.Number(literal), value["nested"].([]any)[0])
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.Equal(t, `{"nested":[`+literal+`]}`, string(encoded))
	}
}
