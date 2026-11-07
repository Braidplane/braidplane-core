package logging

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGowrapSlogTemplateGeneratesMethodCompletionDecorator(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	absRepoRoot, err := filepath.Abs(repoRoot)
	require.NoError(t, err)
	outputDir := filepath.Join(t.TempDir(), "projectssvc")
	require.NoError(t, os.MkdirAll(outputDir, 0o750))
	outputPath := filepath.Join(outputDir, "logging_decorator.go")
	cmd := exec.CommandContext(
		t.Context(),
		"go",
		"tool",
		"-modfile="+filepath.Join(absRepoRoot, "tools/go.mod"),
		"gowrap",
		"gen",
		"-p", "./service/projectssvc",
		"-i", "Service",
		"-t", filepath.Join(absRepoRoot, "pkg/logging/gowrap/slog"),
		"-o", outputPath,
		"-g",
		"-l", "github.com/Braidplane/braidplane-core",
		"-v", "DecoratorName=ServiceWithLogging",
		"-v", "Layer=service",
		"-v", "Package=service/projectssvc",
	)
	cmd.Dir = absRepoRoot

	rawOutput, err := cmd.CombinedOutput()
	require.NoError(t, err, string(rawOutput))

	rawGenerated, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	generated := string(rawGenerated)

	require.Contains(t, generated, "type ServiceWithLogging struct")
	require.Contains(t, generated, "*slog.Logger")
	require.Contains(t, generated, "tracing.StartMethodSpan")
	require.Contains(t, generated, "defer _span.End()")
	require.Contains(t, generated, "logging.LogMethodCompletion")
	require.Contains(t, generated, `Layer:`)
	require.Contains(t, generated, `"service"`)
	require.Contains(t, generated, `Package:`)
	require.Contains(t, generated, `"service/projectssvc"`)
	require.Contains(t, generated, `Interface:`)
	require.Contains(t, generated, `"Service"`)
	require.Contains(t, generated, `Method:`)
	require.Contains(t, generated, `"CreateProject"`)
	require.Contains(t, generated, `_d._base.CreateProject(_ctx,`)
	require.NotContains(t, generated, "ParamsNames")
	require.NotContains(t, generated, "ResultsNames")
	require.NotContains(t, generated, "slog.Any")
	require.NotContains(t, generated, "_params")
	require.NotContains(t, generated, "_results")
}
