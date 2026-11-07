// Package odcs preserves admitted ODCS documents and derives restricted executable shapes.
package odcs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	MaxDocumentBytes = 1 << 20
	maxDepth         = 64
)

var ErrDocument = xerror.New(xerror.KindInvalidInput, "ODCS_DOCUMENT_INVALID", "invalid ODCS catalog document")

// Record validates admission before deriving a digest; no declaration is removed.
func Record(raw json.RawMessage, expectedID string) (*model.ODCSRevision, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, ErrDocument.WithPublicDetail("document exceeds 1 MiB")
	}
	if err := checkJSON(raw, 0); err != nil {
		return nil, err
	}
	var document map[string]any
	if err := jsonvalue.Unmarshal(raw, &document); err != nil || document == nil {
		return nil, ErrDocument.WithPublicDetail("document must be a JSON object")
	}
	if document["kind"] != "DataContract" || document["apiVersion"] != "v3.2.0" {
		return nil, ErrDocument.WithPublicDetail("kind must be DataContract and apiVersion must be v3.2.0")
	}
	id, ok := document["id"].(string)
	if !ok || id != expectedID || variableToken.MatchString(id) {
		return nil, ErrDocument.WithPublicDetail("id must match the catalog identity")
	}
	version, ok := document["version"].(string)
	if !ok || strings.TrimSpace(version) == "" || len(version) > 255 {
		return nil, ErrDocument.WithPublicDetail("version must be a nonempty string of at most 255 bytes")
	}
	status := ""
	if value, present := document["status"]; present {
		var valid bool
		status, valid = value.(string)
		if !valid || len(status) > 255 {
			return nil, ErrDocument.WithPublicDetail("status must be a string of at most 255 bytes")
		}
	}
	validator, err := compiledSchema()
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("compile pinned ODCS schema")
	}
	if err := validator.Validate(document); err != nil {
		return nil, ErrDocument.WithPublicDetail("document does not satisfy pinned ODCS v3.2.0")
	}
	if _, ok := document["schema"]; !ok {
		return nil, ErrDocument.WithPublicDetail("ODCS v3.2.0 requires a schema declaration")
	}
	if err := checkDeclarations(document, ""); err != nil {
		return nil, err
	}
	if err := checkContent(document); err != nil {
		return nil, err
	}
	schema, _ := document["schema"].([]any)
	objects := make([]model.ODCSSchemaObject, 0, len(schema))
	for _, value := range schema {
		object := value.(map[string]any)
		objectID, _ := object["id"].(string)
		if objectID != "" && !variableToken.MatchString(objectID) {
			objects = append(objects, model.ODCSSchemaObject{ID: objectID, Name: object["name"].(string)})
		}
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, ErrDocument
	}
	if len(canonical) > MaxDocumentBytes {
		return nil, ErrDocument.WithPublicDetail("canonical document exceeds 1 MiB")
	}
	digest := sha256.Sum256(canonical)
	return &model.ODCSRevision{
		Digest: "sha256:" + hex.EncodeToString(digest[:]), ODCSVersion: "v3.2.0",
		DocumentVersion: version, DeclaredStatus: status, Status: "recorded",
		SchemaObjects: objects, Document: canonical,
	}, nil
}

// CheckRequestJSON allows the request envelope around a document's 64-level bound.
func CheckRequestJSON(raw []byte) error { return checkJSON(raw, -1) }

func checkJSON(raw []byte, depth int) error {
	if !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return ErrDocument.WithPublicDetail("document must contain valid Unicode")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := checkValue(decoder, depth); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrDocument.WithPublicDetail("expected one JSON document")
	}
	return nil
}

