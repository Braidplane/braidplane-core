package git

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

// TestIntrospectSchema_PreparesPinnedRemote ensures schema cloning gets the same destination
// pinning and redirect safeguards as the execution and connectivity Git paths.
//
// The test replaces a package-local clone seam while exercising the call.
func TestIntrospectSchema_PreparesPinnedRemote(t *testing.T) {
	originalCloneRemote := cloneRemoteForSchema
	t.Cleanup(func() {
		cloneRemoteForSchema = originalCloneRemote
	})

	var clonedURL string
	var clonedEnv []string
	cloneRemoteForSchema = func(
		_ context.Context,
		repoURL, _ string,
		env []string,
	) (repostore.RepoStore, string, func(), error) {
		clonedURL = repoURL
		clonedEnv = env
		return nil, "", func() {}, xerror.Unexpected().Detail("stop after pinned remote assertion")
	}

	connector := &Connector{
		config: Config{RepoURL: "https://alice:secret@93.184.216.34/acme/repo.git"},
		creds:  Credentials{AuthType: AuthTypeHTTPS, Username: "alice", Token: "secret"},
		prepareRemoteFunc: func(_ context.Context, repoURL string, env []string) (string, []string, error) {
			return "https://93.184.216.34/acme/repo.git", append(env,
				"GIT_CONFIG_KEY_0=http.curloptResolve",
				"GIT_CONFIG_VALUE_0=93.184.216.34:443:93.184.216.34",
				"GIT_CONFIG_KEY_1=http.followRedirects",
				"GIT_CONFIG_VALUE_1=false",
			), nil
		},
	}

	err := func() error {
		_, err := connector.IntrospectSchema(context.Background())
		return err
	}()

	require.ErrorContains(t, err, "shallow clone for schema introspection")
	require.Equal(t, "https://93.184.216.34/acme/repo.git", clonedURL)
	require.Contains(t, clonedEnv, "GIT_CONFIG_KEY_0=http.curloptResolve")
	require.Contains(t, clonedEnv, "GIT_CONFIG_VALUE_0=93.184.216.34:443:93.184.216.34")
	require.Contains(t, clonedEnv, "GIT_CONFIG_KEY_1=http.followRedirects")
	require.Contains(t, clonedEnv, "GIT_CONFIG_VALUE_1=false")
	helperConfigured := false
	for _, value := range clonedEnv {
		if strings.HasPrefix(value, "GIT_CONFIG_VALUE_") && strings.Contains(value, "BRAIDPLANE_GIT_PASSWORD") {
			helperConfigured = true
			require.NotContains(t, value, "secret")
		}
	}
	require.True(t, helperConfigured)
	require.Contains(t, clonedEnv, "GIT_TERMINAL_PROMPT=0")
	require.NotContains(t, clonedURL, "secret")
	require.NotContains(t, strings.Join(clonedEnv, "\n"), "GIT_ASKPASS=")
}

func TestRelativeFileNameNormalizesBrowseScope(t *testing.T) {
	for _, base := range []string{"", ".", "./", " . "} {
		require.Equal(t, "users.csv", relativeFileName(base, "users.csv"))
	}
	for _, base := range []string{"data", "./data", "data/", "data/../data", "data\\"} {
		require.Equal(t, "nested/users.csv", relativeFileName(base, "data/nested/users.csv"))
		require.Empty(t, relativeFileName(base, "database/users.csv"))
	}
}

func TestGitSchemaDiscoversNestedStructuredAndOpaqueFiles(t *testing.T) {
	for _, base := range []string{"", ".", "./", "data", "./data/", "data/../data"} {
		t.Run(base, func(t *testing.T) {
			store := newFakeRepoStore()
			store.files["data/nested/users.csv"] = []byte("id,name\n1,Ada\n")
			store.files["data/report.pdf"] = []byte("opaque bytes")
			schema, err := gitFileAdapter.IntrospectSchema(context.Background(), gitSchemaProvider{store: store, baseDir: base}, remotefile.SchemaOptions{})
			require.NoError(t, err)
			require.Len(t, schema.Files, 2)
			require.Len(t, schema.Files[0].Fields, 2)
			require.Empty(t, schema.Files[1].Fields)
			require.Equal(t, int64(12), *schema.Files[1].SizeBytes)
		})
	}
}

func TestRelativeFileNamePreservesLiteralNames(t *testing.T) {
	require.Equal(t, " report.csv ", relativeFileName(".", " report.csv "))
	require.Equal(t, "nested/report.csv ", relativeFileName("data", "data/nested/report.csv "))
}
