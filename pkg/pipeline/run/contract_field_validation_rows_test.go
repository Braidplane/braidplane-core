package run

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

func TestShouldCoerceContractFieldValidationRows_HTTPCSVWithoutRuntimePayload(t *testing.T) {
	t.Parallel()

	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "text/csv"},
		Body:       []byte("amount\n2.50\n"),
	}, 0, nil)
	source := &StepResult{execution: execResult}

	shouldCoerce := shouldCoerceContractFieldValidationRows(source, &payload.Contract{})

	require.True(t, shouldCoerce)
}
