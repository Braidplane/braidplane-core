package pipelineruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestTransformSchemaFieldTypes_FileSchemaPayload(t *testing.T) {
	t.Parallel()

	schema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "ftp",
		Payload: datasourcessvc.FileSchemaPayload{
			Files: []datasourcessvc.SchemaFile{
				{
					Name: "users.json",
					Fields: []datasourcessvc.SchemaField{
						{Name: "id", Type: "integer"},
						{Name: "profile.locale", Type: "string"},
						{Name: "orders[]", Type: "array"},
					},
				},
			},
		},
	}

	nativeTypes, dsType, err := transformSchemaFieldTypes(schema)
	require.NoError(t, err)
	assert.Equal(t, model.DataSourceTypeFTP, dsType)
	assert.Equal(t, "integer", nativeTypes["id"])
	assert.Equal(t, "string", nativeTypes["profile.locale"])
	assert.Equal(t, "array", nativeTypes["orders[]"])
}

func TestTransformSchemaFieldTypes_DuplicateFileFieldsKeepFirstDefinition(t *testing.T) {
	t.Parallel()

	schema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "ftp",
		Payload: datasourcessvc.FileSchemaPayload{
			Files: []datasourcessvc.SchemaFile{
				{
					Name: "users.csv",
					Fields: []datasourcessvc.SchemaField{
						{Name: "id", Type: "integer"},
						{Name: "email", Type: "string"},
					},
				},
				{
					Name: "users-backup.csv",
					Fields: []datasourcessvc.SchemaField{
						{Name: "id", Type: "string"},
						{Name: "is_active", Type: "boolean"},
					},
				},
			},
		},
	}

	nativeTypes, dsType, err := transformSchemaFieldTypes(schema)
	require.NoError(t, err)
	assert.Equal(t, model.DataSourceTypeFTP, dsType)
	assert.Equal(t, "integer", nativeTypes["id"])
	assert.Equal(t, "string", nativeTypes["email"])
	assert.Equal(t, "boolean", nativeTypes["is_active"])
}

func TestTransformSchemaFieldTypes_NilSchemaResult(t *testing.T) {
	t.Parallel()

	nativeTypes, dsType, err := transformSchemaFieldTypes(nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "schema result is required")
	assert.Nil(t, nativeTypes)
	assert.Equal(t, model.DataSourceTypeUnknown, dsType)
}

func TestTransformSchemaFieldTypes_UnsupportedVendorType(t *testing.T) {
	t.Parallel()

	schema := datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeFile,
		Type:            "custom-file-store",
		Payload:         datasourcessvc.FileSchemaPayload{},
	}

	nativeTypes, dsType, err := transformSchemaFieldTypes(schema)
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported schema vendor type")
	assert.Nil(t, nativeTypes)
	assert.Equal(t, model.DataSourceTypeUnknown, dsType)
}
