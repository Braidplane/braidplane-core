package frontend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnifiedLineDiff(t *testing.T) {
	t.Parallel()

	t.Run("replace line includes unified headers and context", func(t *testing.T) {
		t.Parallel()

		diff := unifiedLineDiff(
			"canonical",
			"draft",
			[]byte("{\n  \"name\": \"canonical\"\n}\n"),
			[]byte("{\n  \"name\": \"draft\"\n}\n"),
			3,
		)

		require.Contains(t, diff, "--- canonical\n")
		require.Contains(t, diff, "+++ draft\n")
		require.Contains(t, diff, "@@ -1,3 +1,3 @@\n")
		require.Contains(t, diff, "-  \"name\": \"canonical\"\n")
		require.Contains(t, diff, "+  \"name\": \"draft\"\n")
		require.Contains(t, diff, " {\n")
		require.Contains(t, diff, " }\n")
	})

	t.Run("insert and delete produce separate markers", func(t *testing.T) {
		t.Parallel()

		diff := unifiedLineDiff(
			"canonical",
			"draft",
			[]byte("alpha\nbeta\ngamma\n"),
			[]byte("alpha\ngamma\ndelta\n"),
			1,
		)

		require.Contains(t, diff, "-beta\n")
		require.Contains(t, diff, "+delta\n")
		require.Contains(t, diff, " gamma\n")
	})

	t.Run("zero context still emits the full changed block", func(t *testing.T) {
		t.Parallel()

		diff := unifiedLineDiff(
			"canonical",
			"draft",
			[]byte("one\ntwo\nthree\n"),
			[]byte("one\nTWO\nTHREE\n"),
			0,
		)

		require.Contains(t, diff, "@@ -2,2 +2,2 @@\n")
		require.Contains(t, diff, "-two\n")
		require.Contains(t, diff, "-three\n")
		require.Contains(t, diff, "+TWO\n")
		require.Contains(t, diff, "+THREE\n")
		require.NotContains(t, diff, " one\n")
	})

	t.Run("oversized comparable state omits diff body", func(t *testing.T) {
		t.Parallel()

		before := []byte(strings.Repeat("before\n", unifiedDiffMaxLines+1))
		after := []byte(strings.Repeat("after\n", unifiedDiffMaxLines+1))

		diff := unifiedLineDiff("canonical", "draft", before, after, 3)

		require.Contains(t, diff, "--- canonical\n")
		require.Contains(t, diff, "+++ draft\n")
		require.Contains(t, diff, "diff omitted: comparable state too large")
		require.NotContains(t, diff, "-before\n")
		require.NotContains(t, diff, "+after\n")
	})
}
