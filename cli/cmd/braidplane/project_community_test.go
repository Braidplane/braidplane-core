//go:build !managed

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommunityProjectCommandsExcludeDemo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runWithArgs([]string{"braidplane", "project", "--help"}, &stdout, &stderr)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "repository")
	require.NotContains(t, stdout.String(), "settlement-review-reset")
}
