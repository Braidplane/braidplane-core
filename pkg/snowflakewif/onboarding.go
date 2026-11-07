package snowflakewif

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ObjectGrant names an existing table in the selected database and schema.
type ObjectGrant struct {
	Name      string `json:"name"`
	Privilege string `json:"privilege"`
}

type Setup struct {
	Warehouse string
	Database  string
	Schema    string
	Role      string
	Objects   []ObjectGrant
}

// QuoteIdentifier treats every input as one exact identifier, never SQL syntax.
func QuoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func (s Setup) Validate() error {
	for _, name := range []string{s.Warehouse, s.Database, s.Schema, s.Role} {
		if !validIdentifier(name) {
			return invalidSetup()
		}
	}
	switch strings.ToUpper(s.Role) {
	case "ACCOUNTADMIN", "SECURITYADMIN", "SYSADMIN", "USERADMIN", "ORGADMIN", "GLOBALORGADMIN", "PUBLIC":
		return invalidSetup()
	}
	if len(s.Objects) < 1 || len(s.Objects) > 100 {
		return invalidSetup()
	}
	seen := map[ObjectGrant]bool{}
	for _, object := range s.Objects {
		if !validIdentifier(object.Name) || (object.Privilege != "SELECT" && object.Privilege != "INSERT") || seen[object] {
			return invalidSetup()
		}
		seen[object] = true
	}
	return nil
}

func validIdentifier(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 255 && !strings.ContainsFunc(name, unicode.IsControl)
}

func invalidSetup() error {
	const detail = "Choose a dedicated Snowflake role, exact warehouse/database/schema names, and 1–100 distinct table SELECT or INSERT grants. System roles are not allowed."
	return xerror.InvalidInput(xerror.DataSource).WithPublicDetail(detail).Detail(detail)
}

// Script is displayed for customer review only; no Braidplane execution path uses it.
func (s Setup) Script(identity Identity) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	// Stable connection-owned names avoid using customer input as SQL or sharing users across tenants.
	hash := sha256.Sum256([]byte(identity.Subject.String()))
	userName := fmt.Sprintf("BRAIDPLANE_%X", hash[:16])
	user := QuoteIdentifier(userName)
	policy := QuoteIdentifier(s.Database) + "." + QuoteIdentifier(s.Schema) + "." + QuoteIdentifier(fmt.Sprintf("BRAIDPLANE_%X_AUTH", hash[:16]))
	role, warehouse := QuoteIdentifier(s.Role), QuoteIdentifier(s.Warehouse)
	database, schema := QuoteIdentifier(s.Database), QuoteIdentifier(s.Database)+"."+QuoteIdentifier(s.Schema)
	literal := func(v string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", "''") + "'"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "-- Run this entire block in a Snowsight worksheet for the configured account.\n-- Review every grant. Use an administrator authorized to create and inspect users, create roles and policies, apply authentication policies, and issue these grants.\n-- Reuse only a dedicated role; existing grants are not removed.\n-- A conflicting existing user aborts before policy or user grants change.\nDECLARE\n  identity_conflict EXCEPTION (-20001, 'Existing user does not match this connection identity. Ask your Snowflake administrator to resolve the conflict.');\n  matching_users NUMBER;\n  matching_methods NUMBER;\nBEGIN\nCREATE ROLE IF NOT EXISTS %s;\n", role)
	fmt.Fprintf(&out, "CREATE AUTHENTICATION POLICY IF NOT EXISTS %s\n  AUTHENTICATION_METHODS = ('WORKLOAD_IDENTITY')\n  WORKLOAD_IDENTITY_POLICY = (ALLOWED_PROVIDERS = (OIDC) ALLOWED_OIDC_ISSUERS = (%s));\n", policy, literal(identity.Issuer))
	fmt.Fprintf(&out, "CREATE USER IF NOT EXISTS %s\n  TYPE = SERVICE\n  DEFAULT_ROLE = %s\n  DEFAULT_WAREHOUSE = %s\n  WORKLOAD_IDENTITY = (TYPE = OIDC ISSUER = %s SUBJECT = %s OIDC_AUDIENCE_LIST = (%s));\n", user, role, warehouse, literal(identity.Issuer), literal(identity.Subject.String()), literal(identity.Audience))
	fmt.Fprintf(&out, `SHOW USERS LIKE %s;
SELECT COUNT(*) INTO :matching_users
  FROM TABLE(RESULT_SCAN(LAST_QUERY_ID()))
  WHERE "name" = %s AND "type" = 'SERVICE';
SHOW USER WORKLOAD IDENTITY AUTHENTICATION METHODS FOR USER %s;
SELECT COUNT(*) INTO :matching_methods
  FROM TABLE(RESULT_SCAN(LAST_QUERY_ID()))
  WHERE "type" = 'OIDC'
    AND PARSE_JSON("additional_info"):issuer::STRING = %s
    AND PARSE_JSON("additional_info"):subject::STRING = %s
    AND ARRAY_SIZE(PARSE_JSON("additional_info"):audienceList) = 1
    AND ARRAY_CONTAINS(TO_VARIANT(%s), PARSE_JSON("additional_info"):audienceList);
IF (matching_users <> 1 OR matching_methods <> 1) THEN
  RAISE identity_conflict;
END IF;
`, literal(userName), literal(userName), user, literal(identity.Issuer), literal(identity.Subject.String()), literal(identity.Audience))
	fmt.Fprintf(&out, "ALTER USER %s SET AUTHENTICATION POLICY %s;\nGRANT ROLE %s TO USER %s;\nGRANT USAGE ON WAREHOUSE %s TO ROLE %s;\nGRANT USAGE ON DATABASE %s TO ROLE %s;\nGRANT USAGE ON SCHEMA %s TO ROLE %s;\n", user, policy, role, user, warehouse, role, database, role, schema, role)
	objects := slices.Clone(s.Objects)
	slices.SortFunc(objects, func(a, b ObjectGrant) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.Privilege, b.Privilege)
	})
	for _, object := range objects {
		fmt.Fprintf(&out, "GRANT %s ON TABLE %s.%s TO ROLE %s;\n", object.Privilege, schema, QuoteIdentifier(object.Name), role)
	}
	out.WriteString("RETURN 'Snowflake setup complete. Return to Braidplane and select Verify.';\nEND;\n-- Return to Braidplane and select Verify.\n-- Disabling Braidplane identity blocks new logins; terminate existing Snowflake sessions for immediate removal.\n")
	return out.String(), nil
}
