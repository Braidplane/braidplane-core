package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineRunRecover(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"json", "yaml", "text"} {
		for _, status := range []string{"succeeded", "failed"} {
			t.Run(format+"/"+status, func(t *testing.T) {
				t.Parallel()
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, "/api/organizations/acme/projects/etl/environments/prod/runs/original:recover", r.URL.Path)
					assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
					var body map[string]any
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, map[string]any{"protected_environment_confirmation": "prod"}, body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"metadata":{"run_slug":"recovery","pipeline_slug":"orders","pipeline_version_spec_hash":"pinned-version","status":"` + status + `","replay_of_run_slug":"original","replay_from_step":"write"},"timeline":{"snapshots":[{"step":"write","input":{"sink_replay_decision":"skip_ledger_succeeded","sink_delivery_outcome":"confirmed"}}],"failure":{"step":"write","kind":"data_source","side":"sink","error":"Safe sink failure","action_hint":"Inspect sink acknowledgement"}}}`))
				}))
				t.Cleanup(server.Close)
				config := filepath.Join(t.TempDir(), "braidplane.toml")
				require.NoError(t, saveLogin(config, cliAuthConfig{ServerURL: server.URL + "/api", Organization: "acme", APIKey: "test-key", Environment: "prod"}))
				var out, stderr strings.Builder
				err := runWithRuntime([]string{"braidplane", "pipeline", "run", "recover", "original", "--project", "etl", "--confirm-environment", "prod", "--output", format}, &out, &stderr, cliRuntime{configPath: config})
				if status == "failed" {
					require.ErrorContains(t, err, "recovery run recovery for original ended with status failed")
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, 1, calls, "recovery must not issue source reads or start a substitute run")
				for _, value := range []string{"recovery", "original", "pinned-version", status, "skip_ledger_succeeded", "confirmed", "Safe sink failure", "Inspect sink acknowledgement"} {
					require.Contains(t, out.String(), value)
				}
				require.NotContains(t, out.String(), "test-key")
				if format == "json" {
					require.True(t, json.Valid([]byte(out.String())))
				}
			})
		}
	}
}

func TestPipelineRunRecoverRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		status   int
		response string
		want     string
	}{
		{"source or transform failure", 400, `{"error":{"code":"PIPELINE_RUN_INVALID_INPUT","message":"recovery requires a terminal sink failure with retained source and transform outputs"}}`, "terminal sink failure"},
		{"missing checkpoint", 404, `{"error":{"code":"PIPELINE_REPLAY_STEP_NOT_FOUND","message":"replay step not found in checkpoint"}}`, "PIPELINE_REPLAY_STEP_NOT_FOUND"},
		{"unsafe replay", 400, `{"error":{"code":"PIPELINE_REPLAY_UNSAFE","message":"pipeline replay is unsafe"}}`, "PIPELINE_REPLAY_UNSAFE"},
		{"unknown ledger", 400, `{"error":{"code":"PIPELINE_REPLAY_UNSAFE","message":"sink effect ledger state is unknown"}}`, "ledger state is unknown"},
		{"confirmation required", 400, `{"error":{"code":"PROTECTED_ENVIRONMENT_CONFIRMATION_REQUIRED","message":"confirm the exact environment slug"}}`, "PROTECTED_ENVIRONMENT_CONFIRMATION_REQUIRED"},
		{"forbidden", 403, `{"error":{"code":"FORBIDDEN","message":"execute permission required"}}`, "FORBIDDEN"},
		{"non-json", 502, `private upstream response`, "status 502"},
		{"missing payload", 200, `null`, "missing recovery run payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.NotContains(t, body, "protected_environment_confirmation")
				if tc.response != "private upstream response" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			t.Cleanup(server.Close)
			config := filepath.Join(t.TempDir(), "braidplane.toml")
			require.NoError(t, saveLogin(config, cliAuthConfig{ServerURL: server.URL + "/api", Organization: "acme", APIKey: "test-key", Environment: "dev"}))
			var out, stderr strings.Builder
			err := runWithRuntime([]string{"braidplane", "pipeline", "run", "recover", "original", "--project", "etl"}, &out, &stderr, cliRuntime{configPath: config})
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "private upstream response")
			require.Empty(t, out.String())
			require.Equal(t, 1, calls)
		})
	}
}

func TestPipelineRunRecoverRejectsInvalidArgumentsBeforeRequest(t *testing.T) {
	for _, extra := range [][]string{{"--output", "xml"}, {"--input-json", "{}"}, {"--run-slug", "new-run"}} {
		var out, stderr strings.Builder
		err := runWithRuntime(append([]string{"braidplane", "pipeline", "run", "recover", "original", "--project", "etl"}, extra...), &out, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "missing.toml")})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "missing CLI auth")
	}
}
