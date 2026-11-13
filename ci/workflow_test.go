package ci_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGoModuleCacheLifecycle(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID   string         `yaml:"id"`
				If   string         `yaml:"if"`
				Name string         `yaml:"name"`
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"verify", "go-e2e", "browser"} {
		t.Run(job, func(t *testing.T) {
			steps := workflow.Jobs[job].Steps
			restore, workload, save := -1, -1, -1
			workloadNames := map[string]string{"verify": "Generate, test, lint and build without managed sources", "go-e2e": "Run every Go E2E package on disposable fixtures", "browser": "Run critical browser profile on disposable fixtures"}
			for i, step := range steps {
				switch {
				case step.ID == "go-modules":
					restore = i
					key, _ := step.With["key"].(string)
					if step.With["path"] != "~/go/pkg/mod" || !strings.Contains(key, "'go.mod', 'go.sum', 'tools/go.mod', 'tools/go.sum'") || strings.Contains(key, "github.sha") || !strings.Contains(step.If, "!inputs.cold_cache") {
						t.Fatal("module cache must be dependency-keyed and disabled for cold runs")
					}
				case step.Name == workloadNames[job]:
					workload = i
				case step.With["key"] == "${{ steps.go-modules.outputs.cache-primary-key }}":
					save = i
					if step.With["path"] != "~/go/pkg/mod" || !strings.Contains(step.If, "!inputs.cold_cache") || !strings.Contains(step.If, "!cancelled()") || !strings.Contains(step.If, "cache-hit != 'true'") {
						t.Fatal("module cache save must respect cold runs and save on failure")
					}
					if job == "go-e2e" && !strings.Contains(step.If, "matrix.shard == 0") {
						t.Fatal("only one e2e shard should save the module cache")
					}
				case job == "go-e2e" && step.With["key"] == "${{ steps.cache-1.outputs.cache-primary-key }}":
					if !strings.Contains(step.If, "matrix.shard == 0") {
						t.Fatal("only one e2e shard should save the build cache")
					}
				}
			}
			if restore < 0 || workload <= restore || save <= workload {
				t.Fatal("module cache must restore before the workload and save afterward")
			}
		})
	}
}

