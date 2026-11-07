package repostore

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type nativeGitLimitsKey struct{}

// WithNativeFileLimits binds Git helpers to an explicit finite transfer allowance.
func WithNativeFileLimits(ctx context.Context, maxBytes int64) (context.Context, error) {
	if maxBytes <= 0 || maxBytes > filestream.MaxRetainedBytes {
		return nil, xerror.ValidationFailed().Detail("native Git limits require a positive file allowance")
	}
	if runtime.GOOS != "linux" {
		return nil, xerror.ExecutionUnavailable().WithPublicDetail("Bounded Git streaming requires a Linux executor with process resource limits.")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, xerror.ExecutionUnavailable().Detail("native Git limits require a finite transfer deadline")
	}
	slog.InfoContext(ctx, "native Git resource limits requested", "max_file_bytes", maxBytes,
		"process_address_space_bytes", 512<<20, "single_allocation_bytes", 16<<20,
		"git_file_write_bytes", nativeGitFileBlocks(maxBytes)*512, "deadline", deadline)
	return context.WithValue(ctx, nativeGitLimitsKey{}, maxBytes), nil
}

func gitCommandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Value(nativeGitLimitsKey{}).(int64); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, gitCommandTimeout)
}

// POSIX sh uses 512-byte file blocks. Stricter kernel limits stay in force.
const nativeGitResourceScript = `native_git_blocks=$1
shift
native_git_vmem=$(ulimit -v) || exit 125
if [ "$native_git_vmem" = unlimited ] || [ "$native_git_vmem" -gt 524288 ]; then
  native_git_vmem=524288
fi
ulimit -v "$native_git_vmem" || exit 125
native_git_file_limit=$(ulimit -f) || exit 125
if [ "$native_git_file_limit" != unlimited ] && [ "$native_git_file_limit" -lt "$native_git_blocks" ]; then
  native_git_blocks=$native_git_file_limit
fi
ulimit -f "$native_git_blocks" || exit 125
export GIT_ALLOC_LIMIT=16777216
export GIT_TERMINAL_PROMPT=0
exec "$@"
`

func nativeGitFileBlocks(maxBytes int64) int64 {
	return (2*maxBytes + 64<<20 + 511) / 512
}
