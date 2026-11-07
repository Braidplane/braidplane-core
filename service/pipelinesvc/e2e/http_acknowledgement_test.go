package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/stretchr/testify/require"
)

func TestHTTPAcknowledgementDurableEffectAndCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL pipeline storage")
	}
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"success", `{"status":{"code":"OK"}}`, nil},
		{"rejection", `{"status":{"code":"ERROR"}}`, xerror.HTTPAcknowledgementRejected()},
		{"missing", `{}`, xerror.HTTPAcknowledgementUncertain()},
		{"timeout", "timeout", xerror.HTTPAcknowledgementUncertain()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := `{"status":{"code":"OK"}}`
				if calls.Add(1) > 1 {
					body = tc.body
				}
				if body == "timeout" {
					<-release
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			connector, err := httpconnector.New(transportsecurity.WithPlaintext(t.Context()), httpconnector.Config{BaseURL: server.URL, AllowPrivateHosts: true, TimeoutSeconds: 1}, httpconnector.Credentials{})
			require.NoError(t, err)
			pool := openE2EPool(t)
			org := must.Slug(fmt.Sprintf("http-ack-%d", time.Now().UnixNano()))
			member := createIncrementalCheckpointServiceOrgProject(t, pool, org)
			executor := &incrementalCheckpointServiceQueryExecutor{
				httpSink:           connector,
				httpStatement:      json.RawMessage(`{"method":"POST","path":"/records","acknowledgement":{"status_codes":[200],"success":[{"path":"status.code","equals":"OK"}],"empty_fields":["errors"]}}`),
				sourceObservations: []json.RawMessage{json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`), json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)},
			}
			svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
			pipeline := incrementalCheckpointServicePipeline("http-ack")
			pipeline.Steps[1].Sink.OperationType = "http"
			createAndDeployIncrementalCheckpointPipeline(t, t.Context(), svc, store, org, member, pipeline)
			actor := model.UserEventActor(member)
			_, err = svc.Execute(t.Context(), org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("ack-first"), nil, actor, e2eRunSource(actor), nil)
			require.NoError(t, err)
			_, err = svc.Execute(t.Context(), org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("ack-second"), nil, actor, e2eRunSource(actor), nil)
			close(release)
			expectedState := "succeeded"
			expectedUpper := `{"value":"2026-05-24T11:00:00Z"}`
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
				expectedState = "in_progress"
				expectedUpper = `{"value":"2026-05-24T10:00:00Z"}`
				blocked, _ := blockedIncrementalCheckpoint(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "ack-second")
				require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z"}`, string(blocked))
			}
			upper, status := currentIncrementalCheckpoint(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug)
			require.Equal(t, "committed", status)
			require.JSONEq(t, expectedUpper, string(upper))
			var state string
			require.NoError(t, pool.QueryRow(t.Context(), `SELECT pse.state FROM pipeline_sink_effects pse JOIN project_environments pe ON pe.id=pse.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND pse.run_slug='ack-second'`, org).Scan(&state))
			require.Equal(t, expectedState, state)
			require.EqualValues(t, 2, calls.Load())
		})
	}
}
