package mysql

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
			name:      "SelectWithJoin",
			statement: "SELECT u.name, o.total FROM users u JOIN orders o ON u.id = o.user_id",
			want:      true,
		},
		{
			name:      "SelectWithSubquery",
			statement: "SELECT * FROM (SELECT id FROM users) AS subq",
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
		{
			name:      "SelectLowercase",
			statement: "select * from users",
			want:      true,
		},
		{
			name:      "SelectUppercase",
			statement: "SELECT * FROM USERS",
			want:      true,
		},

		// WITH (CTE) statements (should return true)
		{
			name: "WithCTE",
			statement: `WITH recent AS (
				SELECT * FROM orders WHERE created_at > DATE_SUB(NOW(), INTERVAL 1 DAY)
			) SELECT * FROM recent`,
			want: true,
		},
		{
			name: "WithRecursive",
			statement: `WITH RECURSIVE cte AS (
				SELECT 1 AS n
				UNION ALL
				SELECT n + 1 FROM cte WHERE n < 10
			) SELECT * FROM cte`,
			want: true,
		},
		{
			name:      "WithMixedCase",
			statement: "WiTh temp AS (SELECT 1) SELECT * FROM temp",
			want:      true,
		},

		// EXPLAIN statements (should return true)
		{
			name:      "Explain",
			statement: "EXPLAIN SELECT * FROM users",
			want:      true,
		},
		{
			name:      "ExplainFormat",
			statement: "EXPLAIN FORMAT=JSON SELECT * FROM users WHERE id = 1",
			want:      true,
		},
		{
			name:      "ExplainMixedCase",
			statement: "ExPlAiN SELECT * FROM users",
			want:      true,
		},

		// DESCRIBE statements (should return true)
		{
			name:      "Describe",
			statement: "DESCRIBE users",
			want:      true,
		},
		{
			name:      "DescColumn",
			statement: "DESCRIBE users email",
			want:      true,
		},
		{
			name:      "DescribeMixedCase",
			statement: "DeScRiBe users",
			want:      true,
		},
		{
			name:      "Desc",
			statement: "DESC users",
			want:      true,
		},

		// SHOW statements (MySQL-specific, should return true)
		{
			name:      "ShowTables",
			statement: "SHOW TABLES",
			want:      true,
		},
		{
			name:      "ShowDatabases",
			statement: "SHOW DATABASES",
			want:      true,
		},
		{
			name:      "ShowColumns",
			statement: "SHOW COLUMNS FROM users",
			want:      true,
		},
		{
			name:      "ShowColumnsLike",
			statement: "SHOW COLUMNS FROM users LIKE 'email%'",
			want:      true,
		},
		{
			name:      "ShowCreateTable",
			statement: "SHOW CREATE TABLE users",
			want:      true,
		},
		{
			name:      "ShowIndexes",
			statement: "SHOW INDEXES FROM users",
			want:      true,
		},
		{
			name:      "ShowStatus",
			statement: "SHOW STATUS",
			want:      true,
		},
		{
			name:      "ShowVariables",
			statement: "SHOW VARIABLES LIKE 'max_connections'",
			want:      true,
		},
		{
			name:      "ShowProcessList",
			statement: "SHOW PROCESSLIST",
			want:      true,
		},
		{
			name:      "ShowMixedCase",
			statement: "ShOw TaBlEs",
			want:      true,
		},

		// INSERT statements (should return false - MySQL doesn't support RETURNING)
		{
			name:      "InsertSingleRow",
			statement: "INSERT INTO users (name, email) VALUES ('Alice', 'alice@example.com')",
			want:      false,
		},
		{
			name:      "InsertMultipleRows",
			statement: "INSERT INTO users (name) VALUES ('Bob'), ('Charlie'), ('Diana')",
			want:      false,
		},
		{
			name:      "InsertFromSelect",
			statement: "INSERT INTO archive SELECT * FROM users WHERE deleted = true",
			want:      false,
		},
		{
			name:      "InsertIgnore",
			statement: "INSERT IGNORE INTO users (email) VALUES ('test@example.com')",
			want:      false,
		},
		{
			name:      "InsertOnDuplicateKeyUpdate",
			statement: "INSERT INTO users (id, name) VALUES (1, 'Alice') ON DUPLICATE KEY UPDATE name = 'Alice'",
			want:      false,
		},
		{
			name:      "InsertMixedCase",
			statement: "InSeRt InTo users (name) VALUES ('Test')",
			want:      false,
		},

		// UPDATE statements (should return false)
		{
			name:      "UpdateSingleRow",
			statement: "UPDATE users SET active = false WHERE id = 1",
			want:      false,
		},
		{
			name:      "UpdateMultipleRows",
			statement: "UPDATE users SET last_login = NOW() WHERE active = true",
			want:      false,
		},
		{
			name:      "UpdateWithLimit",
			statement: "UPDATE users SET active = false ORDER BY created_at DESC LIMIT 10",
			want:      false,
		},
		{
			name:      "UpdateMixedCase",
			statement: "UpDaTe users SET name = 'Test' WHERE id = 1",
			want:      false,
		},

		// DELETE statements (should return false)
		{
			name:      "DeleteSingleRow",
			statement: "DELETE FROM users WHERE id = 1",
			want:      false,
		},
		{
			name:      "DeleteMultipleRows",
			statement: "DELETE FROM users WHERE active = false AND created_at < DATE_SUB(NOW(), INTERVAL 1 YEAR)",
			want:      false,
		},
		{
			name:      "DeleteAll",
			statement: "DELETE FROM temp_data",
			want:      false,
		},
		{
			name:      "DeleteWithLimit",
			statement: "DELETE FROM users WHERE active = false ORDER BY created_at LIMIT 100",
			want:      false,
		},
		{
			name:      "DeleteMixedCase",
			statement: "DeLeTe FrOm users WHERE id = 1",
			want:      false,
		},

		// DDL statements (should return false)
		{
			name:      "CreateTable",
			statement: "CREATE TABLE users (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255))",
			want:      false,
		},
		{
			name:      "CreateIndex",
			statement: "CREATE INDEX idx_users_email ON users(email)",
			want:      false,
		},
		{
			name:      "CreateDatabase",
			statement: "CREATE DATABASE testdb",
			want:      false,
		},
		{
			name:      "DropTable",
			statement: "DROP TABLE IF EXISTS temp_users",
			want:      false,
		},
		{
			name:      "DropDatabase",
			statement: "DROP DATABASE IF EXISTS olddb",
			want:      false,
		},
		{
			name:      "AlterTable",
			statement: "ALTER TABLE users ADD COLUMN age INT",
			want:      false,
		},
		{
			name:      "AlterTableModify",
			statement: "ALTER TABLE users MODIFY COLUMN name VARCHAR(500)",
			want:      false,
		},
		{
			name:      "TruncateTable",
			statement: "TRUNCATE TABLE users",
			want:      false,
		},
		{
			name:      "RenameTable",
			statement: "RENAME TABLE old_users TO new_users",
			want:      false,
		},
		{
			name:      "CreateMixedCase",
			statement: "CrEaTe TaBlE test (id INT)",
			want:      false,
		},

		// Other statements (should return false)
		{
			name:      "Set",
			statement: "SET SESSION sql_mode = 'STRICT_ALL_TABLES'",
			want:      false,
		},
		{
			name:      "SetGlobal",
			statement: "SET GLOBAL max_connections = 200",
			want:      false,
		},
		{
			name:      "Use",
			statement: "USE testdb",
			want:      false,
		},
		{
			name:      "Grant",
			statement: "GRANT SELECT ON testdb.* TO 'readonly'@'localhost'",
			want:      false,
		},
		{
			name:      "Revoke",
			statement: "REVOKE ALL ON testdb.* FROM 'guest'@'localhost'",
			want:      false,
		},
		{
			name:      "FlushPrivileges",
			statement: "FLUSH PRIVILEGES",
			want:      false,
		},

		// Edge cases: leading whitespace
		{
			name:      "LeadingSpaces",
			statement: "   SELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingTabs",
			statement: "\t\t\tSELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingNewlines",
			statement: "\n\n\nSELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingMixedWhitespace",
			statement: "\n  \t  \n  SELECT * FROM users",
			want:      true,
		},

		// Edge cases: leading comments
		{
			name:      "LeadingSingleLineComment",
			statement: "-- This is a comment\nSELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingHashComment",
			statement: "# This is a MySQL comment\nSELECT * FROM users",
			want:      true,
		},
		{
			name:      "MultipleLeadingComments",
			statement: "-- Comment 1\n-- Comment 2\n-- Comment 3\nSELECT * FROM users",
			want:      true,
		},
		{
			name:      "LeadingBlockComment",
			statement: "/* This is a block comment */\nSELECT * FROM users",
			want:      true,
		},
		{
			name: "LeadingMultiLineBlockComment",
			statement: `/* This is a
			   multi-line
			   block comment */
			SELECT * FROM users`,
			want: true,
		},
		{
			name:      "MixedCommentsAndWhitespace",
			statement: "\n  -- Comment 1\n  /* Block */\n  SELECT * FROM users",
			want:      true,
		},
		{
			name:      "CommentBeforeInsert",
			statement: "-- Insert user\nINSERT INTO users (name) VALUES ('Test')",
			want:      false,
		},
		{
			name:      "BlockCommentBeforeUpdate",
			statement: "/* Update active status */\nUPDATE users SET active = false",
			want:      false,
		},

		// Edge cases: multi-line statements
		{
			name: "MultiLineSelect",
			statement: `SELECT
				id,
				name,
				email
			FROM users
			WHERE active = true`,
			want: true,
		},
		{
			name: "MultiLineInsert",
			statement: `INSERT INTO users
				(name, email)
			VALUES
				('Alice', 'alice@example.com')`,
			want: false,
		},
		{
			name: "MultiLineShow",
			statement: `SHOW
				COLUMNS
			FROM users`,
			want: true,
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
