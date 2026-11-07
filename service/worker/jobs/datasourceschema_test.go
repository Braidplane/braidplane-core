package jobs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riverqueue/river/rivershared/structtag"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestDataSourceSchemaImportArgs(t *testing.T) {
	t.Parallel()

	args := DataSourceSchemaImportArgs{
		OrganizationSlug: must.Slug("acme"),
		DataSourceSlug:   must.Slug("orders"),
		AttemptID:        "attempt-1",
		RequestedAt:      time.Date(2026, time.July, 7, 12, 0, 0, 0, time.UTC),
	}

	t.Run("UsesImportQueueAndUniqueArgs", func(t *testing.T) {
		t.Parallel()

		opts := args.InsertOpts()

		require.Equal(t, "data_source_schema_import", DataSourceSchemaImportQueue)
		require.Equal(t, "data_source_schema_import", args.Kind())
		require.Equal(t, DataSourceSchemaImportQueue, opts.Queue)
		require.True(t, opts.UniqueOpts.ByArgs)
		require.ElementsMatch(t, rivertype.UniqueOptsByStateDefault(), opts.UniqueOpts.ByState)
	})

	t.Run("DeduplicatesByAttemptNotOnlyDataSource", func(t *testing.T) {
		t.Parallel()

		sameDataSourceNewAttempt := args
		sameDataSourceNewAttempt.AttemptID = "attempt-2"

		require.NotEqual(
			t,
			dataSourceSchemaImportUniqueArgs(t, args),
			dataSourceSchemaImportUniqueArgs(t, sameDataSourceNewAttempt),
		)
	})
}

func dataSourceSchemaImportUniqueArgs(t *testing.T, args DataSourceSchemaImportArgs) map[string]string {
	t.Helper()

	payload, err := json.Marshal(args)
	require.NoError(t, err)

	uniqueFields, err := structtag.SortedFieldsWithTag(args, "unique")
	require.NoError(t, err)

	uniqueValues := structtag.ExtractValues(payload, uniqueFields)
	uniqueArgs := make(map[string]string, len(uniqueFields))
	for i, field := range uniqueFields {
		if uniqueValues[i] == "undefined" {
			continue
		}
		uniqueArgs[field] = uniqueValues[i]
	}

	return uniqueArgs
}
