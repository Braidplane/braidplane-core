package git

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestNativeFileReadPreservesRepositoryBlob(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		maxFileBytes int64
	}{{"Buffered", 0}, {"Retained", 100}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			storage := filestreamfixture.New()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			ctx = filestream.WithStore(ctx, storage)
			source, remote := t.TempDir(), t.TempDir()
			initBareRemote(t, remote)
			seedRemoteRepo(t, ctx, source, remote, map[string]string{
				".gitattributes": "*.bin text eol=crlf ident\n",
				"raw.bin":        "original\n$Id$\n",
			})
			connector := &Connector{config: Config{RepoURL: remote, DefaultBranch: "main"}, creds: Credentials{AuthType: AuthTypeHTTPS}}
			response, err := connector.Execute(ctx, []byte(`{"operation":"read","path":"raw.bin","format":"file"}`), nil, types.ExecutionLimits{MaxFileBytes: tc.maxFileBytes, MaxResponseBytes: 100})
			require.NoError(t, err)
			if tc.maxFileBytes > 0 && runtime.GOOS != "linux" {
				require.ErrorIs(t, response.Error(), xerror.ExecutionUnavailable())
				require.Contains(t, xerror.PublicDetail(response.Error()), "Linux executor")
				return
			}
			require.NoError(t, response.Error())
			file, ok := response.Git()
			require.True(t, ok)
			if tc.maxFileBytes == 0 {
				require.Equal(t, "original\n$Id$\n", string(file.Content))
				return
			}
			require.NotNil(t, file.Reference)
			body, err := storage.Open(ctx, *file.Reference, 100)
			require.NoError(t, err)
			content, err := io.ReadAll(body)
			require.NoError(t, err)
			require.NoError(t, body.Close())
			require.Equal(t, "original\n$Id$\n", string(content))
		})
	}
}

func TestNativeFileWriteUsesRetainedInput(t *testing.T) {
	t.Parallel()
	storage := filestreamfixture.New()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	ctx = filestream.WithStore(ctx, storage)
	content := "changed\r\n$Id$\r\n"
	ref, _, err := storage.Retain(ctx, 100, func(dst io.Writer) (*filestream.Metadata, error) {
		return filestream.Copy(ctx, dst, strings.NewReader(content), 100)
	})
	require.NoError(t, err)
	source, remote := t.TempDir(), t.TempDir()
	initBareRemote(t, remote)
	seedRemoteRepo(t, ctx, source, remote, map[string]string{".gitattributes": "*.bin text eol=crlf ident\n", "untouched.bin": "prior-good"})
	connector := &Connector{config: Config{RepoURL: remote, DefaultBranch: "main"}, creds: Credentials{AuthType: AuthTypeHTTPS}}
	response, err := connector.Execute(ctx, []byte(`{"operation":"write","path":"destination.bin","format":"file"}`), map[string]types.TypedParam{sinkbinding.Input: {Value: *ref, DataType: "text"}}, types.ExecutionLimits{MaxFileBytes: 100, MaxResponseBytes: 100})
	require.NoError(t, err)
	if runtime.GOOS != "linux" {
		require.ErrorIs(t, response.Error(), xerror.ExecutionUnavailable())
		require.Zero(t, storage.Opens.Load(), "unsupported workers must refuse before opening retained data")
		return
	}
	require.NoError(t, response.Error())
	require.EqualValues(t, 1, storage.Opens.Load())
	command := exec.CommandContext(ctx, "git", "show", "refs/heads/main:destination.bin")
	command.Dir = remote
	command.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	stored, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, content, string(stored))
}
