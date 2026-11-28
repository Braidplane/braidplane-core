package ftp_e2e

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	ftpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
)

func TestFTPArtifactHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated FTP provider")
	}
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 30*time.Second)
	defer cancel()
	cfg := testConfig(depsports.FTPConnectorPort())
	connect := func() *ftpconnector.Connector {
		conn, err := ftpconnector.New(ctx, cfg, testCredentials())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	opts := filedata.ParseCSVOptions{}
	prepare := func() (ftpconnector.ArtifactEvidence, []byte) {
		evidence, body, err := ftpconnector.PrepareArtifactCSV(cfg, "nested", "orders.csv", uuid.New(), 1024, []any{
			map[string]any{"id": "102"},
		}, opts)
		require.NoError(t, err)
		return evidence, body
	}

	conn := connect()
	evidence, body := prepare()
	renames := 0
	beforeRename := func(context.Context) error { renames++; return nil }
	require.NoError(t, conn.PublishArtifact(ctx, evidence, bytes.NewReader(body), beforeRename))
	rows, err := conn.FetchArtifactCSV(ctx, evidence, 1024, opts)
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"id": "102"}}, rows.Rows)
	require.Error(t, conn.PublishArtifact(ctx, evidence, bytes.NewReader(body), beforeRename))
	require.Equal(t, 1, renames)

	changed := evidence
	changed.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rows, err = conn.FetchArtifactCSV(ctx, changed, 1024, opts)
	require.Error(t, err)
	require.Nil(t, rows)

	short, full := prepare()
	require.Error(t, conn.PublishArtifact(ctx, short, bytes.NewReader(full[:len(full)-1]), beforeRename))
	require.Equal(t, 1, renames)
	conn = connect()
	rows, err = conn.FetchArtifactCSV(ctx, short, 1024, opts)
	require.Error(t, err)
	require.Nil(t, rows)
	require.Error(t, conn.PublishArtifact(ctx, short, bytes.NewReader(full), beforeRename))
	require.Equal(t, 1, renames)
}
