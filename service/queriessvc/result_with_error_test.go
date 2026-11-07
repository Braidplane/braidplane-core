package queriessvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestResultWithErrorPreservesS3Payload(t *testing.T) {
	t.Parallel()

	payload := &result.FileResult{
		Operation:  "read",
		TargetPath: "exports/customers.csv",
	}
	source := result.NewS3Result(payload, 42, nil)

	wrapped := resultWithError(source, xerror.ExecutionRowLimitExceeded())

	require.Equal(t, result.ExecutionResultTypeS3, wrapped.Type())
	require.Equal(t, int64(42), wrapped.DurationMs())
	require.ErrorIs(t, wrapped.Error(), xerror.ExecutionRowLimitExceeded())
	s3Payload, ok := wrapped.S3()
	require.True(t, ok)
	require.Equal(t, payload, s3Payload)
}
