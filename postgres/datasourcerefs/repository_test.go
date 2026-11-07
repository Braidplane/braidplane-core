package datasourcerefs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepositoryQueriesScopeDataSourceRefsByOrganizationProjectAndPipeline(t *testing.T) {
	t.Parallel()

	querySQL := sqlBlock(readQuerySQL(t), "-- name: FindDataSourceRefBySlug :one")

	require.NotEmpty(t, querySQL)
	require.Contains(t, querySQL, "JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id")
	require.Contains(t, querySQL, "JOIN projects p ON ps.project_id = p.id")
	require.Contains(t, querySQL, "JOIN organizations o ON p.organization_id = o.id")
	require.Contains(t, querySQL, "o.slug = sqlc.arg ('org_slug')")
	require.Contains(t, querySQL, "p.slug = sqlc.arg ('project_slug')")
	require.Contains(t, querySQL, "ps.slug = sqlc.arg ('pipeline_slug')")
	require.Contains(t, querySQL, "ps.deleted_at IS NULL")
	require.Contains(t, querySQL, "dsr.slug = sqlc.arg ('data_source_ref_slug')")
}

func TestRepositoryQueriesScopeEnvironmentBindingsByOrganizationProjectPipelineAndEnvironment(t *testing.T) {
	t.Parallel()

	querySQL := sqlBlock(readQuerySQL(t), "-- name: FindDataSourceBinding :one")

	require.NotEmpty(t, querySQL)
	require.Contains(t, querySQL, "JOIN project_environments pe ON pe.id = dsb.project_environment_id")
	require.Contains(t, querySQL, "JOIN pipeline_specs ps ON ps.id = dsr.pipeline_spec_id")
	require.Contains(t, querySQL, "JOIN projects p ON ps.project_id = p.id")
	require.Contains(t, querySQL, "AND pe.project_id = p.id")
	require.Contains(t, querySQL, "JOIN organizations o ON p.organization_id = o.id")
	require.Contains(t, querySQL, "JOIN environments e ON e.id = pe.environment_id")
	require.Contains(t, querySQL, "AND ds.organization_id = o.id")
	require.Contains(t, querySQL, "o.slug = sqlc.arg ('org_slug')")
	require.Contains(t, querySQL, "p.slug = sqlc.arg ('project_slug')")
	require.Contains(t, querySQL, "e.slug = sqlc.arg ('environment_slug')")
	require.Contains(t, querySQL, "ps.slug = sqlc.arg ('pipeline_slug')")
	require.Contains(t, querySQL, "ps.deleted_at IS NULL")
	require.Contains(t, querySQL, "dsr.slug = sqlc.arg ('data_source_ref_slug')")
}

func TestRepositoryQueriesNeverReturnConcreteDataSourceSecretsForBindings(t *testing.T) {
	t.Parallel()

	bindingQuery := sqlBlock(readQuerySQL(t), "-- name: FindDataSourceBinding :one")

	require.NotContains(t, bindingQuery, "config")
	require.NotContains(t, bindingQuery, "encrypted_credentials")
	require.NotContains(t, bindingQuery, "credentials")
	require.Contains(t, bindingQuery, "ds.read_only AS data_source_read_only")
	require.Contains(t, bindingQuery, "ds.is_active AS data_source_active")
}

func TestRepositoryQueriesListDataSourceRefsOrdersByCursorKey(t *testing.T) {
	t.Parallel()

	querySQL := sqlBlock(readQuerySQL(t), "-- name: ListDataSourceRefs :many")

	require.NotEmpty(t, querySQL)
	require.Contains(t, querySQL, "(dsr.created_at, ps.slug || '/' || dsr.slug) >")
	require.Contains(t, querySQL, "(dsr.created_at, ps.slug || '/' || dsr.slug) <")
	require.Contains(t, querySQL, "WHEN sqlc.arg ('order_dir') = 'asc' THEN ps.slug || '/' || dsr.slug")
	require.Contains(t, querySQL, "WHEN sqlc.arg ('order_dir') = 'desc' THEN ps.slug || '/' || dsr.slug")
	require.Equal(t, 4, strings.Count(querySQL, "ps.slug || '/' || dsr.slug"))
}

func readQuerySQL(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile("../query.sql")
	require.NoError(t, err)
	return string(content)
}

func sqlBlock(content, marker string) string {
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	remaining := content[start+len(marker):]
	next := strings.Index(remaining, "-- name:")
	if next < 0 {
		return remaining
	}
	return remaining[:next]
}
