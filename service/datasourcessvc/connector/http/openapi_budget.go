package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const maxOpenAPIExpansionDepth = 128

type openAPIWorkBudget struct {
	ctx        context.Context
	remaining  int64
	typedRefs  map[string]bool
	refTargets map[string][]any
	fields     map[string]map[string][]openAPIReferenceValue
}

type openAPIReferenceValue struct {
	value any
	path  string
}

func (b *openAPIWorkBudget) charge(size, copies int64) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if size < 0 || copies < 0 || (copies > 0 && size > b.remaining/copies) {
		return xerror.ExecutionUnavailable().Detail("schema result exceeds configured byte limit during OpenAPI expansion")
	}
	b.remaining -= size * copies
	return nil
}

// preflightOpenAPI counts repeated references before the loader or output builders expand them.
func preflightOpenAPI(ctx context.Context, data []byte) (*openAPIWorkBudget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := int64(schemaquota.RemainingBytes(ctx, maxOpenAPISize))
	budget := &openAPIWorkBudget{ctx: ctx, remaining: limit, typedRefs: map[string]bool{}, refTargets: map[string][]any{}, fields: map[string]map[string][]openAPIReferenceValue{}}
	if limit <= 0 || len(data) > maxOpenAPISize {
		return nil, budget.charge(maxOpenAPISize+1, 1)
	}
	var document any
	if json.Valid(data) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	} else {
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			return nil, xerror.Unexpected().With(xerror.Wrapf("parse OpenAPI json or yaml: %w", err))
		}
		// Nodes retain aliases without expanding them. Check before the loader's YAML decoder does.
		if err := budget.yamlNode(&node, 0, map[*yaml.Node]bool{}); err != nil {
			return nil, err
		}
		if err := node.Decode(&document); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		document = nil
		if err := decoder.Decode(&document); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		budget.remaining = limit
	}
	if err := budget.value(document, document, 0, 1, true, map[string]bool{}); err != nil {
		return nil, err
	}
	budget.typedRefs = nil
	budget.refTargets = nil
	budget.fields = nil
	budget.remaining = limit
	return budget, nil
}

func (b *openAPIWorkBudget) yamlNode(node *yaml.Node, depth int, visiting map[*yaml.Node]bool) error {
	if node == nil {
		return nil
	}
	if depth > maxOpenAPIExpansionDepth || visiting[node] {
		return openAPIDepthError()
	}
	if err := b.charge(64+openAPIStringSize(node.Value), 1); err != nil {
		return err
	}
	// The loader treats implicit timestamps as strings.
	if node.Tag == "!!timestamp" && node.Style&yaml.TaggedStyle == 0 {
		node.Tag = "!!str"
	}
	visiting[node] = true
	defer delete(visiting, node)
	if node.Kind == yaml.AliasNode {
		return b.yamlNode(node.Alias, depth+1, visiting)
	}
	for index, child := range node.Content {
		if err := b.yamlNode(child, depth+1, visiting); err != nil {
			return err
		}
		if node.Kind == yaml.MappingNode && index%2 == 0 && child.Tag != "!!merge" {
			var key any
			if err := child.Decode(&key); err != nil {
				return xerror.Unexpected().With(err)
			}
			switch key.(type) {
			case string, int, int64, float64, bool:
				// JSON object keys use the loader's scalar normalization.
				*child = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(key)}
			default:
				return xerror.Unexpected().Detail("OpenAPI YAML object key must be a string, number or boolean")
			}
		}
	}
	return nil
}

func (b *openAPIWorkBudget) value(value, root any, depth int, copies int64, followRefs bool, visiting map[string]bool) error {
	if depth > maxOpenAPIExpansionDepth {
		return openAPIDepthError()
	}
	// Bound expanded entries and serialized strings; draft prefixes have a separate guard.
	if err := b.charge(16, copies); err != nil {
		return err
	}
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if err := b.charge(openAPIStringSize(key), copies); err != nil {
				return err
			}
			if err := b.value(child, root, depth+1, copies, followRefs, visiting); err != nil {
				return err
			}
		}
		for name, refValue := range value {
			ref, ok := refValue.(string)
			if !followRefs || !ok || !strings.EqualFold(name, "$ref") {
				continue
			}
			targets, key, err := b.localReferences(root, ref)
			if err != nil {
				return err
			}
			if len(targets) > 0 && !visiting[key] {
				resolveChildren := true
				if len(targets) == 1 && strings.HasPrefix(key, "/components/schemas/") && strings.Count(key, "/") == 3 {
					// Typed component values are shared; generic extension targets are decoded afresh.
					resolveChildren = !b.typedRefs[key]
					b.typedRefs[key] = true
				}
				visiting[key] = true
				for _, target := range targets {
					if err := b.value(target, root, depth+1, copies, resolveChildren, visiting); err != nil {
						delete(visiting, key)
						return err
					}
				}
				delete(visiting, key)
			}
		}
	case []any:
		for _, child := range value {
			if err := b.value(child, root, depth+1, copies, followRefs, visiting); err != nil {
				return err
			}
		}
	case string:
		return b.charge(openAPIStringSize(value), copies)
	case json.Number:
		return b.charge(int64(len(value)), copies)
	}
	return nil
}

