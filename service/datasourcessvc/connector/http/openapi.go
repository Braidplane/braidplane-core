package http

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type operationResponseContentInfo struct {
	SchemaSummary map[string]any `json:"schema_summary,omitempty"`
}

type operationRequestBodyInfo struct {
	Description string                                  `json:"description,omitempty"`
	Required    bool                                    `json:"required,omitempty"`
	Content     map[string]operationResponseContentInfo `json:"content,omitempty"`
}

type operationResponseInfo struct {
	Description string                                  `json:"description,omitempty"`
	Content     map[string]operationResponseContentInfo `json:"content,omitempty"`
}

// parseOpenAPI parses an OpenAPI specification (JSON or YAML) and extracts HTTP operations.
// The openapi3.Loader automatically detects the format and resolves all $ref references.
func parseOpenAPI(specBytes []byte) (*HTTPSchema, error) {
	return parseOpenAPIWithQuota(context.Background(), specBytes)
}

func parseOpenAPIWithQuota(ctx context.Context, specBytes []byte) (*HTTPSchema, error) {
	budget, err := preflightOpenAPI(ctx, specBytes)
	if err != nil {
		return nil, err
	}
	loader := openapi3.NewLoader()
	loader.Context = ctx
	// External references would perform additional provider reads outside the
	// vetted, byte-limited OpenAPI fetch boundary.
	loader.IsExternalRefsAllowed = false

	spec, err := loader.LoadFromData(specBytes)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	if spec.Info == nil {
		return nil, xerror.Unexpected().Detail("OpenAPI spec must have an info section")
	}

	schema := &HTTPSchema{
		Operations: []Operation{},
	}

	if spec.Paths == nil {
		return schema, nil
	}
	securityWork := openAPISecurityWork(spec.Security)

	// Extract operations from paths
	for _, path := range spec.Paths.InMatchingOrder() {
		pathItem := spec.Paths.Find(path)
		if pathItem == nil {
			continue
		}

		// Check all HTTP methods in order
		methods := map[string]*openapi3.Operation{
			"GET":     pathItem.Get,
			"POST":    pathItem.Post,
			"PUT":     pathItem.Put,
			"DELETE":  pathItem.Delete,
			"PATCH":   pathItem.Patch,
			"OPTIONS": pathItem.Options,
			"HEAD":    pathItem.Head,
			"TRACE":   pathItem.Trace,
		}

		for method, operation := range methods {
			if operation == nil {
				continue
			}
			if err := schemaquota.Charge(ctx, 1, 0); err != nil {
				return nil, err
			}
			if err := budget.charge(256+openAPIStringSize(path)+openAPIStringSize(operation.OperationID)+openAPIStringSize(operation.Description), 1); err != nil {
				return nil, err
			}
			if err := budget.charge(int64(len(path)), int64(len(pathItem.Parameters)+len(operation.Parameters))); err != nil {
				return nil, err
			}
			operationSecurityWork := securityWork
			if operation.Security != nil {
				operationSecurityWork = openAPISecurityWork(*operation.Security)
			}
			if err := budget.charge(operationSecurityWork, 1); err != nil {
				return nil, err
			}
			if operation.RequestBody != nil && operation.RequestBody.Value != nil {
				if err := budget.charge(64+openAPIStringSize(operation.RequestBody.Value.Description), 1); err != nil {
					return nil, err
				}
				if err := budget.content(operation.RequestBody.Value.Content); err != nil {
					return nil, err
				}
				for _, media := range operation.RequestBody.Value.Content {
					if media != nil && media.Schema != nil {
						if err := budget.draftBody(media.Schema.Value, 0, 4, map[*openapi3.Schema]bool{}); err != nil {
							return nil, err
						}
					}
				}
			}
			if operation.Responses != nil {
				if err := budget.charge(64, int64(operation.Responses.Len())); err != nil {
					return nil, err
				}
				for status, response := range operation.Responses.Map() {
					if err := budget.charge(openAPIStringSize(status), 1); err != nil {
						return nil, err
					}
					if response == nil || response.Value == nil {
						continue
					}
					if response.Value.Description != nil {
						if err := budget.charge(openAPIStringSize(*response.Value.Description), 1); err != nil {
							return nil, err
						}
					}
					if err := budget.content(response.Value.Content); err != nil {
						return nil, err
					}
				}
			}

			opID := operation.OperationID
			if opID == "" {
				opID = generateOperationID(method, path)
			}
			requestBodyJSON, err := summarizeOperationRequestBody(operation.RequestBody)
			if err != nil {
				return nil, xerror.Unexpected().With(err)
			}

			responsesJSON, err := summarizeOperationResponses(operation.Responses)
			if err != nil {
				return nil, xerror.Unexpected().With(err)
			}
			draftJSON, diagnostics, err := generateOpenAPIDraft(spec, path, method, pathItem, operation)
			if err != nil {
				return nil, xerror.Unexpected().With(err)
			}
			if err := schemaquota.Charge(ctx, 0, len(opID)+len(path)+len(method)+len(operation.Description)+len(requestBodyJSON)+len(responsesJSON)+len(draftJSON)+diagnosticsLen(diagnostics)); err != nil {
				return nil, err
			}

			op := Operation{
				OperationID: opID,
				Path:        path,
				Method:      method,
				Description: operation.Description,
				RequestBody: requestBodyJSON,
				Responses:   responsesJSON,
				Draft:       draftJSON,
				Diagnostics: diagnostics,
			}

			schema.Operations = append(schema.Operations, op)
		}
	}

	return schema, nil
}

