package passwordpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommonPasswordBlocklist(t *testing.T) {
	t.Parallel()
	require.Len(t, commonPasswords, commonPasswordCount)
	require.ErrorIs(t, CheckCommon("password"), ErrCommonPassword)
	require.ErrorIs(t, CheckCommon(" Password123 "), ErrCommonPassword)
	require.NoError(t, CheckCommon("  Synthetic moss\norbital-97  "))
}
