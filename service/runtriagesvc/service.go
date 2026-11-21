// Package runtriagesvc summarizes which pipelines in one environment are
// currently failing and why. It reads persisted runs through the pipeline
// service and never starts, retries or changes a run.
//
// Callers own permission checks. The HTTP and MCP boundaries must authorize
// read access to pipelines in the organization before calling Triage.
package runtriagesvc

import (
	"context"
	"slices"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

const (
	// RunWindow is the number of most recent runs inspected (the maximum list page size).
	RunWindow = 100
	// MaxDetailed is the number of failing pipelines that get a run drill-down.
	MaxDetailed = 5

	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCanceled  = "canceled"
	statusQueued    = "queued"
	statusRunning   = "running"
)

// RunReader is the read-only part of the pipeline service used for triage.
type RunReader interface {
	ListRuns(ctx context.Context, org, project, environment slug.Slug, filters *pipelinesvc.RunFilters, req model.ListRequest) (model.ListResponse[*pipelinesvc.PipelineRunMetadata], error)
	GetRun(ctx context.Context, org, project, environment slug.Slug, runSlug string) (*pipelinesvc.PipelineRunDetail, error)
	GetRunCompletion(ctx context.Context, org, project, environment, runSlug slug.Slug) (*model.PipelineRunCompletion, error)
}

var _ RunReader = pipelinesvc.Service(nil)

// Params selects the environment to inspect and optionally one pipeline.
type Params struct {
	Organization, Project, Environment slug.Slug
	Pipeline                           *slug.Slug
}

// Report is the structured triage result. It carries only run metadata and the
// sanitized failure context that Core already returns for a run.
type Report struct {
	RunsInspected      int                 `json:"runs_inspected"`
	PipelinesInspected int                 `json:"pipelines_inspected"`
	MoreRunsExist      bool                `json:"more_runs_exist"`
	Failing            []FailingPipeline   `json:"failing"`
	Recovered          []RecoveredPipeline `json:"recovered"`
	// OmittedFailing lists failing pipelines beyond MaxDetailed, newest failure first, without details.
	OmittedFailing []string `json:"omitted_failing"`
	Healthy        int      `json:"healthy"`
}

// RunRef identifies a run and when it happened.
type RunRef struct {
	RunSlug string    `json:"run_slug"`
	At      time.Time `json:"at"`
}

// FailingPipeline is a pipeline whose newest finished run failed.
type FailingPipeline struct {
	Pipeline     string    `json:"pipeline"`
	LatestRun    string    `json:"latest_run"`
	StartedAt    time.Time `json:"started_at"`
	DurationMs   int64     `json:"duration_ms"`
	FailedStreak int       `json:"failed_streak"`
	// FailedInWindow counts failed runs among the inspected finished runs.
	FailedInWindow int `json:"failed_in_window"`
	// LastSuccess is nil when no run in the inspected window succeeded.
	LastSuccess *RunRef `json:"last_success,omitempty"`
	// ActiveRuns counts queued or running runs of this pipeline in the window.
	ActiveRuns int `json:"active_runs"`
	// ErrorSummary is the run-list summary, used when Failure is unavailable.
	ErrorSummary string `json:"error_summary,omitempty"`
	// Failure is nil when the run has no recorded failure context or details were not read.
	Failure *Failure `json:"failure,omitempty"`
	Retry   *Retry   `json:"retry,omitempty"`
	// DetailsUnavailable is set when the run detail could not be read, and for
	// pipelines beyond MaxDetailed.
	DetailsUnavailable bool `json:"details_unavailable,omitempty"`
}

// Failure is the failing-step context recorded on the run.
type Failure struct {
	Step                     string `json:"step"`
	Error                    string `json:"error"`
	Kind                     string `json:"kind,omitempty"`
	Side                     string `json:"side,omitempty"`
	ReasonCode               string `json:"reason_code,omitempty"`
	ActionHint               string `json:"action_hint,omitempty"`
	OutcomeUncertain         bool   `json:"outcome_uncertain,omitempty"`
	AutomaticRetryProhibited bool   `json:"automatic_retry_prohibited,omitempty"`
}

// Retry is the completion-chain state of the failing run.
type Retry struct {
	WaitingForRetry bool `json:"waiting_for_retry"`
	Closed          bool `json:"closed"`
	Attempts        int  `json:"attempts"`
}

// RecoveredPipeline failed within the window but its newest finished run did not.
type RecoveredPipeline struct {
	Pipeline       string    `json:"pipeline"`
	FailedInWindow int       `json:"failed_in_window"`
	LatestRun      string    `json:"latest_run"`
	LatestStatus   string    `json:"latest_status"`
	StartedAt      time.Time `json:"started_at"`
}

// Service builds triage reports from persisted runs.
type Service struct{ runs RunReader }

// New returns a Service. The reader is required.
func New(runs RunReader) *Service {
	if runs == nil {
		panic("run triage requires a run reader")
	}
	return &Service{runs: runs}
}

// Triage inspects the most recent runs of one environment. A run detail or
// completion that cannot be read marks that pipeline's details as unavailable
// without failing the report; listing errors and cancellation are returned.
func (s *Service) Triage(ctx context.Context, p Params) (*Report, error) {
	filters := &pipelinesvc.RunFilters{PipelineSlug: p.Pipeline}
	page, err := s.runs.ListRuns(ctx, p.Organization, p.Project, p.Environment, filters, model.ListRequest{Limit: RunWindow})
	if err != nil {
		return nil, xerror.Wrapf("list runs for triage: %w", err)
	}
	groups := analyse(page.Items)
	report := &Report{
		RunsInspected: len(page.Items), PipelinesInspected: len(groups), MoreRunsExist: page.HasMore,
		Failing: []FailingPipeline{}, Recovered: []RecoveredPipeline{}, OmittedFailing: []string{},
	}
	for _, g := range groups {
		switch {
		case g.latest == nil:
			report.Healthy++ // only queued or running runs so far: nothing has failed
		case g.latest.Status == statusFailed:
			report.Failing = append(report.Failing, g.failing())
		case g.failedInWindow > 0:
			report.Recovered = append(report.Recovered, RecoveredPipeline{
				Pipeline: g.pipeline, FailedInWindow: g.failedInWindow, LatestRun: g.latest.RunSlug,
				LatestStatus: g.latest.Status, StartedAt: g.latest.StartedAt,
			})
		default:
			report.Healthy++
		}
	}
	slices.SortFunc(report.Failing, func(a, b FailingPipeline) int { return newestFirst(a.StartedAt, b.StartedAt, a.Pipeline, b.Pipeline) })
	slices.SortFunc(report.Recovered, func(a, b RecoveredPipeline) int { return newestFirst(a.StartedAt, b.StartedAt, a.Pipeline, b.Pipeline) })
	for i := range report.Failing {
		if i >= MaxDetailed {
			report.Failing[i].DetailsUnavailable = true
			report.OmittedFailing = append(report.OmittedFailing, report.Failing[i].Pipeline)
			continue
		}
		if err := s.addDetails(ctx, p, &report.Failing[i]); err != nil {
			return nil, err
		}
	}
	return report, nil
}

func (s *Service) addDetails(ctx context.Context, p Params, f *FailingPipeline) error {
	detail, err := s.runs.GetRun(ctx, p.Organization, p.Project, p.Environment, f.LatestRun)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return xerror.Wrapf("read failing run: %w", ctxErr)
	}
	if err != nil || detail == nil {
		f.DetailsUnavailable = true
	} else if failure := detail.Timeline.Failure; failure != nil {
		f.Failure = &Failure{
			Step: failure.Step.String(), Error: failure.Error, Kind: failure.Kind, Side: failure.Side,
			ReasonCode: failure.ReasonCode.String(), ActionHint: failure.ActionHint,
			OutcomeUncertain: failure.OutcomeUncertain, AutomaticRetryProhibited: failure.AutomaticRetryProhibited,
		}
	}
	// Run slugs on persisted runs were validated when the run was created.
	completion, err := s.runs.GetRunCompletion(ctx, p.Organization, p.Project, p.Environment, slug.Slug(f.LatestRun))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return xerror.Wrapf("read run completion: %w", ctxErr)
	}
	if err == nil && completion != nil {
		f.Retry = &Retry{WaitingForRetry: completion.WaitingForRetry, Closed: completion.Closed, Attempts: len(completion.Attempts)}
	}
	return nil
}

