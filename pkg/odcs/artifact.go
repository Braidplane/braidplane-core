package odcs

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"gopkg.in/yaml.v3"
)

var jsonNumberLiteral = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// ParseArtifact validates JSON or converts a restricted YAML document to canonical JSON.
// It only parses submitted bytes; admission remains the caller's responsibility.
func ParseArtifact(raw []byte, format string) (json.RawMessage, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, ErrDocument.WithPublicDetail("document exceeds 1 MiB")
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		if err := checkJSON(raw, 0); err != nil {
			return nil, err
		}
		return raw, nil
	case "yaml":
		return parseYAMLArtifact(raw)
	default:
		return nil, ErrDocument.WithPublicDetail("format must be json or yaml")
	}
}

func parseYAMLArtifact(raw []byte) (json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, ErrDocument.WithPublicDetail("document must contain valid Unicode")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, ErrDocument.WithPublicDetail("malformed YAML document")
	}
	if document.Kind == 0 || len(document.Content) != 1 {
		return nil, ErrDocument.WithPublicDetail("YAML document must contain one value")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !xerror.Is(err, io.EOF) {
		if err == nil {
			return nil, ErrDocument.WithPublicDetail("expected one YAML document")
		}
		return nil, ErrDocument.WithPublicDetail("malformed YAML document")
	}
	value, err := yamlJSONValue(document.Content[0], 0)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, ErrDocument.WithPublicDetail("YAML value cannot be represented as JSON")
	}
	if len(canonical) > MaxDocumentBytes {
		return nil, ErrDocument.WithPublicDetail("canonical document exceeds 1 MiB")
	}
	if err := checkJSON(canonical, 0); err != nil {
		return nil, err
	}
	return canonical, nil
}

func yamlJSONValue(node *yaml.Node, depth int) (any, error) {
	if depth > maxDepth {
		return nil, ErrDocument.WithPublicDetail("document nesting exceeds 64 levels")
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return nil, ErrDocument.WithPublicDetail("YAML anchors and aliases are not supported")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return nil, ErrDocument.WithPublicDetail("YAML tags are not supported")
		}
		if len(node.Content)%2 != 0 {
			return nil, ErrDocument.WithPublicDetail("malformed YAML mapping")
		}
		object := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if key.Anchor != "" || key.Kind == yaml.AliasNode {
				return nil, ErrDocument.WithPublicDetail("YAML anchors and aliases are not supported")
			}
			if key.Tag == "!!merge" {
				return nil, ErrDocument.WithPublicDetail("YAML merge keys are not supported")
			}
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, ErrDocument.WithPublicDetail("YAML object keys must be strings")
			}
			if _, exists := object[key.Value]; exists {
				return nil, ErrDocument.WithPublicDetail("duplicate YAML object key")
			}
			converted, err := yamlJSONValue(value, depth+1)
			if err != nil {
				return nil, err
			}
			object[key.Value] = converted
		}
		return object, nil
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return nil, ErrDocument.WithPublicDetail("YAML tags are not supported")
		}
		array := make([]any, len(node.Content))
		for index, child := range node.Content {
			converted, err := yamlJSONValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			array[index] = converted
		}
		return array, nil
	case yaml.ScalarNode:
		return yamlJSONScalar(node)
	default:
		return nil, ErrDocument.WithPublicDetail("unsupported YAML node")
	}
}

func yamlJSONScalar(node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!str":
		return node.Value, nil
	case "!!null":
		if node.Value != "" && node.Value != "~" && strings.ToLower(node.Value) != "null" {
			return nil, ErrDocument.WithPublicDetail("YAML null must use null or ~")
		}
		return json.RawMessage("null"), nil
	case "!!bool":
		switch strings.ToLower(node.Value) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, ErrDocument.WithPublicDetail("YAML boolean is not JSON-compatible")
		}
	case "!!int", "!!float":
		if !jsonNumberLiteral.MatchString(node.Value) || (node.Tag == "!!int" && strings.ContainsAny(node.Value, ".eE")) {
			return nil, ErrDocument.WithPublicDetail("YAML numbers must use JSON numeric syntax")
		}
		return json.Number(node.Value), nil
	case "!!timestamp":
		return nil, ErrDocument.WithPublicDetail("YAML timestamps must be quoted strings")
	default:
		return nil, ErrDocument.WithPublicDetail("YAML tags are not supported")
	}
}
