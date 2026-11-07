package frontend

import (
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestRunProgressPollingRespectsAttemptAndRetryTerminalState(t *testing.T) {
	for _, section := range []string{"overview", "steps", "attempts"} {
		require.True(t, runProgressShouldPoll("queued", nil, section))
		require.True(t, runProgressShouldPoll("running", nil, section))
		require.False(t, runProgressShouldPoll("failed", nil, section))
	}
	require.True(t, runProgressShouldPoll("failed", []api.PipelineRunTriggerRetryAttempt{{Status: "queued"}}, "attempts"))
	require.False(t, runProgressShouldPoll("failed", []api.PipelineRunTriggerRetryAttempt{{Status: "exhausted"}}, "attempts"))
	require.False(t, runProgressShouldPoll("running", nil, "completion"))
}
