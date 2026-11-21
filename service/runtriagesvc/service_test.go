package runtriagesvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

type fakeReader struct {
	runs       []*pipelinesvc.PipelineRunMetadata
	hasMore    bool
	details    map[string]*pipelinesvc.PipelineRunDetail
	completion map[string]*model.PipelineRunCompletion
	listErr    error
	detailErr  error

	listFilters *pipelinesvc.RunFilters
	listReq     model.ListRequest
	detailCalls []string
}

func (f *fakeReader) ListRuns(_ context.Context, _, _, _ slug.Slug, filters *pipelinesvc.RunFilters, req model.ListRequest) (model.ListResponse[*pipelinesvc.PipelineRunMetadata], error) {
	f.listFilters, f.listReq = filters, req
	return model.ListResponse[*pipelinesvc.PipelineRunMetadata]{Items: f.runs, HasMore: f.hasMore}, f.listErr
}

func (f *fakeReader) GetRun(_ context.Context, _, _, _ slug.Slug, run string) (*pipelinesvc.PipelineRunDetail, error) {
	f.detailCalls = append(f.detailCalls, run)
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return f.details[run], nil
}

func (f *fakeReader) GetRunCompletion(_ context.Context, _, _, _, run slug.Slug) (*model.PipelineRunCompletion, error) {
	return f.completion[run.String()], nil
}

func run(pipeline, name, status string, ago time.Duration) *pipelinesvc.PipelineRunMetadata {
	return &pipelinesvc.PipelineRunMetadata{PipelineSlug: pipeline, RunSlug: name, Status: status, StartedAt: testNow.Add(-ago), DurationMs: 1800}
}

func failedDetail(step, code, hint string) *pipelinesvc.PipelineRunDetail {
	reason, err := pipelinerun.FailureReasonCodeString(code)
	if err != nil {
		panic(err)
	}
	return &pipelinesvc.PipelineRunDetail{Timeline: pipelinerun.RunTimeline{Failure: &pipelinerun.FailureContext{
		Step: slug.Slug(step), Error: "boom at " + step, Kind: "data_source", Side: "source", ReasonCode: reason, ActionHint: hint,
	}}}
}

func triage(t *testing.T, reader *fakeReader, pipeline *slug.Slug) *Report {
	t.Helper()
	report, err := New(reader).Triage(t.Context(), Params{Organization: "org", Project: "proj", Environment: "dev", Pipeline: pipeline})
	require.NoError(t, err)
	return report
}

func TestTriageClassifiesFailingRecoveredAndHealthy(t *testing.T) {
	summary := "step fetch failed"
	failing := run("customer-enrichment", "ce-r3", "failed", 3*time.Hour)
	failing.ErrorSummary = &summary
	ended := testNow.Add(-50 * time.Hour)
	success := run("customer-enrichment", "ce-r1", "succeeded", 51*time.Hour)
	success.EndedAt = &ended
	reader := &fakeReader{
		runs: []*pipelinesvc.PipelineRunMetadata{
			success,
			run("customer-enrichment", "ce-r2", "failed", 27*time.Hour),
			failing,
			run("orders", "orders-r2", "succeeded", 2*time.Hour),
			run("orders", "orders-r1", "succeeded", 26*time.Hour),
			run("inventory", "inv-r2", "succeeded", time.Hour),
			run("inventory", "inv-r1", "failed", 25*time.Hour),
			run("new-pipeline", "np-r1", "queued", time.Minute),
		},
		details:    map[string]*pipelinesvc.PipelineRunDetail{"ce-r3": failedDetail("fetch-customers", "source_query_failed", "Check the source table")},
		completion: map[string]*model.PipelineRunCompletion{"ce-r3": {Attempts: []model.PipelineCompletionAttempt{{RunSlug: "ce-r3"}}}},
	}

	report := triage(t, reader, nil)

	require.Equal(t, 8, report.RunsInspected)
	require.Equal(t, 4, report.PipelinesInspected)
	require.Equal(t, 2, report.Healthy, "an all-succeeded pipeline and one with only a queued run")
	require.Len(t, report.Failing, 1)
	f := report.Failing[0]
	require.Equal(t, "customer-enrichment", f.Pipeline)
	require.Equal(t, "ce-r3", f.LatestRun)
	require.Equal(t, 2, f.FailedStreak)
	require.Equal(t, 2, f.FailedInWindow)
	require.Equal(t, &RunRef{RunSlug: "ce-r1", At: ended}, f.LastSuccess)
	require.Equal(t, "step fetch failed", f.ErrorSummary)
	require.Equal(t, &Failure{
		Step: "fetch-customers", Error: "boom at fetch-customers", Kind: "data_source", Side: "source",
		ReasonCode: "source_query_failed", ActionHint: "Check the source table",
	}, f.Failure)
	require.Equal(t, &Retry{Attempts: 1}, f.Retry)
	require.False(t, f.DetailsUnavailable)
	require.Equal(t, []RecoveredPipeline{{
		Pipeline: "inventory", FailedInWindow: 1, LatestRun: "inv-r2", LatestStatus: "succeeded", StartedAt: testNow.Add(-time.Hour),
	}}, report.Recovered)
	require.Equal(t, []string{"ce-r3"}, reader.detailCalls, "only failing pipelines are drilled into")
}

