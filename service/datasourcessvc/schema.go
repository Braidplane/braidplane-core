package datasourcessvc

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
)

//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=SchemaType -json -text -linecomment -trimprefix=SchemaType -output=schema_type_enumer.go
type SchemaType int

const (
	SchemaTypeUnknown     SchemaType = iota // unknown
	SchemaTypeSQL                           // sql
	SchemaTypeHTTP                          // http
	SchemaTypeDocument                      // document
	SchemaTypeFile                          // file
	SchemaTypeObjectAPI                     // object_api
	SchemaTypeSpreadsheet                   // spreadsheet
)

// SchemaResult is the common interface implemented by all schema variants.
type SchemaResult interface {
	SchemaType() SchemaType
	VendorType() string
}

// Schema represents the normalized schema structure stored in cache.
type Schema[T any] struct {
	SchemaTypeValue SchemaType `json:"schema_type"`
	Type            string     `json:"type"`
	Payload         T          `json:"payload"`
}

func (s Schema[T]) SchemaType() SchemaType {
	return s.SchemaTypeValue
}

func (s Schema[T]) VendorType() string {
	return s.Type
}

type SpreadsheetSchemaPayload = workspace.Metadata

type SQLSchemaPayload struct {
	Tables []SchemaTable `json:"tables"`
}

type HTTPSchemaPayload struct {
	Operations []SchemaOperation `json:"operations"`
}

type DocumentSchemaPayload struct {
	Collections []SchemaCollection `json:"collections"`
}

type FileSchemaPayload struct {
	Files       []SchemaFile           `json:"files"`
	Diagnostics []SchemaFileDiagnostic `json:"diagnostics,omitempty"`
}

type ObjectAPISchemaPayload struct {
	Objects []SchemaObjectAPIObject `json:"objects"`
}

type SchemaTable struct {
	Name    string         `json:"name"`
	Columns []SchemaColumn `json:"columns"`
}

type SchemaColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}

