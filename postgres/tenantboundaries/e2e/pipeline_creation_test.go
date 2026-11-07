package e2e

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/tenantfixture"
	"github.com/Braidplane/braidplane-core/postgres/pipelinecreations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestPipelineCreationDoesNotInventCanonicalProvenanceAndIsTenantScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := e2edb.OpenPostgresPool(t, "pipeline creation proposal isolation")
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	f := tenantfixture.SeedPipelineBoundary(t, tx, true)
	var org, member [2]slug.Slug
	for i := range 2 {
		require.NoError(t, tx.QueryRow(t.Context(), `SELECT o.slug,u.slug FROM organizations o JOIN organization_members m ON m.organization_id=o.id JOIN users u ON u.id=m.user_id WHERE m.id=$1`, f.Members[i]).Scan(&org[i], &member[i]))
	}
	repo := pipelinecreations.New()
	row := &pipelinecreations.Row{Slug: "creation-first", PipelineSlug: "brand-new", MemberSlug: member[0], Request: []byte(`{"content":"synthetic definition"}`), PublishAttemptID: "exact-attempt"}
	created, err := repo.Create(t.Context(), tx, org[0], "project", row)
	require.NoError(t, err)
	require.True(t, created)
	second := *row
	second.Slug, second.PipelineSlug = "creation-second", "another-pipeline"
	created, err = repo.Create(t.Context(), tx, org[0], "project", &second)
	require.NoError(t, err)
	require.True(t, created)
	page, err := repo.List(t.Context(), tx, org[0], "project", "", 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, row.Slug, page[0].Slug)
	page, err = repo.List(t.Context(), tx, org[0], "project", row.Slug, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, second.Slug, page[0].Slug)
	for _, scope := range []struct{ org, project slug.Slug }{{org[1], "project"}, {org[0], "second"}} {
		page, err := repo.List(t.Context(), tx, scope.org, scope.project, "", 20)
		require.NoError(t, err)
		require.Empty(t, page)
	}
	_, err = repo.Get(t.Context(), tx, org[1], "project", row.Slug)
	require.ErrorIs(t, err, pipelinecreations.ErrNotFound)
	_, err = repo.Get(t.Context(), tx, org[0], "second", row.Slug)
	require.ErrorIs(t, err, pipelinecreations.ErrNotFound)
	duplicate := *row
	duplicate.Slug = "duplicate-creation"
	created, err = repo.Create(t.Context(), tx, org[0], "project", &duplicate)
	require.NoError(t, err)
	require.False(t, created)
	found, err := repo.Find(t.Context(), tx, org[0], "project", row.PipelineSlug)
	require.NoError(t, err)
	require.Equal(t, row.Slug, found.Slug)
	require.Equal(t, row.MemberSlug, found.MemberSlug)
	require.JSONEq(t, string(row.Request), string(found.Request))
	wrong := *found
	wrong.Status = "proposal_open"
	wrong.PublishAttemptID = "stale-attempt"
	changed, err := repo.Update(t.Context(), tx, org[0], "project", &wrong)
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = repo.Update(t.Context(), tx, org[1], "project", &wrong)
	require.NoError(t, err)
	require.False(t, changed)
	found.Status = "proposal_open"
	found.Result = []byte(`{"proposal_id":"42"}`)
	changed, err = repo.Update(t.Context(), tx, org[0], "project", found)
	require.NoError(t, err)
	require.True(t, changed)
	found.Result = []byte(`{"proposal_id":"substituted"}`)
	changed, err = repo.Update(t.Context(), tx, org[0], "project", found)
	require.NoError(t, err)
	require.False(t, changed, "a replay cannot replace the first provider result")
	found.Status = "publish_failed"
	found.FailureCode = "late_failure"
	changed, err = repo.Update(t.Context(), tx, org[0], "project", found)
	require.NoError(t, err)
	require.False(t, changed, "late failures cannot replace successful publication")
	got, err := repo.Get(t.Context(), tx, org[0], "project", row.Slug)
	require.NoError(t, err)
	require.Equal(t, "proposal_open", got.Status)
	require.JSONEq(t, `{"proposal_id":"42"}`, string(got.Result))
	require.Empty(t, got.FailureCode)
	var canonical, versions int
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_specs WHERE project_id=$1 AND slug='brand-new'`, f.Project[0]).Scan(&canonical))
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM pipeline_versions WHERE project_id=$1 AND pipeline_slug='brand-new'`, f.Project[0]).Scan(&versions))
	require.Zero(t, canonical)
	require.Zero(t, versions)
	sub, err := tx.Begin(t.Context())
	require.NoError(t, err)
	_, err = sub.Exec(t.Context(), `INSERT INTO pipeline_creation_proposals(organization_id,project_id,member_id,slug,pipeline_slug,request,publish_attempt_id) VALUES ($1,$2,$3,'cross-member','cross','{}','attempt')`, f.Org[0], f.Project[0], f.Members[1])
	var constraint *pgconn.PgError
	require.ErrorAs(t, err, &constraint)
	require.Equal(t, "23503", constraint.Code)
	require.NoError(t, sub.Rollback(t.Context()))
}
