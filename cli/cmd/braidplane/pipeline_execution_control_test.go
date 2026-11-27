package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/cli/apiclient"
)

func TestPipelineExecutionCommands(t *testing.T) {
	for _, command := range []string{"status", "pause", "resume"} {
		t.Run(command, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/organizations/acme/projects/ops/environments/dev/pipelines/orders/execution-control", r.URL.Path)
				if command == "status" {
					assert.Equal(t, http.MethodGet, r.Method)
				} else {
					assert.Equal(t, http.MethodPut, r.Method)
					var body api.PipelineExecutionControlUpdate
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.EqualValues(t, 9, body.ExpectedRevision)
					assert.Equal(t, command == "pause", body.Paused)
				}
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineExecutionControl{Revision: 10, QueuedCount: 2}))
			}))
			defer backend.Close()
			config := filepath.Join(t.TempDir(), "cli.toml")
			require.NoError(t, saveLogin(config, cliAuthConfig{ServerURL: backend.URL + "/api", Organization: "acme", APIKey: "fixture-key"}))
			args := []string{"braidplane", "pipeline", "execution", command, "orders", "--project", "ops", "--environment", "dev"}
			if command != "status" {
				args = append(args, "--expected-revision", "9")
			}
			var stdout, stderr bytes.Buffer
			require.NoError(t, runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: config}))
			require.Contains(t, stdout.String(), `"queued_count": 2`)
		})
	}
}
