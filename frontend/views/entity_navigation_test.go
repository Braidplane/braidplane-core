package views

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEntityNavigation(t *testing.T) {
	list := "/orgs/acme/projects/ops/pipelines?environment_slug=dev"
	ctx := WithEntityPageURL(context.Background(), list+"&q=long+name&cursor=a%2Bb&direction=next&pipeline_slug=old&password=secret")
	selected := entityListHref(ctx, list, "pipeline_slug", "daily")
	u, err := url.Parse(selected)
	require.NoError(t, err)
	require.Equal(t, "long name", u.Query().Get("q"))
	require.Equal(t, "a+b", u.Query().Get("cursor"))
	require.Equal(t, "daily", u.Query().Get("pipeline_slug"))
	require.Empty(t, u.Query().Get("password"))
	detail := entityDetailHref("/orgs/acme/projects/ops/pipelines/daily?environment_slug=dev", selected)
	require.Equal(t, selected, entityBackHref(WithEntityPageURL(context.Background(), detail), list))
	closed, err := url.Parse(entityListHref(ctx, selected, "pipeline_slug", ""))
	require.NoError(t, err)
	require.Empty(t, closed.Query().Get("pipeline_slug"))
	require.Equal(t, "a+b", closed.Query().Get("cursor"))
	for _, invalid := range []string{"https://evil.test" + list, "//evil.test" + list, "/orgs/other/projects/ops/pipelines", "/orgs/acme/projects/ops/pipelines/other", list + "#unsafe", "%"} {
		t.Run(invalid, func(t *testing.T) {
			ctx := WithEntityPageURL(context.Background(), "/detail?list_return="+url.QueryEscape(invalid))
			require.Equal(t, list, entityBackHref(ctx, list))
		})
	}
}

func TestEntityBackHrefRejectsCrossTenantEncodedPaths(t *testing.T) {
	fallback := "/orgs/acme/data-sources?environment_slug=dev&selected=warehouse"
	for _, target := range []string{"/orgs/acme/../other/data-sources", "/orgs/acme%2f..%2fother/data-sources", "https://user@evil.test/orgs/acme/data-sources"} {
		ctx := WithEntityPageURL(context.Background(), "/detail?list_return="+url.QueryEscape(target))
		require.Equal(t, fallback, entityBackHref(ctx, fallback))
	}
}

func TestPipelineSectionsPreserveScopedReturnAndTarget(t *testing.T) {
	detail := "/orgs/acme/projects/ops/pipelines/daily?environment_slug=prod"
	list := "/orgs/acme/projects/ops/pipelines?environment_slug=prod&q=customers&cursor=a%2Bb&pipeline_slug=daily"
	ctx := WithEntityPageURL(context.Background(), entityDetailHref(detail, list))
	for _, section := range []string{"overview", "workflow", "runs", "deployments", "git"} {
		href := entitySectionHref(ctx, detail, section)
		next := WithEntityPageURL(context.Background(), href)
		require.Equal(t, section, pipelineDetailSection(next))
		require.Equal(t, list, entityBackHref(next, "/orgs/acme/projects/ops/pipelines"))
		u, err := url.Parse(href)
		require.NoError(t, err)
		require.Equal(t, "prod", u.Query().Get("environment_slug"))
		require.Empty(t, u.Fragment)
	}
	require.Equal(t, "overview", pipelineDetailSection(WithEntityPageURL(context.Background(), detail+"&section=unknown")))
}

func TestTriggerPreviewCloseUsesEncodedQueryAndRetainsFilters(t *testing.T) {
	t.Parallel()
	for _, fallback := range []string{"/orgs/acme/projects/etl/schedules", "/orgs/acme/projects/etl/schedules?environment_slug=prod&enabled=paused&pipeline_slug=sync&trigger_slug=nightly"} {
		closed := entityListHref(context.Background(), fallback, "trigger_slug", "")
		parsed, err := url.Parse(closed)
		require.NoError(t, err)
		require.Equal(t, "closed", parsed.Query().Get("preview"))
		require.Empty(t, parsed.Query().Get("trigger_slug"))
		reopened, err := url.Parse(entityListHref(context.Background(), closed, "trigger_slug", "nightly"))
		require.NoError(t, err)
		require.Empty(t, reopened.Query().Get("preview"))
		require.Equal(t, "nightly", reopened.Query().Get("trigger_slug"))
		original, err := url.Parse(fallback)
		require.NoError(t, err)
		for _, key := range []string{"environment_slug", "enabled", "pipeline_slug"} {
			require.Equal(t, original.Query().Get(key), parsed.Query().Get(key))
		}
	}
}
