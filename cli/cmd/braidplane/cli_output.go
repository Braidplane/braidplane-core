package main

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"gopkg.in/yaml.v3"
)

const (
	cliOutputFormatJSON = "json"
	cliOutputFormatText = "text"
	cliOutputFormatYAML = "yaml"
)

type cliOutputOptions struct {
	Output string `name:"output" short:"o" enum:"json,yaml,text" default:"json" help:"Output format: json, yaml or text."`
}

type cliTextOutputOptions struct {
	Output string `name:"output" short:"o" enum:"json,yaml,text" default:"text" help:"Output format: json, yaml or text."`
}

// cliOutputValue is the common denominator for command output that supports
// both structured JSON and command-specific human text. Generated API records
// stay as JSON DTOs; commands opt in by wrapping them with a small CLI adapter.
type cliOutputValue interface {
	JSONValue() any
	WriteCLIText(io.Writer) error
}

// Serialize the JSON representation so YAML uses the same names and omissions.
func writeCLIYAML(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return xerror.Unexpected().With(err)
	}
	var blockStyle func(*yaml.Node)
	blockStyle = func(n *yaml.Node) {
		n.Style &^= yaml.FlowStyle
		quoted := n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0
		// YAML's resolver can misclassify JSON numbers outside its native range.
		number := n.Kind == yaml.ScalarNode && len(n.Value) > 0 && (n.Value[0] == '-' || n.Value[0] >= '0' && n.Value[0] <= '9') && json.Valid([]byte(n.Value))
		if number && !quoted {
			if strings.ContainsAny(n.Value, ".eE") {
				n.Tag = "!!float"
			} else {
				n.Tag = "!!int"
			}
		}
		// A plain << key is a YAML merge instruction, not a JSON string key.
		if n.Kind == yaml.ScalarNode && n.Value != "<<" && (!number || !quoted) {
			n.Style &^= yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
		}
		for _, child := range n.Content {
			blockStyle(child)
		}
	}
	blockStyle(&node)
	encoder := yaml.NewEncoder(writer)
	encoder.SetIndent(2)
	if err := encoder.Encode(&node); err != nil {
		return xerror.Unexpected().With(err)
	}
	if err := encoder.Close(); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func writeCLIJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

func writeCLIOutput(
	writer io.Writer,
	value cliOutputValue,
	outputFormat string,
) error {
	return writeCLIOutputMode(writer, value, outputFormat, false)
}

func writeCLIOutputMode(writer io.Writer, value cliOutputValue, outputFormat string, stream bool) error {
	switch strings.TrimSpace(outputFormat) {
	case "", cliOutputFormatJSON:
		if stream {
			if err := json.NewEncoder(writer).Encode(value.JSONValue()); err != nil {
				return xerror.Unexpected().With(err)
			}
			return nil
		}
		return writeCLIJSON(writer, value.JSONValue())
	case cliOutputFormatYAML:
		if stream {
			if _, err := io.WriteString(writer, "---\n"); err != nil {
				return xerror.Unexpected().With(err)
			}
		}
		return writeCLIYAML(writer, value.JSONValue())
	case cliOutputFormatText:
		return value.WriteCLIText(writer)
	default:
		return xerror.InvalidInput(xerror.JSON).Detail("output must be json, yaml or text")
	}
}
