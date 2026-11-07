package views

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestRunFailureRendersDataSourceDiagnostics(t *testing.T) {
	for _, reason := range []api.PipelineRunFailureReasonCode{api.DataSourceCredentialsFailed, api.DataSourcePermissionDenied, api.DataSourceConnectionFailed, api.DataSourceTimeout, api.SourceQueryFailed, api.SourceQueryInvalid, api.SourceSchemaReferenceInvalid, api.EventInputRejected, api.DestinationConstraintViolation} {
		t.Run(string(reason), func(t *testing.T) {
			hint := "Review permission for the selected operation."
			failure := &api.PipelineRunFailureContext{ReasonCode: &reason, Error: "The data source denied permission for the selected operation.", ActionHint: &hint}
			var out bytes.Buffer
			require.NoError(t, PipelineRunFailureDiagnostic(failure).Render(t.Context(), &out))
			require.Contains(t, out.String(), failure.Error)
			require.Contains(t, out.String(), hint)
		})
	}
}
