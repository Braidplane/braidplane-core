package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadBootstrapPassword(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{input: "safe password\n", want: "safe password"},
		{input: "safe password\r\n", want: "safe password"},
		{input: "  preserved spaces  ", want: "  preserved spaces  "},
		{input: "short", invalid: true},
		{input: strings.Repeat("x", 1025), invalid: true},
		{input: "first-password\nsecond-password", invalid: true},
	} {
		got, err := readBootstrapPassword(strings.NewReader(tc.input))
		if tc.invalid {
			require.Error(t, err)
			require.Empty(t, got)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}
