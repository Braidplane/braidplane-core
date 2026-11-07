package ftp_e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	ftpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestFTPConnectorStreamsNativeFilesAndPreservesPriorGoodOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated FTP provider")
	}
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 30*time.Second)
	defer cancel()
	ctx = filestream.WithStore(ctx, filestreamfixture.New())
	connector, err := ftpconnector.New(ctx, testConfig(depsports.FTPConnectorPort()), testCredentials())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connector.Close() })
	limits := types.ExecutionLimits{MaxResponseBytes: 1024, MaxFileBytes: 1024 * 1024}
	source, err := executeFTPWithLimits(ctx, connector, map[string]any{"operation": "read", "path": "users.csv", "format": "file"}, nil, limits)
	require.NoError(t, err)
	require.NotNil(t, source.Reference)
	require.Empty(t, source.Content)
	target := "stream-" + uuid.New().String() + ".bin"
	operation := map[string]any{"operation": "write", "path": target, "format": "file", "content": "{{input}}"}
	written, err := executeFTPWithLimits(ctx, connector, operation, map[string]any{sinkbinding.Input: *source.Reference}, limits)
	require.NoError(t, err)
	require.EqualValues(t, source.Reference.SizeBytes, *written.WrittenBytes)
	readback, err := executeFTPWithLimits(ctx, connector, map[string]any{"operation": "read", "path": target, "format": "file"}, nil, limits)
	require.NoError(t, err)
	require.Equal(t, source.File.Digest, readback.File.Digest)
	require.Equal(t, source.File.SizeBytes, readback.File.SizeBytes)
	invalid := *source.Reference
	invalid.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = executeFTPWithLimits(ctx, connector, operation, map[string]any{sinkbinding.Input: invalid}, limits)
	require.Error(t, err)
	// A failed temporary upload must not replace the prior-good destination.
	after, err := executeFTPWithLimits(ctx, connector, map[string]any{"operation": "read", "path": target, "format": "file"}, nil, limits)
	require.NoError(t, err)
	require.Equal(t, source.File.Digest, after.File.Digest)
	operation["overwrite"] = false
	_, err = executeFTPWithLimits(ctx, connector, operation, map[string]any{sinkbinding.Input: *source.Reference}, limits)
	require.ErrorIs(t, err, xerror.Unsupported(xerror.DataSource))
}
