package sinkbinding

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// RecordReadback describes a JSON response containing the record identity and mapped field values.
type RecordReadback struct {
	IdentityField   string `json:"identity_field"`
	FieldsObject    string `json:"fields_object,omitempty"`
	SelectParameter string `json:"select_parameter,omitempty"`
}

var (
	recordFieldName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	recordPathSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
)

// ValidRecordField reports whether a name identifies a single literal field.
func ValidRecordField(name string) bool { return recordFieldName.MatchString(name) }

// Valid reports whether a response can carry the configured identity and fields.
func (r RecordReadback) Valid() bool {
	return ValidRecordField(r.IdentityField) &&
		(r.FieldsObject == "" || (ValidRecordField(r.FieldsObject) && r.FieldsObject != r.IdentityField)) &&
		(r.SelectParameter == "" || ValidRecordField(r.SelectParameter))
}

// HTTPRecordUpdate carries the bounded sink's desired state across the encrypted executor boundary.
type HTTPRecordUpdate struct {
	RecordID   string            `json:"record_id"`
	Properties map[string]string `json:"properties"`
	Reconcile  bool              `json:"reconcile,omitempty"`
	Readback   RecordReadback    `json:"readback"`
}

type httpRecordUpdateKey struct{}

// WithHTTPRecordUpdate binds one record mutation or readback to a sink dispatch.
func WithHTTPRecordUpdate(ctx context.Context, recordID string, properties map[string]string, reconcile bool, readback RecordReadback) context.Context {
	return context.WithValue(ctx, httpRecordUpdateKey{}, &HTTPRecordUpdate{RecordID: recordID, Properties: properties, Reconcile: reconcile, Readback: readback})
}

// HTTPRecordUpdateFrom returns nil for ordinary single-request sinks.
func HTTPRecordUpdateFrom(ctx context.Context) *HTTPRecordUpdate {
	update, _ := ctx.Value(httpRecordUpdateKey{}).(*HTTPRecordUpdate)
	return update
}

// ValidRecordUpdateOperation restricts dispatch to one bound path segment and an explicitly projected JSON body.
func ValidRecordUpdateOperation(operation map[string]any) bool {
	for field := range operation {
		switch field {
		case "method", "path", "body":
		case "type":
			if operation[field] != "http" {
				return false
			}
		default:
			return false
		}
	}
	if operation["method"] != http.MethodPatch && operation["method"] != http.MethodPut {
		return false
	}
	path, ok := operation["path"].(string)
	if !ok || !strings.HasSuffix(path, "/{{record_id}}") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, segment := range segments[:len(segments)-1] {
		if !recordPathSegment.MatchString(segment) || segment == "." || segment == ".." {
			return false
		}
	}
	switch body := operation["body"].(type) {
	case string:
		return body == "{{input}}"
	case map[string]any:
		if len(body) == 1 {
			for key, value := range body {
				return ValidRecordField(key) && value == "{{input}}"
			}
		}
	}
	return false
}
