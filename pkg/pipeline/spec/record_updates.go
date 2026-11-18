package spec

import (
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RecordUpdates maps source row fields to writable destination fields.
type RecordUpdates struct {
	IdentityField string                     `json:"identity_field"`
	Fields        map[string]string          `json:"fields"`
	MaxRecords    int                        `json:"max_records"`
	Readback      sinkbinding.RecordReadback `json:"readback"`
}

// ParseRecordUpdates validates the configuration for bounded record dispatch.
func ParseRecordUpdates(config map[string]any) (*RecordUpdates, error) {
	invalid := func(detail string) error {
		return xerror.InvalidInput(xerror.Pipeline).Detail(detail).WithPublicDetail(detail)
	}
	options, ok := config["record_updates"].(map[string]any)
	if !ok || len(options) != 4 || options["identity_field"] == nil || options["fields"] == nil || options["max_records"] == nil || options["readback"] == nil {
		return nil, invalid("sink.config.record_updates requires identity_field, fields, max_records and readback")
	}
	readback, ok := options["readback"].(map[string]any)
	if !ok {
		return nil, invalid("record_updates.readback must be an object")
	}
	for key, value := range readback {
		switch key {
		case "identity_field", "fields_object", "select_parameter":
			name, ok := value.(string)
			if !ok || !sinkbinding.ValidRecordField(name) {
				return nil, invalid("record_updates.readback requires literal field and parameter names")
			}
		default:
			return nil, invalid("unexpected record_updates.readback field")
		}
	}
	data, err := json.Marshal(options)
	if err != nil {
		return nil, invalid("invalid sink.config.record_updates")
	}
	var updates RecordUpdates
	if json.Unmarshal(data, &updates) != nil || updates.MaxRecords < 1 || updates.MaxRecords > 100 {
		return nil, invalid("record_updates requires string field names and an integer max_records between 1 and 100")
	}
	if !updates.Readback.Valid() {
		return nil, invalid("record_updates.readback requires a response identity field distinct from its fields object")
	}
	if updates.IdentityField == "" || strings.TrimSpace(updates.IdentityField) != updates.IdentityField {
		return nil, invalid("record_updates.identity_field must be a non-empty field name without surrounding whitespace")
	}
	if len(updates.Fields) < 1 || len(updates.Fields) > 100 {
		return nil, invalid("record_updates.fields requires between 1 and 100 property mappings")
	}
	destinations := make(map[string]bool, len(updates.Fields))
	for source, destination := range updates.Fields {
		if source == "" || strings.TrimSpace(source) != source || !sinkbinding.ValidRecordField(destination) || destinations[destination] ||
			(updates.Readback.FieldsObject == "" && destination == updates.Readback.IdentityField) {
			return nil, invalid("record_updates.fields requires non-empty source fields and distinct writable destination fields")
		}
		destinations[destination] = true
	}
	return &updates, nil
}
