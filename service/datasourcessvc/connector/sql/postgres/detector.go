package postgres

import (
	"regexp"
	"strings"
)

var (
	// leadingWS matches leading whitespace and SQL comments (-- and /* */ style).
	leadingWS = regexp.MustCompile(`(?m)^[\s\r\n]*(--[^\n]*\n|/\*[\s\S]*?\*/\s*)*`)

	// queryKeyword matches SQL statements that return rows (case-insensitive).
	queryKeyword = regexp.MustCompile(`(?i)^\s*(SELECT|WITH|EXPLAIN|DESCRIBE)\b`)

	// returningClause matches the PostgreSQL RETURNING clause (case-insensitive).
	returningClause = regexp.MustCompile(`(?i)\bRETURNING\b`)
)

// isQueryStatement determines if a SQL statement should use QueryContext (returns rows)
// vs ExecContext (returns affected row count).
//
// Returns true for:
// - SELECT, WITH (CTEs), EXPLAIN, DESCRIBE
// - Any statement with RETURNING clause (PostgreSQL-specific: INSERT/UPDATE/DELETE...RETURNING)
//
// Returns false for:
// - INSERT, UPDATE, DELETE (without RETURNING)
// - DDL: CREATE, DROP, ALTER, TRUNCATE
// - Other statements: SET, GRANT, etc.
func isQueryStatement(stmt string) bool {
	// Remove leading whitespace and comments
	cleaned := leadingWS.ReplaceAllString(stmt, "")
	cleaned = strings.TrimSpace(cleaned)

	// Check for SELECT, WITH, EXPLAIN, DESCRIBE
	if queryKeyword.MatchString(cleaned) {
		return true
	}

	// PostgreSQL: INSERT/UPDATE/DELETE with RETURNING acts like SELECT
	if returningClause.MatchString(stmt) {
		return true
	}

	// Everything else: INSERT, UPDATE, DELETE, CREATE, DROP, etc.
	return false
}
