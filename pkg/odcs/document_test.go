package odcs

import (
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

const validDocument = `{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"amount","enum":[{"value":9007199254740993.30}]}]}]}`

func TestRecordCanonicalDigest(t *testing.T) {
	a, err := Record([]byte(validDocument), "orders")
	require.NoError(t, err)
	b, err := Record([]byte(` { "version":"1", "id":"orders", "apiVersion":"v3.2.0", "kind":"DataContract", "schema":[{"properties":[{"enum":[{"value":9007199254740993.30}],"name":"amount"}],"name":"Orders","id":"order"}] }`), "orders")
	require.NoError(t, err)
	require.Equal(t, a.Document, b.Document)
	require.Equal(t, a.Digest, b.Digest)
	require.Contains(t, string(a.Document), "9007199254740993.30")
	require.Equal(t, "recorded", a.Status)
	c, err := Record([]byte(strings.Replace(validDocument, "9007199254740993.30", "9007199254740993.3", 1)), "orders")
	require.NoError(t, err)
	require.NotEqual(t, a.Digest, c.Digest)
}

func TestRecordRejectsUnsafeOrAmbiguousDocuments(t *testing.T) {
	cases := []string{
		`{}`, `[]`, `null`, validDocument + ` {}`, strings.Repeat(" ", MaxDocumentBytes+1), string([]byte{0xff}),
		strings.Replace(validDocument, `"version":"1"`, `"version":"1","version":"2"`, 1),
		strings.Replace(validDocument, `"id":"order"`, `"name":"order"`, 1),
		strings.Replace(validDocument, `"schema":[`, `"schema":[{"id":"order"},`, 1),
		strings.Replace(validDocument, "v3.2.0", "v3.1.0", 1),
		strings.Replace(validDocument, `"id":"orders"`, `"id":"other"`, 1),
		strings.Replace(validDocument, `"version":"1"`, `"version":null`, 1),
		strings.Replace(validDocument, `"version":"1"`, `"version":"1","status":{}`, 1),
		strings.Replace(validDocument, `"version":"1"`, `"version":"1","unknown":"private-value"`, 1),
		strings.Replace(validDocument, `9007199254740993.30`, strings.Repeat("[", 65)+"1"+strings.Repeat("]", 65), 1),
	}
	for _, section := range []string{"servers", "credentials", "connection_config", "config", "authentication", "client_secret", "team", "support", "roles", "customProperties", "password", "api_key"} {
		cases = append(cases, strings.Replace(validDocument, `"name":"amount"`, `"name":"amount","`+section+`":"private-value"`, 1))
	}
	for i, raw := range cases {
		_, err := Record([]byte(raw), "orders")
		require.ErrorIs(t, err, ErrDocument, "case %d", i)
		require.NotContains(t, err.Error(), "private-value")
		require.NotContains(t, xerror.PublicDetail(err), "private-value")
	}
}

func TestRecordRichDocumentRoundTrip(t *testing.T) {
	raw := fixture(t, "rich")
	revision, err := Record(raw, "orders")
	require.NoError(t, err)
	again, err := Record(revision.Document, "orders")
	require.NoError(t, err)
	require.Equal(t, revision, again)
	var before, after any
	require.NoError(t, jsonvalue.Unmarshal(raw, &before))
	require.NoError(t, jsonvalue.Unmarshal(revision.Document, &after))
	require.Equal(t, before, after)
	require.Len(t, revision.SchemaObjects, 3)
	result, err := Convert(revision.Document, "order")
	require.NoError(t, err)
	require.True(t, result.DocumentValid)
	require.NotNil(t, result.Shape)
	require.Empty(t, result.Diagnostics)
	require.NotEmpty(t, result.Coverage)
}

func TestRecordRequiresNormativeSchemaButNotObjectIDs(t *testing.T) {
	for _, schema := range []string{`[]`, `[{"name":"Orders"}]`} {
		raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":` + schema + `}`
		revision, err := Record([]byte(raw), "orders")
		require.NoError(t, err)
		require.Empty(t, revision.SchemaObjects)
		result, err := Convert(revision.Document, "")
		require.NoError(t, err)
		require.True(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		requireDiagnostic(t, result, "$.schema", "object_not_found")
	}
	_, err := Record([]byte(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1"}`), "orders")
	require.ErrorIs(t, err, ErrDocument)
}

func TestRecordSecretBearingExtensions(t *testing.T) {
	for _, value := range []string{`"private-value"`, `{"token":"private-value"}`} {
		raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[],"customProperties":[{"property":"password","value":` + value + `}]}`
		_, err := Record([]byte(raw), "orders")
		require.ErrorIs(t, err, ErrDocument)
		require.NotContains(t, xerror.PublicDetail(err), "private-value")
	}
}
