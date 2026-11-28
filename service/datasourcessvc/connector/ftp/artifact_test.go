package ftp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
)

func TestPrepareArtifactCSV(t *testing.T) {
	id := uuid.New()
	content := []any{map[string]any{"id": 102, "amount": "20.00"}}
	cfg := Config{BasePath: "/base"}
	prepare := func(id uuid.UUID, directory string, limit int64) (ArtifactEvidence, []byte, error) {
		return PrepareArtifactCSV(cfg, directory, "orders.csv", id, limit, content, filedata.ParseCSVOptions{})
	}
	evidence, body, err := prepare(id, "reserved", 100)
	require.NoError(t, err)
	require.Equal(t, "amount,id\n20.00,102\n", string(body))
	require.Equal(t, "/base/reserved/orders-"+id.String()+".csv", evidence.FinalPath)
	require.Equal(t, "/base/reserved/braidplane-staging-"+id.String()+".part", evidence.StagingPath)
	other, _, err := prepare(uuid.New(), "reserved", 100)
	require.NoError(t, err)
	require.NotEqual(t, evidence.FinalPath, other.FinalPath)
	require.Equal(t, evidence.SHA256, other.SHA256)
	_, _, err = prepare(id, "reserved", int64(len(body)-1))
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	_, _, err = prepare(id, "../reserved", 100)
	require.Error(t, err)
}

func TestArtifactCSVPreservesEmptySingleColumnRecord(t *testing.T) {
	options := filedata.ParseCSVOptions{}
	content := []any{map[string]any{"id": ""}, map[string]any{"id": "102"}}
	_, body, err := PrepareArtifactCSV(Config{}, "reserved", "orders.csv", uuid.New(), 100, content, options)
	require.NoError(t, err)
	parsed, err := filedata.ParseCSV(body, options)
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"id": ""}, {"id": "102"}}, parsed.Rows)
	_, _, err = PrepareArtifactCSV(Config{}, "reserved", "orders.csv", uuid.New(), 100, []any{map[string]any{" ": "102"}}, options)
	require.Error(t, err)
	options.MaxRows = 1
	_, _, err = PrepareArtifactCSV(Config{}, "reserved", "orders.csv", uuid.New(), 100, content, options)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
	_, _, err = PrepareArtifactCSV(Config{}, "reserved", "orders.csv", uuid.New(), 100, []any{map[string]any{"id": "a\x00b"}}, options)
	require.Error(t, err)
}