func TestTriageRequestsNewestWindowAndPipelineFilter(t *testing.T) {
	only := slug.Slug("orders")
	reader := &fakeReader{hasMore: true}

	report := triage(t, reader, &only)

	require.Equal(t, RunWindow, reader.listReq.Limit)
	require.Equal(t, &only, reader.listFilters.PipelineSlug)
	require.Nil(t, reader.listFilters.Status, "recovery and streaks need succeeded runs too")
	require.True(t, report.MoreRunsExist)
	require.Empty(t, report.Failing)
	require.NotNil(t, report.Failing, "empty lists encode as [] rather than null")
	require.NotNil(t, report.Recovered)
	require.NotNil(t, report.OmittedFailing)
}

func TestTriageStreakStopsAtCanceledRun(t *testing.T) {
	reader := &fakeReader{runs: []*pipelinesvc.PipelineRunMetadata{
		run("p", "p-r4", "failed", time.Hour),
		run("p", "p-r3", "canceled", 2*time.Hour),
		run("p", "p-r2", "failed", 3*time.Hour),
		run("p", "p-r1", "succeeded", 4*time.Hour),
		run("q", "q-r2", "canceled", time.Hour),
		run("q", "q-r1", "failed", 2*time.Hour),
	}}

	report := triage(t, reader, nil)

	require.Len(t, report.Failing, 1)
	require.Equal(t, 1, report.Failing[0].FailedStreak)
	require.Equal(t, 2, report.Failing[0].FailedInWindow)
	require.Equal(t, "p-r1", report.Failing[0].LastSuccess.RunSlug)
	require.Len(t, report.Recovered, 1, "a newest canceled run is not a current failure")
	require.Equal(t, "q", report.Recovered[0].Pipeline)
}

func TestTriageCountsActiveRunsAndKeepsLatestFinished(t *testing.T) {
	reader := &fakeReader{runs: []*pipelinesvc.PipelineRunMetadata{
		run("p", "p-r3", "running", time.Minute),
		run("p", "p-r2", "failed", time.Hour),
	}}

	report := triage(t, reader, nil)

	require.Len(t, report.Failing, 1)
	require.Equal(t, 1, report.Failing[0].ActiveRuns)
	require.Equal(t, "p-r2", report.Failing[0].LatestRun)
	require.Nil(t, report.Failing[0].LastSuccess)
}

func TestTriageDetailsOnlyForNewestFailures(t *testing.T) {
	var runs []*pipelinesvc.PipelineRunMetadata
	details := map[string]*pipelinesvc.PipelineRunDetail{}
	for i := range MaxDetailed + 2 {
		name := fmt.Sprintf("p%d-r1", i)
		runs = append(runs, run(fmt.Sprintf("p%d", i), name, "failed", time.Duration(i+1)*time.Hour))
		details[name] = failedDetail("step", "source_query_failed", "")
	}
	reader := &fakeReader{runs: runs, details: details}

	report := triage(t, reader, nil)

	require.Len(t, report.Failing, MaxDetailed+2)
	require.Equal(t, []string{"p0-r1", "p1-r1", "p2-r1", "p3-r1", "p4-r1"}, reader.detailCalls)
	require.Equal(t, []string{"p5", "p6"}, report.OmittedFailing)
	for i, f := range report.Failing {
		require.Equal(t, i >= MaxDetailed, f.DetailsUnavailable, f.Pipeline)
	}
}

func TestTriageToleratesUnreadableRunDetail(t *testing.T) {
	reader := &fakeReader{
		runs:      []*pipelinesvc.PipelineRunMetadata{run("p", "p-r1", "failed", time.Hour)},
		detailErr: xerror.Unexpected().Detail("connection string postgres://secret@db leaked"),
	}

	report := triage(t, reader, nil)

	require.Len(t, report.Failing, 1)
	require.True(t, report.Failing[0].DetailsUnavailable)
	require.Nil(t, report.Failing[0].Failure)
	encoded := report.Text(testNow)
	require.NotContains(t, encoded, "secret", "dependency error text never reaches the report")
	require.Contains(t, encoded, "(details unavailable)")
}

func TestTriageReturnsListAndCancellationErrors(t *testing.T) {
	_, err := New(&fakeReader{listErr: xerror.Unexpected().Detail("db down")}).Triage(t.Context(), Params{})
	require.ErrorContains(t, err, "list runs for triage")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &fakeReader{runs: []*pipelinesvc.PipelineRunMetadata{run("p", "p-r1", "failed", time.Hour)}, detailErr: context.Canceled}
	_, err = New(reader).Triage(ctx, Params{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewRequiresReader(t *testing.T) {
	require.Panics(t, func() { New(nil) })
}
