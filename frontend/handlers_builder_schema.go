package frontend

import (
	"net/http"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

type builderSchemaDataset struct {
	Name      string                  `json:"name"`
	Fields    []payload.ContractField `json:"fields"`
	Warning   string                  `json:"warning,omitempty"`
	SourceKey string                  `json:"source_key,omitempty"`
}

// Cached metadata only; provider access remains an explicit schema-import request.
func (r *Router) handleBuilderSchema(c fiber.Ctx) error {
	org, err := slug.New(c.Query("org_slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	source, err := slug.New(c.Query("data_source_slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	ds, status, _, err := r.getDataSourceViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org.String(), source.String())
	if err != nil {
		return c.Status(status).JSON(fiber.Map{"error": "Cached schema unavailable. Check data source access and retry."})
	}
	panel := dataSourceSchemaPanelData(ds, "")
	datasets := []builderSchemaDataset{}
	if panel.Status == api.DataSourceSchemaStatusReady {
		datasets = builderSchemaDatasets(ds)
	}
	return c.JSON(fiber.Map{"status": panel.Status, "title": panel.Title, "lines": panel.Lines, "error": panel.Error, "datasets": datasets})
}

func builderSchemaDatasets(ds *api.DataSource) []builderSchemaDataset {
	datasets := []builderSchemaDataset{}
	provider, err := model.DataSourceTypeString(string(ds.Type))
	if err != nil {
		return datasets
	}
	add := func(name string, fields []api.FieldInfo) {
		dataset := builderSchemaDataset{Name: name, Fields: []payload.ContractField{}}
		for _, native := range fields {
			field := transformsql.ContractFieldFromNative(provider, native.Name, native.Type)
			if field.Type == model.ContractFieldTypeUnknown {
				dataset.Warning = "Some native types cannot be proposed safely. Inspect the cached schema and enter fields manually."
			}
			// Introspection does not establish query projection, requiredness or nullability.
			field.Nullable = true
			dataset.Fields = append(dataset.Fields, field)
		}
		if dataset.Warning != "" {
			dataset.Fields = nil
		}
		datasets = append(datasets, dataset)
	}
	family, _ := ds.Schema.Discriminator()
	switch family {
	case "sql":
		schema, err := ds.Schema.AsSQLSchemaResponse()
		if err != nil {
			return datasets
		}
		for _, table := range schema.Tables {
			fields := make([]api.FieldInfo, 0, len(table.Columns))
			for _, col := range table.Columns {
				fields = append(fields, api.FieldInfo(col))
			}
			add(table.Name, fields)
		}
	case "document":
		schema, err := ds.Schema.AsDocumentSchemaResponse()
		if err != nil {
			return datasets
		}
		for _, collection := range schema.Collections {
			add(collection.Name, collection.Fields)
		}
	case "file":
		schema, err := ds.Schema.AsFileSchemaResponse()
		if err != nil {
			return datasets
		}
		var s3Prefix string
		var hasS3Config bool
		if provider == model.DataSourceTypeS3 {
			config, configErr := ds.Config.AsS3Config()
			hasS3Config = configErr == nil
			if config.Prefix != nil {
				s3Prefix = strings.Trim(strings.TrimSpace(*config.Prefix), "/")
			}
		}
		for _, file := range schema.Files {
			add(file.Name, file.Fields)
			if hasS3Config {
				// Listings contain full keys; operations resolve relative to the configured prefix.
				if s3Prefix == "" {
					datasets[len(datasets)-1].SourceKey = file.Name
				} else if strings.HasPrefix(file.Name, s3Prefix+"/") {
					datasets[len(datasets)-1].SourceKey = strings.TrimPrefix(file.Name, s3Prefix+"/")
				}
			}
		}
	case "object_api":
		schema, err := ds.Schema.AsObjectAPISchemaResponse()
		if err != nil {
			return datasets
		}
		for _, object := range schema.Objects {
			fields := make([]api.FieldInfo, 0, len(object.Fields))
			for _, field := range object.Fields {
				fields = append(fields, api.FieldInfo{Name: field.Name, Type: field.Type})
			}
			add(object.Name, fields)
		}
	}
	return datasets
}
