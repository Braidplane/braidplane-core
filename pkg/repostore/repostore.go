package repostore

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrFileNotFound      = xerror.NotFound(xerror.RepoFile)
	ErrFileExists        = xerror.AlreadyExists(xerror.RepoFile)
	ErrInvalidPath       = xerror.InvalidInput(xerror.RepoFile)
	ErrRepositorySetup   = xerror.Unavailable(xerror.RepoFile)
	ErrRemoteRefNotFound = xerror.New(
		xerror.KindNotFound,
		"REPOSITORY_REMOTE_REF_NOT_FOUND",
		"remote repository ref not found",
	)
)

// FileEntry describes a file returned by ListFiles.
type FileEntry struct {
	Path string
	Size int64
}

// RemoteBranch describes a branch returned by a remote git repository.
type RemoteBranch struct {
	Name      string
	IsDefault bool
}

// PushOptions controls git push behavior for PushRemote.
type PushOptions struct {
	Force   bool
	Refspec string
}

// RepoStore abstracts a content-addressed file store.
// Implementations must be safe for sequential use but are not required to be
// safe for concurrent use.
type RepoStore interface {
	// ReadFile returns up to maxBytes of the file at path. A positive maxBytes
	// must be enforced before the implementation materializes file contents.
	// Returns ErrFileNotFound if the file does not exist.
	ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error)

	// WriteFile creates or overwrites the file at path with data.
	// message is an opaque commit description used by git-backed implementations.
	WriteFile(ctx context.Context, path string, data []byte, message string) error

	// DeleteFile removes the file at path.
	// Returns ErrFileNotFound if the file does not exist.
	DeleteFile(ctx context.Context, path, message string) error

	// ListFiles returns all files under dir recursively.
	// Pass "" or "." to list files from the repository root.
	ListFiles(ctx context.Context, dir string) ([]FileEntry, error)

	// FileExists reports whether the file at path exists.
	FileExists(ctx context.Context, path string) (bool, error)
}
