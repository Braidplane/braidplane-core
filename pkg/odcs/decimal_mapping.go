package odcs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/decimal"
)

// Physical spellings are identifiers, not a provider-specific SQL grammar.
type decimalTypeMapping struct {
	Precision *int `json:"precision"`
	Scale     *int `json:"scale"`
}

// IsFlatCompiler accepts retained shapes without recompiling their documents.
func IsFlatCompiler(id string) bool {
	return id == CompilerID || id == DecimalCompilerID
}

func (r *ConversionResult) readDecimalMappings(document map[string]any) bool {
	properties, _ := document["customProperties"].([]any)
	for i, value := range properties {
		property := value.(map[string]any)
		if property["vendor"] != "braidplane" || property["property"] != "decimalPhysicalTypes" {
			continue
		}
		path := fmt.Sprintf("$.customProperties[%d].value", i)
		if r.decimalMappingPath != "" {
			r.add("decimal_mapping_invalid", path, "Declare Braidplane decimal physical types once per contract revision.")
			return false
		}
		r.decimalMappingPath = path
		r.CompilerID = DecimalCompilerID
		raw, err := json.Marshal(property["value"])
		if err != nil {
			r.add("decimal_mapping_invalid", path, "Supply a map of literal physical type names to decimal precision and scale.")
			return false
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&r.decimalMappings); err != nil || len(r.decimalMappings) == 0 || len(r.decimalMappings) > 32 {
			r.add("decimal_mapping_invalid", path, "Supply between 1 and 32 exact decimal physical type definitions.")
			return false
		}
		for physical, mapping := range r.decimalMappings {
			if strings.TrimSpace(physical) == "" || len(physical) > 128 || strings.Contains(physical, "${") || mapping.Precision == nil || mapping.Scale == nil || decimal.ValidateDeclaration(*mapping.Precision, *mapping.Scale) != nil {
				r.add("decimal_mapping_invalid", path, "Use literal physical type names of at most 128 bytes, precision 1–38 and scale 0–precision.")
				return false
			}
		}
	}
	return true
}