func summarizeOperationRequestBody(requestBody *openapi3.RequestBodyRef) (json.RawMessage, error) {
	if requestBody == nil || requestBody.Value == nil {
		return nil, nil
	}

	summary := operationRequestBodyInfo{
		Description: requestBody.Value.Description,
		Required:    requestBody.Value.Required,
	}

	content, err := summarizeContent(requestBody.Value.Content)
	if err != nil {
		return nil, err
	}
	if len(content) > 0 {
		summary.Content = content
	}

	if summary.Description == "" && !summary.Required && len(summary.Content) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}

	return json.RawMessage(encoded), nil
}

func summarizeOperationResponses(responses *openapi3.Responses) (json.RawMessage, error) {
	if responses == nil {
		return nil, nil
	}

	responseMap := responses.Map()
	if len(responseMap) == 0 {
		return nil, nil
	}

	summary := make(map[string]operationResponseInfo, len(responseMap))
	for statusCode, responseRef := range responseMap {
		if responseRef == nil || responseRef.Value == nil {
			continue
		}

		statusInfo := operationResponseInfo{}
		if responseRef.Value.Description != nil {
			statusInfo.Description = *responseRef.Value.Description
		}

		content, err := summarizeContent(responseRef.Value.Content)
		if err != nil {
			return nil, err
		}
		if len(content) > 0 {
			statusInfo.Content = content
		}

		// Only include this status code if there is a non-empty description or content.
		if statusInfo.Description == "" && len(statusInfo.Content) == 0 {
			continue
		}

		summary[statusCode] = statusInfo
	}

	if len(summary) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}

	return json.RawMessage(encoded), nil
}

func summarizeContent(content openapi3.Content) (map[string]operationResponseContentInfo, error) {
	if len(content) == 0 {
		return map[string]operationResponseContentInfo{}, nil
	}

	summary := make(map[string]operationResponseContentInfo, len(content))
	for mediaType, mediaTypeSchema := range content {
		if mediaTypeSchema == nil {
			continue
		}

		contentInfo := operationResponseContentInfo{}
		if mediaTypeSchema.Schema != nil {
			schemaSummary := summarizeSchemaRef(mediaTypeSchema.Schema, map[*openapi3.Schema]bool{})
			if len(schemaSummary) > 0 {
				contentInfo.SchemaSummary = schemaSummary
			}
		}

		summary[mediaType] = contentInfo
	}

	if len(summary) == 0 {
		return map[string]operationResponseContentInfo{}, nil
	}

	return summary, nil
}

