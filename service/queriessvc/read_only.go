package queriessvc

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

// enforceReadOnlyDirectExecution blocks direct connector dispatch when a read-only data source
// receives a statement that is mutating or cannot be classified as read-only.
func enforceReadOnlyDirectExecution(
	ds *datasourcessvc.DataSource,
	dsSlug slug.Slug,
	statementJSON json.RawMessage,
) error {
	if ds == nil || !ds.ReadOnly {
		return nil
	}

	if reason := directOperationReadOnlyRejection(ds.Type, statementJSON); reason == "" {
		return nil
	} else {
		return datasourcessvc.ErrDataSourceReadOnly.Detailf(
			"data source %s is read-only: %s",
			dsSlug,
			reason,
		)
	}
}

func hubSpotStatementIsUpsert(statementJSON json.RawMessage) bool {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return false
	}

	statementType, _ := statementStringField(fields, "type")
	operation, _ := statementStringField(fields, "operation")
	return strings.EqualFold(strings.TrimSpace(statementType), "hubspot") &&
		strings.EqualFold(strings.TrimSpace(operation), "upsert")
}

func hubSpotQueryDataIsUpsert(data json.RawMessage) bool {
	statementJSON, err := ExtractQueryStatement(data)
	return err == nil && hubSpotStatementIsUpsert(statementJSON)
}

// directOperationIsReadOnly classifies connector statements conservatively for read-only data
// sources. Unknown or malformed operation shapes return false so callers fail closed.
func directOperationIsReadOnly(dsType model.DataSourceType, statementJSON json.RawMessage) bool {
	return directOperationReadOnlyRejection(dsType, statementJSON) == ""
}

func directOperationReadOnlyRejection(
	dsType model.DataSourceType,
	statementJSON json.RawMessage,
) string {
	switch dsType {
	case model.DataSourceTypePostgres, model.DataSourceTypeMySQL, model.DataSourceTypeSnowflake:
		return sqlDirectOperationReadOnlyRejection(dsType, statementJSON)
	case model.DataSourceTypeGoogleWorkspace:
		statement, err := workspace.DecodeStatement(statementJSON)
		if err == nil && statement.IsRead() {
			return ""
		}
		return "Google Workspace operation is not a supported read operation"
	case model.DataSourceTypeBigQuery:
		return bigQueryDirectOperationReadOnlyRejection(statementJSON)
	case model.DataSourceTypeHTTP:
		if httpDirectOperationIsReadOnly(statementJSON) {
			return ""
		}
		return "HTTP operation is not a supported read method"
	case model.DataSourceTypeMongoDB:
		if mongoDirectOperationIsReadOnly(statementJSON) {
			return ""
		}
		return "MongoDB operation is not a supported read operation"
	case model.DataSourceTypeHubSpot:
		if hubSpotDirectOperationIsReadOnly(statementJSON) {
			return ""
		}
		return "HubSpot operation is not a supported read operation"
	case model.DataSourceTypeFTP, model.DataSourceTypeGit, model.DataSourceTypeGithub,
		model.DataSourceTypeGitlab, model.DataSourceTypeS3:
		if fileDirectOperationIsReadOnly(statementJSON) {
			return ""
		}
		return "file operation is not a supported read operation"
	default:
		return "connector does not declare a supported read-only operation"
	}
}

func sqlDirectOperationReadOnlyRejection(
	dsType model.DataSourceType,
	statementJSON json.RawMessage,
) string {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return "SQL operation payload is malformed"
	}

	sqlText, ok := statementStringField(fields, "sql")
	if !ok {
		return "SQL operation must contain a string sql field"
	}
	return sqlTextReadOnlyRejection(dsType, sqlText)
}

func bigQueryDirectOperationReadOnlyRejection(statementJSON json.RawMessage) string {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return "BigQuery operation payload is malformed"
	}

	operation, _ := statementStringField(fields, "operation")
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "", "query":
		sqlText, ok := statementStringField(fields, "sql")
		if !ok {
			return "BigQuery query must contain a string sql field"
		}
		return sqlTextReadOnlyRejection(model.DataSourceTypeBigQuery, sqlText)
	case "insert_rows":
		return "BigQuery insert_rows is unsupported in read-only mode"
	default:
		return "BigQuery operation is unsupported in read-only mode"
	}
}

