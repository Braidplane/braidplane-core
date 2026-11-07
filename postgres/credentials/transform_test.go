package credentials

import (
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestMapRepositoryErrorPreservesMissingRowCause(t *testing.T) {
	t.Parallel()
	for name, source := range map[string]error{"sql": sql.ErrNoRows, "pgx": pgx.ErrNoRows} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cause := xerror.Unexpected().With(source).Detail("credential lookup")
			err := mapRepositoryError(cause)
			require.ErrorIs(t, err, ErrCredentialNotFound)
			require.ErrorIs(t, err, source)
			require.ErrorContains(t, err, "credential lookup")
		})
	}
}
