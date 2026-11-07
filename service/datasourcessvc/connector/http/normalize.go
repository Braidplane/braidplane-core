package http

import "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"

// normalizeParams converts TypedParam values to plain map[string]any for HTTP parameter
// substitution. For HTTP APIs, we format values appropriately (integers without decimals, booleans
// as true/false strings).
func normalizeParams(params map[string]types.TypedParam) (map[string]any, error) {
	return types.NormalizeParams(params)
}
