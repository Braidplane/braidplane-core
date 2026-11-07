package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/kongcmd"
	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEveryOutputFlagAdvertisesAllFormats(t *testing.T) {
	parser, err := kongcmd.New(&braidplaneCLI{}, "braidplane", "", io.Discard, io.Discard)
	require.NoError(t, err)
	count := 0
	var visit func(*kong.Node)
	visit = func(node *kong.Node) {
		for _, flag := range node.Flags {
			if flag.Name != "output" && flag.Short != 'o' {
				continue
			}
			count++
			require.Equal(t, "output", flag.Name, "command %s", node.Name)
			require.Equal(t, 'o', flag.Short, "command %s", node.Name)
			for _, format := range []string{"text", "json", "yaml"} {
				require.True(t, flag.EnumMap()[format], "command %s lacks %s", node.Name, format)
			}
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(parser.Model.Node)
	require.Positive(t, count)
}

func TestCLIOutputProcess(t *testing.T) {
	if os.Getenv("BRAIDPLANE_TEST_CLI_PROCESS") == "1" {
		separator := slices.Index(os.Args, "--")
		if separator < 0 {
			os.Exit(2)
		}
		os.Args = append([]string{"braidplane"}, os.Args[separator+1:]...)
		main()
		os.Exit(0)
	}

	executable, err := os.Executable()
	require.NoError(t, err)
	spec, err := filepath.Abs(filepath.Join("..", "..", "examples", "multi-source-roots.example.yaml"))
	require.NoError(t, err)
	run := func(args ...string) (string, string, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), executable, append([]string{"-test.run=^TestCLIOutputProcess$", "--"}, args...)...)
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "BRAIDPLANE_") {
				command.Env = append(command.Env, env)
			}
		}
		command.Env = append(command.Env, "BRAIDPLANE_TEST_CLI_PROCESS=1")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		return stdout.String(), stderr.String(), err
	}

	for _, format := range []string{"json", "yaml"} {
		t.Run("local_"+format, func(t *testing.T) {
			stdout, stderr, err := run("pipeline", "validate", "--local", "--file", spec, "-o", format)
			require.NoError(t, err)
			require.Empty(t, stderr)
			var result map[string]any
			if format == "json" {
				require.NoError(t, json.Unmarshal([]byte(stdout), &result))
			} else {
				require.NoError(t, yaml.Unmarshal([]byte(stdout), &result))
			}
			require.Equal(t, true, result["valid"])
			require.Equal(t, "local", result["scope"])
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":false,"diagnostics":[{"severity":"error","field":"steps","code":"BAD_BINDING","message":"binding missing"}]}`))
	}))
	defer server.Close()
	stdout, stderr, err := run("pipeline", "validate", "--file", spec, "--server-url", server.URL+"/api", "--org", "acme", "--api-key", "fixture-key", "--project", "etl", "--environment", "dev", "-o", "json")
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.NotZero(t, exitErr.ExitCode())
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	require.Equal(t, false, result["valid"])
	require.Contains(t, stdout, "BAD_BINDING")
	require.Contains(t, stderr, "pipeline validation failed")

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION_FAILED","message":"Request validation failed","request_id":"credential-validation","validation_errors":[{"field":"name","code":"VALUE_TOO_LONG","message":"submitted-secret","details":{"provided_value":"submitted-secret"}}]}}`))
	}))
	defer errorServer.Close()
	stdout, stderr, err = run("organization", "get", "--server-url", errorServer.URL+"/api", "--org", "acme", "--api-key", "fixture-key")
	require.ErrorAs(t, err, &exitErr)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "VALIDATION_FAILED")
	require.Contains(t, stderr, `request_id="credential-validation"`)
	require.Contains(t, stderr, `field="name" code="VALUE_TOO_LONG"`)
	require.NotContains(t, stderr, "fixture-key")
	require.NotContains(t, stderr, "submitted-secret")
}

func TestYAMLUsesJSONFieldsAndPreservesValues(t *testing.T) {
	value := struct {
		Identifier string `json:"identifier"`
		Count      int64  `json:"count"`
		Omitted    string `json:"omitted,omitempty"`
	}{Identifier: "001", Count: 9007199254740993}
	var out bytes.Buffer
	require.NoError(t, writeCLIYAML(&out, value))
	var result map[string]any
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "001", result["identifier"])
	require.EqualValues(t, value.Count, result["count"])
	require.NotContains(t, result, "omitted")
}

func TestYAMLPreservesAmbiguousStrings(t *testing.T) {
	values := map[string]any{
		"boolean": "true", "null": "null", "timestamp": "2026-11-04",
		"numeric": "9007199254740993", "multiline": "one\ntwo", "<<": "value",
		"nested": []any{"yes", "001", nil, true},
	}
	var out bytes.Buffer
	require.NoError(t, writeCLIYAML(&out, values))
	var result map[string]any
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &result))
	require.Equal(t, values, result)
}