// httpDirectOperationIsReadOnly treats explicitly allowed HTTP read methods as read-only.
func httpDirectOperationIsReadOnly(statementJSON json.RawMessage) bool {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return false
	}

	method, ok := statementStringField(fields, "method")
	if !ok {
		return false
	}

	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// mongoDirectOperationIsReadOnly treats only MongoDB read operations as read-only.
func mongoDirectOperationIsReadOnly(statementJSON json.RawMessage) bool {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return false
	}

	operation, ok := statementStringField(fields, "operation")
	if !ok {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "find", "aggregate", "count_documents":
		return true
	default:
		return false
	}
}

// hubSpotDirectOperationIsReadOnly allows HubSpot's v1 list and search operations.
func hubSpotDirectOperationIsReadOnly(statementJSON json.RawMessage) bool {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return false
	}

	operation, _ := statementStringField(fields, "operation")
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "", "list", "search":
		return true
	default:
		return false
	}
}

// fileDirectOperationIsReadOnly handles file-backed connectors that default omitted operation to
// read and expose explicit write operations for mutations.
func fileDirectOperationIsReadOnly(statementJSON json.RawMessage) bool {
	fields, ok := decodeStatementFields(statementJSON)
	if !ok {
		return false
	}

	operation, _ := statementStringField(fields, "operation")
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "", "read":
		return true
	case "write":
		return false
	default:
		return false
	}
}

// decodeStatementFields decodes the statement object into raw JSON fields for lightweight
// connector-aware classification.
func decodeStatementFields(statementJSON json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(statementJSON, &fields); err != nil {
		return nil, false
	}
	if fields == nil {
		return nil, false
	}
	return fields, true
}

// statementStringField returns a string field value when the statement contains one.
func statementStringField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

var (
	sqlMutationKeywordPattern = regexp.MustCompile(
		`(?i)\b(?:INSERT|UPDATE|DELETE|MERGE|CALL|CREATE|ALTER|DROP|TRUNCATE|GRANT|REVOKE|COPY|EXPORT|LOAD|REPLACE)\b`,
	)
	sqlSelectKeywordPattern = regexp.MustCompile(`(?i)\bSELECT\b`)
	sqlLockingClausePattern = regexp.MustCompile(
		`(?i)\bFOR\s+(?:(?:NO\s+KEY|KEY)\s+)?(?:UPDATE|SHARE)\b`,
	)
	mysqlShareLockPattern             = regexp.MustCompile(`(?i)\bLOCK\s+IN\s+SHARE\s+MODE\b`)
	postgresSideEffectFunctionPattern = regexp.MustCompile(
		`(?i)\b(?:nextval|setval|set_config|pg_notify|pg_logical_emit_message|pg_advisory_lock(?:_shared)?|pg_advisory_xact_lock(?:_shared)?|pg_try_advisory_lock(?:_shared)?|pg_try_advisory_xact_lock(?:_shared)?|pg_advisory_unlock(?:_shared)?|pg_advisory_unlock_all|lo_import|lo_export|dblink_exec)\s*\(`,
	)
	mysqlSideEffectFunctionPattern = regexp.MustCompile(
		`(?i)\b(?:get_lock|release_lock|release_all_locks)\s*\(`,
	)
	snowflakeSideEffectFunctionPattern = regexp.MustCompile(
		`(?i)(?:\bnextval\b|\bgetnextval\s*\(|\bsystem\$(?:reference|add_reference|enable_behavior_change_bundle|disable_behavior_change_bundle|abort_session|abort_transaction|cancel_all_queries|cancel_query|set_return_value|wait|add_event|set_span_attributes|log(?:_(?:trace|debug|info|warn|error|fatal))?)\s*\()`,
	)
	mysqlOutputPattern             = regexp.MustCompile(`(?i)\bINTO\s+(?:OUTFILE|DUMPFILE)\b`)
	mysqlUserVariablePattern       = regexp.MustCompile(`(?i)\bINTO\s+@`)
	mysqlVariableAssignmentPattern = regexp.MustCompile(`(?i)@\s*[\w$.]+\s*:=`)
	postgresSelectIntoPattern      = regexp.MustCompile(`(?i)\bINTO\b`)
)

