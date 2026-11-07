package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/cli/apiclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestProgressJSONStreamUsesOneLinePerPage(t *testing.T) {
	var out bytes.Buffer
	for _, slug := range []string{"first", "second"} {
		require.NoError(t, writePipelineRunProgress(&out, &api.PipelineRunProgress{Metadata: api.PipelineRunMetadata{RunSlug: slug}}, "json"))
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	for i, slug := range []string{"first", "second"} {
		var progress api.PipelineRunProgress
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &progress))
		require.Equal(t, slug, progress.Metadata.RunSlug)
	}
}

func TestProgressYAMLStream(t *testing.T) {
	var out bytes.Buffer
	for _, slug := range []string{"first", "second"} {
		require.NoError(t, writePipelineRunProgress(&out, &api.PipelineRunProgress{Metadata: api.PipelineRunMetadata{RunSlug: slug}}, "yaml"))
	}
	decoder := yaml.NewDecoder(&out)
	for _, slug := range []string{"first", "second"} {
		var document map[string]any
		require.NoError(t, decoder.Decode(&document))
		require.Equal(t, slug, document["metadata"].(map[string]any)["run_slug"])
	}
	var extra any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF)
}

func TestRunCommandsOutputFormats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{RunSlug: "attempt", Status: "succeeded"}}))
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineRunProgress{Metadata: api.PipelineRunMetadata{RunSlug: "attempt", Status: "succeeded"}, Events: []api.PipelineRunTimelineEvent{}}))
	}))
	defer server.Close()
	t.Setenv("BRAIDPLANE_SERVER_URL", server.URL+"/api")
	t.Setenv("BRAIDPLANE_ORG", "acme")
	t.Setenv("BRAIDPLANE_API_KEY", "fixture-key")
	t.Setenv("BRAIDPLANE_PAT", "")
	t.Setenv("BRAIDPLANE_PROJECT", "etl")
	t.Setenv("BRAIDPLANE_ENVIRONMENT", "dev")
	for _, command := range [][]string{{"start", "orders"}, {"progress", "attempt"}, {"watch", "attempt"}} {
		t.Run(command[0], func(t *testing.T) {
			var reference map[string]any
			for _, flag := range []string{"-o", "--output"} {
				for _, format := range []string{"json", "yaml", "text"} {
					t.Run(flag+"/"+format, func(t *testing.T) {
						var stdout, stderr bytes.Buffer
						args := append([]string{"braidplane", "pipeline", "run"}, command...)
						args = append(args, flag, format)
						err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "config")})
						require.NoError(t, err)
						require.Empty(t, stderr.String())
						if format == "text" {
							require.Contains(t, stdout.String(), "attempt")
							return
						}
						var result map[string]any
						if format == "json" {
							require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
						} else {
							var yamlResult map[string]any
							require.NoError(t, yaml.Unmarshal(stdout.Bytes(), &yamlResult))
							data, err := json.Marshal(yamlResult)
							require.NoError(t, err)
							require.NoError(t, json.Unmarshal(data, &result))
						}
						require.Equal(t, "attempt", result["metadata"].(map[string]any)["run_slug"])
						if reference == nil {
							reference = result
						} else {
							require.Equal(t, reference, result)
						}
					})
				}
			}
		})
	}
}

