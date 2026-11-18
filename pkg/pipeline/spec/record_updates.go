package spec

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RecordUpdates maps source row fields to writable HubSpot company properties.
type RecordUpdates struct {
	IdentityField string            `json:"identity_field"`
	Fields        map[string]string `json:"fields"`
	MaxRecords    int               `json:"max_records"`
}

var companyPropertyName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ParseRecordUpdates validates the configuration for bounded record dispatch.
func ParseRecordUpdates(config map[string]any) (*RecordUpdates, error) {
	invalid := func(detail string) error {
		return xerror.InvalidInput(xerror.Pipeline).Detail(detail).WithPublicDetail(detail)
	}
	options, ok := config["record_updates"].(map[string]any)
	if !ok || len(options) != 3 || options["identity_field"] == nil || options["fields"] == nil || options["max_records"] == nil {
		return nil, invalid("sink.config.record_updates requires identity_field, fields and max_records")
	}
	data, err := json.Marshal(options)
	if err != nil {
		return nil, invalid("invalid sink.config.record_updates")
	}
	var updates RecordUpdates
	if json.Unmarshal(data, &updates) != nil || updates.MaxRecords < 1 || updates.MaxRecords > 100 {
		return nil, invalid("record_updates requires string field names and an integer max_records between 1 and 100")
	}
	if updates.IdentityField == "" || strings.TrimSpace(updates.IdentityField) != updates.IdentityField {
		return nil, invalid("record_updates.identity_field must be a non-empty field name without surrounding whitespace")
	}
	if len(updates.Fields) < 1 || len(updates.Fields) > 100 {
		return nil, invalid("record_updates.fields requires between 1 and 100 property mappings")
	}
	destinations := make(map[string]bool, len(updates.Fields))
	for source, destination := range updates.Fields {
		if source == "" || strings.TrimSpace(source) != source || !companyPropertyName.MatchString(destination) || destination == "hs_object_id" || destinations[destination] {
			return nil, invalid("record_updates.fields requires non-empty source fields and distinct writable company property names")
		}
		destinations[destination] = true
	}
	return &updates, nil
}
