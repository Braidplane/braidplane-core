//go:build linux || darwin

package repostore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func newGitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	if maxBytes, ok := ctx.Value(nativeGitLimitsKey{}).(int64); ok && cmd.Err == nil {
		gitPath, err := filepath.Abs(cmd.Path)
		if err != nil {
			cmd.Err = ErrRepositorySetup.With(err)
		} else {
			limitedArgs := []string{"-c", nativeGitResourceScript, "braidplane-native-git", strconv.FormatInt(nativeGitFileBlocks(maxBytes), 10), gitPath}
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
