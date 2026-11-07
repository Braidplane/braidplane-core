package odcs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestPinnedUpstreamConformance(t *testing.T) {
	raw, err := os.ReadFile("testdata/upstream-conformance.json")
	require.NoError(t, err)
	var cases []struct {
		Source   string          `json:"source"`
		Valid    bool            `json:"valid"`
		Document json.RawMessage `json:"document"`
	}
	require.NoError(t, json.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Source, func(t *testing.T) {
			var document map[string]any
			require.NoError(t, jsonvalue.Unmarshal(tc.Document, &document))
			id, _ := document["id"].(string)
			revision, err := Record(tc.Document, id)
			if !tc.Valid {
				require.ErrorIs(t, err, ErrDocument)
				return
			}
			require.NoError(t, err, "%s", xerror.PublicDetail(err))
			var reloaded map[string]any
			require.NoError(t, jsonvalue.Unmarshal(revision.Document, &reloaded))
			require.Equal(t, document, reloaded)
			again, err := Record(revision.Document, id)
			require.NoError(t, err)
			require.Equal(t, revision.Digest, again.Digest)
		})
	}
}

func TestNormativeRulesBeyondSchema(t *testing.T) {
	base := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"integer"}],"relationships":RELATIONSHIPS}]}`
	for _, relationships := range []string{
		`[{"from":["Orders.id","Orders.id"],"to":["Orders.id"]}]`,
		`[{"id":"bad.id","from":"Orders.id","to":"Orders.id"}]`,
		`[{"id":"same","from":"Orders.id","to":"Orders.id"},{"id":"same","from":"Orders.id","to":"Orders.id"}]`,
	} {
		_, err := Record([]byte(strings.Replace(base, "RELATIONSHIPS", relationships, 1)), "orders")
		require.ErrorIs(t, err, ErrDocument)
	}
	for _, target := range []string{"file:///path/contract.yaml#/schema/order/properties/id", "../../path/contract.yaml#/schema/order/properties/id"} {
		relationship := `[{"from":"Orders.id","to":"` + target + `"}]`
		_, err := Record([]byte(strings.Replace(base, "RELATIONSHIPS", relationship, 1)), "orders")
		require.NoError(t, err)
	}
}

func TestRecordUnicodeAndNumericLimits(t *testing.T) {
	base := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],"customProperties":[{"property":"opaque","value":VALUE}]}`
	for _, value := range []string{`"\ud800"`, `"\udc00"`, `1e4097`, `1e-4097`, strings.Repeat("1", 1025)} {
		_, err := Record([]byte(strings.Replace(base, "VALUE", value, 1)), "orders")
		require.ErrorIs(t, err, ErrDocument)
	}
	for _, value := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `1e4096`, `1e-4096`, strings.Repeat("1", 1024)} {
		_, err := Record([]byte(strings.Replace(base, "VALUE", value, 1)), "orders")
		require.NoError(t, err)
	}
}

func TestVariableAdmissionDoesNotResolveOrExecute(t *testing.T) {
	t.Setenv("ODCS_TEST_TYPE", "string")
	for _, raw := range []string{
		`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"${OBJECT_ID}","name":"Orders","properties":[{"name":"id","logicalType":"${ODCS_TEST_TYPE}"}]}]}`,
		`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],"contractCreatedTs":"${CREATED_AT}"}`,
		`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],"servers":[{"server":"custom","type":"custom","endpointUrl":"${ENDPOINT:-https://example.invalid}"}]}`,
	} {
		revision, err := Record([]byte(raw), "orders")
		require.NoError(t, err)
		require.Contains(t, string(revision.Document), "${")
		require.Empty(t, revision.SchemaObjects)
		result, err := Convert(revision.Document, "order")
		require.NoError(t, err)
		require.True(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		requireDiagnostic(t, result, "$.schema", "object_not_found")
		require.Contains(t, string(mustCoverageJSON(t, result)), `"state":"unresolved"`)
	}
}

func TestEnumSemanticUniquenessAndOpaqueValues(t *testing.T) {
	base := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"name":"Orders","properties":[{"name":"amount","logicalType":"number","enum":[{"value":1},{"value":1e0}]}]}]}`
	_, err := Record([]byte(base), "orders")
	require.ErrorIs(t, err, ErrDocument)
	// An opaque extension's enum/id keys are vendor data, not ODCS declarations.
	raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],"customProperties":[{"property":"vendor","value":{"enum":[null],"items":[{"id":"same"},{"id":"same"}]}}]}`
	_, err = Record([]byte(raw), "orders")
	require.NoError(t, err)
}

func TestNonObjectSchemaDeclarationsAreStoredWithoutExecutableShape(t *testing.T) {
	for _, logicalType := range []string{"array"} {
		raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","logicalType":"` + logicalType + `","properties":[{"name":"id","logicalType":"string"}]}]}`
		_, err := Record([]byte(raw), "orders")
		require.NoError(t, err)
		result, err := Convert([]byte(raw), "order")
		require.NoError(t, err)
		require.True(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		requireDiagnostic(t, result, "$.schema[0].logicalType", "type_unsupported")
	}
}

func TestNormativeCategoryInvalidCases(t *testing.T) {
	for name, declaration := range map[string]string{
		"description": `"description":{"purpose":42}`,
		"tags":        `"tags":[42]`,
		"context":     `"context":{"constraints":[{"description":"missing constraint"}]}`,
		"support":     `"support":[{"url":"https://example.invalid"}]`,
		"price":       `"price":{"priceAmount":"one"}`,
		"team":        `"team":{"members":[{"name":"missing username"}]}`,
		"roles":       `"roles":[{"access":"read"}]`,
		"sla":         `"slaProperties":[{"property":"latency"}]`,
		"definitions": `"authoritativeDefinitions":[{"url":"https://example.invalid"}]`,
		"extensions":  `"customProperties":[{"property":"missing value"}]`,
		"servers":     `"servers":[{"server":"db","type":"postgres"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],` + declaration + `}`
			_, err := Record([]byte(raw), "orders")
			require.ErrorIs(t, err, ErrDocument)
		})
	}
}

func TestStableIDsRejectUnicodeWhitespace(t *testing.T) {
	for _, id := range []string{"order\u2003id", "order\u00a0id", "order\u0085id"} {
		raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"` + id + `","name":"Orders"}]}`
		_, err := Record([]byte(raw), "orders")
		require.ErrorIs(t, err, ErrDocument)
	}
}

func TestSecretAliasesInOpaqueExtensions(t *testing.T) {
	for _, key := range []string{"secret_access_key", "aws_secret_access_key", "session_token", "authorization", "passwd", "bearer_token", "client_key"} {
		for _, value := range []string{`"literal-private-value"`, `"${SECRET}"`} {
			raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],"customProperties":[{"property":"opaque","value":{"` + key + `":` + value + `}}]}`
			_, err := Record([]byte(raw), "orders")
			if value == `"${SECRET}"` {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrDocument)
				require.NotContains(t, xerror.PublicDetail(err), "literal-private-value")
			}
		}
	}
}
