package views

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvalidEmptyIdentifierRemainsVisible(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	require.NoError(t, CustomizeIdentifierWithError("", true, "feedback").Render(context.Background(), &out))
	require.Regexp(t, `<details[^>]* open>`, out.String())
	require.Contains(t, out.String(), `aria-errormessage="feedback"`)
}
