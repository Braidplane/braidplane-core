// uuid is an auxiliary package providing UUID generation and parsing for
// internal use. It wraps github.com/gofrs/uuid, which is kept because the
// database pool uses github.com/jackc/pgx-gofrs-uuid for pgx TypeMap
// registration and both must stay in sync.

package uuid

import (
	"github.com/gofrs/uuid"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type UUID struct {
	uuid.UUID
}

func FromString(s string) (UUID, error) {
	uid, err := uuid.FromString(s)
	if err != nil {
		return Nil, xerror.InvalidInput(xerror.JSON).Detailf("parse UUID: %w", err)
	}
	return UUID{UUID: uid}, nil
}

func New() UUID {
	return UUID{UUID: uuid.Must(uuid.NewV4())}
}

var Nil = UUID{}
