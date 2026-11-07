package backend

import (
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

//go:generate -command enumer go tool -modfile=../tools/go.mod enumer
//go:generate enumer -type=Mode -trimprefix=Mode -transform=snake -output=mode_enumer.go

// Mode selects the process components; private commands use the required distribution extension.
type Mode uint8

const (
	ModeAPI Mode = iota
	ModeWorker
	ModeExecutor
	ModeServiceCommand
)

type backendRuntimeCapabilities struct {
	apiHTTP      bool
	riverWorkers bool
}

type executionBackendComposition struct {
	dispatchesRiverJobs bool
	processesRiverJobs  bool
}

func executionBackendCompositionFor(
	mode Mode,
	backend string,
) executionBackendComposition {
	if backend != backendconfig.ExecutionBackendRiver {
		return executionBackendComposition{}
	}
	return executionBackendComposition{
		dispatchesRiverJobs: mode == ModeAPI || mode == ModeWorker || mode == ModeServiceCommand,
		processesRiverJobs:  mode == ModeWorker,
	}
}

type backendRuntimeLifecycle struct {
	startRiverWorkers func() (func() error, error)
	runHTTP           func() error
	runWorkerHTTP     func() error
}

// capabilities returns the process components selected by a backend command.
func (m Mode) capabilities() backendRuntimeCapabilities {
	switch m {
	case ModeAPI:
		return backendRuntimeCapabilities{apiHTTP: true}
	case ModeWorker:
		return backendRuntimeCapabilities{riverWorkers: true}
	default:
		return backendRuntimeCapabilities{}
	}
}

// runBackendRuntimeLifecycle starts only the selected components and waits for worker cleanup after runtime exit.
func runBackendRuntimeLifecycle(mode Mode, lifecycle backendRuntimeLifecycle) error {
	capabilities := mode.capabilities()
	var stopWorkers func() error
	if capabilities.riverWorkers {
		var err error
		stopWorkers, err = lifecycle.startRiverWorkers()
		if err != nil {
			return err
		}
	}

	if capabilities.apiHTTP {
		return lifecycle.runHTTP()
	}
	runtimeErr := lifecycle.runWorkerHTTP()
	if stopWorkers == nil {
		return runtimeErr
	}
	return xerror.Join(runtimeErr, stopWorkers())
}

func validateBackendRuntimeMode(mode Mode, executionBackend string) error {
	if mode == ModeExecutor && executionBackend != backendconfig.ExecutionBackendPoller {
		return xerror.ValidationFailed().Detail("executor runtime requires execution.backend=poller")
	}
	return nil
}
