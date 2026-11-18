package sinkbinding

import "context"

// HTTPRecordUpdate carries the bounded sink's desired state across the encrypted executor boundary.
type HTTPRecordUpdate struct {
	RecordID   string            `json:"record_id"`
	Properties map[string]string `json:"properties"`
	Reconcile  bool              `json:"reconcile,omitempty"`
}

type httpRecordUpdateKey struct{}

// WithHTTPRecordUpdate binds one company mutation or readback to a sink dispatch.
func WithHTTPRecordUpdate(ctx context.Context, recordID string, properties map[string]string, reconcile bool) context.Context {
	return context.WithValue(ctx, httpRecordUpdateKey{}, &HTTPRecordUpdate{RecordID: recordID, Properties: properties, Reconcile: reconcile})
}

// HTTPRecordUpdateFrom returns nil for ordinary single-request sinks.
func HTTPRecordUpdateFrom(ctx context.Context) *HTTPRecordUpdate {
	update, _ := ctx.Value(httpRecordUpdateKey{}).(*HTTPRecordUpdate)
	return update
}

// ValidRecordUpdateOperation restricts row data to one path segment and projected properties.
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
	body, ok := operation["body"].(map[string]any)
	return ok && len(body) == 1 && body["properties"] == "{{input}}" &&
		operation["method"] == "PATCH" && operation["path"] == "/crm/v3/objects/companies/{{record_id}}"
}
