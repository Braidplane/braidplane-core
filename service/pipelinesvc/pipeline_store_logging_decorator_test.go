package pipelinesvc

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

func TestPipelineStoreWithLogging_DelegatesAndLogsMethods(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	store := NewPipelineStoreWithLogging(
		&fakeServicePipelineStore{},
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	ctx := context.Background()
	orgSlug := slug.Slug("org-a")
	pipelineSlug := slug.Slug("pipeline-a")
	specHash := "abc123"

	calls := []struct {
		method string
		call   func()
	}{
		{
			method: "FindSpec",
			call: func() {
				_, _ = store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
			},
		},
		{
			method: "FindSpecForUpdate",
			call: func() {
				_, _ = store.FindSpecForUpdate(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
			},
		},
		{
			method: "Get",
			call: func() {
				_, _ = store.Get(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
			},
		},
		{
			method: "List",
			call: func() {
				_, _ = store.List(ctx, orgSlug, defaultProjectSlug, model.ListRequest{Limit: 10})
			},
		},
		{
			method: "Exists",
			call: func() {
				_, _ = store.Exists(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
			},
		},
		{
			method: "FindDraft",
			call: func() {
				_, _ = store.FindDraft(ctx, orgSlug, defaultProjectSlug, pipelineSlug, slug.Slug("member-a"))
			},
		},
		{
			method: "SaveDraft",
			call: func() {
				_ = store.SaveDraft(ctx, orgSlug, defaultProjectSlug, slug.Slug("member-a"), testPipelineSpec(), &specHash)
			},
		},
		{
			method: "PrepareGitDraftPublish",
			call: func() {
				_, _ = store.PrepareGitDraftPublish(ctx, orgSlug, defaultProjectSlug, pipelineSlug, slug.Slug("member-a"), nil, &specHash, false)
			},
		},
		{
			method: "CompleteProposal",
			call: func() {
				_ = store.CompleteProposal(ctx, pipelinegit.ProposalCompletion{})
			},
		},
		{
			method: "DeleteDraft",
			call: func() {
				_ = store.DeleteDraft(ctx, orgSlug, defaultProjectSlug, pipelineSlug, slug.Slug("member-a"))
			},
		},
		{
			method: "ListVersions",
			call: func() {
				_, _ = store.ListVersions(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
			},
		},
		{
			method: "FindVersionBySpecHash",
			call: func() {
				_, _ = store.FindVersionBySpecHash(ctx, orgSlug, defaultProjectSlug, pipelineSlug, specHash)
			},
		},
		{
			method: "FindVersionBySpecHashPrefix",
			call: func() {
				_, _ = store.FindVersionBySpecHashPrefix(ctx, orgSlug, defaultProjectSlug, pipelineSlug, "abc")
			},
		},
		{
			method: "ReconcileFromGit",
			call: func() {
				_, _ = store.ReconcileFromGit(ctx, orgSlug, defaultProjectSlug, noopRepoStore{}, ReconcileFromGitParams{})
			},
		},
	}

	for _, tc := range calls {
		tc.call()
	}

	logs := buf.String()
	require.Equal(t, len(calls), strings.Count(logs, "service.method.completed"))
	for _, tc := range calls {
		require.Contains(t, logs, "service/pipelinesvc.PipelineStore."+tc.method)
	}
	require.NotContains(t, logs, orgSlug.String())
	require.NotContains(t, logs, pipelineSlug.String())
}

type noopRepoStore struct{}

func (noopRepoStore) ReadFile(context.Context, string, int64) ([]byte, error) {
	return nil, repostore.ErrFileNotFound
}

func (noopRepoStore) WriteFile(context.Context, string, []byte, string) error {
	return nil
}

func (noopRepoStore) DeleteFile(context.Context, string, string) error {
	return nil
}

func (noopRepoStore) ListFiles(context.Context, string) ([]repostore.FileEntry, error) {
	return nil, nil
}

func (noopRepoStore) FileExists(context.Context, string) (bool, error) {
	return false, nil
}
