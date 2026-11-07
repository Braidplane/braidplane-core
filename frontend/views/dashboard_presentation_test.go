package views

import (
	"bytes"
	"context"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestDashboardRecordedChecksDoNotImplyLiveHealth(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"known", "failure-history", "connection-failure", "unavailable", "unloaded", "no-project", "no-target", "restricted", "no-git"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			data := DashboardData{
				ProjectScope:        DashboardProjectScope{ActiveProjectSlug: "demo", ActiveEnvironmentSlug: "dev", HasPipelineBackend: true},
				DataSourceReadiness: DashboardDataSourceReadiness{Metrics: []DashboardMetric{{Label: "Data sources", Value: "1"}}},
				ContractChecks:      DashboardContractChecks{Metrics: []DashboardMetric{{Label: "Missing", Value: "0"}}},
				PipelineStatus:      DashboardPipelineStatus{Metrics: []DashboardMetric{{Label: "Not deployed", Value: "0", Kind: "success"}}},
			}
			known := false
			switch state {
			case "known":
				known = true
			case "failure-history":
				known = true
				data.IncidentQueue.Runs = []DashboardRunSummary{{RunSlug: "old-failure", Status: api.PipelineRunMetadataStatusFailed}}
			case "connection-failure":
				data.DataSourceReadiness.Issues = []DashboardInventoryIssue{{Label: "Connection", Detail: "last connection test failed", Href: "/connection"}}
			case "unavailable":
				data.ContractChecks.Unavailable = true
				data.ContractChecks.Message = "Bindings are unavailable."
			case "unloaded":
				data.PipelineStatus.Metrics = nil
			case "no-git":
				data.ProjectScope.HasPipelineBackend = false
			case "no-project":
				data.ProjectScope.ActiveProjectSlug = ""
			case "no-target":
				data.ProjectScope.ActiveEnvironmentSlug = ""
			case "restricted":
				data.DataSourceReadiness.Unavailable = true
				data.DataSourceReadiness.Message = "Permission required: ask a workspace admin."
			}
			var out bytes.Buffer
			require.NoError(t, DashboardAttentionPanel(data).Render(context.Background(), &out))
			require.Equal(t, !known, DashboardHasRecordedAttention(data))
			if known {
				require.Contains(t, out.String(), "No setup or connection-test issues in the loaded records.")
			} else {
				require.NotContains(t, out.String(), "No setup or connection-test issues")
			}
			if state == "restricted" {
				require.Contains(t, out.String(), "ask a workspace admin")
			}
			if state == "failure-history" {
				out.Reset()
				require.NoError(t, DashboardIncidentQueuePanel(data).Render(context.Background(), &out))
				require.Contains(t, out.String(), "old-failure")
				require.Contains(t, out.String(), "A past failure does not establish current health.")
			}
		})
	}
}
