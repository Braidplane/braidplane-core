package views

import (
	"bytes"
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventsPageContentRendersOnlyActiveSelectOptionsAsSelected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filters  EventFilters
		selected []string
	}{
		{
			name:    "empty filters select all options",
			filters: EventFilters{EventClass: EventClassGeneric},
			selected: []string{
				`<option selected value="">All actors</option>`,
				`<option selected value="">All entities</option>`,
				`<option selected value="">All actions</option>`,
			},
		},
		{
			name: "active filters select their matching options",
			filters: EventFilters{
				EventClass: EventClassGeneric,
				ActorType:  "user",
				EntityType: "pipeline",
				Action:     "update",
			},
			selected: []string{
				`<option selected value="user">User</option>`,
				`<option selected value="pipeline">Pipeline</option>`,
				`<option selected value="update">Update</option>`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, EventsPageContent(
				"acme",
				nil,
				false,
				"",
				"",
				tt.filters,
				0,
			).Render(context.Background(), &out))

			html := out.String()
			require.NotContains(t, html, `selected="false"`)
			require.Equal(t, 3, bytes.Count(out.Bytes(), []byte("<option selected")))
			for _, option := range tt.selected {
				require.Contains(t, html, option)
			}
		})
	}
}

func TestBuildSecurityAuditExportHrefPreservesExportFilters(t *testing.T) {
	t.Parallel()

	href, err := url.Parse(buildSecurityAuditExportHref("acme", EventFilters{
		EventClass:     EventClassSecurityAudit,
		ActorSlug:      "operator",
		RequestID:      "req-123",
		OccurredFrom:   "2026-07-01T00:00:00Z",
		OccurredBefore: "2026-08-01T00:00:00Z",
	}))

	require.NoError(t, err)
	require.Equal(t, "/orgs/acme/events/export", href.Path)
	require.Equal(t, "operator", href.Query().Get("actor_slug"))
	require.Equal(t, "req-123", href.Query().Get("request_id"))
	require.Equal(t, "2026-07-01T00:00:00Z", href.Query().Get("occurred_from"))
	require.Equal(t, "2026-08-01T00:00:00Z", href.Query().Get("occurred_before"))
	require.Empty(t, href.Query().Get("event_class"))
}

func TestEventMetadataJSONRemovesProhibitedFieldsRecursively(t *testing.T) {
	t.Parallel()

	rendered := eventMetadataJSON(map[string]interface{}{
		"role_slug": "admin",
		"password":  "hidden",
		"context": map[string]interface{}{
			"request_id":    "req-123",
			"session_token": "hidden",
		},
		"nested": []interface{}{
			[]interface{}{
				map[string]interface{}{
					"safe_value":       "visible",
					"body":             "hidden-body",
					"content":          "hidden-content",
					"file_contents":    "hidden-file",
					"parameter_values": "hidden-parameters",
					"private_key":      "hidden-key",
					"email":            "hidden@example.com",
				},
			},
		},
	})

	require.Contains(t, rendered, "role_slug")
	require.Contains(t, rendered, "request_id")
	require.Contains(t, rendered, "safe_value")
	require.Contains(t, rendered, "visible")
	require.NotContains(t, rendered, "password")
	require.NotContains(t, rendered, "session_token")
	require.NotContains(t, rendered, "hidden")
}
