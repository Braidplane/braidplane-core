package backend

import (
	"io"
	"os"
	"strings"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/kongcmd"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RunCLI loads Community configuration and starts the selected public runtime role.
func RunCLI(args []string, stdout, stderr io.Writer) error {
	mode, configArgs, exited, err := parseBackendCommand(args, stdout, stderr)
	if exited || err != nil {
		return err
	}
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.toml"
	}
	var cfg *backendconfig.Config
	if mode == ModeExecutor {
		cfg, err = backendconfig.LoadExecutorWithArgs(configPath, configArgs)
	} else {
		cfg, err = backendconfig.LoadWithArgs(configPath, configArgs)
	}
	if err != nil {
		return xerror.Wrapf("failed to load config: %w", err)
	}
	return Run(cfg, mode, NewCommunityDistribution)
}

type backendCLI struct {
	API      apiCommand      `cmd:"" help:"Run the HTTP API without worker consumers."`
	Worker   workerCommand   `cmd:"" help:"Run background workers without the public HTTP API."`
	Executor executorCommand `cmd:"" help:"Run tenant connector execution requests on an isolated host."`
}
type (
	apiCommand      struct{}
	workerCommand   struct{}
	executorCommand struct{}
)

// parseBackendCommand uses Kong for role commands while preserving shared config flag precedence.
func parseBackendCommand(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) (Mode, []string, bool, error) {
	configArgs, commandArgs, err := config.ExtractFlagArgs(&backendconfig.Config{}, args)
	if err != nil {
		return ModeAPI, nil, false, xerror.Wrapf(
			"extract backend configuration arguments: %w",
			err,
		)
	}

	result, err := kongcmd.Parse(
		commandArgs,
		backendCLI{},
		"braidplane-server",
		"Run the Community backend API, workers or isolated executor.",
		stdout,
		stderr,
		func(code int) error {
			return xerror.ValidationFailed().Detailf("backend command exited with status %d", code)
		},
	)
	if result.Exited {
		return ModeAPI, configArgs, true, err
	}
	if err != nil {
		return ModeAPI, configArgs, false, xerror.ValidationFailed().Detailf(
			"parse backend command: %w",
			err,
		)
	}

	mode, err := ModeString(strings.ReplaceAll(result.Context.Command(), "-", "_"))
	if err != nil {
		return ModeAPI, configArgs, false, xerror.Unexpected().Detailf(
			"unsupported backend command %q",
			result.Context.Command(),
		)
	}

	return mode, configArgs, false, nil
}
