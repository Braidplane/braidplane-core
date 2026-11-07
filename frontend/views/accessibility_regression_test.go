package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"
	htmlparser "golang.org/x/net/html"
)

type accessibilityFixture struct {
	name      string
	component templ.Component
	ctx       context.Context
	want      []string
}

func accessibilityFixtures(t *testing.T) []accessibilityFixture {
	t.Helper()
	shell := AuthenticatedShellData{Title: "Accessibility fixture", CurrentPath: "/dashboard", ActiveOrganizationName: "Acme HQ", ActiveOrganizationSlug: "acme", User: ShellUserSummary{DisplayName: "Jordan Lee"}}
	pipeline := testPipelineWithSpecContracts(t)
	workflowCtx := WithEntityPageURL(WithDefinitionInspection(t.Context(), DefinitionInspection{Spec: &pipeline.Spec}), "/orgs/acme/projects/ops/pipelines/daily?section=workflow")
	source := api.DataSource{Name: " \t", Slug: "legacy-source", Type: api.DataSourceTypePostgres}
	sources := []api.DataSource{source, {Slug: "empty-name", Type: api.DataSourceTypePostgres}, {Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypePostgres}}
	members := []api.Member{{Email: "alex@example.test", UserSlug: "alex"}, {Email: "blair@example.test", UserSlug: "blair"}}
	return []accessibilityFixture{
		{"members", AuthenticatedShell(shell, MembersPageContent(members, nil, false, "", "", false, "", "", "acme", MemberInviteFormData{}, MemberRoleFormData{}, "", "", "", NewOrganizationPermissions("members:update"), true)), t.Context(), []string{`aria-label="Role for alex@example.test (alex)"`, `aria-label="Role for blair@example.test (blair)"`, `action="/organizations/acme/members/alex/role"`}},
		{"sources", AuthenticatedShell(shell, DataSourcesPageContent(sources, sources, false, "", "", "acme", "dev", "", &source, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, DataSourceCreateFormData{}, OrganizationPermissions{}, "", "", DataSourcePageFeedback{})), t.Context(), []string{`>legacy-source</a>`, `>legacy-source</h2>`, `aria-label="Preview legacy-source (legacy-source)"`, `>empty-name</a>`, `>Warehouse</a>`, `aria-label="Preview Warehouse (warehouse)"`}},
		{"builder", BuilderBlueprintPage(shell, BuilderBlueprintState{}), t.Context(), []string{`<h2 class="text-base font-semibold text-base-content">Add your first step</h2>`, `aria-label="Search pipeline steps"`, `aria-label="Pipeline source"`, `aria-label="Source output contract JSON"`, `aria-label="Transform input contract JSON"`, `aria-label="Transform output contract JSON"`, `aria-label="Sink input contract JSON"`}},
		{"workflow", AuthenticatedShell(shell, PipelineDetailPageContent("acme", "ops", "dev", "daily", pipeline, nil, false, PipelineRunPreview{}, nil, false, PipelineBindingContext{}, PipelineUpdateFormData{}, OrganizationPermissions{})), workflowCtx, []string{`id="definition-inspector"`}},
		{"workflow-selected", AuthenticatedShell(shell, DefinitionInspector("acme", "ops")), WithEntityPageURL(workflowCtx, "/pipeline?lineage_step=load-customers"), []string{`data-pipeline-step`}},
		{"creation-history", PipelineCreationListPage(shell, PipelineCreationListData{Org: "acme", Project: "ops", Page: &api.PipelineCreationProposalListResponse{}}), t.Context(), []string{`No creation proposals on this page`}},
		{"runs", AuthenticatedShell(shell, PipelineEnvironmentRunPreview("acme", "ops", "dev", "daily", "/pipeline", PipelineRunPreview{Runs: []api.PipelineRunMetadata{{RunSlug: "run-one"}, {RunSlug: "run-two"}, {RunSlug: "run-three"}}}, nil, false, true)), t.Context(), []string{`<th scope="col"><span class="sr-only">Actions</span></th>`, `>Open run</a>`}},
	}
}

func TestAccessibilityAuditRegressions(t *testing.T) {
	for _, fixture := range accessibilityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, fixture.component.Render(fixture.ctx, &out))
			body := out.String()
			for _, want := range fixture.want {
				require.Contains(t, body, want)
			}
			root, err := htmlparser.Parse(strings.NewReader(body))
			require.NoError(t, err)
			previous := 0
			var visit func(*htmlparser.Node)
			visit = func(node *htmlparser.Node) {
				if node.Type == htmlparser.ElementNode && len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6' {
					level := int(node.Data[1] - '0')
					require.LessOrEqual(t, level, previous+1, "heading %s skips a level after h%d", node.Data, previous)
					previous = level
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(root)
			require.Contains(t, body, `<span class="sr-only">Switch organization: </span>`)
			require.Contains(t, body, `<span class="sr-only">Jordan Lee Account</span>`)
		})
	}
}
