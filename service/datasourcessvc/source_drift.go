package datasourcessvc

import (
	"fmt"
	"sort"

	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type sourceSchemaField struct {
	Type     string
	Nullable bool
}

// sourceSchemaDriftReport compares two cached source schemas and returns a report
// when the refreshed shape changes the fields or operations exposed by a data source.
func sourceSchemaDriftReport(
	dsSlug slug.Slug,
	before SchemaResult,
	after SchemaResult,
) *pipelinedrift.Report {
	if before == nil || after == nil {
		return nil
	}

	beforeFields := flattenSourceSchema(before)
	afterFields := flattenSourceSchema(after)
	changes := sourceSchemaChanges(beforeFields, afterFields)
	if len(changes) == 0 {
		return nil
	}

	severity := pipelinedrift.ReportSeverityWarning
	for _, change := range changes {
		if change.Severity == pipelinedrift.ReportSeverityBreaking {
			severity = pipelinedrift.ReportSeverityBreaking
			break
		}
	}

	return &pipelinedrift.Report{
		Type:     pipelinedrift.ReportTypeSourceSchema,
		Status:   pipelinedrift.ReportStatusOpen,
		Severity: severity,
		Summary: fmt.Sprintf(
			"%d source schema drift change(s), highest severity %s",
			len(changes),
			severity,
		),
		Changes: changes,
		Impacts: []pipelinedrift.Impact{{
			Kind:     "unknown_consumer",
			Severity: pipelinedrift.ReportSeverityWarning,
			Message: fmt.Sprintf(
				"source schema changed for data source %q; dependent contracts should be reviewed",
				dsSlug.String(),
			),
		}},
	}
}

func sourceSchemaChanges(
	beforeFields map[string]sourceSchemaField,
	afterFields map[string]sourceSchemaField,
) []pipelinedrift.Change {
	keys := make(map[string]struct{}, len(beforeFields)+len(afterFields))
	for key := range beforeFields {
		keys[key] = struct{}{}
	}
	for key := range afterFields {
		keys[key] = struct{}{}
	}

	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	var changes []pipelinedrift.Change
	for _, key := range ordered {
		before, hasBefore := beforeFields[key]
		after, hasAfter := afterFields[key]
		if !hasBefore {
			changes = append(changes, pipelinedrift.Change{
				Kind:       "addition",
				Path:       key,
				Before:     "<none>",
				After:      formatSourceSchemaField(after),
				Severity:   pipelinedrift.ReportSeverityWarning,
				ReasonCode: "addition",
			})
			continue
		}
		if !hasAfter {
			changes = append(changes, pipelinedrift.Change{
				Kind:       "removal",
				Path:       key,
				Before:     formatSourceSchemaField(before),
				After:      "<none>",
				Severity:   pipelinedrift.ReportSeverityBreaking,
				ReasonCode: "removal",
			})
			continue
		}
		if before.Type != after.Type {
			changes = append(changes, pipelinedrift.Change{
				Kind:       "type_change",
				Path:       key + ".type",
				Before:     before.Type,
				After:      after.Type,
				Severity:   pipelinedrift.ReportSeverityBreaking,
				ReasonCode: "type_changed",
			})
		}
		if before.Nullable != after.Nullable {
			severity := pipelinedrift.ReportSeverityWarning
			if after.Nullable {
				severity = pipelinedrift.ReportSeverityBreaking
			}
			changes = append(changes, pipelinedrift.Change{
				Kind:       "nullable_change",
				Path:       key + ".nullable",
				Before:     fmt.Sprintf("%v", before.Nullable),
				After:      fmt.Sprintf("%v", after.Nullable),
				Severity:   severity,
				ReasonCode: "nullable_changed",
			})
		}
	}
	return changes
}

func flattenSourceSchema(schema SchemaResult) map[string]sourceSchemaField {
	switch typed := schema.(type) {
	case Schema[SQLSchemaPayload]:
		return flattenSQLSourceSchema(typed.Payload)
	case Schema[DocumentSchemaPayload]:
		return flattenDocumentSourceSchema(typed.Payload)
	case Schema[FileSchemaPayload]:
		return flattenFileSourceSchema(typed.Payload)
	case Schema[ObjectAPISchemaPayload]:
		return flattenObjectAPISourceSchema(typed.Payload)
	case Schema[HTTPSchemaPayload]:
		return flattenHTTPSourceSchema(typed.Payload)
	default:
		return map[string]sourceSchemaField{}
	}
}

func flattenSQLSourceSchema(payload SQLSchemaPayload) map[string]sourceSchemaField {
	out := map[string]sourceSchemaField{}
	for _, table := range payload.Tables {
		for _, column := range table.Columns {
			out["tables."+table.Name+"."+column.Name] = sourceSchemaField{
				Type:     column.Type,
				Nullable: column.Nullable,
			}
		}
	}
	return out
}

func flattenDocumentSourceSchema(payload DocumentSchemaPayload) map[string]sourceSchemaField {
	out := map[string]sourceSchemaField{}
	for _, collection := range payload.Collections {
		for _, field := range collection.Fields {
			out["collections."+collection.Name+"."+field.Name] = sourceSchemaField{Type: field.Type}
		}
	}
	return out
}

func flattenFileSourceSchema(payload FileSchemaPayload) map[string]sourceSchemaField {
	out := map[string]sourceSchemaField{}
	for _, file := range payload.Files {
		for _, field := range file.Fields {
			out["files."+file.Name+"."+field.Name] = sourceSchemaField{Type: field.Type}
		}
	}
	return out
}

func flattenObjectAPISourceSchema(payload ObjectAPISchemaPayload) map[string]sourceSchemaField {
	out := map[string]sourceSchemaField{}
	for _, object := range payload.Objects {
		for _, field := range object.Fields {
			out["objects."+object.Name+".fields."+field.Name] = sourceSchemaField{Type: field.Type}
		}
	}
	return out
}

func flattenHTTPSourceSchema(payload HTTPSchemaPayload) map[string]sourceSchemaField {
	out := map[string]sourceSchemaField{}
	for _, operation := range payload.Operations {
		out["operations."+operation.ID] = sourceSchemaField{
			Type: operation.Method + " " + operation.Path,
		}
	}
	return out
}

func formatSourceSchemaField(field sourceSchemaField) string {
	return fmt.Sprintf("%s(nullable=%t)", field.Type, field.Nullable)
}
