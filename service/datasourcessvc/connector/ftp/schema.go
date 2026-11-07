package ftp

import (
	"context"
	"math"
	"strings"

	ftpclient "github.com/jlaffaye/ftp"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

// IntrospectSchema enumerates supported files and infers schema fields.
func (c *Connector) IntrospectSchema(ctx context.Context) (*filedata.Schema, error) {
	if c == nil || c.conn == nil {
		return nil, xerror.Unexpected().Detail("FTP connector is not initialized")
	}

	return ftpFileAdapter.IntrospectSchema(ctx, ftpSchemaProvider{connector: c}, remotefile.SchemaOptions{})
}

type ftpSchemaProvider struct {
	connector *Connector
}

// ListFiles maps FTP directory entries into remote-file schema candidates.
func (provider ftpSchemaProvider) ListFiles(ctx context.Context) (files []remotefile.SchemaFile, err error) {
	finish, err := provider.connector.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if ctxErr := finish(); ctxErr != nil {
			files, err = nil, ctxErr
		}
	}()
	return provider.listDirectory(ctx, "", 0)
}

func (provider ftpSchemaProvider) listDirectory(ctx context.Context, relative string, depth int) ([]remotefile.SchemaFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth > 32 {
		return nil, xerror.ExecutionUnavailable().Detail("schema discovery exceeds 32 directory levels")
	}
	directory := provider.connector.basePath
	if relative != "" {
		var err error
		directory, err = joinFTPPath(directory, relative)
		if err != nil {
			return nil, err
		}
	}
	entries, err := provider.connector.conn.List(directory)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	files := make([]remotefile.SchemaFile, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry == nil || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\") {
			continue
		}
		if entry.Type != ftpclient.EntryTypeFile && entry.Type != ftpclient.EntryTypeFolder {
			continue
		}
		name := entry.Name
		if relative != "" {
			name = relative + "/" + name
		}
		target, err := joinFTPPath(provider.connector.basePath, name)
		if err != nil {
			continue
		}
		if err := schemaquota.Charge(ctx, 1, len(name)+len(target)); err != nil {
			return nil, err
		}
		if entry.Type == ftpclient.EntryTypeFolder {
			nested, err := provider.listDirectory(ctx, name, depth+1)
			if err != nil {
				return nil, err
			}
			files = append(files, nested...)
			continue
		}
		if entry.Size > math.MaxInt64 {
			return nil, xerror.ExecutionUnavailable().Detail("invalid FTP file size")
		}
		size := int64(entry.Size)
		files = append(files, remotefile.SchemaFile{Name: name, Target: target, SizeBytes: &size})
	}
	return files, nil
}

// ReadFile samples FTP bytes for shared schema inference.
func (provider ftpSchemaProvider) ReadFile(
	ctx context.Context,
	target string,
	maxBytes int64,
) (*remotefile.ReadResult, error) {
	return ftpProvider(provider).ReadFile(ctx, target, maxBytes)
}
