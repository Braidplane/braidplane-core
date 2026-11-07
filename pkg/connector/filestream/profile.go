package filestream

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Profile is an explicit operator allowance for one organization's Custom file
// qualification. It does not change billing or other organizations' limits.
type Profile struct {
	Organization slug.Slug
	MaxBytes     int64
	Timeout      time.Duration
	Transfers    int
	Connectors   int
}

func (p Profile) Validate() error {
	if p == (Profile{}) {
		return nil
	}
	if _, err := slug.New(p.Organization.String()); err != nil {
		return xerror.ValidationFailed().Detail("native file profile requires an organization slug")
	}
	if p.MaxBytes <= 0 || p.MaxBytes > MaxRetainedBytes || p.Timeout <= 0 || p.Transfers < 1 || p.Connectors < 1 || p.Connectors > 1<<31-1 || p.Transfers > 1<<31-1 {
		return xerror.ValidationFailed().Detail("native file profile requires positive byte/time/concurrency limits within storage bounds")
	}
	return nil
}

func (p Profile) Matches(org slug.Slug) bool { return p.Organization != "" && p.Organization == org }

// HTTP response media types are unknown before dispatch; conservatively reserve
// a file slot for HTTP operations in the explicitly configured profile.
func NativeOperation(vendor string, raw json.RawMessage) bool {
	var statement struct {
		Format    string `json:"format"`
		Resource  string `json:"resource"`
		Operation string `json:"operation"`
	}
	if json.Unmarshal(raw, &statement) != nil {
		return false
	}
	switch vendor {
	case "ftp", "git", "github", "gitlab", "s3":
		return statement.Format == "file"
	case "http":
		return true
	case "google_workspace":
		return statement.Format == "file" && statement.Resource == "file" || statement.Operation == "export" && (statement.Resource == "document" || statement.Resource == "presentation")
	default:
		return false
	}
}

type attemptStartKey struct{}

type attempt struct {
	start    time.Time
	id       uuid.UUID
	mu       sync.Mutex
	cleanup  func()
	finished bool
}

// BeginAttempt uses wall time, independent of retained logical run dates. Its
// finish function cancels remaining work and releases transfer admission.
func BeginAttempt(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	a := &attempt{start: at, id: uuid.New()}
	ctx = context.WithValue(ctx, attemptStartKey{}, a)
	return ctx, func() {
		cancel()
		a.mu.Lock()
		cleanup := a.cleanup
		a.cleanup, a.finished = nil, true
		a.mu.Unlock()
		if cleanup != nil {
			cleanup()
		}
	}
}

// AttemptID groups all native-file operations in one pipeline attempt.
func AttemptID(ctx context.Context) (uuid.UUID, bool) {
	a, ok := ctx.Value(attemptStartKey{}).(*attempt)
	if !ok {
		return uuid.Nil, false
	}
	return a.id, true
}

// OnAttemptFinish registers one admission cleanup for the attempt. Standalone
// operations have no attempt and release their slot at the connector boundary.
func OnAttemptFinish(ctx context.Context, cleanup func()) bool {
	a, ok := ctx.Value(attemptStartKey{}).(*attempt)
	if !ok {
		return false
	}
	a.mu.Lock()
	finished := a.finished
	if a.cleanup == nil && !finished {
		a.cleanup = cleanup
	}
	a.mu.Unlock()
	if finished {
		cleanup()
	}
	return true
}

func (p Profile) Deadline(ctx context.Context, now time.Time) time.Time {
	start := now
	if a, ok := ctx.Value(attemptStartKey{}).(*attempt); ok {
		start = a.start
	}
	return start.Add(p.Timeout)
}