type SchemaOperation struct {
	Draft       json.RawMessage `json:"draft,omitempty"`
	Diagnostics []string        `json:"diagnostics,omitempty"`
	ID          string          `json:"id"`
	Path        string          `json:"path"`
	Method      string          `json:"method"`
	Description string          `json:"description,omitempty"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
	Responses   json.RawMessage `json:"responses,omitempty"`
}

type SchemaCollection struct {
	Name   string        `json:"name"`
	Fields []SchemaField `json:"fields"`
}

type SchemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type SchemaFile struct {
	ResourceID string        `json:"resource_id,omitempty"`
	SizeBytes  *int64        `json:"size_bytes,omitempty"`
	MIMEType   string        `json:"mime_type,omitempty"`
	Name       string        `json:"name"`
	Fields     []SchemaField `json:"fields"`
}

type SchemaFileDiagnostic struct {
	Name     string `json:"name"`
	Target   string `json:"target"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type SchemaObjectAPIObject struct {
	Name             string                     `json:"name"`
	Label            string                     `json:"label,omitempty"`
	Fields           []SchemaObjectAPIField     `json:"fields"`
	Operations       []SchemaObjectAPIOperation `json:"operations,omitempty"`
	ProviderMetadata map[string]any             `json:"provider_metadata,omitempty"`
}

type SchemaObjectAPIField struct {
	Name             string         `json:"name"`
	Label            string         `json:"label,omitempty"`
	Type             string         `json:"type"`
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

type SchemaObjectAPIOperation struct {
	Name             string         `json:"name"`
	Label            string         `json:"label,omitempty"`
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

func schemaTypeForDataSource(dsType model.DataSourceType) (SchemaType, string, error) {
	descriptor, ok := catalog.DescriptorFor(dsType)
	if !ok {
		if dsType == model.DataSourceTypeUnknown {
			return SchemaTypeUnknown, "", ErrInvalidDataSourceType
		}
		return SchemaTypeUnknown, "", ErrInvalidDataSourceType.Detailf(
			"unsupported data source type: %s",
			dsType.String(),
		)
	}

	switch descriptor.SchemaFamily {
	case catalog.SchemaFamilySpreadsheet:
		return SchemaTypeSpreadsheet, dsType.String(), nil
	case catalog.SchemaFamilySQL:
		return SchemaTypeSQL, dsType.String(), nil
	case catalog.SchemaFamilyHTTP:
		return SchemaTypeHTTP, dsType.String(), nil
	case catalog.SchemaFamilyDocument:
		return SchemaTypeDocument, dsType.String(), nil
	case catalog.SchemaFamilyObjectAPI:
		return SchemaTypeObjectAPI, dsType.String(), nil
	case catalog.SchemaFamilyFile:
		return SchemaTypeFile, dsType.String(), nil
	case catalog.SchemaFamilyObjectStore:
		return SchemaTypeFile, dsType.String(), nil
	default:
		return SchemaTypeUnknown, "", ErrInvalidDataSourceType.Detailf(
			"unsupported schema family: %s",
			descriptor.SchemaFamily,
		)
	}
}

func schemaFromSQL(
	dsType model.DataSourceType,
	sqlSchema *sqlconnector.Schema,
) (SchemaResult, error) {
	schemaType, vendorType, err := schemaTypeForDataSource(dsType)
	if err != nil {
		return nil, err
	}

	tables := make([]SchemaTable, 0, len(sqlSchema.Tables))
	for _, table := range sqlSchema.Tables {
		columns := make([]SchemaColumn, 0, len(table.Columns))
		for _, column := range table.Columns {
			columns = append(columns, SchemaColumn{
				Name:     column.Name,
				Type:     column.Type,
				Nullable: column.Nullable,
			})
		}

		tables = append(tables, SchemaTable{
			Name:    table.Name,
			Columns: columns,
		})
	}

	return Schema[SQLSchemaPayload]{
		SchemaTypeValue: schemaType,
		Type:            vendorType,
		Payload: SQLSchemaPayload{
			Tables: tables,
		},
	}, nil
}

func schemaFromDocument(
	dsType model.DataSourceType,
	docSchema *mongoconnector.Schema,
) (SchemaResult, error) {
	schemaType, vendorType, err := schemaTypeForDataSource(dsType)
	if err != nil {
		return nil, err
	}

	collections := make([]SchemaCollection, 0, len(docSchema.Collections))
	for _, collection := range docSchema.Collections {
		fields := make([]SchemaField, 0, len(collection.Fields))
		for _, field := range collection.Fields {
			fields = append(fields, SchemaField{
				Name: field.Name,
				Type: field.Type.String(),
			})
		}

		collections = append(collections, SchemaCollection{
			Name:   collection.Name,
			Fields: fields,
		})
	}

	return Schema[DocumentSchemaPayload]{
		SchemaTypeValue: schemaType,
		Type:            vendorType,
		Payload: DocumentSchemaPayload{
			Collections: collections,
		},
	}, nil
}

func schemaFromHTTP(
	dsType model.DataSourceType,
	httpSchema *httpconnector.HTTPSchema,
) (SchemaResult, error) {
	if httpSchema == nil {
		return emptySchemaForType(dsType)
	}

	schemaType, vendorType, err := schemaTypeForDataSource(dsType)
	if err != nil {
		return nil, err
	}

	operations := make([]SchemaOperation, 0, len(httpSchema.Operations))
	for _, operation := range httpSchema.Operations {
		requestBody := append(json.RawMessage(nil), operation.RequestBody...)
		responses := append(json.RawMessage(nil), operation.Responses...)
		operations = append(operations, SchemaOperation{
			ID:          operation.OperationID,
			Draft:       append(json.RawMessage(nil), operation.Draft...),
			Diagnostics: append([]string(nil), operation.Diagnostics...),
			Path:        operation.Path,
			Method:      operation.Method,
			Description: operation.Description,
			RequestBody: requestBody,
			Responses:   responses,
		})
	}

	return Schema[HTTPSchemaPayload]{
		SchemaTypeValue: schemaType,
		Type:            vendorType,
		Payload: HTTPSchemaPayload{
			Operations: operations,
		},
	}, nil
}

func schemaFromFileConnector(
	dsType model.DataSourceType,
	fileSchema *filedata.Schema,
) (SchemaResult, error) {
	if fileSchema == nil {
		return emptySchemaForType(dsType)
	}

	schemaType, vendorType, err := schemaTypeForDataSource(dsType)
	if err != nil {
		return nil, err
	}

	files := make([]SchemaFile, 0, len(fileSchema.Files))
	for _, file := range fileSchema.Files {
		fields := make([]SchemaField, 0, len(file.Fields))
		for _, field := range file.Fields {
			fields = append(fields, SchemaField{
				Name: field.Name,
				Type: field.Type,
			})
		}

		files = append(files, SchemaFile{
			Name: file.Name, ResourceID: file.ResourceID, MIMEType: file.MIMEType, SizeBytes: file.SizeBytes,
			Fields: fields,
		})
	}

	var diagnostics []SchemaFileDiagnostic
	if len(fileSchema.Diagnostics) > 0 {
		diagnostics = make([]SchemaFileDiagnostic, 0, len(fileSchema.Diagnostics))
		for _, diagnostic := range fileSchema.Diagnostics {
			diagnostics = append(diagnostics, SchemaFileDiagnostic{
				Name:     diagnostic.Name,
				Target:   diagnostic.Target,
				Severity: diagnostic.Severity,
				Code:     diagnostic.Code,
				Message:  diagnostic.Message,
			})
		}
	}

	return Schema[FileSchemaPayload]{
		SchemaTypeValue: schemaType,
		Type:            vendorType,
		Payload: FileSchemaPayload{
			Files:       files,
			Diagnostics: diagnostics,
		},
	}, nil
}

func schemaFromHubSpot(
	dsType model.DataSourceType,
	hubspotSchema *hubspotconnector.Schema,
) (SchemaResult, error) {
	if hubspotSchema == nil {
		return emptySchemaForType(dsType)
	}

	schemaType, vendorType, err := schemaTypeForDataSource(dsType)
	if err != nil {
		return nil, err
	}

	objects := make([]SchemaObjectAPIObject, 0, len(hubspotSchema.Objects))
	for _, object := range hubspotSchema.Objects {
		fields := make([]SchemaObjectAPIField, 0, len(object.Properties)+1)
		fields = append(fields, hubSpotRecordIDObjectAPIField())
		for _, property := range object.Properties {
			if property.Name == hubspotconnector.RecordIDFieldName {
				continue
			}
			fields = append(fields, SchemaObjectAPIField{
				Name:             property.Name,
				Label:            property.Label,
				Type:             property.Type,
				ProviderMetadata: property.ProviderMetadata,
			})
		}

		operations := make([]SchemaObjectAPIOperation, 0, len(object.Operations))
		for _, operation := range object.Operations {
			operations = append(operations, SchemaObjectAPIOperation{
				Name:  operation.Name,
				Label: operation.Label,
			})
		}

		objects = append(objects, SchemaObjectAPIObject{
			Name:             object.Name,
			Label:            object.Label,
			Fields:           fields,
			Operations:       operations,
			ProviderMetadata: object.ProviderMetadata,
		})
	}

	return Schema[ObjectAPISchemaPayload]{
		SchemaTypeValue: schemaType,
		Type:            vendorType,
		Payload: ObjectAPISchemaPayload{
			Objects: objects,
		},
	}, nil
}

func hubSpotRecordIDObjectAPIField() SchemaObjectAPIField {
	return SchemaObjectAPIField{
		Name:  hubspotconnector.RecordIDFieldName,
		Label: "HubSpot record ID",
		Type:  "string",
		ProviderMetadata: map[string]any{
			"source": "record_id",
		},
	}
}

func emptySchemaForType(dsType model.DataSourceType) (SchemaResult, error) {
	schemaType, vendorType, err := schemaTypeForDataSource(dsType)
	if err != nil {
		return nil, err
	}

	switch schemaType {
	case SchemaTypeUnknown:
		return nil, ErrInvalidDataSourceType.Detailf("unsupported schema type: %s", schemaType)
	case SchemaTypeSpreadsheet:
		return Schema[SpreadsheetSchemaPayload]{SchemaTypeValue: schemaType, Type: vendorType, Payload: SpreadsheetSchemaPayload{Worksheets: []workspace.Worksheet{}}}, nil
	case SchemaTypeSQL:
		return Schema[SQLSchemaPayload]{
			SchemaTypeValue: schemaType,
			Type:            vendorType,
			Payload: SQLSchemaPayload{
				Tables: []SchemaTable{},
			},
		}, nil
	case SchemaTypeHTTP:
		return Schema[HTTPSchemaPayload]{
			SchemaTypeValue: schemaType,
			Type:            vendorType,
			Payload: HTTPSchemaPayload{
				Operations: []SchemaOperation{},
			},
		}, nil
	case SchemaTypeDocument:
		return Schema[DocumentSchemaPayload]{
			SchemaTypeValue: schemaType,
			Type:            vendorType,
			Payload: DocumentSchemaPayload{
				Collections: []SchemaCollection{},
			},
		}, nil
	case SchemaTypeFile:
		return Schema[FileSchemaPayload]{
			SchemaTypeValue: schemaType,
			Type:            vendorType,
			Payload: FileSchemaPayload{
				Files: []SchemaFile{},
			},
		}, nil
	case SchemaTypeObjectAPI:
		return Schema[ObjectAPISchemaPayload]{
			SchemaTypeValue: schemaType,
			Type:            vendorType,
			Payload: ObjectAPISchemaPayload{
				Objects: []SchemaObjectAPIObject{},
			},
		}, nil
	}

	return nil, ErrInvalidDataSourceType.Detailf("unsupported schema type: %s", schemaType)
}

type schemaEnvelope struct {
	SchemaType SchemaType      `json:"schema_type"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
}

func decodeSchemaJSON(data []byte) (SchemaResult, error) {
	var envelope schemaEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	switch envelope.SchemaType {
	case SchemaTypeSpreadsheet:
		var payload SpreadsheetSchemaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return Schema[SpreadsheetSchemaPayload]{SchemaTypeValue: envelope.SchemaType, Type: envelope.Type, Payload: payload}, nil
	case SchemaTypeSQL:
		var payload SQLSchemaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return Schema[SQLSchemaPayload]{
			SchemaTypeValue: envelope.SchemaType,
			Type:            envelope.Type,
			Payload:         payload,
		}, nil
	case SchemaTypeHTTP:
		var payload HTTPSchemaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return Schema[HTTPSchemaPayload]{
			SchemaTypeValue: envelope.SchemaType,
			Type:            envelope.Type,
			Payload:         payload,
		}, nil
	case SchemaTypeDocument:
		var payload DocumentSchemaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return Schema[DocumentSchemaPayload]{
			SchemaTypeValue: envelope.SchemaType,
			Type:            envelope.Type,
			Payload:         payload,
		}, nil
	case SchemaTypeFile:
		var payload FileSchemaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return Schema[FileSchemaPayload]{
			SchemaTypeValue: envelope.SchemaType,
			Type:            envelope.Type,
			Payload:         payload,
		}, nil
	case SchemaTypeObjectAPI:
		var payload ObjectAPISchemaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return Schema[ObjectAPISchemaPayload]{
			SchemaTypeValue: envelope.SchemaType,
			Type:            envelope.Type,
			Payload:         payload,
		}, nil
	default:
		return nil, ErrInvalidDataSourceType.Detailf(
			"unsupported schema type: %s",
			envelope.SchemaType,
		)
	}
}
