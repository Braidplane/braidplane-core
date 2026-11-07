package pipelinefixture

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotCommitIsStableAndContentSensitive(t *testing.T) {
	t.Parallel()

	first := snapshotCommit(map[string][]byte{
		"pipelines/b.pipeline.json": []byte(`{"name":"B"}`),
		"pipelines/a.pipeline.json": []byte(`{"name":"A"}`),
	})
	reordered := snapshotCommit(map[string][]byte{
		"pipelines/a.pipeline.json": []byte(`{"name":"A"}`),
		"pipelines/b.pipeline.json": []byte(`{"name":"B"}`),
	})
	changed := snapshotCommit(map[string][]byte{
		"pipelines/a.pipeline.json": []byte(`{"name":"A2"}`),
		"pipelines/b.pipeline.json": []byte(`{"name":"B"}`),
	})

	require.Len(t, first, 40)
	require.Equal(t, first, reordered)
	require.NotEqual(t, first, changed)
}
