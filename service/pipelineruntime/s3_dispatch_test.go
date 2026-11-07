package pipelineruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This executor keeps real S3 decoding and serialization in the adapter.
type s3DispatchExecutor struct {
	connector *s3.Connector
	operation map[string]any
}

func (e s3DispatchExecutor) Execute(ctx context.Context, org, source, _ slug.Slug, _ model.EventActor, values map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
	return e.ExecuteInline(ctx, org, source, e.operation, values)
}

func (e s3DispatchExecutor) ExecuteInline(ctx context.Context, _, _ slug.Slug, operation, values map[string]any) (*result.ExecutionResult, error) {
	raw, err := json.Marshal(operation)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.Query).With(err)
	}
	params := make(map[string]types.TypedParam, len(values))
	for key, value := range values {
		params[key] = types.TypedParam{Value: value}
	}
	output, err := e.connector.Execute(ctx, raw, params)
	if err != nil {
		return nil, err
	}
	if output.Error() != nil {
		return nil, queriessvc.ErrQueryExecutionFailed.With(output.Error())
	}
	return output, nil
}

func TestS3PreparationAndDispatchLedger(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, mode := range []string{"malformed", "serialization", "valid", "provider_failure", "completed"} {
			t.Run(mode+map[bool]string{false: "_saved", true: "_inline"}[inline], func(t *testing.T) {
				ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true, State: pipelinerun.SinkEffectStateInProgress}}
				if mode == "completed" {
					ledger.effect = pipelinerun.SinkEffect{State: pipelinerun.SinkEffectStateSucceeded}
				}
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					assert.Equal(t, 1, ledger.beginCalls, "the durable claim must precede provider dispatch")
					if mode == "provider_failure" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()
				ctx := transportsecurity.WithPlaintext(t.Context())
				connector, err := s3.New(ctx, s3.Config{Region: "test-region", Bucket: "fixtures", Endpoint: server.URL, ForcePathStyle: true, AllowPrivateHosts: true}, s3.Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"})
				require.NoError(t, err)
				defer func() { require.NoError(t, connector.Close()) }()
				operation := map[string]any{"operation": "write", "key": "out.json", "format": "json"}
				if mode == "malformed" {
					operation["key"] = 42
					operation["metadata"] = map[string]any{"private-name": 42}
				}
				if mode == "serialization" {
					operation["format"] = "csv"
					operation["delimiter"] = "||"
				}
				input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)
				input.SinkBackend = model.SinkBackendS3
				input.SinkConfig = map[string]any{"key": "out.json"}
				if inline {
					input.SinkOperationRef = ""
					input.SinkOperationInline = operation
				}
				runtime := NewSinkRuntime(s3DispatchExecutor{connector: connector, operation: operation}, ledger)
				err = runtime.Execute(ctx, input)
				switch mode {
				case "malformed", "serialization":
					require.ErrorIs(t, err, errSinkConfigurationInvalid)
					require.Zero(t, requests)
					require.Zero(t, ledger.beginCalls)
					require.Zero(t, ledger.markCalls)
					require.Equal(t, "failed", input.DeliveryOutcome)
					require.Empty(t, input.ReplayDecision)
					require.Contains(t, xerror.PublicDetail(err), "before provider dispatch")
					require.NotContains(t, xerror.PublicDetail(err), "private-")
				case "valid":
					require.NoError(t, err)
					require.Equal(t, 1, requests)
					require.Equal(t, 1, ledger.markCalls)
					require.Equal(t, "confirmed", input.DeliveryOutcome)
				case "provider_failure":
					require.Error(t, err)
					require.NotErrorIs(t, err, errSinkConfigurationInvalid)
					require.Equal(t, 1, requests)
					require.Zero(t, ledger.markCalls)
					require.Equal(t, "unknown", input.DeliveryOutcome)
					ledger.effect.Claimed = false
					err = runtime.Execute(ctx, input)
					require.ErrorIs(t, err, errSinkReplayBlocked)
					require.Equal(t, 1, requests, "unknown effects must not be redelivered")
				case "completed":
					require.NoError(t, err)
					require.Zero(t, requests)
					require.Zero(t, ledger.markCalls)
					require.Nil(t, input.SinkResult)
					require.Equal(t, "skip_ledger_succeeded", input.ReplayDecision)
					require.Equal(t, "confirmed", input.DeliveryOutcome)
				}
			})
		}
	}
}
