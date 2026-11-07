package odcs

import (
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestParseArtifactYAMLCanonicalizesRichLiteralDocument(t *testing.T) {
	raw, err := ParseArtifact([]byte(`# comments are not data
kind: DataContract
apiVersion: v3.2.0
id: ${ODCS_ID}
version: "1"
schema:
  - id: order
    name: Orders
    properties:
      - name: amount
        value: 9007199254740993.30
        enabled: true
        nothing: null
        note: >
          unresolved ${ODCS_TOKEN}
          remains literal
`), "yaml")
	require.NoError(t, err)
	require.JSONEq(t, `{"apiVersion":"v3.2.0","id":"${ODCS_ID}","kind":"DataContract","schema":[{"id":"order","name":"Orders","properties":[{"enabled":true,"name":"amount","note":"unresolved ${ODCS_TOKEN} remains literal\n","nothing":null,"value":9007199254740993.30}]}],"version":"1"}`, string(raw))
	require.Contains(t, string(raw), `9007199254740993.30`)
}

func TestParseArtifactJSONPreservesValidatedInput(t *testing.T) {
	input := []byte(` { "number":9007199254740993.30 } `)
	raw, err := ParseArtifact(input, "json")
	require.NoError(t, err)
	require.Equal(t, input, []byte(raw))
}

func TestParseArtifactRejectsUnsafeYAML(t *testing.T) {
	cases := map[string]string{
		"duplicate key":      "one: 1\none: 2\n",
		"nonstring key":      "1: one\n",
		"anchor":             "one: &shared value\n",
		"alias":              "one: &shared value\ntwo: *shared\n",
		"invalid null tag":   "one: !!null data\n",
		"invalid int tag":    "one: !!int 1.5\n",
		"merge":              "next: {<<: {one: 1}}\n",
		"multiple documents": "one: 1\n---\ntwo: 2\n",
		"custom tag":         "one: !custom value\n",
		"container tag":      "one: !custom {two: three}\n",
		"timestamp":          "one: 2026-10-15\n",
		"non-json number":    "one: 0x12\n",
		"infinite number":    "one: .inf\n",
		"deep nesting":       strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
		"oversize document":  strings.Repeat(" ", MaxDocumentBytes+1),
		"invalid utf8":       string([]byte{0xff}),
		"unsupported format": "one: 1\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			format := "yaml"
			if name == "unsupported format" {
				format = "toml"
			}
			_, err := ParseArtifact([]byte(input), format)
			require.ErrorIs(t, err, ErrDocument)
			require.NotEmpty(t, xerror.PublicDetail(err))
		})
	}
}

func TestParseArtifactRejectsMalformedJSON(t *testing.T) {
	_, err := ParseArtifact([]byte(`{"one":}`), "json")
	require.ErrorIs(t, err, ErrDocument)
}

func TestParseArtifactRetainsQuotedMergeSpelling(t *testing.T) {
	raw, err := ParseArtifact([]byte("opaque: {'<<': value}\n"), "yaml")
	require.NoError(t, err)
	require.JSONEq(t, `{"opaque":{"<<":"value"}}`, string(raw))
}
