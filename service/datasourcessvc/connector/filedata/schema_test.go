package filedata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferJSONFields_FlattensNestedPaths(t *testing.T) {
	t.Parallel()

	content := []byte(`[
	  {
	    "id": 1,
	    "email": "alice@example.com",
	    "profile": {"locale": "en", "flags": {"vip": true}},
	    "tags": ["alpha", "beta"],
	    "orders": [{"id": 10, "total": 12.5}],
	    "last_login": null
	  }
	]`)

	fields, err := InferJSONFields(content)
	require.NoError(t, err)
	require.NotEmpty(t, fields)

	fieldTypeByName := map[string]string{}
	for _, field := range fields {
		fieldTypeByName[field.Name] = field.Type
	}

	assert.Equal(t, "integer", fieldTypeByName["id"])
	assert.Equal(t, "string", fieldTypeByName["email"])
	assert.Equal(t, "object", fieldTypeByName["profile"])
	assert.Equal(t, "string", fieldTypeByName["profile.locale"])
	assert.Equal(t, "object", fieldTypeByName["profile.flags"])
	assert.Equal(t, "boolean", fieldTypeByName["profile.flags.vip"])
	assert.Equal(t, "array", fieldTypeByName["tags[]"])
	assert.Equal(t, "array", fieldTypeByName["orders[]"])
	assert.Equal(t, "integer", fieldTypeByName["orders[].id"])
	assert.Equal(t, "number", fieldTypeByName["orders[].total"])
	assert.Equal(t, "null", fieldTypeByName["last_login"])
}

func TestInferJSONFields_IncludesEmptyContainersAndNestedArrayObjectFields(t *testing.T) {
	t.Parallel()

	content := []byte(`[
	  {
	    "meta": {},
	    "items": [],
	    "catalog": [{"sku": "A-1", "price": 12.5}],
	    "user": {"profile": {"name": "Alice"}}
	  }
	]`)

	fields, err := InferJSONFields(content)
	require.NoError(t, err)

	fieldTypeByName := map[string]string{}
	for _, field := range fields {
		fieldTypeByName[field.Name] = field.Type
	}

	assert.Equal(t, "object", fieldTypeByName["meta"])
	assert.Equal(t, "array", fieldTypeByName["items[]"])
	assert.Equal(t, "array", fieldTypeByName["catalog[]"])
	assert.Equal(t, "string", fieldTypeByName["catalog[].sku"])
	assert.Equal(t, "number", fieldTypeByName["catalog[].price"])
	assert.Equal(t, "object", fieldTypeByName["user"])
	assert.Equal(t, "object", fieldTypeByName["user.profile"])
	assert.Equal(t, "string", fieldTypeByName["user.profile.name"])
}

func TestInferJSONFields_MergesMixedAndNullValues(t *testing.T) {
	t.Parallel()

	content := []byte(`[
	  {
	    "profile": {
	      "age": 30,
	      "nickname": null
	    },
	    "orders": [{"total": 10, "discount": null}],
	    "score": null
	  },
	  {
	    "profile": {
	      "age": 30.5,
	      "nickname": "ally"
	    },
	    "orders": [{"total": 11.75, "discount": 1}],
	    "score": 9,
	    "variant": "A"
	  },
	  {
	    "variant": 2
	  }
	]`)

	fields, err := InferJSONFields(content)
	require.NoError(t, err)

	fieldTypeByName := map[string]string{}
	for _, field := range fields {
		fieldTypeByName[field.Name] = field.Type
	}

	assert.Equal(t, "number", fieldTypeByName["profile.age"])
	assert.Equal(t, "string", fieldTypeByName["profile.nickname"])
	assert.Equal(t, "number", fieldTypeByName["orders[].total"])
	assert.Equal(t, "integer", fieldTypeByName["orders[].discount"])
	assert.Equal(t, "integer", fieldTypeByName["score"])
	assert.Equal(t, "string", fieldTypeByName["variant"])
}

func TestInferCSVFields_InfersHeaderFields(t *testing.T) {
	t.Parallel()

	content := []byte("id,email,active\n1,a@example.com,true\n")

	fields, err := InferCSVFields(content)
	require.NoError(t, err)
	require.Len(t, fields, 3)

	assert.Equal(t, "id", fields[0].Name)
	assert.Equal(t, "string", fields[0].Type)
	assert.Equal(t, "email", fields[1].Name)
	assert.Equal(t, "string", fields[1].Type)
	assert.Equal(t, "active", fields[2].Name)
	assert.Equal(t, "string", fields[2].Type)
}

func TestInferJSONFieldsRejectsTrailingContent(t *testing.T) {
	for _, input := range []string{`{"price":0.1} {"price":0.2}`, `{"price":0.1} trailing`} {
		_, err := InferJSONFields([]byte(input))
		require.Error(t, err)
	}
}