func (b *openAPIWorkBudget) localReferences(root any, ref string) ([]any, string, error) {
	if !strings.HasPrefix(ref, "#") {
		return nil, "", nil
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return nil, "", nil
	}
	if parsed.Fragment == "" || parsed.Fragment == "/" {
		return []any{root}, "/", nil
	}
	if !strings.HasPrefix(parsed.Fragment, "/") {
		return nil, "", nil
	}
	if targets, ok := b.refTargets[parsed.Fragment]; ok {
		return targets, parsed.Fragment, nil
	}
	values := []openAPIReferenceValue{{value: root}}
	// The loader treats an initial empty pointer segment as the extension root.
	pointer := strings.TrimPrefix(parsed.Fragment[1:], "/")
	for part := range strings.SplitSeq(pointer, "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		var next []openAPIReferenceValue
		for _, value := range values {
			switch current := value.value.(type) {
			case map[string]any:
				// Struct field tags are matched without case by encoding/json.
				// Include every spelling, including ambiguous aliases, before resolution.
				fields, ok := b.fields[value.path]
				if !ok {
					if err := b.charge(64, int64(len(current))); err != nil {
						return nil, "", err
					}
					fields = make(map[string][]openAPIReferenceValue, len(current))
					for key, child := range current {
						if err := b.charge(32+int64(len(value.path))+4*int64(len(key)), 1); err != nil {
							return nil, "", err
						}
						name := openAPIFoldName(key)
						fields[name] = append(fields[name], openAPIReferenceValue{value: child, path: value.path + "/" + url.PathEscape(key)})
					}
					b.fields[value.path] = fields
				}
				matches := fields[openAPIFoldName(part)]
				if err := b.charge(32, int64(len(matches))); err != nil {
					return nil, "", err
				}
				next = append(next, matches...)
			case []any:
				index, err := strconv.ParseUint(part, 10, 32)
				if err == nil && index < uint64(len(current)) {
					if err := b.charge(32+int64(len(value.path))+int64(len(part)), 1); err != nil {
						return nil, "", err
					}
					next = append(next, openAPIReferenceValue{value: current[index], path: value.path + "/" + strconv.FormatUint(index, 10)})
				}
			}
		}
		values = next
	}
	if err := b.charge(16, int64(len(values))); err != nil {
		return nil, "", err
	}
	targets := make([]any, len(values))
	for index, value := range values {
		targets[index] = value.value
	}
	b.refTargets[parsed.Fragment] = targets
	return targets, parsed.Fragment, nil
}

func openAPIFoldName(name string) string {
	var folded strings.Builder
	for _, r := range name {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			canonical = min(canonical, next)
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}

func openAPIStringSize(value string) int64 {
	size := int64(2)
	for _, r := range value {
		switch r {
		case '<', '>', '&', '\u2028', '\u2029', utf8.RuneError:
			size += 6
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			size += 2
		default:
			if r < 32 {
				size += 6
			} else {
				size += int64(utf8.RuneLen(r))
			}
		}
	}
	return size
}

func openAPIDepthError() error {
	return xerror.ExecutionUnavailable().Detail("OpenAPI expansion exceeds the 128-level nesting limit")
}

func (b *openAPIWorkBudget) content(content openapi3.Content) error {
	for name, media := range content {
		if err := b.charge(64+openAPIStringSize(name), 1); err != nil {
			return err
		}
		if media != nil {
			if err := b.summary(media.Schema, 0, 1, map[*openapi3.Schema]bool{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *openAPIWorkBudget) summary(ref *openapi3.SchemaRef, depth int, copies int64, visiting map[*openapi3.Schema]bool) error {
	if ref == nil || ref.Value == nil {
		return nil
	}
	if depth > maxOpenAPIExpansionDepth {
		return openAPIDepthError()
	}
	schema := ref.Value
	if err := b.charge(64+openAPIStringSize(schema.Title)+openAPIStringSize(schema.Format)+openAPIStringSize(schema.Description), copies); err != nil {
		return err
	}
	if schema.Type != nil {
		for _, name := range schema.Type.Slice() {
			if err := b.charge(16+openAPIStringSize(name), copies); err != nil {
				return err
			}
		}
	}
	for _, name := range schema.Required {
		if err := b.charge(16+openAPIStringSize(name), copies); err != nil {
			return err
		}
	}
	if err := b.value(schema.Enum, nil, depth+1, copies, false, nil); err != nil {
		return err
	}
	if visiting[schema] {
		return nil
	}
	visiting[schema] = true
	defer delete(visiting, schema)
	for name, property := range schema.Properties {
		if err := b.charge(32+openAPIStringSize(name), copies); err != nil {
			return err
		}
		if err := b.summary(property, depth+1, copies, visiting); err != nil {
			return err
		}
	}
	for _, child := range []*openapi3.SchemaRef{schema.Items, schema.AdditionalProperties.Schema} {
		if err := b.summary(child, depth+1, copies, visiting); err != nil {
			return err
		}
	}
	for index, children := range []openapi3.SchemaRefs{schema.OneOf, schema.AnyOf, schema.AllOf} {
		childCopies := copies
		if index == 0 {
			// oneOf can serialize each property again through the merged properties map.
			if err := b.charge(1, copies); err != nil {
				return err
			}
			childCopies *= 2
		}
		for _, child := range children {
			if err := b.summary(child, depth+1, childCopies, visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *openAPIWorkBudget) draftBody(schema *openapi3.Schema, depth int, prefix int64, visiting map[*openapi3.Schema]bool) error {
	if schema == nil || visiting[schema] {
		return nil
	}
	if depth > maxOpenAPIExpansionDepth {
		return openAPIDepthError()
	}
	if err := b.charge(64*int64(depth+1)+4*prefix+openAPIStringSize(schema.Description), 1); err != nil {
		return err
	}
	visiting[schema] = true
	defer delete(visiting, schema)
	for name, property := range schema.Properties {
		if property == nil {
			continue
		}
		if err := b.draftBody(property.Value, depth+1, prefix+int64(len(name))+1, visiting); err != nil {
			return err
		}
	}
	return nil
}
