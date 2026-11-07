package views

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppHeadIncludesAppIconMetadata(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, AppHead("Braidplane", false).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `name="theme-color" content="#0b1118"`)
	require.Contains(t, html, `rel="icon" href="/assets/icons/app/favicon.ico?v=`)
	require.Contains(t, html, `rel="icon" href="/assets/icons/app/favicon-32.png?v=`)
	require.Contains(t, html, `rel="icon" href="/assets/icons/app/favicon-16.png?v=`)
	require.Contains(t, html, `rel="apple-touch-icon" href="/assets/icons/app/apple-touch-icon.png?v=`)
	require.Contains(t, html, `rel="manifest" href="/assets/site.webmanifest?v=`)
	require.Contains(t, html, `rel="stylesheet" href="/assets/css/app-custom.css?v=`)
}
