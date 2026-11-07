package filestream

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProfileValidationAndDeadline(t *testing.T) {
	t.Parallel()
	valid := Profile{Organization: "fixture", MaxBytes: 1, Timeout: time.Minute, Transfers: 1, Connectors: 2}
	require.NoError(t, (Profile{}).Validate())
	require.False(t, (Profile{}).Matches("fixture"))
	require.NoError(t, valid.Validate())
	require.True(t, valid.Matches("fixture"))
	independent := valid
	independent.Transfers = 3
	require.NoError(t, independent.Validate(), "attempt and connector concurrency are independent budgets")
	require.False(t, valid.Matches("other"))
	for _, mutate := range []func(*Profile){
		func(p *Profile) { p.Organization = "" },
		func(p *Profile) { p.Organization = "INVALID" },
		func(p *Profile) { p.MaxBytes = 0 },
		func(p *Profile) { p.MaxBytes = MaxRetainedBytes + 1 },
		func(p *Profile) { p.Timeout = 0 },
		func(p *Profile) { p.Transfers = 0 },
		func(p *Profile) { p.Connectors = 0 },
		func(p *Profile) { p.Transfers = -1 },
	} {
		invalid := valid
		mutate(&invalid)
		require.Error(t, invalid.Validate())
	}
	start := time.Unix(1700000000, 0)
	require.Equal(t, start.Add(time.Minute), valid.Deadline(context.Background(), start))
	ctx, finish := BeginAttempt(context.Background(), start)
	defer finish()
	require.Equal(t, start.Add(time.Minute), valid.Deadline(ctx, start.Add(45*time.Second)), "later sink operations must not reset the transfer clock")
}

func TestAttemptAdmissionCleanup(t *testing.T) {
	t.Parallel()
	ctx, finish := BeginAttempt(t.Context(), time.Now())
	defer finish()
	id, ok := AttemptID(ctx)
	require.True(t, ok)
	otherContext, finishOther := BeginAttempt(t.Context(), time.Now())
	defer finishOther()
	other, _ := AttemptID(otherContext)
	require.NotEqual(t, id, other)
	var released atomic.Int32
	var calls sync.WaitGroup
	for range 20 {
		calls.Go(func() { require.True(t, OnAttemptFinish(ctx, func() { released.Add(1) })) })
	}
	calls.Wait()
	require.Zero(t, released.Load(), "source completion must not release attempt admission")
	finish()
	finish()
	require.EqualValues(t, 1, released.Load())
	require.True(t, OnAttemptFinish(ctx, func() { released.Add(1) }))
	require.EqualValues(t, 2, released.Load(), "late registration cannot leave a finished attempt holding admission")
	require.False(t, OnAttemptFinish(t.Context(), func() { t.Error("standalone cleanup must be explicit") }))
}

func TestNativeOperation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		vendor, statement string
		want              bool
	}{
		{"s3", `{"format":"file"}`, true},
		{"ftp", `{"format":"file"}`, true},
		{"git", `{"format":"file"}`, true},
		{"github", `{"format":"file"}`, true},
		{"gitlab", `{"format":"file"}`, true},
		{"http", `{}`, true},
		{"google_workspace", `{"resource":"file","format":"file"}`, true},
		{"google_workspace", `{"resource":"document","operation":"export"}`, true},
		{"google_workspace", `{"resource":"presentation","operation":"export"}`, true},
		{"google_workspace", `{"resource":"spreadsheet","format":"file"}`, false},
		{"s3", `{"format":"csv"}`, false},
		{"postgres", `{"format":"file"}`, false},
		{"mysql", `{"format":"file"}`, false},
		{"http", `{`, false},
	} {
		require.Equal(t, tc.want, NativeOperation(tc.vendor, json.RawMessage(tc.statement)), "%s %s", tc.vendor, tc.statement)
	}
}