func summarizeSchemaRef(
	schemaRef *openapi3.SchemaRef,
	visiting map[*openapi3.Schema]bool,
) map[string]any {
	if schemaRef == nil || schemaRef.Value == nil {
		return nil
	}

	schema := schemaRef.Value
	if visiting[schema] {
		return summarizeSchemaBase(schema)
	}

	visiting[schema] = true
	defer delete(visiting, schema)

	summary := summarizeSchemaBase(schema)

	if len(schema.Properties) > 0 {
		properties := make(map[string]any, len(schema.Properties))
		for name, propertyRef := range schema.Properties {
			propertySummary := summarizeSchemaRef(propertyRef, visiting)
			if len(propertySummary) == 0 {
				properties[name] = map[string]any{}
				continue
			}
			properties[name] = propertySummary
		}
		summary["properties"] = properties
	}

	if schema.Items != nil {
		items := summarizeSchemaRef(schema.Items, visiting)
		if len(items) > 0 {
			summary["items"] = items
		}
	}

	if schema.AdditionalProperties.Schema != nil {
		additional := summarizeSchemaRef(schema.AdditionalProperties.Schema, visiting)
		if len(additional) > 0 {
			summary["additional_properties"] = additional
		}
	} else if schema.AdditionalProperties.Has != nil {
		summary["additional_properties"] = *schema.AdditionalProperties.Has
	}

	if oneOf := summarizeSchemaRefs(schema.OneOf, visiting); len(oneOf) > 0 {
		summary["one_of"] = oneOf
		if _, hasProperties := summary["properties"]; !hasProperties {
			if properties := mergePropertiesFromSchemaSummaries(oneOf); len(properties) > 0 {
				summary["properties"] = properties
			}
		}
	}
	if anyOf := summarizeSchemaRefs(schema.AnyOf, visiting); len(anyOf) > 0 {
		summary["any_of"] = anyOf
	}
	if allOf := summarizeSchemaRefs(schema.AllOf, visiting); len(allOf) > 0 {
		summary["all_of"] = allOf
	}

	return summary
}

func mergePropertiesFromSchemaSummaries(summaries []any) map[string]any {
	properties := make(map[string]any)
	for _, item := range summaries {
		summary, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemProperties, ok := summary["properties"].(map[string]any)
		if !ok {
			continue
		}
		for name, property := range itemProperties {
			if _, exists := properties[name]; exists {
				continue
			}
			properties[name] = property
		}
	}

	if len(properties) == 0 {
		return nil
	}

	return properties
}

func summarizeSchemaBase(schema *openapi3.Schema) map[string]any {
	if schema == nil {
		return nil
	}

	summary := make(map[string]any)

	if schema.Type != nil {
		types := schema.Type.Slice()
		switch len(types) {
		case 1:
			summary["type"] = types[0]
		case 2, 3, 4, 5:
			summary["type"] = types
		default:
			if len(types) >= 2 {
				summary["type"] = types
			}
		}
	}

	if schema.Title != "" {
		summary["title"] = schema.Title
	}
	if schema.Format != "" {
		summary["format"] = schema.Format
	}
	if schema.Description != "" {
		summary["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		summary["enum"] = append([]any(nil), schema.Enum...)
	}
	if schema.Nullable {
		summary["nullable"] = true
	}
	if len(schema.Required) > 0 {
		summary["required"] = append([]string(nil), schema.Required...)
	}

	return summary
}

func summarizeSchemaRefs(schemaRefs openapi3.SchemaRefs, visiting map[*openapi3.Schema]bool) []any {
	if len(schemaRefs) == 0 {
		return nil
	}

	summary := make([]any, 0, len(schemaRefs))
	for _, schemaRef := range schemaRefs {
		schemaSummary := summarizeSchemaRef(schemaRef, visiting)
		if len(schemaSummary) == 0 {
			continue
		}
		summary = append(summary, schemaSummary)
	}

	if len(summary) == 0 {
		return nil
	}

	return summary
}

// generateOperationID creates an operationId from HTTP method and path.
// Example: GET /users/{id} -> getUsersId
func generateOperationID(method, path string) string {
	// Remove leading slash and split by /
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return strings.ToLower(method)
	}

	parts := strings.Split(path, "/")
	var result strings.Builder

	// Start with method in lowercase
	result.WriteString(strings.ToLower(method))

	// Capitalize each path segment and remove parameter braces
	for _, part := range parts {
		if part == "" {
			continue
		}

		// Remove parameter braces: {id} -> id
		part = strings.TrimPrefix(part, "{")
		part = strings.TrimSuffix(part, "}")

		if part == "" {
			continue
		}

		// Capitalize first letter
		if len(part) > 0 {
			result.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}

	return result.String()
}
