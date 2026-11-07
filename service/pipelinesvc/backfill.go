package pipelinesvc

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/decimal"

	"github.com/Braidplane/braidplane-core/service/pipelineruntime"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func validateBackfillWindows(pipeline *pipelinespec.Pipeline, windows map[slug.Slug]pipelinerun.IncrementalSourceWindow) error {
	invalid := func(message string) error {
		return xerror.InvalidInput(xerror.PipelineRun).Detail(message).WithPublicDetail(message)
	}
	sources := 0
	for _, step := range pipeline.Steps {
		if step.Sink != nil && !step.Sink.IdempotencyClass.IsReplaySafe() {
			return invalid("backfill requires replay-safe sinks")
		}
		if step.Query == nil || step.Query.Incremental == nil {
			continue
		}
		sources++
		window, ok := windows[step.Slug]
		if !ok {
			return invalid("backfill requires a bounded window for every incremental source")
		}
		kind := step.Query.Incremental.CheckpointKind
		if kind == nil {
			return invalid("incremental source requires a checkpoint kind")
		}
		lower, err := checkpointBoundValue(window.LowerBound, kind.String(), step.Query.OperationType)
		if err != nil {
			return err
		}
		upper, err := checkpointBoundValue(window.UpperBound, kind.String(), step.Query.OperationType)
		if err != nil {
			return err
		}
		if kind.String() == "watermark" && !orderedBackfillBounds(lower, upper) {
			return invalid("backfill watermark lower bound must precede upper bound and use the same value type")
		}
	}
	if sources == 0 || len(windows) != sources {
		return invalid("backfill windows must name exactly the pipeline's incremental source steps")
	}
	return nil
}

func checkpointBoundValue(raw json.RawMessage, kind, operationType string) (any, error) {
	invalid := func() (any, error) {
		if operationType == "http" && kind == "watermark" {
			message := "HTTP watermark seed or bound requires a value envelope containing an RFC3339 timestamp with an explicit timezone and at most nine fractional second digits"
			return nil, xerror.InvalidInput(xerror.PipelineRun).Detail(message).WithPublicDetail(message)
		}
		return nil, xerror.InvalidInput(xerror.PipelineRun).Detail("checkpoint bound requires one non-empty cursor string or watermark string/number in its matching envelope")
	}
	if len(raw) == 0 || len(raw) > 16384 || !json.Valid(raw) {
		return invalid()
	}
	var envelope map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil || len(envelope) != 1 {
		return invalid()
	}
	key := "value"
	if kind == "cursor" {
		key = "cursor"
	} else if kind != "watermark" {
		return invalid()
	}
	value := envelope[key]
	if operationType == "http" && kind == "watermark" {
		if _, ok := pipelinerun.HTTPWatermarkTime(value); !ok {
			return invalid()
		}
		return value, nil
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return invalid()
		}
	case json.Number:
		if kind != "watermark" {
			return invalid()
		}
		if _, err := decimal.From(v); err != nil {
			return nil, xerror.Wrapf("validate numeric checkpoint bound: %w", err)
		}
	default:
		return invalid()
	}
	return value, nil
}

func orderedBackfillBounds(lower, upper any) bool {
	switch l := lower.(type) {
	case json.Number:
		u, ok := upper.(json.Number)
		if !ok {
			return false
		}
		ln, le := decimal.From(l)
		un, ue := decimal.From(u)
		return le == nil && ue == nil && ln.Cmp(un) < 0
	case string:
		u, ok := upper.(string)
		if !ok {
			return false
		}
		lt, le := time.Parse(time.RFC3339Nano, l)
		ut, ue := time.Parse(time.RFC3339Nano, u)
		if le == nil || ue == nil {
			return le == nil && ue == nil && lt.Before(ut)
		}
		if ln, err := decimal.From(l); err == nil {
			if un, err := decimal.From(u); err == nil {
				return ln.Cmp(un) < 0
			}
		}
		return l < u
	}
	return false
}

func requestedIncrementalState(options pipelinerun.IncrementalRunOptions) (pipelinerun.IncrementalRunSummary, pipelinerun.IncrementalRunDiagnostics) {
	summary, diagnostics := emptyPipelineRunSummary(), emptyPipelineRunDiagnostics()
	if options.BackfillSeriesSlug == "" {
		return summary, diagnostics
	}
	summary.Mode = "backfill"
	summary.CheckpointStatus = "skipped"
	summary.BackfillSeriesSlug = options.BackfillSeriesSlug.String()
	summary.BackfillChunkCount = options.BackfillChunkCount
	summary.BackfillChunkIndex = options.BackfillChunkIndex
	sources := make([]string, 0, len(options.SourceWindows))
	for source := range options.SourceWindows {
		sources = append(sources, source.String())
	}
	sort.Strings(sources)
	for _, source := range sources {
		window := options.SourceWindows[slug.Slug(source)]
		kind := "watermark"
		var envelope map[string]json.RawMessage
		if json.Unmarshal(window.LowerBound, &envelope) == nil && envelope["cursor"] != nil {
			kind = "cursor"
		}
		lower := pipelineruntime.CheckpointValueSummary(kind, window.LowerBound)
		upper := pipelineruntime.CheckpointValueSummary(kind, window.UpperBound)
		diagnostics.Sources = append(diagnostics.Sources, pipelinerun.IncrementalSourceDiagnostic{
			SourceStepSlug: source, CheckpointKind: kind,
			Window:       pipelinerun.IncrementalCheckpointWindowSummary{Summary: lower + " → " + upper, LowerBound: lower, UpperBound: upper},
			CommitResult: pipelinerun.IncrementalCheckpointCommitResult{Status: "skipped", BlockedReason: "backfill preserves normal checkpoints"},
		})
	}
	return summary, diagnostics
}
