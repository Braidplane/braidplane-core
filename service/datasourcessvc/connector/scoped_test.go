package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

type scopedTestConnector struct {
	fakeConnector
	calls int
}

func (c *scopedTestConnector) ExecuteScoped(_ context.Context, _ json.RawMessage, _ map[string]types.TypedParam, scope resourcescope.Scope, _ ...types.ExecutionLimits) (*result.ExecutionResult, error) {
	c.calls++
	return result.NewS3Result(&result.FileResult{Operation: "read"}, 0, nil), scope.Validate()
}

func TestScopedDispatchSurvivesTracingAndNeverFallsBack(t *testing.T) {
	t.Parallel()
	scope := &resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "store", Operation: "read", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test", Bucket: "bucket"}}
	base := &scopedTestConnector{}
	wrapped := wrapTracingConnector(base, model.DataSourceTypeS3)
	_, err := Execute(t.Context(), wrapped, nil, nil, scope, types.ExecutionLimits{})
	require.NoError(t, err)
	require.Equal(t, 1, base.calls)
	_, err = Execute(t.Context(), wrapTracingConnector(fakeConnector{}, model.DataSourceTypePostgres), nil, nil, scope, types.ExecutionLimits{})
	require.ErrorIs(t, err, resourcescope.ErrInvalid)
	output, err := Execute(t.Context(), fakeConnector{}, nil, nil, nil, types.ExecutionLimits{})
	require.NoError(t, err)
	require.NoError(t, output.Error())
}
