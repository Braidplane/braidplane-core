package validate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepositoryLoadRequiresOnePipelineDocument(t *testing.T) {
	t.Parallel()
	const yamlPipeline = `metadata:
  slug: users-sync
  name: Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
`
	const jsonPipeline = `{
  "metadata": {"slug":"users-sync","name":"Users Sync"},
  "steps": [{"kind":"query","slug":"fetch-users","query":{
    "data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"
  }}]
}`
	for _, tc := range []struct {
		name string
		path string
		data string
	}{
		{"YAML", "users.pipeline.yaml", yamlPipeline + "...\n# end\n"},
		{"JSON", "users.pipeline.json", jsonPipeline + "\n \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := Load(tc.path, []byte(tc.data))
			require.NoError(t, err)
			require.Equal(t, "users-sync", loaded.Metadata.Slug.String())
		})
	}
	for _, tc := range []struct {
		name   string
		path   string
		data   string
		detail string
	}{
		{"additional YAML document", "users.pipeline.yaml", yamlPipeline + "---\n{}\n", "multiple YAML documents"},
		{"empty YAML document", "users.pipeline.yml", yamlPipeline + "---\n", "multiple YAML documents"},
		{"malformed YAML tail", "users.pipeline.yaml", yamlPipeline + "---\n[", "decode pipeline"},
		{"additional JSON value", "users.pipeline.json", jsonPipeline + "\n{}", "multiple JSON values"},
		{"malformed JSON tail", "users.pipeline.json", jsonPipeline + "\n{", "decode pipeline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := Load(tc.path, []byte(tc.data))
			require.ErrorContains(t, err, tc.detail)
			require.Nil(t, loaded)
		})
	}
}

func TestValidateDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setup           func(t *testing.T, root string)
		wantCount       int
		wantErrContains string
	}{
		{
			name: "ValidatesCanonicalSpecsOnly",
			setup: func(t *testing.T, root string) {
				t.Helper()

				require.NoError(
					t,
					os.WriteFile(
						filepath.Join(root, "schema.cue"),
						[]byte("package pipeline"),
						0o600,
					),
				)
				require.NoError(
					t,
					os.WriteFile(
						filepath.Join(root, "single-query.pipeline.yaml"),
						[]byte("metadata:\n"+
							"  slug: acme-users-sync\n"+
							"  name: ACME Users Sync\n"+
							"steps:\n"+
							"  - kind: query\n"+
							"    slug: fetch-users\n"+
							"    query:\n"+
							"      data_source_ref: acme-postgres\n"+
							"      operation_type: sql\n"+
							"      operation_ref: list-active-users\n"),
						0o600,
					),
				)
				require.NoError(
					t,
					os.WriteFile(
						filepath.Join(root, "notes.yaml"),
						[]byte("hello: world\n"),
						0o600,
					),
				)
			},
			wantCount: 1,
		},
		{
			name: "ReturnsAggregatedErrorForInvalidSpec",
			setup: func(t *testing.T, root string) {
				t.Helper()

				require.NoError(
					t,
					os.WriteFile(
						filepath.Join(root, "broken.pipeline.yaml"),
						[]byte("metadata:\n"+
							"  slug: BAD SLUG\n"+
							"  name: ACME Users Sync\n"+
							"steps:\n"+
							"  - kind: query\n"+
							"    slug: fetch-users\n"+
							"    query:\n"+
							"      data_source_ref: acme-postgres\n"+
							"      operation_type: sql\n"+
							"      operation_ref: list-active-users\n"),
						0o600,
					),
				)
			},
			wantCount:       1,
			wantErrContains: "invalid spec",
		},
		{
			name: "ReturnsErrorWhenNoSpecsFound",
			setup: func(t *testing.T, root string) {
				t.Helper()
				require.NoError(
					t,
					os.WriteFile(filepath.Join(root, "README.md"), []byte("notes"), 0o600),
				)
				require.NoError(
					t,
					os.WriteFile(
						filepath.Join(root, "notes.yaml"),
						[]byte("hello: world\n"),
						0o600,
					),
				)
			},
			wantCount:       0,
			wantErrContains: ErrNoPipelineSpecsFound.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			tc.setup(t, root)

			count, err := ValidateDirectory(root)
			require.Equal(t, tc.wantCount, count)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}
