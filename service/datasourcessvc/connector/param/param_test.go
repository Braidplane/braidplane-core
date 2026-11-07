package param

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNullOnlySupportsWholeValueBindings(t *testing.T) {
	_, err := SubstituteString("prefix/{{value}}", map[string]any{"value": nil})
	require.ErrorContains(t, err, `parameter "value" is null`)
	result, err := SubstituteInMap(map[string]any{"value": "{{value}}"}, map[string]any{"value": nil})
	require.NoError(t, err)
	require.Contains(t, result, "value")
	require.Nil(t, result["value"])
}

func TestExtractParamNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		want     []string
	}{
		{
			name:     "NoParams",
			template: "SELECT * FROM users",
			want:     []string{},
		},
		{
			name:     "SingleParam",
			template: "WHERE id = {{userId}}",
			want:     []string{"userId"},
		},
		{
			name:     "MultipleParams",
			template: "WHERE user_id = {{userId}} AND status = {{status}} AND limit {{limit}}",
			want:     []string{"userId", "status", "limit"},
		},
		{
			name:     "DuplicateParams",
			template: "WHERE a = {{id}} AND b = {{id}} AND c = {{id}}",
			want:     []string{"id"},
		},
		{
			name:     "MixedOrder",
			template: "{{name}} WHERE id = {{id}} AND x = {{name}}",
			want:     []string{"name", "id"},
		},
		{
			name:     "ValidParamCharacters",
			template: "{{user_id}} {{_var}} {{Var123}}",
			want:     []string{"user_id", "_var", "Var123"},
		},
		{
			name:     "DottedNamespacedParam",
			template: "reports/{{runtime.run.date}}/{{runtime.run.datetime}}.csv",
			want:     []string{"runtime.run.date", "runtime.run.datetime"},
		},
		{
			name:     "WhitespaceAroundName",
			template: "reports/{{ runtime.run.date }}/{{runtime.run.datetime }}.csv",
			want:     []string{"runtime.run.date", "runtime.run.datetime"},
		},
		{
			name:     "InvalidParamCharacters",
			template: "{{123invalid}} {{-invalid}}",
			want:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractParamNames(tt.template)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSubstituteParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		template  string
		params    map[string]any
		wantArgs  []any
		assertErr func(t *testing.T, err error)
	}{
		{
			name:     "NoParams",
			template: "SELECT * FROM users",
			params:   map[string]any{},
			wantArgs: []any{},
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "MatchingParams",
			template: "WHERE id = {{id}} AND name = {{name}}",
			params:   map[string]any{"id": 123, "name": "John"},
			wantArgs: []any{123, "John"},
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "MissingParameter",
			template: "WHERE id = {{id}} AND name = {{name}}",
			params:   map[string]any{"id": 123},
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorContains(t, err, `missing required parameter "name"`)
			},
		},
		{
			name:     "ExtraParameters",
			template: "WHERE id = {{id}}",
			params:   map[string]any{"id": 123, "extra": "ignored"},
			wantArgs: []any{123},
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "DuplicateParamReferenceSingleValue",
			template: "WHERE id = {{id}} AND backup_id = {{id}}",
			params:   map[string]any{"id": 123},
			wantArgs: []any{123},
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, args, err := SubstituteParams(tt.template, tt.params)
			tt.assertErr(t, err)
			if err == nil {
				require.Equal(t, tt.wantArgs, args)
			}
		})
	}
}