func TestCommunityConcurrency(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Concurrency map[string]any `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"group":              "community-${{ github.workflow }}-${{ github.event_name }}-${{ github.event.pull_request.number || github.ref }}",
		"cancel-in-progress": "${{ github.event_name == 'pull_request' }}",
	}
	if !reflect.DeepEqual(workflow.Concurrency, want) {
		t.Fatalf("PRs should cancel stale runs; running main and dispatch should finish: %v", workflow.Concurrency)
	}
}

func TestReleaseSigningCredentialLifecycle(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/core-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run, If string }
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := map[string]string{}
	for _, step := range workflow.Jobs["publish"].Steps {
		steps[step.Name] = step.Run
		if step.Name == "Remove signing credentials" && step.If != "${{ always() && steps.prepare_paths.outcome == 'success' }}" {
			t.Fatal("signing credentials must be removed after failure")
		}
	}
	t.Run("Docker and pinned attestation resolver share credentials", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		cmd := exec.Command("bash", "-e", "-c", steps["Set temporary paths"])
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "RUNNER_TEMP=" + dir, "GITHUB_ENV=" + envFile}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prepare paths: %v: %s", err, output)
		}
		contents, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatal(err)
		}
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "DOCKER_CONFIG="+filepath.Join(home, ".docker")+"\n") {
			t.Fatal("pinned actions/attest resolves credentials only from the default Docker config")
		}
		if strings.TrimSpace(steps["Remove signing credentials"]) != "docker logout ghcr.io" {
			t.Fatal("cleanup must remove GHCR credentials while preserving other registry entries")
		}
	})
	for _, allSigned := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing bundle, all signed=%t", allSigned), func(t *testing.T) {
			dir := t.TempDir()
			available := filepath.Join(dir, "available.json")
			payload := []byte(`{"synthetic":true}`)
			if err := os.WriteFile(available, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-e", "-c", steps["Retain signature bundles"])
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "RELEASE_OUTPUT=" + dir,
				fmt.Sprintf("ALL_SIGNED=%t", allSigned), "BACKEND_PROVENANCE=" + filepath.Join(dir, "missing.json"), "BACKEND_SBOM=" + available}
			output, err := cmd.CombinedOutput()
			if allSigned {
				if err == nil {
					t.Fatal("a claimed complete signature set must reject a missing file")
				}
				return
			}
			if err != nil {
				t.Fatalf("retain partial evidence: %v: %s", err, output)
			}
			retained, err := os.ReadFile(filepath.Join(dir, "signatures/BACKEND_SBOM.sigstore.json"))
			if err != nil || string(retained) != string(payload) {
				t.Fatalf("available bundle was not retained: %v", err)
			}
		})
	}
}

// This is a regression contract, not a sandbox for candidate-controlled scripts.
func TestCommunityWorkflowTrustBoundary(t *testing.T) {
	files := map[string]string{}
	for _, pattern := range []string{"../.github/workflows/*", "../.github/actions/*/action.yml"} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.ToSlash(strings.TrimPrefix(path, "../"))] = string(data)
		}
	}
	if err := communityBoundary(files); err != nil {
		t.Fatal(err)
	}

	for _, change := range []struct{ name, path, from, to string }{
		{"privileged event", ".github/workflows/ci.yml", "  pull_request:", "  pull_request_target:"},
		{"persistent runner", ".github/workflows/ci.yml", "runs-on: ubuntu-24.04", "runs-on: [self-hosted, braidplane-ci]"},
		{"job write token", ".github/workflows/ci.yml", "    timeout-minutes: 30", "    permissions: {contents: write}\n    timeout-minutes: 30"},
		{"OIDC token", ".github/workflows/ci.yml", "  contents: read", "  contents: read\n  id-token: write"},
		{"persisted checkout", ".github/workflows/ci.yml", "persist-credentials: false", "persist-credentials: true"},
		{"alternate checkout", ".github/workflows/ci.yml", "persist-credentials: false", "persist-credentials: false\n          ref: main"},
		{"deployment environment", ".github/workflows/ci.yml", "    timeout-minutes: 30", "    environment: production\n    timeout-minutes: 30"},
		{"release secret", ".github/workflows/ci.yml", "      GOWORK: 'off'", "      TOKEN: ${{ secrets.RELEASE_TOKEN }}\n      GOWORK: 'off'"},
		{"artifact upload", ".github/workflows/ci.yml", "actions/cache/save@", "actions/upload-artifact@"},
		{"release cache", ".github/workflows/ci.yml", "key: community-go-", "key: release-go-"},
		{"cached home", ".github/workflows/ci.yml", "~/go/pkg/mod", "~/"},
		{"image publication", ".github/workflows/ci.yml", "          load: true", "          push: true\n          load: true"},
		{"release build cache", ".github/workflows/ci.yml", "scope=community-", "scope=release-"},
		{"composite secret", ".github/actions/cache-fixture-images/action.yml", "        CACHE_ENABLED:", "        TOKEN: ${{ secrets.RELEASE_TOKEN }}\n        CACHE_ENABLED:"},
		{"composite release cache", ".github/actions/cache-fixture-images/action.yml", "key: community-fixture-", "key: release-fixture-"},
		{"release push trigger", ".github/workflows/core-release.yml", "  workflow_dispatch:", "  push:\n  workflow_dispatch:"},
		{"release missing environment", ".github/workflows/core-release.yml", "    environment: core-release", "    environment: unprotected"},
		{"release unguarded", ".github/workflows/core-release.yml", "vars.CORE_RELEASE_PUBLICATION_ENABLED == 'true'", "true"},
		{"preparation unguarded", ".github/workflows/core-release.yml", "vars.CORE_RELEASE_PREPARATION_ENABLED == 'true'", "true"},
		{"preparation write token", ".github/workflows/core-release.yml", "permissions:\n      contents: read", "permissions:\n      contents: write"},
		{"preparation missing CI admission", ".github/workflows/core-release.yml", "name: Admit successful main CI", "name: Skip CI admission"},
		{"preparation skipped CI admission", ".github/workflows/core-release.yml", "      - name: Admit successful main CI\n", "      - name: Admit successful main CI\n        if: false\n"},
		{"preparation ignored CI failure", ".github/workflows/core-release.yml", "      - name: Admit successful main CI\n", "      - name: Admit successful main CI\n        continue-on-error: true\n"},
		{"preparation build after failure", ".github/workflows/core-release.yml", "      - name: Prepare candidate OCI archives\n", "      - name: Prepare candidate OCI archives\n        if: always()\n"},
		{"release unconditional", ".github/workflows/core-release.yml", "github.event.repository.visibility == 'public' && vars.CORE_RELEASE_PUBLICATION_ENABLED == 'true'", "true"},
		{"release checkout credential", ".github/workflows/core-release.yml", "persist-credentials: false", "persist-credentials: true"},
		{"release wrong attestation", ".github/workflows/core-release.yml", "sboms/backend.spdx.json", "sboms/frontend.spdx.json"},
		{"release foreign artifact", ".github/workflows/core-release.yml", "name: core-release-candidate\n          path: ${{ runner.temp }}/core-release-candidate\n", "name: other-candidate\n          path: ${{ runner.temp }}/core-release-candidate\n"},
	} {
		t.Run(change.name, func(t *testing.T) {
			mutated := map[string]string{}
			for path, source := range files {
				mutated[path] = source
			}
			if !strings.Contains(mutated[change.path], change.from) {
				t.Fatalf("mutation no longer applies: %q", change.from)
			}
			mutated[change.path] = strings.Replace(mutated[change.path], change.from, change.to, 1)
			if err := communityBoundary(mutated); err == nil {
				t.Fatal("unsafe workflow change was accepted")
			}
		})
	}
	t.Run("preparation before CI admission", func(t *testing.T) {
		var release map[string]any
		if err := yaml.Unmarshal([]byte(files[".github/workflows/core-release.yml"]), &release); err != nil {
			t.Fatal(err)
		}
		steps := release["jobs"].(map[string]any)["prepare"].(map[string]any)["steps"].([]any)
		if steps[1].(map[string]any)["name"] != "Admit successful main CI" || steps[2].(map[string]any)["name"] != "Prepare candidate OCI archives" {
			t.Fatal("update the mutation for the reviewed preparation sequence")
		}
		steps[1], steps[2] = steps[2], steps[1]
		if err := releaseBoundary(release); err == nil {
			t.Fatal("candidate build before CI admission was accepted")
		}
	})
}

func communityBoundary(files map[string]string) error {
	allowedFiles := map[string]bool{
		".github/workflows/ci.yml":                         true,
		".github/workflows/core-release.yml":               true,
		".github/actions/cache-fixture-images/action.yml":  true,
		".github/actions/prepare-hosted-runner/action.yml": true,
	}
	if len(files) != len(allowedFiles) {
		return fmt.Errorf("workflow/action inventory changed; review its trust allocation")
	}
	for path, source := range files {
		if !allowedFiles[path] {
			return fmt.Errorf("unreviewed workflow/action: %s", path)
		}
		var document map[string]any
		if err := yaml.Unmarshal([]byte(source), &document); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if path == ".github/workflows/core-release.yml" {
			if err := releaseBoundary(document); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		if path == ".github/workflows/ci.yml" {
			events, ok := document["on"].(map[string]any)
			_, pullRequest := events["pull_request"]
			_, dispatch := events["workflow_dispatch"]
			if !ok || len(events) != 3 || !pullRequest || !dispatch || !reflect.DeepEqual(events["push"], map[string]any{"branches": []any{"main"}}) {
				return fmt.Errorf("community execution is limited to pull_request, main push and manual dispatch")
			}
			if !reflect.DeepEqual(document["permissions"], map[string]any{"contents": "read"}) {
				return fmt.Errorf("community events and default read-only token must remain explicit")
			}
			jobs, ok := document["jobs"].(map[string]any)
			if !ok || len(jobs) == 0 {
				return fmt.Errorf("missing community jobs")
			}
			for name, value := range jobs {
				job, ok := value.(map[string]any)
				if !ok || job["runs-on"] != "ubuntu-24.04" || job["uses"] != nil || job["environment"] != nil {
					return fmt.Errorf("%s must run directly on a disposable hosted VM without deployment environment", name)
				}
			}
		}
		if err := checkCommunityNode(document); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func releaseBoundary(document map[string]any) error {
	events, ok := document["on"].(map[string]any)
	if !ok || len(events) != 1 || events["workflow_dispatch"] == nil {
		return fmt.Errorf("release must be manually dispatched")
	}
	if !reflect.DeepEqual(document["permissions"], map[string]any{"contents": "read"}) {
		return fmt.Errorf("release default permissions must be read-only")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok || len(jobs) != 2 {
		return fmt.Errorf("release job inventory changed")
	}
	attestations := 0
	attestationInputs := map[string]map[string]any{}
	for _, component := range []string{"backend", "frontend", "migrate", "cli"} {
		commonName := fmt.Sprintf("${{ steps.images.outputs.%s_name }}", component)
		commonDigest := fmt.Sprintf("${{ steps.images.outputs.%s_digest }}", component)
		attestationInputs[component+"_provenance"] = map[string]any{
			"subject-name": commonName, "subject-digest": commonDigest, "predicate-type": "https://slsa.dev/provenance/v1",
			"predicate-path": "${{ runner.temp }}/core-release-output/provenance.json", "push-to-registry": true,
		}
		attestationInputs[component+"_sbom"] = map[string]any{
			"subject-name": commonName, "subject-digest": commonDigest,
			"sbom-path": fmt.Sprintf("${{ runner.temp }}/core-release-output/sboms/%s.spdx.json", component), "push-to-registry": true,
		}
	}
	for name, file := range map[string]string{"record": "record.json", "archive": "installation.tar.gz"} {
		attestationInputs[name+"_provenance"] = map[string]any{
			"subject-path": "${{ runner.temp }}/core-release-candidate/" + file, "predicate-type": "https://slsa.dev/provenance/v1",
			"predicate-path": "${{ runner.temp }}/core-release-output/provenance.json",
		}
	}
	for _, name := range []string{"prepare", "publish"} {
		job, ok := jobs[name].(map[string]any)
		condition := "${{ github.ref == 'refs/heads/main' && github.event.repository.visibility == 'public' && vars.CORE_RELEASE_PUBLICATION_ENABLED == 'true' }}"
		if name == "prepare" {
			condition = "${{ github.ref == 'refs/heads/main' && github.event.repository.visibility == 'public' && vars.CORE_RELEASE_PREPARATION_ENABLED == 'true' }}"
		}
		if !ok || job["uses"] != nil ||
			job["runs-on"] != "${{ inputs.platform == 'linux/arm64' && 'ubuntu-24.04-arm' || 'ubuntu-24.04' }}" ||
			job["if"] != condition {
			return fmt.Errorf("release requires explicitly enabled public-main jobs on hosted runners")
		}
		if name == "prepare" {
			if job["environment"] != nil || !reflect.DeepEqual(job["permissions"], map[string]any{"contents": "read"}) {
				return fmt.Errorf("preparation must not receive publication permissions")
			}
		} else if job["environment"] != "core-release" || !reflect.DeepEqual(job["needs"], []any{"prepare"}) ||
			!reflect.DeepEqual(job["permissions"], map[string]any{"contents": "read", "packages": "write", "id-token": "write", "attestations": "write", "artifact-metadata": "write"}) {
			return fmt.Errorf("publication must follow preparation in the named environment")
		}
		steps, ok := job["steps"].([]any)
		if !ok {
			return fmt.Errorf("release steps missing")
		}
		admission, build := -1, -1
		for index, item := range steps {
			step, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid release step")
			}
			if name == "prepare" {
				switch step["name"] {
				case "Admit successful main CI":
					admission = index
					if step["if"] != nil || step["continue-on-error"] != nil ||
						!reflect.DeepEqual(step["env"], map[string]any{"GH_TOKEN": "${{ github.token }}", "CI_RUN_ID": "${{ inputs.ci_run_id }}", "CI_ATTEMPT": "${{ inputs.ci_attempt }}"}) ||
						!strings.HasPrefix(fmt.Sprint(step["run"]), "python3 -m scripts.admit_core_ci ") {
						return fmt.Errorf("preparation must admit exact main CI using the read-only token")
					}
				case "Prepare candidate OCI archives":
					build = index
					if step["if"] != nil {
						return fmt.Errorf("preparation must stop after failed CI admission")
					}
				}
			}
			if step["name"] == "Retain signature bundles" && step["if"] != "${{ always() && steps.publish_candidate.outcome == 'success' }}" {
				return fmt.Errorf("available signature bundles must be retained after signing failure")
			}
			if uses, ok := step["uses"].(string); ok {
				switch uses {
				case "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1":
					with, _ := step["with"].(map[string]any)
					if len(with) != 1 || with["persist-credentials"] != false {
						return fmt.Errorf("release checkout must discard its credential")
					}
				case "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6":
					if name != "publish" {
						return fmt.Errorf("preparation must not sign artifacts")
					}
					id, _ := step["id"].(string)
					inputs, exists := attestationInputs[id]
					if !exists || !reflect.DeepEqual(step["with"], inputs) {
						return fmt.Errorf("unreviewed attestation subject or predicate: %s", id)
					}
					delete(attestationInputs, id)
					attestations++
				case "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9":
					if name == "publish" && step["if"] != "${{ always() && steps.prepare_paths.outcome == 'success' }}" {
						return fmt.Errorf("publication evidence must be retained after failure")
					}
				case "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333":
					if name != "publish" {
						return fmt.Errorf("preparation must not consume uploaded artifacts")
					}
					if !reflect.DeepEqual(step["with"], map[string]any{"name": "core-release-candidate", "path": "${{ runner.temp }}/core-release-candidate"}) {
						return fmt.Errorf("publication must download the candidate from this run")
					}
				default:
					return fmt.Errorf("unreviewed release action: %s", uses)
				}
			}
		}
		if name == "prepare" && (admission < 0 || build <= admission) {
			return fmt.Errorf("preparation must admit main CI before building")
		}
	}
	if attestations != 10 || len(attestationInputs) != 0 {
		return fmt.Errorf("release attestation count changed")
	}
	return nil
}

func checkCommunityNode(value any) error {
	switch node := value.(type) {
	case map[string]any:
		if permissions, exists := node["permissions"]; exists {
			p, ok := permissions.(map[string]any)
			if !ok || (len(p) != 0 && !reflect.DeepEqual(p, map[string]any{"contents": "read"})) {
				return fmt.Errorf("only contents-read or empty permissions are allowed")
			}
		}
		if uses, ok := node["uses"].(string); ok {
			action := strings.Split(uses, "@")[0]
			allowed := map[string]bool{
				"actions/checkout": true, "actions/cache/restore": true, "actions/cache/save": true,
				"docker/setup-buildx-action": true, "docker/build-push-action": true,
				"./.github/actions/cache-fixture-images": true, "./.github/actions/prepare-hosted-runner": true,
			}
			if !allowed[action] || (!strings.HasPrefix(action, "./") && !regexp.MustCompile(`@[0-9a-f]{40}$`).MatchString(uses)) {
				return fmt.Errorf("unreviewed or unpinned action: %s", uses)
			}
			inputs, _ := node["with"].(map[string]any)
			if action == "actions/checkout" && (inputs["persist-credentials"] != false || inputs["ref"] != nil || inputs["repository"] != nil || inputs["token"] != nil) {
				return fmt.Errorf("checkout must use the candidate and discard its credential")
			}
			if action == "docker/build-push-action" && (inputs["load"] != true || inputs["push"] != nil) {
				return fmt.Errorf("community images must only be loaded locally")
			}
			if action == "docker/build-push-action" {
				for _, key := range []string{"cache-from", "cache-to"} {
					cache, _ := inputs[key].(string)
					if !strings.Contains(cache, "scope=community-") {
						return fmt.Errorf("Buildx caches must stay in the community namespace")
					}
				}
			}
			if strings.HasPrefix(action, "actions/cache/") {
				allowedPaths := map[string]bool{
					"~/go/pkg/mod": true, "~/.cache/braidplane/go": true, "${{ runner.temp }}/bin": true,
					"~/.cache/braidplane/node": true, "~/.cache/braidplane/npm": true,
					"~/.cache/braidplane/golangci-lint": true, "~/.cache/ms-playwright": true,
					"~/.cache/pip": true, "${{ runner.temp }}/community-fixture-images.tar": true,
				}
				paths, _ := inputs["path"].(string)
				for _, path := range strings.Split(strings.TrimSpace(paths), "\n") {
					if !allowedPaths[strings.TrimSpace(path)] {
						return fmt.Errorf("unreviewed cache path: %s", path)
					}
				}
				for _, key := range []string{"key", "restore-keys"} {
					text, _ := inputs[key].(string)
					for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
						if line != "" && !strings.HasPrefix(line, "community-") && !regexp.MustCompile(`^\$\{\{ steps\.[a-z0-9-]+\.outputs\.cache-primary-key \}\}$`).MatchString(line) {
							return fmt.Errorf("cache must stay in the community namespace: %s", line)
						}
					}
				}
			}
		}
		for key, child := range node {
			if key == "secrets" {
				return fmt.Errorf("community jobs must not receive secrets")
			}
			if err := checkCommunityNode(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := checkCommunityNode(child); err != nil {
				return err
			}
		}
	case string:
		if regexp.MustCompile(`\bsecrets\s*[.\[]`).MatchString(node) {
			return fmt.Errorf("community workflows/actions must not reference secrets")
		}
	}
	return nil
}
