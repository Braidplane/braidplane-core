package validate

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

func TestRecordUpdatesDefinition(t *testing.T) {
	const path = "../../../examples/pipelines/yaml/http-inventory-updates.pipeline.yaml"
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = Load(path, source)
	require.NoError(t, err)
	p, err := spec.Load(path, source)
	require.NoError(t, err)
	sink := p.Steps[1].Sink
	sink.OperationInline = map[string]any{"method": "PUT", "path": "/v2/inventory/{{runtime.sink.record_id}}", "body": map[string]any{"attributes": "{{input}}"}}
	cfg := sink.Config["record_updates"].(map[string]any)
	cfg["readback"] = map[string]any{"identity_field": "recordID", "fields_object": "attributes"}
	cfg["fields"] = map[string]string{"label": "Display-Name"}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	_, err = Load("inventory.pipeline.json", data)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		edit func(*spec.SinkSpec)
	}{
		{"missing record mode", func(s *spec.SinkSpec) { delete(s.Config, "record_updates") }},
		{"unbounded", func(s *spec.SinkSpec) { s.Config["record_updates"].(map[string]any)["max_records"] = 101 }},
		{"missing identity", func(s *spec.SinkSpec) { delete(s.Config["record_updates"].(map[string]any), "identity_field") }},
		{"duplicate mapping", func(s *spec.SinkSpec) {
			s.Config["record_updates"].(map[string]any)["fields"] = map[string]string{"first": "name", "second": "name"}
		}},
		{"mapped response ID", func(s *spec.SinkSpec) {
			config := s.Config["record_updates"].(map[string]any)
			config["readback"] = map[string]any{"identity_field": "id"}
			config["fields"] = map[string]string{"item": "id"}
		}},
		{"missing readback", func(s *spec.SinkSpec) { delete(s.Config["record_updates"].(map[string]any), "readback") }},
		{"missing response ID", func(s *spec.SinkSpec) {
			s.Config["record_updates"].(map[string]any)["readback"] = map[string]any{"fields_object": "fields"}
		}},
		{"extra readback option", func(s *spec.SinkSpec) {
			s.Config["record_updates"].(map[string]any)["readback"] = map[string]any{"identity_field": "id", "url": "/elsewhere"}
		}},
		{"unexpected field", func(s *spec.SinkSpec) { s.Config["record_updates"].(map[string]any)["page"] = 1 }},
		{"saved operation", func(s *spec.SinkSpec) { s.OperationInline = nil; s.OperationRef = "old-saved-operation" }},
		{"unexpected path", func(s *spec.SinkSpec) { s.OperationInline["path"] = "/other" }},
		{"unmapped body", func(s *spec.SinkSpec) { s.OperationInline["body"] = "literal" }},
		{"extra HTTP settings", func(s *spec.SinkSpec) { s.OperationInline["write_retry"] = true }},
		{"unsafe replay", func(s *spec.SinkSpec) { s.IdempotencyClass = spec.SinkIdempotencyClassUnsafe }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := spec.Load(path, source)
			require.NoError(t, err)
			sink := p.Steps[1].Sink
			tc.edit(sink)
			require.Error(t, validateSinkOperationContract(sink))
		})
	}
}
