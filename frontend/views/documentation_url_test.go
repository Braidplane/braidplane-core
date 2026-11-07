package views

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDocumentationPageURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://docs.example.test/guides/"+"connectors/google-workspace/",
		DocumentationPageURL("https://docs.example.test/guides/", "connectors/google-workspace/"))
	require.Empty(t, DocumentationPageURL("", "connectors/google-workspace/"))
	require.Empty(t, DocumentationPageURL("://invalid", "connectors/google-workspace/"))
}
