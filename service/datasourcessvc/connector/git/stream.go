package git

import (
	"context"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

func (p gitRepoProvider) ReadFileTo(ctx context.Context, target string, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	store, ok := p.store.(repostore.StreamingStore)
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	return store.ReadFileTo(ctx, target, maxBytes, dst)
}

func (p gitRepoProvider) WriteFileFrom(ctx context.Context, req remotefile.StreamWriteRequest) (int64, error) {
	store, ok := p.store.(repostore.StreamingStore)
	if !ok {
		return 0, filestream.ErrUnavailable
	}
	if err := store.WriteFileFrom(ctx, req.Target, req.Body, req.Reference, req.Overwrite, p.commitMsg); err != nil {
		return 0, err
	}
	if err := repostore.PushRemote(ctx, p.repoDir, p.repoURL, p.env, repostore.PushOptions{}); err != nil {
		return 0, err
	}
	return req.Reference.SizeBytes, nil
}
