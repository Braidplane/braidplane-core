package http

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

type openAPIDraft struct {
	Statement  openAPIHTTPStatement    `json:"statement"`
	Parameters []openAPIDraftParameter `json:"parameters"`
}

type openAPIHTTPStatement struct {
	Type        string         `json:"type"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	QueryParams map[string]any `json:"query_params,omitempty"`
	Headers     map[string]any `json:"headers,omitempty"`
	Body        any            `json:"body,omitempty"`
}

type openAPIDraftParameter struct {
	Name        string `json:"name"`
	DataType    string `json:"data_type"`
	IsRequired  bool   `json:"is_required"`
	Description string `json:"description,omitempty"`
}

func generateOpenAPIDraft(spec *openapi3.T, path, method string, pathItem *openapi3.PathItem, operation *openapi3.Operation) (json.RawMessage, []string, error) {
	diagnostics := draftPathDiagnostics(path)
	if !draftMethodSupported(method) {
		diagnostics = append(diagnostics, "HTTP method is not supported by query drafts")
	}
	if len(pathItem.Servers) > 0 || (operation.Servers != nil && len(*operation.Servers) > 0) {
		diagnostics = append(diagnostics, "path or operation server overrides are not supported by query drafts")
	}
	if draftHasAPIKeySecurity(spec, operation) {
		diagnostics = append(diagnostics, "OpenAPI apiKey security schemes are not supported by query drafts")
	}

	draft := openAPIDraft{Statement: openAPIHTTPStatement{Type: "http", Method: method, Path: path}}
	usedNames := make(map[string]int)
	parameters, parameterDiagnostics := draftParameters(pathItem.Parameters, operation.Parameters, &draft.Statement, usedNames)
	diagnostics = append(diagnostics, parameterDiagnostics...)
	draft.Parameters = parameters
	if body, bodyParameters, bodyDiagnostics := draftBody(operation.RequestBody, usedNames); len(bodyDiagnostics) > 0 {
		diagnostics = append(diagnostics, bodyDiagnostics...)
	} else if body != nil {
		draft.Statement.Body = body
		draft.Parameters = append(draft.Parameters, bodyParameters...)
	}
	if len(diagnostics) > 0 {
		return nil, diagnostics, nil
	}

	encoded, err := json.Marshal(draft)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(encoded), nil, nil
}

func draftPathDiagnostics(path string) []string {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "://") || strings.ContainsAny(path, "?#") || strings.Contains(path, "{{") || strings.Contains(path, "}}") {
		return []string{"operation path is not a safe relative path"}
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || strings.ContainsAny(decoded, "\\?#%") {
		return []string{"operation path is not a safe relative path"}
	}
	for _, r := range decoded {
		if r < 32 || r == 127 {
			return []string{"operation path is not a safe relative path"}
		}
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return []string{"operation path is not a safe relative path"}
		}
	}
	return nil
}

func draftMethodSupported(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD":
		return true
	default:
		return false
	}
}

func draftHasAPIKeySecurity(spec *openapi3.T, operation *openapi3.Operation) bool {
	requirements := spec.Security
	if operation.Security != nil {
		requirements = *operation.Security
	}
	for _, requirement := range requirements {
		for name := range requirement {
			if spec.Components == nil || spec.Components.SecuritySchemes == nil {
				continue
			}
			scheme := spec.Components.SecuritySchemes[name]
			if scheme != nil && scheme.Value != nil && scheme.Value.Type == "apiKey" {
				return true
			}
		}
	}
	return false
}

func draftParameters(pathParameters, operationParameters openapi3.Parameters, statement *openAPIHTTPStatement, usedNames map[string]int) ([]openAPIDraftParameter, []string) {
	byKey := make(map[string]*openapi3.ParameterRef, len(pathParameters)+len(operationParameters))
	keys := make([]string, 0, len(pathParameters)+len(operationParameters))
	for _, parameters := range []openapi3.Parameters{pathParameters, operationParameters} {
		for _, parameterRef := range parameters {
			if parameterRef == nil || parameterRef.Value == nil {
				continue
			}
			key := parameterRef.Value.In + "\x00" + parameterRef.Value.Name
			if _, exists := byKey[key]; !exists {
				keys = append(keys, key)
			}
			byKey[key] = parameterRef
		}
	}
	sort.Strings(keys)

	parameters := make([]openAPIDraftParameter, 0, len(keys))
	var diagnostics []string
	for _, key := range keys {
		parameter := byKey[key].Value
		if draftCredentialParameter(parameter) {
			diagnostics = append(diagnostics, "credential-like parameter is not supported by query drafts")
			continue
		}
		dataType, ok := draftScalarDataType(parameter.Schema)
		if parameter.In == openapi3.ParameterInCookie || parameter.Content != nil || parameter.AllowReserved || !draftParameterStyleSupported(parameter) || !ok || !draftParameterNameSafe(parameter) {
			diagnostics = append(diagnostics, "operation has a parameter with unsupported serialization or schema")
			continue
		}
		draftName := draftParameterName(parameter.Name, usedNames)
		placeholder := "{{" + draftName + "}}"
		switch parameter.In {
		case openapi3.ParameterInPath:
			if !parameter.Required || !strings.Contains(statement.Path, "{"+parameter.Name+"}") {
				diagnostics = append(diagnostics, "operation has an invalid path parameter declaration")
				continue
			}
			statement.Path = strings.ReplaceAll(statement.Path, "{"+parameter.Name+"}", placeholder)
		case openapi3.ParameterInQuery:
			if statement.QueryParams == nil {
				statement.QueryParams = map[string]any{}
			}
			statement.QueryParams[parameter.Name] = placeholder
		case openapi3.ParameterInHeader:
			if statement.Headers == nil {
				statement.Headers = map[string]any{}
			}
			statement.Headers[parameter.Name] = placeholder
		default:
			diagnostics = append(diagnostics, "operation has a parameter in an unsupported location")
			continue
		}
		parameters = append(parameters, openAPIDraftParameter{Name: draftName, DataType: dataType, IsRequired: parameter.Required, Description: parameter.Description})
	}
	if draftPathHasUnresolvedTemplate(statement.Path) {
		diagnostics = append(diagnostics, "operation path has an undeclared or malformed path parameter")
	}
	return parameters, diagnostics
}

func draftPathHasUnresolvedTemplate(path string) bool {
	for index := 0; index < len(path); {
		switch path[index] {
		case '{':
			if index+1 < len(path) && path[index+1] == '{' {
				end := strings.Index(path[index+2:], "}}")
				if end < 0 {
					return true
				}
				index += end + 4
				continue
			}
			return true
		case '}':
			return true
		}
		index++
	}
	return false
}

func draftCredentialParameter(parameter *openapi3.Parameter) bool {
	if parameter.In != openapi3.ParameterInHeader && parameter.In != openapi3.ParameterInQuery {
		return false
	}
	name := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(parameter.Name))
	return name == "host" || name == "cookie" || name == "setcookie" || name == "contenttype" || name == "contentlength" || name == "proxyconnection" || name == "connection" || name == "transferencoding" || name == "proxyauthorization" || name == "authorization" || strings.Contains(name, "apikey") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "credential")
}

func draftParameterStyleSupported(parameter *openapi3.Parameter) bool {
	switch parameter.In {
	case openapi3.ParameterInQuery:
		return parameter.Style == "" || parameter.Style == "form"
	case openapi3.ParameterInPath, openapi3.ParameterInHeader:
		return parameter.Style == "" || parameter.Style == "simple"
	default:
		return false
	}
}

func draftScalarDataType(schemaRef *openapi3.SchemaRef) (string, bool) {
	if schemaRef == nil || schemaRef.Value == nil {
		return "", false
	}
	schema := schemaRef.Value
	if len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 || len(schema.AllOf) > 0 || schema.Items != nil || len(schema.Properties) > 0 || schema.AdditionalProperties.Schema != nil || (schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has) || schema.Type == nil || len(schema.Type.Slice()) != 1 {
		return "", false
	}
	switch schema.Type.Slice()[0] {
	case "string":
		if schema.Format == "uuid" {
			return "uuid", true
		}
		if schema.Format == "date" {
			return "date", true
		}
		return "text", true
	case "integer":
		return "integer", true
	case "boolean":
		return "boolean", true
	default:
		return "", false
	}
}

func draftParameterName(source string, used map[string]int) string {
	var builder strings.Builder
	for _, r := range source {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || (r >= '0' && r <= '9' && builder.Len() > 0) {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	name := strings.Trim(builder.String(), "_")
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "param_" + name
	}
	candidate := name
	suffix := max(2, used[name]+1)
	for used[candidate] > 0 {
		candidate = fmt.Sprintf("%s_%d", name, suffix)
		suffix++
	}
	if candidate != name {
		used[name] = suffix - 1
	}
	used[candidate] = 1
	return candidate
}

func openAPISecurityWork(requirements openapi3.SecurityRequirements) int64 {
	work := int64(len(requirements))
	for _, requirement := range requirements {
		for name := range requirement {
			work += int64(len(name)) + 1
		}
	}
	return work
}

func draftBody(requestBodyRef *openapi3.RequestBodyRef, used map[string]int) (any, []openAPIDraftParameter, []string) {
	if requestBodyRef == nil || requestBodyRef.Value == nil {
		return nil, nil, nil
	}
	content := requestBodyRef.Value.Content
	if len(content) != 1 {
		return nil, nil, []string{"request body content is not an unambiguous application/json schema"}
	}
	for mediaType, media := range content {
		if !strings.EqualFold(strings.TrimSpace(strings.Split(mediaType, ";")[0]), "application/json") || media == nil {
			return nil, nil, []string{"request body content is not an unambiguous application/json schema"}
		}
		if media.Schema == nil || media.Schema.Value == nil {
			return nil, nil, []string{"request body schema is unsupported by query drafts"}
		}
		return draftBodyObject(media.Schema.Value, "body", requestBodyRef.Value.Required, used, map[*openapi3.Schema]bool{})
	}
	return nil, nil, nil
}

func draftBodyObject(schema *openapi3.Schema, prefix string, required bool, used map[string]int, visiting map[*openapi3.Schema]bool) (any, []openAPIDraftParameter, []string) {
	if schema == nil || visiting[schema] || schema.Type == nil || len(schema.Type.Slice()) != 1 || schema.Type.Slice()[0] != "object" || len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 || len(schema.AllOf) > 0 || schema.AdditionalProperties.Schema != nil || (schema.AdditionalProperties.Has != nil && *schema.AdditionalProperties.Has) {
		return nil, nil, []string{"request body schema is unsupported by query drafts"}
	}
	visiting[schema] = true
	defer delete(visiting, schema)
	if len(schema.Properties) == 0 {
		return nil, nil, []string{"request body schema is unsupported by query drafts"}
	}
	requiredProperties := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		requiredProperties[name] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	body := make(map[string]any, len(names))
	var parameters []openAPIDraftParameter
	for _, name := range names {
		if strings.Contains(name, "{{") || strings.Contains(name, "}}") {
			return nil, nil, []string{"request body property names cannot contain template expressions"}
		}
		property := schema.Properties[name]
		if property == nil || property.Value == nil {
			return nil, nil, []string{"request body schema is unsupported by query drafts"}
		}
		if property.Value.ReadOnly {
			continue
		}
		propertyRequired := required && requiredProperties[name]
		parameterName := draftParameterName(prefix+"_"+name, used)
		if dataType, ok := draftScalarDataType(property); ok {
			body[name] = "{{" + parameterName + "}}"
			parameters = append(parameters, openAPIDraftParameter{Name: parameterName, DataType: dataType, IsRequired: propertyRequired, Description: property.Value.Description})
			continue
		}
		nested, nestedParameters, diagnostics := draftBodyObject(property.Value, parameterName, propertyRequired, used, visiting)
		if len(diagnostics) > 0 {
			return nil, nil, diagnostics
		}
		body[name] = nested
		parameters = append(parameters, nestedParameters...)
	}
	return body, parameters, nil
}

func diagnosticsLen(diagnostics []string) int {
	length := 0
	for _, diagnostic := range diagnostics {
		length += len(diagnostic)
	}
	return length
}

func draftParameterNameSafe(parameter *openapi3.Parameter) bool {
	if parameter.Name == "" || strings.ContainsAny(parameter.Name, "{}") {
		return false
	}
	for _, r := range parameter.Name {
		if r < 33 || r > 126 {
			return false
		}
		if parameter.In == openapi3.ParameterInHeader && !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
			return false
		}
	}
	return true
}
