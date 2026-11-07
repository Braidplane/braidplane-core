package types

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// NormalizeParams converts TypedParam values to their appropriate runtime types.
// Integer parameters are coerced to int64 for consistent type matching across database drivers
// and serialization formats. All other types pass through as-is.
func NormalizeParams(params map[string]TypedParam) (map[string]any, error) {
	normalized := make(map[string]any, len(params))

	for name, param := range params {
		switch param.DataType {
		case "integer":
			coerced, err := CoerceIntegerParam(param.Value)
			if err != nil {
				return nil, xerror.Wrapf("invalid integer parameter %q: %w", name, err)
			}
			normalized[name] = coerced
		default:
			normalized[name] = param.Value
		}
	}

	return normalized, nil
}

// CoerceIntegerParam converts numeric and string values to int64.
// Rejects fractional floats and out-of-range values.
func CoerceIntegerParam(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		if v > uint(math.MaxInt64) {
			return 0, xerror.Unexpected().Detail("value exceeds int64 range")
		}
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, xerror.Unexpected().Detail("value exceeds int64 range")
		}
		return int64(v), nil
	case float32:
		if math.Trunc(float64(v)) != float64(v) {
			return 0, xerror.Unexpected().Detail("value must be a whole number")
		}
		return int64(v), nil
	case float64:
		if math.Trunc(v) != v {
			return 0, xerror.Unexpected().Detail("value must be a whole number")
		}
		if v < math.MinInt64 || v > math.MaxInt64 {
			return 0, xerror.Unexpected().Detail("value exceeds int64 range")
		}
		return int64(v), nil
	case json.Number:
		number, err := decimal.From(v)
		if err != nil {
			return 0, xerror.Wrapf("parse integer parameter: %w", err)
		}
		exact, ok := new(big.Rat).SetString(number.String())
		if !ok || !exact.IsInt() {
			return 0, xerror.Unexpected().Detail("value must be a whole number")
		}
		if !exact.Num().IsInt64() {
			return 0, xerror.Unexpected().Detail("value exceeds int64 range")
		}
		return exact.Num().Int64(), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, xerror.Unexpected().Detail("value must be a valid integer")
		}
		return parsed, nil
	default:
		return 0, xerror.Unexpected().Detailf("unsupported type %T for integer parameter", value)
	}
}
