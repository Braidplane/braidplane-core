package sql

import "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"

// NormalizeParams converts TypedParam values to their appropriate SQL types
// based on the DataType field. SQL databases require strict type matching
// for prepared statement parameters (e.g., MySQL rejects float64 for integer placeholders).
func NormalizeParams(params map[string]types.TypedParam) (map[string]any, error) {
	return types.NormalizeParams(params)
}
