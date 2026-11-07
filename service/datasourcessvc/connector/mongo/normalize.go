package mongo

import (
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// normalizeParams converts TypedParam values to appropriate types for MongoDB BSON encoding.
// BSON type matching is critical for MongoDB queries (e.g., filter {age: 25} requires int64, not
// float64).
func normalizeParams(params map[string]types.TypedParam) (map[string]any, error) {
	return types.NormalizeParams(params)
}
