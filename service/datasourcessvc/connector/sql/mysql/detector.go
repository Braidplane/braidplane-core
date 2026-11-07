package mysql

import (
	"regexp"
	"strings"
)

var (
	// leadingWS matches leading whitespace and SQL comments (MySQL supports --, # and /* */ style).
	leadingWS = regexp.MustCompile(`(?m)^[\s\r\n]*(--[^\n]*\n|#[^\n]*\n|/\*[\s\S]*?\*/\s*)*`)

	// queryKeyword matches SQL statements that return rows (case-insensitive).
	// MySQL-specific: includes SHOW and DESC (shorthand for DESCRIBE).
	queryKeyword = regexp.MustCompile(`(?i)^\s*(SELECT|WITH|EXPLAIN|DESCRIBE|DESC|SHOW)\b`)
)

// isQueryStatement determines if a SQL statement should use QueryContext (returns rows)
// vs ExecContext (returns affected row count).
//
// Returns true for:
// - SELECT, WITH (CTEs), EXPLAIN, DESCRIBE, SHOW
//
// Returns false for:
// - INSERT, UPDATE, DELETE
// - DDL: CREATE, DROP, ALTER, TRUNCATE
// - Other statements: SET, GRANT, etc.
//
// Note: MySQL does not support the RETURNING clause like PostgreSQL.
func isQueryStatement(stmt string) bool {
	// Remove leading whitespace and comments
	cleaned := leadingWS.ReplaceAllString(stmt, "")
	cleaned = strings.TrimSpace(cleaned)

	// Check for SELECT, WITH, EXPLAIN, DESCRIBE, SHOW
	return queryKeyword.MatchString(cleaned)
}
