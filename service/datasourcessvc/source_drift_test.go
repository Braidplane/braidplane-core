package datasourcessvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestSourceSchemaDriftReport_ReportsTypeAndNullableChanges(t *testing.T) {
	t.Parallel()

	before := Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            model.DataSourceTypePostgres.String(),
		Payload: SQLSchemaPayload{
			Tables: []SchemaTable{{
				Name: "users",
				Columns: []SchemaColumn{{
					Name:     "score",
					Type:     "integer",
					Nullable: false,
				}},
			}},
		},
	}
	after := Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            model.DataSourceTypePostgres.String(),
		Payload: SQLSchemaPayload{
			Tables: []SchemaTable{{
				Name: "users",
				Columns: []SchemaColumn{{
					Name:     "score",
					Type:     "number",
					Nullable: true,
				}},
			}},
		},
	}

	report := sourceSchemaDriftReport(must.Slug("prod-db"), before, after)
	require.NotNil(t, report)
	require.Len(t, report.Changes, 2)
	assert.Equal(t, "type_change", report.Changes[0].Kind)
	assert.Equal(t, "tables.users.score.type", report.Changes[0].Path)
	assert.Equal(t, "breaking", report.Changes[0].Severity.String())
	assert.Equal(t, "nullable_change", report.Changes[1].Kind)
	assert.Equal(t, "tables.users.score.nullable", report.Changes[1].Path)
	assert.Equal(t, "breaking", report.Changes[1].Severity.String())
}
