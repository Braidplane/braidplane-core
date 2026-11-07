package pipelinegit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func TestPipelineRepositoryTemplateContainsOnlyVersionedReferences(t *testing.T) {
	t.Parallel()

	files := RepositoryTemplate("definitions/pipelines")

	require.Len(t, files, 2)
	require.Equal(t, ".braidplane/template.yaml", files[0].Path)
	require.Contains(t, files[0].Content, "version: "+model.PipelineRepositoryTemplateVersion)
	require.Contains(t, files[0].Content, `pipeline_dir: "definitions/pipelines"`)
	require.Equal(t, "definitions/pipelines/README.md", files[1].Path)
	joined := strings.ToLower(files[0].Content + files[1].Content)
	for _, forbidden := range []string{"token:", "password:", "credential:", "environment:", "production_id", "binding:"} {
		require.NotContains(t, joined, forbidden)
	}
}
