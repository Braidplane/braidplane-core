package http

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type httpAcknowledgement struct {
	StatusCodes []int                  `json:"status_codes"`
	Success     []acknowledgementCheck `json:"success"`
	EmptyFields []string               `json:"empty_fields,omitempty"`
}

type acknowledgementCheck struct {
	Path   string          `json:"path"`
	Equals json.RawMessage `json:"equals"`
}

func invalidAcknowledgement() *xerror.Error {
	return xerror.InvalidInput(xerror.Query).WithPublicDetail("HTTP acknowledgement requires 1-16 distinct 2xx status codes, 1-8 scalar equality checks with object-only paths, and at most 8 distinct empty field names")
}

func decodeAcknowledgement(raw json.RawMessage) (*httpAcknowledgement, error) {
	if _, err := acknowledgementJSON(raw); err != nil {
		return nil, invalidAcknowledgement()
	}
	var a *httpAcknowledgement
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil || a == nil {
		return nil, invalidAcknowledgement()
	}
	if len(a.StatusCodes) < 1 || len(a.StatusCodes) > 16 || len(a.Success) < 1 || len(a.Success) > 8 || len(a.EmptyFields) > 8 {
		return nil, invalidAcknowledgement()
	}
	statuses := map[int]bool{}
	for _, status := range a.StatusCodes {
		if status < 200 || status > 299 || statuses[status] {
			return nil, invalidAcknowledgement()
		}
		statuses[status] = true
	}
	for _, check := range a.Success {
		parts := strings.Split(check.Path, ".")
		if len(check.Path) == 0 || len(check.Path) > 256 || len(parts) > 16 || strings.ContainsAny(check.Path, "[]*{}") {
			return nil, invalidAcknowledgement()
		}
		for _, part := range parts {
			if part == "" {
				return nil, invalidAcknowledgement()
			}
		}
		value, err := acknowledgementJSON(check.Equals)
		if err != nil || !acknowledgementScalar(value) {
			return nil, invalidAcknowledgement()
		}
	}
	fields := map[string]bool{}
	for _, name := range a.EmptyFields {
		if len(name) == 0 || len(name) > 64 || fields[name] {
			return nil, invalidAcknowledgement()
		}
		fields[name] = true
	}
	return a, nil
}

func acknowledgementJSON(raw []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var err error
	value, err = acknowledgementValue(decoder, 0)
	if err != nil {
		return nil, invalidAcknowledgement().With(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, invalidAcknowledgement()
	}
	return value, nil
}

// Duplicate object keys cannot provide unambiguous delivery evidence.
func acknowledgementValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, invalidAcknowledgement()
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, invalidAcknowledgement().With(err)
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, invalidAcknowledgement().With(err)
			}
			name, ok := key.(string)
			if !ok {
				return nil, invalidAcknowledgement()
			}
			if _, exists := object[name]; exists {
				return nil, invalidAcknowledgement()
			}
			value, err := acknowledgementValue(decoder, depth+1)
			if err != nil {
				return nil, invalidAcknowledgement().With(err)
			}
			object[name] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, invalidAcknowledgement()
		}
		return object, nil
	case json.Delim('['):
		array := []any{}
		for decoder.More() {
			value, err := acknowledgementValue(decoder, depth+1)
			if err != nil {
				return nil, invalidAcknowledgement().With(err)
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, invalidAcknowledgement()
		}
		return array, nil
	default:
		return token, nil
	}
}

func acknowledgementScalar(value any) bool {
	switch value.(type) {
	case string, bool, json.Number:
		return true
	default:
		return false
	}
}

func (a *httpAcknowledgement) evaluate(status int, body []byte) error {
	rejected := func() error {
		return xerror.HTTPAcknowledgementRejected().WithPublicDetail("HTTP response rejected by the acknowledgement contract. A rejected acknowledgement does not prove that a write was rolled back; verify remote state before retrying.")
	}
	uncertain := func() error {
		return xerror.HTTPAcknowledgementUncertain().WithPublicDetail("HTTP response has missing or malformed acknowledgement evidence. Verify remote state before retrying a write.")
	}
	acceptedStatus := false
	for _, expected := range a.StatusCodes {
		acceptedStatus = acceptedStatus || status == expected
	}
	if !acceptedStatus {
		return rejected()
	}
	root, err := acknowledgementJSON(body)
	if err != nil {
		return uncertain()
	}
	// Decode once; providers may return validation errors below arbitrarily named records.
	if !emptyAcknowledgementFields(root, a.EmptyFields) {
		return rejected()
	}
	for _, check := range a.Success {
		value := root
		for _, part := range strings.Split(check.Path, ".") {
			object, ok := value.(map[string]any)
			if !ok {
				return uncertain()
			}
			value, ok = object[part]
			if !ok {
				return uncertain()
			}
		}
		if !acknowledgementScalar(value) {
			return uncertain()
		}
		expected, _ := acknowledgementJSON(check.Equals)
		if value != expected {
			return rejected()
		}
	}
	return nil
}

func emptyAcknowledgementFields(value any, names []string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, nested := range typed {
			for _, forbidden := range names {
				if name == forbidden {
					switch field := nested.(type) {
					case nil:
					case map[string]any:
						if len(field) > 0 {
							return false
						}
					case []any:
						if len(field) > 0 {
							return false
						}
					default:
						return false
					}
				}
			}
			if !emptyAcknowledgementFields(nested, names) {
				return false
			}
		}
	case []any:
		for _, nested := range typed {
			if !emptyAcknowledgementFields(nested, names) {
				return false
			}
		}
	}
	return true
}
