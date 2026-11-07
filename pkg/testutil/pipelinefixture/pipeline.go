// Package pipelinefixture materializes pipeline test data through the same
// Git reconciliation boundary used by production.
package pipelinefixture

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

const (
	testSourceRef = "refs/heads/main"
)

// Materialize reconciles the complete supplied pipeline set into one project.
// Callers must include every pipeline that should remain materialized because
// reconciliation removes project pipelines absent from the supplied snapshot.
func Materialize(
	ctx context.Context,
	store pipelinesvc.PipelineStore,
	orgSlug, projectSlug slug.Slug,
	pipelines ...*pipelinespec.Pipeline,
) error {
	files := make(map[string][]byte, len(pipelines))
	for _, pipeline := range pipelines {
		if pipeline == nil {
			return xerror.Unexpected().Detail("materialize test pipeline: pipeline is nil")
		}
		data, err := json.Marshal(pipeline)
		if err != nil {
			return xerror.Wrapf("materialize test pipeline %s: %w", pipeline.Metadata.Slug, err)
		}
		files[fmt.Sprintf("pipelines/%s.pipeline.json", pipeline.Metadata.Slug)] = data
	}

	result, err := store.ReconcileFromGit(
		ctx,
		orgSlug,
		projectSlug,
		pipelinegit.NewSnapshotRepoStore(files),
		pipelinesvc.ReconcileFromGitParams{
			PipelineDir:       "pipelines",
			SourceRef:         stringPointer(testSourceRef),
			LastAppliedCommit: stringPointer(snapshotCommit(files)),
		},
	)
	if err != nil {
		return xerror.Wrapf("materialize test pipelines: %w", err)
	}
	if result.Failed != 0 {
		return xerror.Unexpected().Detailf(
			"materialize test pipelines: %d reconciliation failures: %+v",
			result.Failed,
			result.Failures,
		)
	}
	return nil
}

func snapshotCommit(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	hash := sha256.New()
	for _, path := range paths {
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(files[path])
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))[:40]
}

func stringPointer(value string) *string {
	return &value
}
