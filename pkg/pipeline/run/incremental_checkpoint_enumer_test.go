package run

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIncrementalCheckpointStatusEnumContracts(t *testing.T) {
	t.Parallel()

	status, err := IncrementalCheckpointStatusString("committed")
	require.NoError(t, err)
	require.Equal(t, IncrementalCheckpointStatusCommitted, status)
	require.Equal(t, "committed", status.String())
	require.True(t, status.IsAIncrementalCheckpointStatus())
	require.False(t, IncrementalCheckpointStatus(99).IsAIncrementalCheckpointStatus())
	require.Contains(t, IncrementalCheckpointStatusStrings(), "blocked")
	require.Contains(t, IncrementalCheckpointStatusValues(), IncrementalCheckpointStatusSuperseded)

	raw, err := json.Marshal(IncrementalCheckpointStatusBlocked)
	require.NoError(t, err)
	require.JSONEq(t, `"blocked"`, string(raw))

	var decoded IncrementalCheckpointStatus
	require.NoError(t, json.Unmarshal([]byte(`"superseded"`), &decoded))
	require.Equal(t, IncrementalCheckpointStatusSuperseded, decoded)
	require.Error(t, json.Unmarshal([]byte(`"missing"`), &decoded))

	text, err := IncrementalCheckpointStatusCommitted.MarshalText()
	require.NoError(t, err)
	require.Equal(t, "committed", string(text))
	require.NoError(t, decoded.UnmarshalText([]byte("blocked")))
	require.Equal(t, IncrementalCheckpointStatusBlocked, decoded)
	require.Error(t, decoded.UnmarshalText([]byte("missing")))
}

func TestIncrementalCheckpointTransitionStatusEnumContracts(t *testing.T) {
	t.Parallel()

	status, err := IncrementalCheckpointTransitionStatusString("committed")
	require.NoError(t, err)
	require.Equal(t, IncrementalCheckpointTransitionCommitted, status)
	require.Equal(t, "committed", status.String())
	require.True(t, status.IsAIncrementalCheckpointTransitionStatus())
	require.False(t, IncrementalCheckpointTransitionStatus(99).IsAIncrementalCheckpointTransitionStatus())
	require.Contains(t, IncrementalCheckpointTransitionStatusStrings(), "blocked")
	require.Contains(t, IncrementalCheckpointTransitionStatusValues(), IncrementalCheckpointTransitionBlocked)

	raw, err := json.Marshal(IncrementalCheckpointTransitionBlocked)
	require.NoError(t, err)
	require.JSONEq(t, `"blocked"`, string(raw))

	var decoded IncrementalCheckpointTransitionStatus
	require.NoError(t, json.Unmarshal([]byte(`"committed"`), &decoded))
	require.Equal(t, IncrementalCheckpointTransitionCommitted, decoded)
	require.Error(t, json.Unmarshal([]byte(`"missing"`), &decoded))

	text, err := IncrementalCheckpointTransitionCommitted.MarshalText()
	require.NoError(t, err)
	require.Equal(t, "committed", string(text))
	require.NoError(t, decoded.UnmarshalText([]byte("blocked")))
	require.Equal(t, IncrementalCheckpointTransitionBlocked, decoded)
	require.Error(t, decoded.UnmarshalText([]byte("missing")))
}
