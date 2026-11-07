package remotefile

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
)

func TestSchemaSpreadsheetUsesSampleBudget(t *testing.T) {
	t.Parallel()
	workbook := excelize.NewFile()
	t.Cleanup(func() { require.NoError(t, workbook.Close()) })
	require.NoError(t, workbook.SetCellValue("Sheet1", "A1", "id"))
	require.NoError(t, workbook.SetCellValue("Sheet1", "CV31", "tail"))
	content, err := workbook.WriteToBuffer()
	require.NoError(t, err)
	for _, parsedColumns := range []bool{false, true} {
		for _, quota := range []bool{false, true} {
			t.Run(fmt.Sprintf("parsed=%t/quota=%t", parsedColumns, quota), func(t *testing.T) {
				ctx := t.Context()
				opts := SchemaOptions{MaxBytes: 32 << 10}
				if quota {
					ctx = schemaquota.WithQuota(ctx, 10, 32<<10)
					opts = SchemaOptions{}
				}
				provider := &fakeProvider{
					files:  map[string]fakeFile{"sparse.xlsx": {content: content.Bytes()}},
					listed: []SchemaFile{{Name: "sparse.xlsx", Target: "sparse.xlsx"}},
				}
				schema, schemaErr := (Adapter{SchemaParsedColumns: parsedColumns, SpreadsheetFilePath: true}).IntrospectSchema(ctx, provider, opts)
				require.NoError(t, schemaErr)
				require.Len(t, schema.Files, 1)
				require.Empty(t, schema.Files[0].Fields)
				require.Len(t, schema.Diagnostics, 1)
				require.Equal(t, "parse_failed", schema.Diagnostics[0].Code)
			})
		}
	}
}
