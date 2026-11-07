package jobs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
)

func TestExecutionRequestArgsContainsOnlyOpaqueRequestID(t *testing.T) {
	t.Parallel()
	requestID := uuid.New()
	args := ExecutionRequestArgs{RequestID: requestID}
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, map[string]any{"request_id": requestID.String()}, fields)
	require.Equal(t, "execution_request", args.Kind())
	require.Equal(t, ExecutionQueue, args.InsertOpts().Queue)
	require.True(t, args.InsertOpts().UniqueOpts.ByArgs)
}