func TestSubstituteString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		template  string
		params    map[string]any
		want      string
		assertErr func(t *testing.T, err error)
	}{
		{
			name:     "NoParams",
			template: "/api/users",
			params:   map[string]any{},
			want:     "/api/users",
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "SingleParam",
			template: "/users/{{userId}}/profile",
			params:   map[string]any{"userId": "john-doe"},
			want:     "/users/john-doe/profile",
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "MultipleParams",
			template: "/orgs/{{orgSlug}}/teams/{{teamSlug}}",
			params:   map[string]any{"orgSlug": "acme", "teamSlug": "devs"},
			want:     "/orgs/acme/teams/devs",
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "NumericParam",
			template: "/repos/{{repoId}}",
			params:   map[string]any{"repoId": 42},
			want:     "/repos/42",
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "DottedNamespacedParam",
			template: "/exports/{{runtime.run.date}}/orders-{{runtime.run.datetime}}.json",
			params: map[string]any{
				"runtime.run.date":     "2026-05-14",
				"runtime.run.datetime": "2026-05-14T22:30:00Z",
			},
			want: "/exports/2026-05-14/orders-2026-05-14T22:30:00Z.json",
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "WhitespaceAndLiteralValueAreOnePass",
			template: "/exports/{{ runtime.run.date }}/{{next}}",
			params: map[string]any{
				"runtime.run.date": "{{next}}",
				"next":             "final",
			},
			want: "/exports/{{next}}/final",
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name:     "MissingParam",
			template: "/users/{{userId}}",
			params:   map[string]any{},
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorContains(t, err, `missing required parameter "userId"`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := SubstituteString(tt.template, tt.params)
			tt.assertErr(t, err)
			if err == nil {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSubstituteInMap(t *testing.T) {
	t.Parallel()

	t.Run("SimpleStringValues", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{
			"username": "{{user}}",
			"email":    "{{email}}",
		}
		params := map[string]any{
			"user":  "john",
			"email": "john@example.com",
		}

		result, err := SubstituteInMap(data, params)
		require.NoError(t, err)
		require.Equal(t, "john", result["username"])
		require.Equal(t, "john@example.com", result["email"])
	})

	t.Run("NestedMap", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{
			"filter": map[string]any{
				"user_id": "{{userId}}",
				"status":  "active",
			},
		}
		params := map[string]any{"userId": 123}

		result, err := SubstituteInMap(data, params)
		require.NoError(t, err)

		filterMap := result["filter"].(map[string]any)
		require.Equal(t, 123, filterMap["user_id"])
		require.Equal(t, "active", filterMap["status"])
	})

	t.Run("MissingParameterInNestedMap", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{
			"filter": map[string]any{
				"user_id": "{{userId}}",
			},
		}
		params := map[string]any{}

		_, err := SubstituteInMap(data, params)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing required parameter")
	})

	t.Run("PrimitiveValuesUnchanged", func(t *testing.T) {
		t.Parallel()
		data := map[string]any{
			"count":   42,
			"active":  true,
			"timeout": 30.5,
			"name":    "{{username}}",
		}
		params := map[string]any{"username": "john"}

		result, err := SubstituteInMap(data, params)
		require.NoError(t, err)
		require.Equal(t, 42, result["count"])
		require.True(t, result["active"].(bool))
		require.InEpsilon(t, 30.5, result["timeout"], 0.000001)
		require.Equal(t, "john", result["name"])
	})
}

func TestSubstituteInArray(t *testing.T) {
	t.Parallel()

	t.Run("StringArrayWithParams", func(t *testing.T) {
		t.Parallel()
		data := []any{"{{field1}}", "{{field2}}", "static"}
		params := map[string]any{"field1": "name", "field2": "age"}

		result, err := SubstituteInArray(data, params)
		require.NoError(t, err)
		require.Equal(t, []any{"name", "age", "static"}, result)
	})

	t.Run("MixedArrayWithMaps", func(t *testing.T) {
		t.Parallel()
		data := []any{
			map[string]any{"query": "{{searchTerm}}"},
			"static",
		}
		params := map[string]any{"searchTerm": "database"}

		result, err := SubstituteInArray(data, params)
		require.NoError(t, err)
		require.Equal(t, "database", result[0].(map[string]any)["query"])
		require.Equal(t, "static", result[1])
	})

	t.Run("MissingParameterInArray", func(t *testing.T) {
		t.Parallel()
		data := []any{"{{missing}}"}
		params := map[string]any{}

		_, err := SubstituteInArray(data, params)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing required parameter")
	})
}

func TestComplexNestedStructure(t *testing.T) {
	t.Parallel()

	data := map[string]any{
		"filter": map[string]any{
			"status": "{{status}}",
			"tags":   []any{"{{tag1}}", "{{tag2}}", "fixed"},
		},
		"projection": map[string]any{
			"id":   1,
			"name": 1,
		},
	}
	params := map[string]any{
		"status": "active",
		"tag1":   "urgent",
		"tag2":   "feature",
	}

	result, err := SubstituteInMap(data, params)
	require.NoError(t, err)

	filterMap := result["filter"].(map[string]any)
	require.Equal(t, "active", filterMap["status"])

	tagsArray := filterMap["tags"].([]any)
	require.Equal(t, "urgent", tagsArray[0])
	require.Equal(t, "feature", tagsArray[1])
	require.Equal(t, "fixed", tagsArray[2])
}

func TestSubstituteValue(t *testing.T) {
	t.Parallel()

	t.Run("ExactTemplateReturnsStructuredValue", func(t *testing.T) {
		t.Parallel()

		value, err := SubstituteValue("{{payload}}", map[string]any{
			"payload": []any{map[string]any{"status": "pending"}},
		})
		require.NoError(t, err)

		rows, ok := value.([]any)
		require.True(t, ok)
		require.Len(t, rows, 1)
		first, ok := rows[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "pending", first["status"])
	})

	t.Run("PrimitivesRemainUnchanged", func(t *testing.T) {
		t.Parallel()

		value, err := SubstituteValue(true, map[string]any{"unused": "value"})
		require.NoError(t, err)
		require.Equal(t, true, value)
	})
}
