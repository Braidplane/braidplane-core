package mongo

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// normalizeParams converts TypedParam values to appropriate types for MongoDB BSON encoding.
// BSON type matching is critical for MongoDB queries (e.g., filter {age: 25} requires int64, not
// float64).
func normalizeParams(params map[string]types.TypedParam) (map[string]any, error) {
	normalized, err := types.NormalizeParams(params)
	if err != nil {
		return nil, err
	}
	for name, definition := range params {
		if value, ok := definition.Value.(string); definition.DataType == "boolean" && ok {
			if parsed, err := strconv.ParseBool(value); err == nil {
				normalized[name] = parsed
			}
		}
	}
	return normalized, nil
}

func isMongoStructuredField(name string) bool {
	switch name {
	case "filter", "projection", "sort", "pipeline", "document", "documents", "update":
		return true
	default:
		return false
	}
}

type mongoBindingPosition struct {
	wholeField   bool
	data         bool
	expression   bool
	insert       bool
	update       bool
	setFields    bool
	appendFields bool
	appendValue  bool
}

func validateMongoBindings(statement map[string]any, definitions map[string]types.TypedParam, values map[string]any) error {
	for field, value := range statement {
		name := strings.ToLower(field)
		if text, ok := value.(string); name == "operation" && ok && len(param.ExtractParamNames(text)) > 0 {
			return xerror.InvalidInput(xerror.Query).WithPublicDetail("Set the MongoDB operation in the statement.")
		}
		insert := name == "document" || name == "documents"
		position := mongoBindingPosition{
			wholeField: isMongoStructuredField(name), data: insert, insert: insert,
			expression: name == "pipeline" || name == "projection", update: name == "update",
		}
		if err := validateMongoBindingValue(value, position, definitions, values); err != nil {
			return err
		}
	}
	return nil
}

func validateMongoBindingValue(value any, position mongoBindingPosition, definitions map[string]types.TypedParam, values map[string]any) error {
	switch v := value.(type) {
	case string:
		names := param.ExtractParamNames(v)
		_, exact := param.ExactParamName(v)
		for _, name := range names {
			if !validMongoBinding(name, definitions[name].DataType, values[name], exact, position) {
				return xerror.InvalidInput(xerror.Query).WithPublicDetail("A MongoDB parameter does not match its declared type or binding position.")
			}
		}
		if position.expression && len(names) > 0 {
			resolved, err := param.SubstituteValue(v, values)
			if err != nil {
				return err
			}
			if text, ok := resolved.(string); ok && strings.HasPrefix(text, "$") {
				return xerror.InvalidInput(xerror.Query).WithPublicDetail("Use a literal expression for MongoDB parameter text beginning with a dollar sign.")
			}
		}
	case map[string]any:
		appendModifiers := position.appendValue && hasMongoModifierKeys(v)
		for key, child := range v {
			nested := position
			nested.wholeField = false
			nested.update = false
			nested.setFields = position.update && (key == "$set" || key == "$setOnInsert")
			nested.appendFields = position.update && (key == "$push" || key == "$addToSet")
			nested.appendValue = position.appendFields
			if position.setFields || position.appendValue && !appendModifiers {
				nested.data = true
			}
			if appendModifiers && key == "$each" {
				_, nested.data = child.([]any)
			}
			if !nested.data {
				switch key {
				case "$literal":
					if len(v) == 1 {
						nested.data, nested.expression = true, false
					}
				case "$match":
					nested.expression = false
				case "$expr":
					nested.expression = true
				}
			}
			if err := validateMongoBindingValue(child, nested, definitions, values); err != nil {
				return err
			}
		}
	case []any:
		position.wholeField = false
		if position.appendValue {
			position.data = true
			position.appendValue = false
		}
		for _, child := range v {
			if err := validateMongoBindingValue(child, position, definitions, values); err != nil {
				return err
			}
		}
	}
	return nil
}

func validMongoBinding(name, dataType string, value any, exact bool, position mongoBindingPosition) bool {
	if value == nil {
		return true
	}
	var isString, isBoolean bool
	switch value.(type) {
	case string:
		isString = true
	case bool:
		isBoolean = true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
	default:
		if position.appendValue && !literalMongoAppendValue(value) {
			return false
		}
		// Legacy saved sinks declare input as text for their whole document payload.
		return exact && ((dataType == "" || dataType == "json") && (position.wholeField || position.data || position.appendValue) ||
			dataType == "text" && name == sinkbinding.Input && position.wholeField && position.insert)
	}
	switch dataType {
	case "", "json", "integer":
		return true
	case "text", "date", "uuid":
		return isString
	case "boolean":
		return isBoolean
	default:
		return false
	}
}

func hasMongoModifierKeys[V any](document map[string]V) bool {
	for key := range document {
		if strings.HasPrefix(key, "$") {
			return true
		}
	}
	return false
}

func literalMongoAppendValue(value any) bool {
	if document, ok := value.(map[string]any); ok {
		return !hasMongoModifierKeys(document)
	}
	// Execute serializes the resolved statement as JSON before decoding its update.
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	if raw[0] != '{' {
		return true
	}
	var document map[string]json.RawMessage
	return json.Unmarshal(raw, &document) == nil && !hasMongoModifierKeys(document)
}
