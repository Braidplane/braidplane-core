package git

import (
	"context"
	"path"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

var cloneRemoteForSchema = repostore.CloneRemote

// IntrospectSchema shallow-clones the repository and infers schema from files in BasePath.
func (c *Connector) IntrospectSchema(ctx context.Context) (*filedata.Schema, error) {
	env, modifier, cleanup, err := authEnv(c.creds)
	if err != nil {
		return nil, xerror.Wrapf("prepare Git auth: %w", err)
	}
	defer cleanup()

	repoURL, err := modifier(c.config.RepoURL)
	if err != nil {
		return nil, xerror.Wrapf("build Git repo URL: %w", err)
	}
	repoURL, env, err = c.prepareRemote(ctx, repoURL, env)
	if err != nil {
		return nil, xerror.Wrapf("prepare Git destination: %w", err)
	}

	store, _, cleanup, err := cloneRemoteForSchema(ctx, repoURL, c.config.DefaultBranch, env)
	if err != nil {
		return nil, xerror.Wrapf("shallow clone for schema introspection: %w", err)
	}
	defer cleanup()

	baseDir := strings.Trim(strings.TrimSpace(c.config.BasePath), "/")
	return gitFileAdapter.IntrospectSchema(ctx, gitSchemaProvider{
		store:   store,
		baseDir: baseDir,
	}, remotefile.SchemaOptions{})
}

type gitSchemaProvider struct {
	store   repostore.RepoStore
	baseDir string
}

// ListFiles maps repository files into remote-file schema candidates.
func (provider gitSchemaProvider) ListFiles(ctx context.Context) ([]remotefile.SchemaFile, error) {
	entries, err := provider.store.ListFiles(ctx, provider.baseDir)
	if err != nil {
		return nil, xerror.Wrapf("list Git directory %q: %w", provider.baseDir, err)
	}

	files := make([]remotefile.SchemaFile, 0, len(entries))
	for _, entry := range entries {
		relName := relativeFileName(provider.baseDir, entry.Path)
		if relName == "" {
			continue
		}

		files = append(files, remotefile.SchemaFile{Name: relName, Target: entry.Path, SizeBytes: &entry.Size})
	}

	return files, nil
}

// ReadFile samples repository bytes for shared schema inference.
func (provider gitSchemaProvider) ReadFile(
	ctx context.Context,
	target string,
	maxBytes int64,
) (*remotefile.ReadResult, error) {
	return gitRepoProvider{store: provider.store}.ReadFile(ctx, target, maxBytes)
}

func relativeFileName(baseDir, fullPath string) string {
	normalizedPath := strings.Trim(fullPath, "/")
	if normalizedPath == "" {
		return ""
	}

	normalizedBase := path.Clean(strings.ReplaceAll(strings.TrimSpace(baseDir), "\\", "/"))
	if normalizedBase == "." {
		return normalizedPath
	}

	prefix := normalizedBase + "/"
	if !strings.HasPrefix(normalizedPath, prefix) {
		return ""
	}

	return strings.TrimPrefix(normalizedPath, prefix)
}
