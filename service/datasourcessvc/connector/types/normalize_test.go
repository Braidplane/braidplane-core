package types

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeParamsCoercesIntegerParamsAndPreservesOtherValues(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeParams(map[string]TypedParam{
		"id":      {Value: "42", DataType: "integer"},
		"name":    {Value: "Ada", DataType: "text"},
		"enabled": {Value: true, DataType: "boolean"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(42), normalized["id"])
	require.Equal(t, "Ada", normalized["name"])
	require.Equal(t, true, normalized["enabled"])

	_, err = NormalizeParams(map[string]TypedParam{
		"id": {Value: "not-an-int", DataType: "integer"},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, `invalid integer parameter "id"`)
}

func TestCoerceIntegerParamAcceptsIntegralValuesAndRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		value any
		want  int64
	}{
		{name: "Int", value: int(1), want: 1},
		{name: "Int8", value: int8(2), want: 2},
		{name: "Int16", value: int16(3), want: 3},
		{name: "Int32", value: int32(4), want: 4},
		{name: "Int64", value: int64(5), want: 5},
		{name: "Uint", value: uint(6), want: 6},
		{name: "Uint8", value: uint8(7), want: 7},
		{name: "Uint16", value: uint16(8), want: 8},
		{name: "Uint32", value: uint32(9), want: 9},
		{name: "Uint64", value: uint64(10), want: 10},
		{name: "Float32Whole", value: float32(11), want: 11},
		{name: "Float64Whole", value: float64(12), want: 12},
		{name: "String", value: "13", want: 13},
		{name: "JSONWholeFraction", value: json.Number("1.0"), want: 1},
		{name: "JSONExponent", value: json.Number("1e3"), want: 1000},
		{name: "ExactJSONInteger", value: json.Number("9007199254740993"), want: 9007199254740993},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CoerceIntegerParam(tt.value)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	for _, tt := range []struct {
		name        string
		value       any
		errContains string
	}{
		{name: "Uint64Overflow", value: uint64(math.MaxInt64) + 1, errContains: "exceeds int64 range"},
		{name: "Float32Fractional", value: float32(1.5), errContains: "whole number"},
		{name: "Float64Fractional", value: float64(1.5), errContains: "whole number"},
		{name: "Float64Overflow", value: float64(math.MaxInt64) * 2, errContains: "exceeds int64 range"},
		{name: "InvalidString", value: "abc", errContains: "valid integer"},
		{name: "FractionalJSONNumber", value: json.Number("1.1"), errContains: "whole number"},
		{name: "JSONIntegerOverflow", value: json.Number("9223372036854775808"), errContains: "exceeds int64 range"},
		{name: "Unsupported", value: []int{1}, errContains: "unsupported type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := CoerceIntegerParam(tt.value)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.errContains)
		})
	}
}

func TestCoerceIntegerParamRejectsNativeUintOverflow(t *testing.T) {
	t.Parallel()

	if strconv.IntSize < 64 {
		t.Skip("native uint cannot exceed int64 on 32-bit platforms")
	}

	_, err := CoerceIntegerParam(uint(math.MaxUint))

	require.Error(t, err)
	require.ErrorContains(t, err, "exceeds int64 range")
}

func TestResolveExecutionLimitsReturnsFirstLimitOrZeroValue(t *testing.T) {
	t.Parallel()

	require.Equal(t, ExecutionLimits{}, ResolveExecutionLimits())

	first := ExecutionLimits{
		ConnectorTimeout:     time.Second,
		MaxResultRows:        10,
		MaxResponseBytes:     1024,
		HTTPTimeout:          2 * time.Second,
		HTTPMaxRedirects:     3,
		HTTPMaxRequestBytes:  2048,
		HTTPMaxResponseBytes: 4096,
		HTTPRetryAttempts:    2,
		HTTPRetryBackoff:     50 * time.Millisecond,
	}
	second := ExecutionLimits{MaxResultRows: 99}
	require.Equal(t, first, ResolveExecutionLimits(first, second))
}
