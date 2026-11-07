package apischema

import _ "embed"

//go:embed schema.yml
var source string

// Source returns the public API contract.
func Source() []byte { return []byte(source) }
