package datasourcessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	bigqueryconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
)

// schemaAdapter asserts the connector to the appropriate introspector interface,
// calls IntrospectSchema, and converts the result to SchemaResult.
type schemaAdapter interface {
	introspect(
		ctx context.Context,
		conn connector.Connector,
		dsType model.DataSourceType,
	) (SchemaResult, error)
}

// adapterForType returns the schemaAdapter for the given data source type.
func adapterForType(dsType model.DataSourceType) (schemaAdapter, error) {
	switch dsType {
	case model.DataSourceTypePostgres, model.DataSourceTypeMySQL, model.DataSourceTypeSnowflake:
		return sqlAdapter{}, nil
	case model.DataSourceTypeGoogleWorkspace:
		return fileAdapter{}, nil
	case model.DataSourceTypeBigQuery:
		return bigQueryAdapter{}, nil
	case model.DataSourceTypeMongoDB:
		return mongoAdapter{}, nil
	case model.DataSourceTypeHTTP:
		return httpAdapter{}, nil
	case model.DataSourceTypeHubSpot:
		return hubspotAdapter{}, nil
	case model.DataSourceTypeFTP, model.DataSourceTypeGit,
		model.DataSourceTypeGithub, model.DataSourceTypeGitlab:
		return fileAdapter{}, nil
	case model.DataSourceTypeS3:
		return s3Adapter{}, nil
	case model.DataSourceTypeUnknown:
		return nil, ErrInvalidDataSourceType
	default:
		return nil, ErrInvalidDataSourceType.Detailf(
			"unsupported data source type: %s",
			dsType.String(),
		)
	}
}

type sqlAdapter struct{}

func (sqlAdapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(sqlconnector.SQLSchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromSQL(dsType, schema)
}

type bigQueryAdapter struct{}

func (bigQueryAdapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(bigqueryconnector.SchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromSQL(dsType, schema)
}

type mongoAdapter struct{}

func (mongoAdapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(mongoconnector.SchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromDocument(dsType, schema)
}

type httpAdapter struct{}

func (httpAdapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(httpconnector.HTTPSchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromHTTP(dsType, schema)
}

type hubspotAdapter struct{}

func (hubspotAdapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(hubspotconnector.SchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromHubSpot(dsType, schema)
}

type fileAdapter struct{}

func (fileAdapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(filedata.SchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromFileConnector(dsType, schema)
}

type s3Adapter struct{}

func (s3Adapter) introspect(
	ctx context.Context,
	conn connector.Connector,
	dsType model.DataSourceType,
) (SchemaResult, error) {
	i, ok := conn.(s3connector.SchemaIntrospector)
	if !ok {
		return nil, ErrSchemaIntrospectionUnsupported
	}
	schema, err := i.IntrospectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return schemaFromFileConnector(dsType, schema)
}