func checkValue(decoder *json.Decoder, depth int) error {
	if depth > maxDepth {
		return ErrDocument.WithPublicDetail("document nesting exceeds 64 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrDocument.WithPublicDetail("malformed JSON document")
	}
	delim, composite := token.(json.Delim)
	if !composite {
		if number, ok := token.(json.Number); ok {
			literal := number.String()
			if len(literal) > 1024 {
				return ErrDocument.WithPublicDetail("numeric literals must not exceed 1024 bytes")
			}
			if at := strings.IndexAny(literal, "eE"); at >= 0 {
				exponent, err := strconv.Atoi(literal[at+1:])
				if err != nil || exponent < -4096 || exponent > 4096 {
					return ErrDocument.WithPublicDetail("numeric exponents must be between -4096 and 4096")
				}
			}
		}
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ErrDocument.WithPublicDetail("malformed JSON object")
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return ErrDocument.WithPublicDetail("duplicate JSON object key")
			}
			seen[key] = true
			if err := checkValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for i := 0; decoder.More(); i++ {
			if err := checkValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrDocument.WithPublicDetail("malformed JSON document")
	}
	if _, err := decoder.Token(); err != nil {
		return ErrDocument.WithPublicDetail("malformed JSON document")
	}
	return nil
}

func normalizedKey(key string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
}

var variableToken = regexp.MustCompile(variablePattern)

var unresolvedSecret = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Content checks apply to opaque values too; they never resolve or execute them.
func checkContent(value any) error {
	if value, ok := value.(map[string]any); ok {
		for _, key := range sortedKeys(value) {
			if secretKey(key) && !safeSecretValue(value[key]) {
				return ErrDocument.WithPublicDetail("literal secret-bearing content is not admitted; use an unresolved variable token")
			}
			if err := checkContent(value[key]); err != nil {
				return err
			}
		}
		if key, ok := value["property"].(string); ok && secretKey(key) && !safeSecretValue(value["value"]) {
			return ErrDocument.WithPublicDetail("literal secret-bearing custom property is not admitted; use an unresolved variable token")
		}
	}
	if values, ok := value.([]any); ok {
		for _, child := range values {
			if err := checkContent(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func secretKey(key string) bool {
	normalized := normalizedKey(key)
	switch normalized {
	case "connectionstring", "authorization", "passwd":
		return true
	}
	for _, suffix := range []string{"secret", "secrets", "token", "password", "apikey", "privatekey", "accesskey", "clientkey", "secretkey"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return strings.Contains(normalized, "credential")
}

func safeSecretValue(value any) bool {
	text, ok := value.(string)
	return value == nil || ok && (text == "" || unresolvedSecret.MatchString(text))
}

// Opaque extension values and engine implementations are not ODCS declaration trees.
func checkDeclarations(value any, section string) error {
	switch value := value.(type) {
	case map[string]any:
		if section == "relationships" {
			if id, ok := value["id"].(string); ok && !variableToken.MatchString(id) && (id == "" || strings.ContainsAny(id, ".#/\\@!%&^") || strings.ContainsFunc(id, unicode.IsSpace)) {
				return ErrDocument.WithPublicDetail("relationship IDs must be stable reference identifiers")
			}
			from, fromArray := value["from"].([]any)
			to, toArray := value["to"].([]any)
			if fromArray && toArray && len(from) != len(to) {
				return ErrDocument.WithPublicDetail("composite relationship sides must have equal lengths")
			}
		}
		if err := checkEnum(value); err != nil {
			return err
		}
		for _, key := range sortedKeys(value) {
			if key == "value" && (section == "customProperties" || section == "enum") || key == "implementation" || key == "examples" || key == "arguments" {
				continue
			}
			if err := checkDeclarations(value[key], key); err != nil {
				return err
			}
		}
	case []any:
		seen := map[string]bool{}
		for _, child := range value {
			if object, ok := child.(map[string]any); ok {
				if id, ok := object["id"].(string); ok && section != "constraints" && section != "verifiedStatements" {
					if seen[id] {
						return ErrDocument.WithPublicDetail("declaration IDs must be unique within their containing array")
					}
					seen[id] = true
				}
			}
			if err := checkDeclarations(child, section); err != nil {
				return err
			}
		}
	}
	return nil
}

func containsVariable(value any) bool {
	switch value := value.(type) {
	case string:
		return variableToken.MatchString(value)
	case map[string]any:
		for _, child := range value {
			if containsVariable(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsVariable(child) {
				return true
			}
		}
	}
	return false
}

func checkEnum(property map[string]any) error {
	entries, _ := property["enum"].([]any)
	logicalType, _ := property["logicalType"].(string)
	seen := map[string]bool{}
	for _, entry := range entries {
		value := entry.(map[string]any)["value"]
		var key string
		compatible := logicalType == "" || variableToken.MatchString(logicalType)
		switch value := value.(type) {
		case string:
			key = "string:" + value
			compatible = compatible || variableToken.MatchString(value) || logicalType == "string" || logicalType == "date" || logicalType == "timestamp" || logicalType == "time"
		case bool:
			if value {
				key = "boolean:true"
			} else {
				key = "boolean:false"
			}
			compatible = compatible || logicalType == "boolean"
		case json.Number:
			number, ok := new(big.Rat).SetString(value.String())
			if !ok {
				return ErrDocument.WithPublicDetail("enum value must be an exact number")
			}
			key = "number:" + number.RatString()
			compatible = compatible || logicalType == "number" || logicalType == "integer" && number.IsInt()
		default:
			return ErrDocument.WithPublicDetail("enum values must be non-null scalars")
		}
		if !compatible || seen[key] {
			return ErrDocument.WithPublicDetail("enum values must be unique and compatible with the declared logical type")
		}
		seen[key] = true
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 escapes; admission must not change values.
func validJSONSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' || i+4 >= len(raw) {
			continue
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			continue // The JSON decoder reports malformed escapes.
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
