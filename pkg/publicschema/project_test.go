package publicschema

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func TestProjectRejectsPublicReferencesToInternalComponents(t *testing.T) {
	t.Parallel()
	source := []byte(`{"openapi":"3.0.3","info":{"title":"test","version":"1"},"paths":{"/public":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Private"}}}}}}}},"components":{"schemas":{"Private":{"x-internal":true,"type":"string"}}}}`)
	document, err := openapi3.NewLoader().LoadFromData(source)
	require.NoError(t, err)
	_, err = Project(document)
	require.ErrorContains(t, err, "resolve public API document references")
	require.Contains(t, document.Components.Schemas, "Private")
}
