package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		err            error
		constraintName string
		expected       bool
	}{
		{
			name: "UniqueViolationErrorWithoutConstraintCheck",
			err: &pgconn.PgError{
				Code: errCodeUniqueViolation,
			},
			expected: true,
		},
		{
			name: "UniqueViolationErrorWithMatchingConstraint",
			err: &pgconn.PgError{
				Code:           errCodeUniqueViolation,
				ConstraintName: "users_email_unique",
			},
			constraintName: "users_email_unique",
			expected:       true,
		},
		{
			name: "UniqueViolationErrorWithNonMatchingConstraint",
			err: &pgconn.PgError{
				Code:           errCodeUniqueViolation,
				ConstraintName: "users_email_unique",
			},
			constraintName: "users_username_unique",
			expected:       false,
		},
		{
			name: "UniqueViolationErrorWithEmptyConstraintCheck",
			err: &pgconn.PgError{
				Code:           errCodeUniqueViolation,
				ConstraintName: "users_email_unique",
			},
			constraintName: "",
			expected:       true,
		},
		{
			name: "ForeignKeyViolationError",
			err: &pgconn.PgError{
				Code: errCodeForeignKeyViolation,
			},
			expected: false,
		},
		{
			name: "OtherPostgresError",
			err: &pgconn.PgError{
				Code: "22001", // string_data_right_truncation
			},
			expected: false,
		},
		{
			name:     "GenericError",
			err:      assert.AnError,
			expected: false,
		},
		{
			name:     "NilError",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var result bool
			if tt.constraintName != "" {
				result = IsUniqueViolation(tt.err, tt.constraintName)
			} else {
				result = IsUniqueViolation(tt.err)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsForeignKeyViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name: "ForeignKeyViolationError",
			err: &pgconn.PgError{
				Code: errCodeForeignKeyViolation,
			},
			expected: true,
		},
		{
			name: "UniqueViolationError",
			err: &pgconn.PgError{
				Code: errCodeUniqueViolation,
			},
			expected: false,
		},
		{
			name: "OtherPostgresError",
			err: &pgconn.PgError{
				Code: "22001",
			},
			expected: false,
		},
		{
			name:     "GenericError",
			err:      assert.AnError,
			expected: false,
		},
		{
			name:     "NilError",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := IsForeignKeyViolation(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
