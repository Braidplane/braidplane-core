package run

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

func TestNewVerifiedJSONEventInput(t *testing.T) {
	t.Parallel()

	document := json.RawMessage(`{"id":"evt-1"}`)
	input, err := NewVerifiedJSONEventInput(must.Slug("event"), document)
	require.NoError(t, err)

	document[2] = 'X'
	require.JSONEq(t, `{"id":"evt-1"}`, string(input.Payload().Document))
	require.Equal(t, model.ContractRepresentationJSON, input.Summary().Representation)
	require.Equal(t, int64(14), input.Summary().SizeBytes)
	require.Equal(t, "sha256", input.Summary().DigestAlgorithm)
	require.Equal(t, "1b3b9ad33f5bac2567e961731f4a9af2617ea39aee9d240ec70e45dec92b7371", input.Summary().Digest)
	require.Equal(t, []string{"event"}, input.Summary().Keys)
	require.Equal(t, pipepayload.ProvenanceRunInput, input.Payload().Provenance)
}

func TestNewVerifiedJSONEventInputRejectsInvalidOrOversizedInput(t *testing.T) {
	t.Parallel()

	_, err := NewVerifiedJSONEventInput(must.Slug("event"), json.RawMessage(`{"id":`))
	require.ErrorContains(t, err, "must be valid JSON")

	exactLimit := json.RawMessage(`"` + strings.Repeat("x", MaxEventInputBytes-2) + `"`)
	require.Len(t, exactLimit, MaxEventInputBytes)
	_, err = NewVerifiedJSONEventInput(must.Slug("event"), exactLimit)
	require.NoError(t, err)

	_, err = NewVerifiedJSONEventInput(must.Slug("event"), json.RawMessage{})
	require.ErrorContains(t, err, "event input JSON is required")

	_, err = NewVerifiedJSONEventInput(
		must.Slug("event"),
		json.RawMessage(`"`+strings.Repeat("x", MaxEventInputBytes)+`"`),
	)
	require.ErrorContains(t, err, "exceeds 1048576 byte limit")
}
