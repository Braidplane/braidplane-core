package views

import (
	"bytes"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestProofConfigurationSummaryAndFailedEditor(t *testing.T) {
	configuration := api.DataSourceProofConfiguration{Name: "Repository", DataSourceSlug: "repo", Provider: "gitlab", RepositoryId: "confirmed-repository", Enabled: true, PublishChecks: true, WebhookSetupRequired: true, Status: "GitLab webhook setup failed", Targets: []api.ProofAssessmentTarget{{ProjectSlug: "ops", EnvironmentSlug: "prod", PipelinePath: "pipelines/orders.yaml"}}, AvailableTargets: []api.ProofTargetContext{{ProjectSlug: "ops", EnvironmentSlug: "prod", TargetRef: "main"}}}
	var summary bytes.Buffer
	require.NoError(t, ProofConfigurationSummaries("acme", []api.DataSourceProofConfiguration{configuration}).Render(t.Context(), &summary))
	for _, text := range []string{"Enabled", "1 assessment targets", "GitLab webhook setup failed", "/orgs/acme/data-sources/repo/proof"} {
		require.Contains(t, summary.String(), text)
	}
	require.NotContains(t, summary.String(), "<form")
	require.NotContains(t, summary.String(), "<textarea")
	for _, canEdit := range []bool{true, false} {
		var editor bytes.Buffer
		require.NoError(t, ProofConfigurationContent("acme", "repo", []api.DataSourceProofConfiguration{configuration}, "Save failed", true, canEdit).Render(t.Context(), &editor))
		html := editor.String()
		for _, text := range []string{"Assessment targets (1)", "Publication options", "open", "confirmed-repository", "pipelines/orders.yaml", "publish_checks"} {
			require.Contains(t, html, text)
		}
		if canEdit {
			require.Contains(t, html, "Retry GitLab webhook setup")
		} else {
			require.Contains(t, html, "fieldset disabled")
			require.NotContains(t, html, "Save Proof configuration")
			require.NotContains(t, html, "Retry GitLab webhook setup")
		}
	}
	var normal bytes.Buffer
	require.NoError(t, ProofDataSourceForm("acme", "repo", configuration, true, false).Render(t.Context(), &normal))
	require.NotContains(t, normal.String(), " open")
}

func TestProofEmptyStatesLinkToCanonicalCreateForm(t *testing.T) {
	t.Parallel()
	var forms, summary bytes.Buffer
	require.NoError(t, ProofConfigurationForms("acme", "", nil, true, false).Render(t.Context(), &forms))
	require.NoError(t, ProofConfigurationSummaries("acme", nil).Render(t.Context(), &summary))
	for _, html := range []string{forms.String(), summary.String()} {
		require.Contains(t, html, `href="/orgs/acme/data-sources?create=true#create-data-source"`)
	}
}
