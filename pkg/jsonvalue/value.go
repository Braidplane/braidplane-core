// Package jsonvalue preserves numeric values at internal JSON transport boundaries.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Unmarshal keeps numeric literals exact until a consumer applies its declared type.
func Unmarshal(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return xerror.Unexpected().Detail("expected one JSON value")
	}
	return nil
}

// Normalize restores native integers while preserving fractional and exponent lexemes.
// Converting those lexemes to float64 here would erase declared decimal precision.
func Normalize(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			if int64(int(integer)) == integer {
				return int(integer)
			}
			return integer
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = Normalize(item)
		}
	case []any:
		for i, item := range typed {
			typed[i] = Normalize(item)
		}
	case []map[string]any:
		for _, row := range typed {
			Normalize(row)
		}
	}
	return value
}
