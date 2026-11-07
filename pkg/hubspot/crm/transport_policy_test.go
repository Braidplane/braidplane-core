package crm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsPlaintextEvenWithPrivateHostAccess(t *testing.T) {
	t.Parallel()
	_, err := New(t.Context(), Config{BaseURL: "http://127.0.0.1:1", AllowPrivateHosts: true}, "synthetic-token")
	require.ErrorContains(t, err, "requires HTTPS")
}
