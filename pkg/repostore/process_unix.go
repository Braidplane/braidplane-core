//go:build linux || darwin

package repostore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func newGitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	script := ""
	var blocks int64
	if maxBytes, ok := ctx.Value(nativeGitLimitsKey{}).(int64); ok {
		script, blocks = nativeGitResourceScript, nativeGitFileBlocks(maxBytes)
	} else if limit, ok := ctx.Value(gitWriteLimitKey{}).(int64); ok {
		script, blocks = ordinaryGitResourceScript, (limit+511)/512
		if runtime.GOOS == "linux" {
			script = gitAddressSpaceLimitScript + script
		}
	}
	if script != "" && cmd.Err == nil {
		gitPath, err := filepath.Abs(cmd.Path)
		if err != nil {
			cmd.Err = ErrRepositorySetup.With(err)
		} else {
			limitedArgs := []string{"-c", script, "braidplane-git", strconv.FormatInt(blocks, 10), gitPath}
			limitedArgs = append(limitedArgs, args...)
			cmd = exec.CommandContext(ctx, "/bin/sh", limitedArgs...)
		}
	}
	// Git's transport and pack helpers must share its cancellation lifetime.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		if err != nil {
			return ErrRepositorySetup.With(err)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	return cmd
}

func gitFileLimitSignal(err error) bool {
	var exit *exec.ExitError
	if !xerror.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGXFSZ
}
