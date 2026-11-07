package pipelines

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncStatusGeneratedEnumContracts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value SyncStatus
		text  string
	}{
		{SyncStatusUnknown, "unknown"},
		{SyncStatusSynced, "synced"},
		{SyncStatusDrifted, "drifted"},
	} {
		require.Equal(t, tc.text, tc.value.String())

		parsed, err := SyncStatusString(tc.text)
		require.NoError(t, err)
		require.Equal(t, tc.value, parsed)

		text, err := tc.value.MarshalText()
		require.NoError(t, err)
		require.Equal(t, tc.text, string(text))

		var fromText SyncStatus
		require.NoError(t, fromText.UnmarshalText([]byte(tc.text)))
		require.Equal(t, tc.value, fromText)

		payload, err := json.Marshal(tc.value)
		require.NoError(t, err)
		require.JSONEq(t, `"`+tc.text+`"`, string(payload))

		var fromJSON SyncStatus
		require.NoError(t, json.Unmarshal(payload, &fromJSON))
		require.Equal(t, tc.value, fromJSON)
	}

	require.Equal(t, "SyncStatus(99)", SyncStatus(99).String())
	_, err := SyncStatusString("bogus")
	require.Error(t, err)
	var value SyncStatus
	require.Error(t, value.UnmarshalText([]byte("bogus")))
	require.Error(t, json.Unmarshal([]byte(`"bogus"`), &value))
}
