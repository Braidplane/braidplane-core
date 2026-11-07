package notificationsvc

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var unsafeNotificationConfigKeys = map[string]struct{}{
	"authorization":         {},
	"authorization_header":  {},
	"cookie":                {},
	"cookies":               {},
	"credential":            {},
	"credentials":           {},
	"file_contents":         {},
	"header":                {},
	"headers":               {},
	"password":              {},
	"payload":               {},
	"provider_response":     {},
	"raw_file":              {},
	"raw_payload":           {},
	"raw_provider_response": {},
	"raw_query_result":      {},
	"refresh_token":         {},
	"request_body":          {},
	"request_header":        {},
	"request_headers":       {},
	"runtime_input":         {},
	"runtime_input_values":  {},
	"session":               {},
	"session_id":            {},
	"token":                 {},
}

var unsafeNotificationConfigKeyAliases = map[string]struct{}{
	"accesskey":           {},
	"apikey":              {},
	"authorizationheader": {},
	"bearertoken":         {},
	"clientsecret":        {},
	"privatekey":          {},
	"secretkey":           {},
	"signingkey":          {},
	"webhooksecret":       {},
}

// normalizeSafeMap validates a displayable JSON object and encodes it for JSONB storage.
func normalizeSafeMap(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	if err := rejectUnsafeKeys(value, ""); err != nil {
		return nil, err
	}
	return encodeJSON(value)
}

// normalizeSourceRefs validates source references and encodes them for JSONB storage.
func normalizeSourceRefs(value map[string]string) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	for key := range value {
		if unsafeNotificationKey(key) {
			return nil, ErrInvalidInput.Detailf("notification source ref %q must not contain secret material", key)
		}
	}
	return encodeJSON(value)
}

// rejectUnsafeKeys recursively rejects key names associated with secret or raw payload data.
func rejectUnsafeKeys(value any, path string) error {
	return rejectUnsafeValue(reflect.ValueOf(value), path)
}

// rejectUnsafeValue walks generic nested JSON-like maps and slices by reflection.
func rejectUnsafeValue(value reflect.Value, path string) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		for _, keyValue := range value.MapKeys() {
			nested := value.MapIndex(keyValue)
			if keyValue.Kind() == reflect.String {
				key := keyValue.String()
				nextPath := joinJSONPath(path, key)
				if unsafeNotificationKey(key) {
					return ErrInvalidInput.Detailf("notification config key %q must not contain secret material", nextPath)
				}
				if err := rejectUnsafeValue(nested, nextPath); err != nil {
					return err
				}
				continue
			}
			if err := rejectUnsafeValue(nested, path); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := rejectUnsafeValue(value.Index(i), path); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	return nil
}

// unsafeNotificationKey reports whether a JSON key name is reserved for secret-bearing data.
func unsafeNotificationKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(key))
	if _, ok := unsafeNotificationConfigKeys[normalized]; ok {
		return true
	}
	compact := strings.ReplaceAll(normalized, "_", "")
	if _, ok := unsafeNotificationConfigKeyAliases[compact]; ok {
		return true
	}
	return strings.Contains(compact, "authorization") ||
		strings.Contains(compact, "bearer") ||
		strings.Contains(compact, "token") ||
		strings.Contains(compact, "password") ||
		strings.Contains(compact, "credential") ||
		strings.Contains(compact, "secret") ||
		strings.Contains(compact, "apikey") ||
		strings.Contains(compact, "accesskey") ||
		strings.Contains(compact, "privatekey")
}

// encodeJSON marshals a validated object and wraps encoder failures as invalid input.
func encodeJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, xerror.Wrapf("encode notification json: %w", ErrInvalidInput.With(err))
	}
	return json.RawMessage(encoded), nil
}

// joinJSONPath appends a key to a display path for validation errors.
func joinJSONPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