func TestYAMLPreservesLargeJSONNumberTypes(t *testing.T) {
	value := json.RawMessage(`{"large":18446744073709551616,"negative":-9223372036854775809,"exponent":1e400,"string":"1e400"}`)
	var out bytes.Buffer
	require.NoError(t, writeCLIYAML(&out, value))
	require.Contains(t, out.String(), `string: "1e400"`)
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &root))
	fields := root.Content[0].Content
	got := make(map[string]*yaml.Node)
	for i := 0; i < len(fields); i += 2 {
		got[fields[i].Value] = fields[i+1]
	}
	for key, want := range map[string]struct{ tag, value string }{
		"large":    {"!!int", "18446744073709551616"},
		"negative": {"!!int", "-9223372036854775809"},
		"exponent": {"!!float", "1e400"},
		"string":   {"!!str", "1e400"},
	} {
		require.Contains(t, got, key)
		require.Equal(t, want.tag, got[key].Tag, "%s", key)
		require.Equal(t, want.value, got[key].Value, "%s", key)
	}
}

func TestValidationOutputPreservesRawImpactValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/organizations/acme/projects/etl/environments/dev/pipeline-validation" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true,"diagnostics":[],"impact_report":{"severity":"warning","summary":"fixture","impacts":[],"changes":[{"kind":"field","path":"limit","before":18446744073709551616,"after":"1e400","severity":"warning","reason_code":"CHANGED"}]}}`))
	}))
	defer server.Close()
	t.Setenv("BRAIDPLANE_SERVER_URL", server.URL+"/api")
	t.Setenv("BRAIDPLANE_ORG", "acme")
	t.Setenv("BRAIDPLANE_API_KEY", "fixture-key")
	t.Setenv("BRAIDPLANE_PAT", "")
	t.Setenv("BRAIDPLANE_PROJECT", "etl")
	t.Setenv("BRAIDPLANE_ENVIRONMENT", "dev")
	spec := filepath.Join("..", "..", "examples", "multi-source-roots.example.yaml")
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runWithRuntime([]string{"braidplane", "pipeline", "validate", "--file", spec, "-o", format}, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "missing")})
			require.NoError(t, err)
			require.Empty(t, stderr.String())
			if format == "json" {
				decoder := json.NewDecoder(&stdout)
				decoder.UseNumber()
				var result map[string]any
				require.NoError(t, decoder.Decode(&result))
				change := result["impact_report"].(map[string]any)["changes"].([]any)[0].(map[string]any)
				require.Equal(t, json.Number("18446744073709551616"), change["before"])
				require.Equal(t, "1e400", change["after"])
				return
			}
			var root yaml.Node
			require.NoError(t, yaml.Unmarshal(stdout.Bytes(), &root))
			lookup := func(node *yaml.Node, key string) *yaml.Node {
				t.Helper()
				for i := 0; i < len(node.Content); i += 2 {
					if node.Content[i].Value == key {
						return node.Content[i+1]
					}
				}
				t.Fatalf("missing YAML field %q", key)
				return nil
			}
			change := lookup(lookup(root.Content[0], "impact_report"), "changes").Content[0]
			before, after := lookup(change, "before"), lookup(change, "after")
			require.Equal(t, "!!int", before.Tag)
			require.Equal(t, "18446744073709551616", before.Value)
			require.Equal(t, "!!str", after.Tag)
			require.Equal(t, "1e400", after.Value)
		})
	}
}

func TestOutputRejectsUnsupportedFormat(t *testing.T) {
	var out bytes.Buffer
	require.Error(t, writeCLIOutput(&out, localPipelineValidationOutput{count: 1}, "xml"))
	require.Empty(t, out.String())
}

func TestStreamingOutputReturnsWriteErrors(t *testing.T) {
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			err := writeCLIOutputMode(failingWriter{}, localPipelineValidationOutput{count: 1}, format, true)
			require.ErrorContains(t, err, "write failed")
		})
	}
}

func TestInvalidOutputFormatDoesNotCallAPI(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("BRAIDPLANE_SERVER_URL", server.URL+"/api")
	t.Setenv("BRAIDPLANE_ORG", "acme")
	t.Setenv("BRAIDPLANE_API_KEY", "fixture-key")
	t.Setenv("BRAIDPLANE_PAT", "")
	t.Setenv("BRAIDPLANE_PROJECT", "etl")
	t.Setenv("BRAIDPLANE_ENVIRONMENT", "dev")
	for _, command := range [][]string{
		{"pipeline", "run", "start", "orders"},
		{"pipeline", "run", "recover", "original"},
		{"data-source", "test", "http", "--base-url", "https://example.test"},
	} {
		for _, flag := range []string{"-o", "--output"} {
			args := append([]string{"braidplane"}, command...)
			args = append(args, flag, "xml")
			var stdout, stderr bytes.Buffer
			err := runWithRuntime(args, &stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "missing")})
			require.Error(t, err, "%v", args)
			require.Contains(t, stderr.String(), "output", "%v", args)
			require.Empty(t, stdout.String())
		}
	}
	require.Zero(t, requests.Load())
}
