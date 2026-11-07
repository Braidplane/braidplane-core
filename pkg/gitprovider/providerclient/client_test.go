package providerclient

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNewProposalReaderRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()

	_, err := NewProposalReader(t.Context(), repostore.ProviderGeneric, "https://example.com/acme/pipelines.git", nil)

	var typed *xerror.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, xerror.KindInvalidInput, typed.Kind())
	require.Equal(t, "secret", tokenFromAuthEnv([]string{"OTHER=value", "GITHUB_TOKEN=secret"}, "GITHUB_TOKEN"))
}