const (
	sqlKeywordSelect               = "SELECT"
	sqlUnterminatedInputDiagnostic = "SQL contains an unterminated string, identifier, or comment"

	sentinelMySQLExecutableComment    byte = 1
	sentinelMySQLBackslash            byte = 2
	sentinelPostgresUnicodeIdentifier byte = 3
	sentinelPostgresBackslash         byte = 4
)

func sqlTextReadOnlyRejection(dsType model.DataSourceType, sqlText string) string {
	statement, rejection := singleSQLStatement(dsType, sqlText)
	if rejection != "" {
		return rejection
	}

	keyword, rest := leadingSQLKeyword(statement)
	securitySurface, ok := sqlLexicalSurface(statement, dsType, true)
	if !ok {
		return sqlUnterminatedInputDiagnostic
	}
	if dsType == model.DataSourceTypeBigQuery &&
		keyword != sqlKeywordSelect && keyword != "WITH" {
		return "BigQuery statement type " + keyword + " is unsupported"
	}
	if dsType == model.DataSourceTypeMySQL &&
		strings.IndexByte(securitySurface, sentinelMySQLExecutableComment) >= 0 {
		return "MySQL executable comments are unsupported"
	}
	switch keyword {
	case sqlKeywordSelect, "SHOW", "DESCRIBE", "DESC", "VALUES":
		// Continue with dialect-specific defense-in-depth checks below.
	case "WITH":
		if !sqlSelectKeywordPattern.MatchString(securitySurface) ||
			sqlMutationKeywordPattern.MatchString(securitySurface) {
			return "SQL common table expression is not a supported read"
		}
	case "EXPLAIN":
		if !explainSQLIsReadOnly(dsType, rest) {
			return "EXPLAIN ANALYZE and mutating EXPLAIN targets are unsupported"
		}
	default:
		return "SQL statement type " + keyword + " is unsupported"
	}

	switch dsType {
	case model.DataSourceTypePostgres:
		switch {
		case strings.IndexByte(securitySurface, sentinelPostgresUnicodeIdentifier) >= 0:
			return "PostgreSQL Unicode-escaped identifiers are unsupported"
		case strings.IndexByte(securitySurface, sentinelPostgresBackslash) >= 0:
			return "PostgreSQL backslash-escaped strings require E-prefixed escape syntax"
		case sqlLockingClausePattern.MatchString(securitySurface):
			return "PostgreSQL row-locking SELECT clauses are unsupported"
		case postgresSideEffectFunctionPattern.MatchString(securitySurface):
			return "PostgreSQL side-effecting functions are unsupported"
		case postgresSelectIntoPattern.MatchString(securitySurface):
			return "PostgreSQL SELECT INTO is unsupported"
		}
	case model.DataSourceTypeMySQL:
		switch {
		case strings.IndexByte(securitySurface, sentinelMySQLBackslash) >= 0:
			return "MySQL backslash-escaped strings are unsupported because the session SQL mode is unknown"
		case sqlLockingClausePattern.MatchString(securitySurface) ||
			mysqlShareLockPattern.MatchString(securitySurface):
			return "MySQL locking reads are unsupported"
		case mysqlOutputPattern.MatchString(securitySurface):
			return "MySQL output-file clauses are unsupported"
		case mysqlUserVariablePattern.MatchString(securitySurface):
			return "MySQL SELECT INTO user variables is unsupported"
		case mysqlVariableAssignmentPattern.MatchString(securitySurface):
			return "MySQL user-variable mutation is unsupported"
		case mysqlSideEffectFunctionPattern.MatchString(securitySurface):
			return "MySQL side-effecting functions are unsupported"
		}
	case model.DataSourceTypeSnowflake:
		if snowflakeSideEffectFunctionPattern.MatchString(securitySurface) {
			return "Snowflake side-effecting functions are unsupported"
		}
	default:
	}

	return ""
}