func TestWatchProgressStreamsBoundariesAndReconnectsWithoutRepeatingEvents(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/organizations/acme/projects/ops/environments/staging/runs/attempt-2/progress", r.URL.Path)
		assert.Equal(t, "Bearer synthetic-api-key", r.Header.Get("Authorization"))
		after := r.URL.Query().Get("after_event")
		if requests == 1 {
			assert.Equal(t, "0", after)
		} else {
			assert.Equal(t, "1", after)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"UNAVAILABLE","message":"unavailable"}}`))
			return
		}
		event := api.PipelineRunTimelineEvent{At: time.Now(), Type: "step_started", Status: "running"}
		progress := api.PipelineRunProgress{Metadata: api.PipelineRunMetadata{RunSlug: "attempt-2", PipelineVersionSpecHash: "recorded-hash", Status: "running"}, Revision: 2, Events: []api.PipelineRunTimelineEvent{event}, EventCount: 1, NextAfterEvent: 1, WorkerLiveness: "recent"}
		if requests == 3 {
			progress.Metadata.Status = "failed"
			progress.Revision = 3
			progress.AfterEvent = 1
			progress.NextAfterEvent = 2
			progress.EventCount = 2
			progress.Events[0].Type = "step_failed"
			progress.Events[0].Status = "failed"
			progress.WorkerLiveness = "terminal"
		}
		assert.NoError(t, json.NewEncoder(w).Encode(progress))
	}))
	defer server.Close()
	client, err := newCLIAPIClient(cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: "synthetic-api-key"})
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	command := watchPipelineRunCommand{RunSlug: "attempt-2", cliOutputOptions: cliOutputOptions{Output: "json"}}
	err = command.watchProgress(t.Context(), &cliOutput{stdout: &stdout, stderr: &stderr}, client, "ops", "staging", time.Millisecond)
	require.ErrorContains(t, err, "attempt-2 failed")
	require.Equal(t, 3, requests)
	require.Contains(t, stderr.String(), "reconnecting")
	require.Contains(t, stderr.String(), "reconnected")
	decoder := json.NewDecoder(&stdout)
	for i, status := range []api.PipelineRunMetadataStatus{"running", "failed"} {
		var progress api.PipelineRunProgress
		require.NoError(t, decoder.Decode(&progress))
		require.Equal(t, status, progress.Metadata.Status)
		require.Equal(t, "recorded-hash", progress.Metadata.PipelineVersionSpecHash)
		require.EqualValues(t, i, progress.AfterEvent)
		require.Len(t, progress.Events, 1)
	}
	var extra any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF)
}

func TestWatchYAMLStreamsEachProgressPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, map[int]string{1: "0", 2: "1"}[requests], r.URL.Query().Get("after_event"))
		w.Header().Set("Content-Type", "application/json")
		progress := api.PipelineRunProgress{
			Metadata: api.PipelineRunMetadata{RunSlug: "attempt", Status: "running"},
			Revision: int64(requests), AfterEvent: int64(requests - 1), NextAfterEvent: int64(requests), EventCount: int64(requests),
			Events: []api.PipelineRunTimelineEvent{{At: time.Now(), Type: "step_started", Status: "running"}},
		}
		if requests == 2 {
			progress.Metadata.Status = "failed"
			progress.Events[0].Type = "step_failed"
			progress.Events[0].Status = "failed"
		}
		assert.NoError(t, json.NewEncoder(w).Encode(progress))
	}))
	defer server.Close()
	client, err := newCLIAPIClient(cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: "synthetic-api-key"})
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	command := watchPipelineRunCommand{RunSlug: "attempt", cliOutputOptions: cliOutputOptions{Output: "yaml"}}
	err = command.watchProgress(t.Context(), &cliOutput{stdout: &stdout, stderr: &stderr}, client, "ops", "dev", time.Millisecond)
	require.ErrorContains(t, err, "attempt failed")
	require.Equal(t, 2, requests)
	require.Empty(t, stderr.String())
	decoder := yaml.NewDecoder(&stdout)
	for _, status := range []string{"running", "failed"} {
		var page map[string]any
		require.NoError(t, decoder.Decode(&page))
		require.Equal(t, status, page["metadata"].(map[string]any)["status"])
	}
	var extra any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF)
}

func TestWatchProgressDenialFailsWithoutRetryAndInterruptionDoesNotCancelRun(t *testing.T) {
	for _, denied := range []bool{true, false} {
		t.Run(map[bool]string{true: "denied", false: "interrupted"}[denied], func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				if denied {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"forbidden"}}`))
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineRunProgress{Metadata: api.PipelineRunMetadata{RunSlug: "quiet", Status: "running"}, Events: []api.PipelineRunTimelineEvent{}, WorkerLiveness: "stale"}))
			}))
			defer server.Close()
			client, err := newCLIAPIClient(cliAuthConfig{ServerURL: server.URL, Organization: "acme", APIKey: "synthetic-api-key"})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			var stdout, stderr bytes.Buffer
			command := watchPipelineRunCommand{RunSlug: "quiet", cliOutputOptions: cliOutputOptions{Output: "json"}}
			err = command.watchProgress(ctx, &cliOutput{stdout: &stdout, stderr: &stderr}, client, "ops", "staging", time.Second)
			require.Error(t, err)
			require.Equal(t, 1, requests)
			require.Empty(t, stderr.String())
			if denied {
				require.Contains(t, err.Error(), "403")
				require.Empty(t, stdout.String())
			} else {
				require.Contains(t, err.Error(), "execution was not canceled")
				require.Contains(t, stdout.String(), `"worker_liveness":"stale"`)
			}
		})
	}
}

func TestWatchProgressRejectsOverflowingDurations(t *testing.T) {
	command := watchPipelineRunCommand{PollIntervalSec: int(^uint(0) >> 1), cliOutputOptions: cliOutputOptions{Output: "json"}}
	require.ErrorContains(t, command.Run(&cliOutput{}, &cliRuntime{}), "exceeds supported duration")
	command.PollIntervalSec = 2
	command.TimeoutSec = int(^uint(0) >> 1)
	require.ErrorContains(t, command.Run(&cliOutput{}, &cliRuntime{}), "exceeds supported duration")
}