type pipelineRuns struct {
	pipeline       string
	latest         *pipelinesvc.PipelineRunMetadata
	lastSuccess    *pipelinesvc.PipelineRunMetadata
	streak         int
	failedInWindow int
	active         int
}

func (g *pipelineRuns) failing() FailingPipeline {
	f := FailingPipeline{
		Pipeline: g.pipeline, LatestRun: g.latest.RunSlug, StartedAt: g.latest.StartedAt, DurationMs: g.latest.DurationMs,
		FailedStreak: g.streak, FailedInWindow: g.failedInWindow, ActiveRuns: g.active,
	}
	if g.latest.ErrorSummary != nil {
		f.ErrorSummary = *g.latest.ErrorSummary
	}
	if g.lastSuccess != nil {
		at := g.lastSuccess.StartedAt
		if g.lastSuccess.EndedAt != nil {
			at = *g.lastSuccess.EndedAt
		}
		f.LastSuccess = &RunRef{RunSlug: g.lastSuccess.RunSlug, At: at}
	}
	return f
}

// analyse groups runs by pipeline and classifies each from its newest finished run.
func analyse(runs []*pipelinesvc.PipelineRunMetadata) []*pipelineRuns {
	byPipeline := map[string][]*pipelinesvc.PipelineRunMetadata{}
	for _, r := range runs {
		if r != nil {
			byPipeline[r.PipelineSlug] = append(byPipeline[r.PipelineSlug], r)
		}
	}
	out := make([]*pipelineRuns, 0, len(byPipeline))
	for pipeline, rs := range byPipeline {
		slices.SortStableFunc(rs, func(a, b *pipelinesvc.PipelineRunMetadata) int { return b.StartedAt.Compare(a.StartedAt) })
		g := &pipelineRuns{pipeline: pipeline}
		streaking := true
		for _, r := range rs {
			switch r.Status {
			case statusQueued, statusRunning:
				g.active++
				continue
			case statusSucceeded, statusFailed, statusCanceled:
			default:
				continue
			}
			if g.latest == nil {
				g.latest = r
			}
			if r.Status == statusFailed {
				g.failedInWindow++
				if streaking {
					g.streak++
				}
				continue
			}
			streaking = false
			if r.Status == statusSucceeded && g.lastSuccess == nil {
				g.lastSuccess = r
			}
		}
		out = append(out, g)
	}
	return out
}

func newestFirst(a, b time.Time, nameA, nameB string) int {
	if c := b.Compare(a); c != 0 {
		return c
	}
	if nameA < nameB {
		return -1
	}
	if nameA > nameB {
		return 1
	}
	return 0
}
