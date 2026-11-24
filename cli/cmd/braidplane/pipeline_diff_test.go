package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/repostore"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineDiffSelectsDeployedOrUpstream(t *testing.T) {
	const candidate = `{
		"metadata":{"slug":"sync","name":"Sync"},
		"steps":[{"slug":"fetch-users","kind":"query","query":{
			"data_source_ref":"db","operation_type":"sql","operation_ref":"list-users",
			"output_contract":{"name":"users","representation":"rows","fields":[
				{"name":"email_address","type":"string","required":true}
			]}
		}}]
	}`
	dir := t.TempDir()
	t.Chdir(dir)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", string(out))
		return strings.TrimSpace(string(out))
	}
	spec := filepath.Join(dir, "candidate.json")
	require.NoError(t, os.WriteFile(spec, []byte(candidate), 0o600))

	parsed, err := pipelinevalidate.LoadFile(spec)
	require.NoError(t, err)
	expectedJSON, err := json.Marshal(parsed)
	require.NoError(t, err)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		const base = "/organizations/acme/projects/data"
		switch r.URL.Path {
		case base + "/pipelines/sync":
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = fmt.Fprintf(w, `{"slug":"sync","spec":%s}`, candidate)
		case base + "/environments/dev/pipelines/sync/deployment:compare":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer xto_test_token", r.Header.Get("Authorization"))
			raw, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.JSONEq(t, string(expectedJSON), string(raw))
			_, _ = fmt.Fprint(w, `{
				"organization_slug":"acme","project_slug":"data","environment_slug":"dev","pipeline_slug":"sync",
				"deployment_revision":3,"deployed_at":"2026-11-19T12:00:00Z",
				"baseline_spec_hash":"deployed-hash","candidate_spec_hash":"candidate-hash","severity":"breaking",
				"findings":[{"severity":"breaking","message":"step[fetch-users].query.output_contract.email removed"}]
			}`)
		case base + "/environments/missing/pipelines/sync/deployment:compare":
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"code":"PIPELINE_DEPLOYMENT_NOT_FOUND","error":"pipeline deployment not found"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	args := []string{
		"braidplane", "pipeline", "diff", "sync", "--server-url", server.URL,
		"--org", "acme", "--api-key", "xto_test_token", "--project", "data",
	}
	run := func(flags ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := runWithRuntime(append(args, flags...), &stdout, &stderr, cliRuntime{configPath: filepath.Join(dir, "config.toml")})
		return stdout.String(), err
	}

	out, err := run("--file", spec, "--baseline", "upstream")
	require.NoError(t, err)
	require.Contains(t, out, "baseline: upstream")
	require.Contains(t, out, "no changes detected")

	out, err = run("--file", spec, "--baseline", "deployed", "--environment", "dev")
	require.ErrorContains(t, err, "breaking pipeline changes detected")
	require.Contains(t, out, "baseline: deployed (acme/data/dev/sync, revision 3)")
	require.Contains(t, out, "baseline spec hash: deployed-hash")
	require.Contains(t, out, "candidate spec hash: candidate-hash")
	require.Contains(t, out, "severity: breaking")
	require.Contains(t, out, "output_contract.email removed")

	out, err = run("--file", spec, "--baseline", "deployed", "--environment", "missing")
	require.Error(t, err)
	require.Empty(t, out)
	_, err = run("--file", spec, "--baseline", "deployed")
	require.ErrorContains(t, err, "requires a valid --environment")
	_, err = run("--file", spec, "--baseline", "upstream", "--environment", "dev")
	require.ErrorContains(t, err, "requires --baseline deployed")
	git("init")
	git("add", "candidate.json")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null", "commit", "-m", "Candidate pipeline")
	head := git("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(spec, []byte("invalid working-tree contents"), 0o600))
	out, err = run("--git-commit", head, "--git-path", "candidate.json", "--baseline", "deployed", "--environment", "dev")
	require.ErrorContains(t, err, "breaking pipeline changes detected")
	require.Contains(t, out, "candidate Git commit: "+head)
	require.Contains(t, out, `candidate Git path: "candidate.json"`)
	_, err = run("--git-commit", "HEAD", "--git-path", "candidate.json")
	require.ErrorContains(t, err, "full commit ID")
	_, err = run("--git-commit", head, "--git-path", "missing.json")
	require.ErrorContains(t, err, "regular file")
	_, err = run("--file", spec, "--git-commit", head, "--git-path", "candidate.json")
	require.ErrorContains(t, err, "use --file or both --git-commit and --git-path")
	require.EqualValues(t, 4, requests.Load())
}
