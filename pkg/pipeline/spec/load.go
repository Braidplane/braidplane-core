package spec

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrUnsupportedFormat = xerror.UnsupportedPipelineFormat()

const (
	yamlFileExt = ".yaml"
	ymlFileExt  = ".yml"
	jsonFileExt = ".json"
)

// Load decodes one YAML or JSON pipeline spec from data. path is used only to
// detect file format and produce useful error messages. Semantic validation
// belongs to pipeline/validate.
func Load(path string, data []byte) (*Pipeline, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case yamlFileExt, ymlFileExt, jsonFileExt:
		return parseYAMLOrJSON(path, data, ext)
	default:
		return nil, ErrUnsupportedFormat.Detailf(
			"%q (use %s, %s, or %s)",
			ext,
			yamlFileExt,
			ymlFileExt,
			jsonFileExt,
		)
	}
}

func parseYAMLOrJSON(path string, data []byte, ext string) (*Pipeline, error) {
	var p Pipeline
	switch ext {
	case yamlFileExt, ymlFileExt:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&p); err != nil {
			return nil, xerror.Wrapf("parse %s: decode pipeline: %w", path, err)
		}
		var trailing yaml.Node
		if err := decoder.Decode(&trailing); !xerror.Is(err, io.EOF) {
			if err == nil {
				err = xerror.InvalidInput(xerror.Pipeline).Detail("multiple YAML documents")
			}
			return nil, xerror.Wrapf("parse %s: decode pipeline: %w", path, err)
		}
		normalizeYAMLDynamicValues(&p)
	case jsonFileExt:
		decoded, err := DecodeJSON(data)
		if err != nil {
			return nil, xerror.Wrapf("parse %s: decode pipeline: %w", path, err)
		}
		p = *decoded
		for _, step := range p.Steps {
			var maps []map[string]any
			if step.Query != nil {
				maps = append(maps, step.Query.Parameters, step.Query.OperationInline)
			}
			if step.Sink != nil {
				maps = append(maps, step.Sink.Config, step.Sink.OperationInline)
			}
			for _, values := range maps {
				if _, err := normalizeJSONNumbers(values); err != nil {
					return nil, xerror.Wrapf("parse %s: decode pipeline: %w", path, err)
				}
			}
		}
	default:
		return nil, ErrUnsupportedFormat.Detailf("%q", ext)
	}

	return &p, nil
}

// DecodeJSON decodes exactly one pipeline, preserving numbers for formatting.
// Use Load when preparing a pipeline for validation or execution.
func DecodeJSON(data []byte) (*Pipeline, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var pipeline Pipeline
	if err := decoder.Decode(&pipeline); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !xerror.Is(err, io.EOF) {
		if err == nil {
			err = xerror.InvalidInput(xerror.Pipeline).Detail("multiple JSON values")
		}
		return nil, err
	}
	return &pipeline, nil
}

func normalizeYAMLDynamicValues(p *Pipeline) {
	if p == nil {
		return
	}
	for i := range p.Steps {
		step := &p.Steps[i]
		if step.Query != nil {
			step.Query.Parameters = normalizeStringAnyMap(step.Query.Parameters)
			step.Query.OperationInline = normalizeStringAnyMap(step.Query.OperationInline)
		}
		if step.Sink != nil {
			step.Sink.OperationInline = normalizeStringAnyMap(step.Sink.OperationInline)
			step.Sink.Config = normalizeStringAnyMap(step.Sink.Config)
		}
	}
}

func normalizeStringAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	for key, value := range values {
		values[key] = normalizeYAMLValue(value)
	}
	return values
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case map[string]any:
		return normalizeStringAnyMap(typed)
	case []any:
		for i, item := range typed {
			typed[i] = normalizeYAMLValue(item)
		}
		return typed
	default:
		return value
	}
}