// singleSQLStatement removes leading comments and a trailing semicolon, then rejects additional
// semicolons so read-only classification does not allow stacked statements.
func singleSQLStatement(dsType model.DataSourceType, sqlText string) (string, string) {
	trimmed, valid := stripLeadingSQLNoise(dsType, sqlText)
	if !valid {
		return "", sqlUnterminatedInputDiagnostic
	}
	if trimmed == "" {
		return "", "SQL must contain exactly one statement"
	}

	trimmed = strings.TrimSpace(trimmed)
	surface, ok := sqlLexicalSurface(trimmed, dsType, false)
	if !ok {
		return "", sqlUnterminatedInputDiagnostic
	}
	surface = strings.TrimSpace(surface)
	if strings.HasSuffix(surface, ";") {
		surface = strings.TrimSpace(strings.TrimSuffix(surface, ";"))
	}
	if surface == "" || strings.Contains(surface, ";") {
		return "", "SQL must contain exactly one statement"
	}
	return trimmed, ""
}

// sqlLexicalSurface removes values and comments while preserving SQL tokens and statement
// separators. Security surfaces retain quoted identifier words for deny-rule matching.
func sqlLexicalSurface(
	sqlText string,
	dsType model.DataSourceType,
	preserveIdentifiers bool,
) (string, bool) {
	surface := []byte(sqlText)
	for i := 0; i < len(surface); {
		switch {
		case dsType == model.DataSourceTypePostgres && surface[i] == '$':
			delimiterEnd := i + 1
			for delimiterEnd < len(surface) &&
				((surface[delimiterEnd] >= 'a' && surface[delimiterEnd] <= 'z') ||
					(surface[delimiterEnd] >= 'A' && surface[delimiterEnd] <= 'Z') ||
					(surface[delimiterEnd] >= '0' && surface[delimiterEnd] <= '9') ||
					surface[delimiterEnd] == '_') {
				delimiterEnd++
			}
			if delimiterEnd >= len(surface) || surface[delimiterEnd] != '$' {
				i++
				continue
			}
			delimiter := string(surface[i : delimiterEnd+1])
			closeOffset := strings.Index(string(surface[delimiterEnd+1:]), delimiter)
			if closeOffset == -1 {
				return "", false
			}
			closeEnd := delimiterEnd + 1 + closeOffset + len(delimiter)
			for j := i; j < closeEnd; j++ {
				surface[j] = ' '
			}
			i = closeEnd
		case surface[i] == '\'':
			quote := surface[i]
			quoteWidth := sqlQuoteWidth(surface, i, dsType)
			postgresEscapeString := postgresEscapeStringStart(surface, i, dsType)
			bigQueryBackslashEscapes := dsType == model.DataSourceTypeBigQuery &&
				!bigQueryRawStringStart(surface, i)
			for range quoteWidth {
				surface[i] = ' '
				i++
			}
			closed := false
			userVariableName := preserveIdentifiers &&
				dsType == model.DataSourceTypeMySQL &&
				previousNonSpaceByte(surface, i-quoteWidth) == '@'
			preservedUserVariableName := false
			for i < len(surface) {
				if surface[i] == '\\' {
					switch {
					case dsType == model.DataSourceTypeMySQL:
						// SQL mode controls whether backslashes escape quotes. Without the
						// session mode, the statement is rejected after lexing.
						surface[i] = sentinelMySQLBackslash
					case dsType == model.DataSourceTypePostgres && !postgresEscapeString:
						// standard_conforming_strings controls plain-string backslashes.
						// Require E-prefixed syntax so both parsing and classification agree.
						surface[i] = sentinelPostgresBackslash
					case postgresEscapeString || bigQueryBackslashEscapes:
						surface[i] = ' '
					default:
						surface[i] = ' '
						i++
						continue
					}
					i++
					if i < len(surface) {
						surface[i] = ' '
						i++
					}
					continue
				}
				if surface[i] == quote {
					if quoteWidth == 3 {
						if !hasSQLQuoteDelimiter(surface, i, quote, quoteWidth) {
							surface[i] = ' '
							i++
							continue
						}
						for range quoteWidth {
							surface[i] = ' '
							i++
						}
						closed = true
						break
					}
					surface[i] = ' '
					if i+1 < len(surface) && surface[i+1] == quote {
						surface[i+1] = ' '
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				if userVariableName && !preservedUserVariableName {
					surface[i] = 'v'
					preservedUserVariableName = true
				} else {
					surface[i] = ' '
				}
				i++
			}
			if !closed {
				return "", false
			}
		case surface[i] == '"' || surface[i] == '`':
			quote := surface[i]
			quoteWidth := sqlQuoteWidth(surface, i, dsType)
			identifierQuote := quote == '`' ||
				(quote == '"' && dsType == model.DataSourceTypePostgres)
			userVariableName := preserveIdentifiers &&
				dsType == model.DataSourceTypeMySQL &&
				previousNonSpaceByte(surface, i) == '@'
			bigQueryBackslashEscapes := dsType == model.DataSourceTypeBigQuery &&
				!bigQueryRawStringStart(surface, i)
			unicodeEscapedIdentifier := dsType == model.DataSourceTypePostgres &&
				i >= 2 && (surface[i-2] == 'U' || surface[i-2] == 'u') &&
				surface[i-1] == '&'
			if unicodeEscapedIdentifier {
				surface[i] = sentinelPostgresUnicodeIdentifier
			} else {
				surface[i] = ' '
			}
			i++
			for range quoteWidth - 1 {
				surface[i] = ' '
				i++
			}
			closed := false
			preservedUserVariableName := false
			for i < len(surface) {
				if surface[i] == '\\' &&
					(dsType == model.DataSourceTypeMySQL || bigQueryBackslashEscapes) {
					if dsType == model.DataSourceTypeMySQL {
						surface[i] = sentinelMySQLBackslash
					} else {
						surface[i] = ' '
					}
					i++
					if i < len(surface) {
						surface[i] = ' '
						i++
					}
					continue
				}
				if surface[i] == quote {
					if quoteWidth == 3 {
						if !hasSQLQuoteDelimiter(surface, i, quote, quoteWidth) {
							surface[i] = ' '
							i++
							continue
						}
						for range quoteWidth {
							surface[i] = ' '
							i++
						}
						closed = true
						break
					}
					surface[i] = ' '
					if i+1 < len(surface) && surface[i+1] == quote {
						surface[i+1] = ' '
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				if userVariableName {
					if !preservedUserVariableName {
						surface[i] = 'v'
						preservedUserVariableName = true
					} else {
						surface[i] = ' '
					}
				} else if !preserveIdentifiers || !identifierQuote ||
					!isSQLIdentifierByte(surface[i]) {
					surface[i] = ' '
				}
				i++
			}
			if !closed {
				return "", false
			}
		case isSQLLineCommentStart(surface, i, dsType):
			for i < len(surface) && surface[i] != '\r' && surface[i] != '\n' {
				surface[i] = ' '
				i++
			}
		case dsType == model.DataSourceTypeMySQL && surface[i] == '#':
			for i < len(surface) && surface[i] != '\r' && surface[i] != '\n' {
				surface[i] = ' '
				i++
			}
		case i+1 < len(surface) && surface[i] == '/' && surface[i+1] == '*':
			if mysqlExecutableCommentStart(surface, i, dsType) {
				surface[i] = sentinelMySQLExecutableComment
			} else {
				surface[i] = ' '
			}
			surface[i+1] = ' '
			i += 2
			closed := false
			for i < len(surface) {
				if i+1 < len(surface) && surface[i] == '*' && surface[i+1] == '/' {
					surface[i], surface[i+1] = ' ', ' '
					i += 2
					closed = true
					break
				}
				surface[i] = ' '
				i++
			}
			if !closed {
				return "", false
			}
		default:
			i++
		}
	}
	return string(surface), true
}

func isSQLIdentifierByte(value byte) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9') ||
		value == '_' || value == '$'
}

func sqlQuoteWidth(surface []byte, i int, dsType model.DataSourceType) int {
	if dsType == model.DataSourceTypeBigQuery &&
		hasSQLQuoteDelimiter(surface, i, surface[i], 3) {
		return 3
	}
	return 1
}

func hasSQLQuoteDelimiter(surface []byte, i int, quote byte, width int) bool {
	if i+width > len(surface) {
		return false
	}
	for offset := range width {
		if surface[i+offset] != quote {
			return false
		}
	}
	return true
}

func postgresEscapeStringStart(
	surface []byte,
	quoteIndex int,
	dsType model.DataSourceType,
) bool {
	if dsType != model.DataSourceTypePostgres || quoteIndex == 0 {
		return false
	}
	prefix := surface[quoteIndex-1]
	if prefix != 'E' && prefix != 'e' {
		return false
	}
	return quoteIndex == 1 || !isSQLIdentifierByte(surface[quoteIndex-2])
}

func bigQueryRawStringStart(surface []byte, quoteIndex int) bool {
	if quoteIndex == 0 {
		return false
	}
	prefix := surface[quoteIndex-1]
	if prefix == 'R' || prefix == 'r' {
		return quoteIndex == 1 || !isSQLIdentifierByte(surface[quoteIndex-2]) ||
			((surface[quoteIndex-2] == 'B' || surface[quoteIndex-2] == 'b') &&
				(quoteIndex == 2 || !isSQLIdentifierByte(surface[quoteIndex-3])))
	}
	return (prefix == 'B' || prefix == 'b') &&
		quoteIndex >= 2 &&
		(surface[quoteIndex-2] == 'R' || surface[quoteIndex-2] == 'r') &&
		(quoteIndex == 2 || !isSQLIdentifierByte(surface[quoteIndex-3]))
}

func previousNonSpaceByte(surface []byte, before int) byte {
	for i := before - 1; i >= 0; i-- {
		if surface[i] != ' ' && surface[i] != '\t' &&
			surface[i] != '\r' && surface[i] != '\n' {
			return surface[i]
		}
	}
	return 0
}

func isSQLLineCommentStart(surface []byte, i int, dsType model.DataSourceType) bool {
	if i+1 >= len(surface) || surface[i] != '-' || surface[i+1] != '-' {
		return false
	}
	if dsType != model.DataSourceTypeMySQL {
		return true
	}
	return i+2 >= len(surface) || surface[i+2] <= ' '
}

func mysqlExecutableCommentStart(
	surface []byte,
	i int,
	dsType model.DataSourceType,
) bool {
	if dsType != model.DataSourceTypeMySQL || i+2 >= len(surface) {
		return false
	}
	if surface[i+2] == '!' {
		return true
	}
	return i+3 < len(surface) &&
		(surface[i+2] == 'M' || surface[i+2] == 'm') &&
		surface[i+3] == '!'
}

// explainSQLIsReadOnly allows EXPLAIN only when it does not execute the explained statement and
// the explained statement itself is read-only.
func explainSQLIsReadOnly(dsType model.DataSourceType, rest string) bool {
	rest, valid := stripLeadingSQLNoise(dsType, rest)
	if !valid || rest == "" {
		return false
	}

	keyword, _ := leadingSQLKeyword(rest)
	if keyword == "ANALYZE" {
		return false
	}

	return sqlTextReadOnlyRejection(dsType, rest) == ""
}

// leadingSQLKeyword returns the first ASCII keyword and the remainder of the statement.
func leadingSQLKeyword(statement string) (string, string) {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" {
		return "", ""
	}

	end := 0
	for end < len(trimmed) {
		ch := trimmed[end]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", strings.TrimSpace(trimmed)
	}

	return strings.ToUpper(trimmed[:end]), strings.TrimSpace(trimmed[end:])
}

// stripLeadingSQLNoise removes whitespace plus leading line and block comments before SQL keyword
// classification.
func stripLeadingSQLNoise(dsType model.DataSourceType, sqlText string) (string, bool) {
	remaining := strings.TrimSpace(sqlText)
	for {
		switch {
		case isSQLLineCommentStart([]byte(remaining), 0, dsType):
			newline := strings.IndexAny(remaining, "\r\n")
			if newline == -1 {
				return "", true
			}
			remaining = strings.TrimSpace(remaining[newline+1:])
		case strings.HasPrefix(remaining, "/*"):
			if mysqlExecutableCommentStart([]byte(remaining), 0, dsType) {
				return remaining, true
			}
			end := strings.Index(remaining, "*/")
			if end == -1 {
				return "", false
			}
			remaining = strings.TrimSpace(remaining[end+2:])
		case dsType == model.DataSourceTypeMySQL && strings.HasPrefix(remaining, "#"):
			newline := strings.IndexAny(remaining, "\r\n")
			if newline == -1 {
				return "", true
			}
			remaining = strings.TrimSpace(remaining[newline+1:])
		default:
			return remaining, true
		}
	}
}
