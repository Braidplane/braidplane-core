package validate

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

func TestRecordUpdatesDefinition(t *testing.T) {
	const path = "../../../examples/pipelines/yaml/http-company-updates.pipeline.yaml"
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = Load(path, source)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		edit func(*spec.SinkSpec)
	}{
		{"unbounded", func(s *spec.SinkSpec) { s.Config["record_updates"].(map[string]any)["max_records"] = 101 }},
		{"missing identity", func(s *spec.SinkSpec) { delete(s.Config["record_updates"].(map[string]any), "identity_field") }},
		{"duplicate mapping", func(s *spec.SinkSpec) {
			s.Config["record_updates"].(map[string]any)["fields"] = map[string]string{"first": "name", "second": "name"}
		}},
		{"read-only ID", func(s *spec.SinkSpec) {
			s.Config["record_updates"].(map[string]any)["fields"] = map[string]string{"company": "hs_object_id"}
		}},
		{"unexpected field", func(s *spec.SinkSpec) { s.Config["record_updates"].(map[string]any)["page"] = 1 }},
		{"saved operation", func(s *spec.SinkSpec) { s.OperationInline = nil; s.OperationRef = "old-saved-operation" }},
		{"unexpected path", func(s *spec.SinkSpec) { s.OperationInline["path"] = "/other" }},
		{"unmapped body", func(s *spec.SinkSpec) { s.OperationInline["body"] = "{{input}}" }},
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
