package postgres

import (
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// PostgreSQL error codes
const (
	// errCodeUniqueViolation is the PostgreSQL error code for unique constraint violations
	errCodeUniqueViolation = "23505"
	// errCodeForeignKeyViolation is the PostgreSQL error code for foreign key violations
	errCodeForeignKeyViolation = "23503"
)

// IsUniqueViolation checks if the error is a unique constraint violation.
// If constraintName is provided, it also checks if the constraint name matches.
func IsUniqueViolation(err error, constraintName ...string) bool {
	var pgErr *pgconn.PgError
	if xerror.As(err, &pgErr) {
		if pgErr.Code != errCodeUniqueViolation {
			return false
		}
		// If constraint name is provided, check if it matches
		if len(constraintName) > 0 && constraintName[0] != "" {
			return pgErr.ConstraintName == constraintName[0]
		}
		return true
	}
	return false
}

// IsForeignKeyViolation checks if the error is a foreign key constraint violation
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if xerror.As(err, &pgErr) {
		return pgErr.Code == errCodeForeignKeyViolation
	}
	return false
}
