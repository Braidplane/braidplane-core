package snowflakewif

import (
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/stretchr/testify/require"
)

func TestOnboardingScript(t *testing.T) {
	identity, err := NewIdentity("https://issuer.test", slug.Slug("tenant-one"), slug.Slug("warehouse-source"), "acme-account")
	require.NoError(t, err)
	setup := Setup{Warehouse: "WH", Database: "DB", Schema: "PUBLIC", Role: "DEDICATED", Objects: []ObjectGrant{{"SOURCE", "SELECT"}, {"SINK", "INSERT"}}}
	script, err := setup.Script(identity)
	require.NoError(t, err)
	require.Contains(t, script, "OIDC_AUDIENCE_LIST = ('https://acme-account.snowflakecomputing.com')")
	require.Contains(t, script, "SUBJECT = '"+identity.Subject.String()+"'")
	require.Contains(t, script, `GRANT SELECT ON TABLE "DB"."PUBLIC"."SOURCE" TO ROLE "DEDICATED";`)
	require.Contains(t, script, `GRANT INSERT ON TABLE "DB"."PUBLIC"."SINK" TO ROLE "DEDICATED";`)
	require.Contains(t, script, "AUTHENTICATION_METHODS = ('WORKLOAD_IDENTITY')")
	require.Contains(t, script, "CREATE USER IF NOT EXISTS")
	require.Contains(t, script, `WHERE "name" = '`)
	require.Contains(t, script, `AND "type" = 'SERVICE'`)
	require.Contains(t, script, `PARSE_JSON("additional_info"):issuer::STRING = '`+identity.Issuer+`'`)
	require.Contains(t, script, `ARRAY_CONTAINS(TO_VARIANT('`+identity.Audience+`'), PARSE_JSON("additional_info"):audienceList)`)
	require.NotContains(t, script, `"additional_info":`)
	require.Contains(t, script, `PARSE_JSON("additional_info"):subject::STRING = '`+identity.Subject.String()+`'`)
	require.Contains(t, script, `ARRAY_SIZE(PARSE_JSON("additional_info"):audienceList) = 1`)
	require.Less(t, strings.Index(script, "RAISE identity_conflict"), strings.Index(script, "ALTER USER"))
	require.NotContains(t, script, "ALL TABLES")
	require.NotContains(t, script, "FUTURE")
	require.NotContains(t, script, "CREATE OR REPLACE")
	setup.Objects[0], setup.Objects[1] = setup.Objects[1], setup.Objects[0]
	reordered, err := setup.Script(identity)
	require.NoError(t, err)
	require.Equal(t, script, reordered)
	other, err := NewIdentity("https://issuer.test", slug.Slug("tenant-two"), slug.Slug("warehouse-source"), "acme-account")
	require.NoError(t, err)
	otherScript, err := setup.Script(other)
	require.NoError(t, err)
	require.NotEqual(t, script, otherScript)
}

func TestOnboardingRejectsUnsafeOrBroadGrants(t *testing.T) {
	valid := Setup{Warehouse: "WH", Database: "DB", Schema: "PUBLIC", Role: "DEDICATED", Objects: []ObjectGrant{{"TABLE_ONE", "SELECT"}}}
	for _, role := range []string{"", "ACCOUNTADMIN", "public", "SYSADMIN", strings.Repeat("x", 256)} {
		setup := valid
		setup.Role = role
		require.Error(t, setup.Validate())
	}
	for _, objects := range [][]ObjectGrant{nil, {{"TABLE_ONE", "ALL"}}, {{"TABLE_ONE", "SELECT"}, {"TABLE_ONE", "SELECT"}}, {{"A\nB", "SELECT"}}} {
		setup := valid
		setup.Objects = objects
		require.Error(t, setup.Validate())
	}
	require.NoError(t, valid.Validate())
}

func TestOnboardingQuotesExactIdentifiers(t *testing.T) {
	identity, err := NewIdentity("https://issuer.test", "tenant-one", "warehouse-source", "acme-account")
	require.NoError(t, err)
	setup := Setup{Warehouse: "WH", Database: `A.B`, Schema: `Mixed case`, Role: `Dedicated "role"`, Objects: []ObjectGrant{{Name: `Table"; DROP USER X;--`, Privilege: "SELECT"}}}
	script, err := setup.Script(identity)
	require.NoError(t, err)
	require.Contains(t, script, `GRANT SELECT ON TABLE "A.B"."Mixed case"."Table""; DROP USER X;--" TO ROLE "Dedicated ""role""";`)
}
