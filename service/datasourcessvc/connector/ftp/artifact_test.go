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
