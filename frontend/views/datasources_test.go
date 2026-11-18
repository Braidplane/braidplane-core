package views

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderRepositoryWebHref(t *testing.T) {
	t.Parallel()
	for _, href := range []string{
		"https://gitlab.example.test/group/repository",
		"http://gitlab.example.test/group/repository",
	} {
		require.Equal(t, href, providerRepositoryWebHref(href))
	}
	for _, href := range []string{
		"mailto:fixture@example.test",
		"//gitlab.example.test/group/repository",
		"https://fixture@gitlab.example.test/group/repository",
		"/group/repository",
	} {
		require.Empty(t, providerRepositoryWebHref(href))
	}
}
