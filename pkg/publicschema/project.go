package publicschema

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/getkin/kin-openapi/openapi3"
)

// Project omits internal paths and types without modifying the source document.
func Project(schema *openapi3.T) ([]byte, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, xerror.Wrapf("encode API document: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, xerror.Wrapf("copy API document: %w", err)
	}
	removeInternal := func(entries map[string]any) {
		for name, value := range entries {
			if item, ok := value.(map[string]any); ok && item["x-internal"] == true {
				delete(entries, name)
			}
		}
	}
	paths, _ := document["paths"].(map[string]any)
	removeInternal(paths)
	components, _ := document["components"].(map[string]any)
	for _, group := range components {
		entries, _ := group.(map[string]any)
		removeInternal(entries)
	}
	result, err := json.Marshal(document)
	if err != nil {
		return nil, xerror.Wrapf("encode public API document: %w", err)
	}
	if _, err := openapi3.NewLoader().LoadFromData(result); err != nil {
		return nil, xerror.Wrapf("resolve public API document references: %w", err)
	}
	return result, nil
}
