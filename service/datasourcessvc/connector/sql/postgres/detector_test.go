package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsQueryStatement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		statement string
		want      bool
	}{
		// SELECT statements (should return true)
		{
			name:      "BasicSelect",
			statement: "SELECT * FROM users",
			want:      true,
		},
		{
			name:      "SelectWithWhere",
			statement: "SELECT id, name FROM users WHERE active = true",
			want:      true,
		},
		{
			name:      "SelectCount",
			statement: "SELECT COUNT(*) FROM users",
			want:      true,
		},
		{
			name:      "SelectMixedCase",
			statement: "SeLeCt * FrOm users",
			want:      true,
		},

		// WITH (CTE) statements (should return true)
		{
			name:      "WithCTE",
			statement: "WITH recent AS (SELECT * FROM orders) SELECT * FROM recent",
			want:      true,
		},

		// EXPLAIN statements (should return true)
		{
			name:      "Explain",
			statement: "EXPLAIN SELECT * FROM users",
			want:      true,
		},

		// DESCRIBE statements (should return true)
		{
			name:      "Describe",
			statement: "DESCRIBE users",
			want:      true,
		},

		// PostgreSQL RETURNING clause (should return true - makes DML act like SELECT)
		{
			name:      "InsertReturning",
			statement: "INSERT INTO users (name) VALUES ('Alice') RETURNING id",
			want:      true,
		},
		{
			name:      "UpdateReturning",
			statement: "UPDATE users SET active = false WHERE id = 1 RETURNING id",
			want:      true,
		},
		{
			name:      "DeleteReturning",
			statement: "DELETE FROM users WHERE id = 1 RETURNING *",
			want:      true,
		},
		{
			name:      "ReturningMixedCase",
			statement: "INSERT INTO users (name) VALUES ('Test') ReTuRnInG id",
			want:      true,
		},

		// INSERT without RETURNING (should return false)
		{
			name:      "InsertSingleRow",
			statement: "INSERT INTO users (name) VALUES ('Alice')",
			want:      false,
		},
		{
			name:      "InsertMultipleRows",
			statement: "INSERT INTO users (name) VALUES ('Bob'), ('Charlie')",
			want:      false,
		},

		// UPDATE without RETURNING (should return false)
		{
			name:      "UpdateSingleRow",
			statement: "UPDATE users SET active = false WHERE id = 1",
			want:      false,
		},

		// DELETE without RETURNING (should return false)
		{
			name:      "DeleteSingleRow",
			statement: "DELETE FROM users WHERE id = 1",
			want:      false,
		},

		// DDL statements (should return false)
		{
			name:      "CreateTable",
			statement: "CREATE TABLE users (id SERIAL PRIMARY KEY)",
			want:      false,
		},
		{
			name:      "DropTable",
			statement: "DROP TABLE IF EXISTS temp_users",
			want:      false,
		},
		{
			name:      "AlterTable",
			statement: "ALTER TABLE users ADD COLUMN age INTEGER",
			want:      false,
		},
		{
			name:      "TruncateTable",
			statement: "TRUNCATE TABLE users",
			want:      false,
		},

		// Other statements (should return false)
		{
			name:      "Set",
			statement: "SET timezone = 'UTC'",
			want:      false,
		},

		// Edge cases: leading whitespace
		{
			name:      "LeadingSpaces",
			statement: "   SELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingNewlines",
			statement: "\n\n\nSELECT * FROM users",
			want:      true,
		},

		// Edge cases: leading comments
		{
			name:      "LeadingSingleLineComment",
			statement: "-- This is a comment\nSELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingBlockComment",
			statement: "/* This is a block comment */\nSELECT * FROM users",
			want:      true,
		},
		{
			name:      "CommentBeforeInsert",
			statement: "-- Insert user\nINSERT INTO users (name) VALUES ('Test')",
			want:      false,
		},

		// Edge cases: multi-line statements
		{
			name:      "MultiLineSelect",
			statement: "SELECT \nid, \nname \nFROM users",
			want:      true,
		},
		{
			name:      "MultiLineInsert",
			statement: "INSERT INTO users \n(name) \nVALUES \n('Alice')",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isQueryStatement(tt.statement)
			assert.Equal(t, tt.want, got, "isQueryStatement(%q)", tt.statement)
		})
	}
}
