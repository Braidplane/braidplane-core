package pipelinesvc

import (
	"context"
	"fmt"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// InspectConnectedRuns bounds decision visibility checks before traversing authorized evidence.
// Each read is a consistent snapshot; outcomes may advance between reads.
func InspectConnectedRuns(ctx context.Context, selected slug.Slug, read func(slug.Slug) (*model.PipelineRunCompletion, error), visible func(model.PipelineCompletionDecision) (bool, error)) (*model.PipelineRunConnected, error) {
	result := &model.PipelineRunConnected{SelectedRunSlug: selected, Nodes: []*model.PipelineRunCompletion{}}
	queue := []slug.Slug{selected}
	seen := map[slug.Slug]bool{}
	roots := map[slug.Slug]bool{}
	decisions := map[string]bool{}
	reads := 0
	for len(queue) > 0 {
		run := queue[0]
		queue = queue[1:]
		if seen[run] {
			continue
		}
		seen[run] = true
		if reads >= 40 {
			result.Truncated = true
			break
		}
		reads++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		evidence, err := read(run)
		if xerror.Is(err, ErrPipelineRunNotFound) {
			result.ContextUnavailable = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if roots[evidence.RootRunSlug] {
			continue
		}
		roots[evidence.RootRunSlug] = true
		seen[evidence.RootRunSlug] = true
		result.Truncated = result.Truncated || evidence.Truncated
		result.ContextUnavailable = result.ContextUnavailable || evidence.ContextUnavailable
		// Include endpoints because a pending decision can gain a child between reads.
		kept := make([]model.PipelineCompletionDecision, 0, len(evidence.Children))
		visit := func(d model.PipelineCompletionDecision) (bool, error) {
			child := ""
			if d.ChildRunSlug != nil {
				child = d.ChildRunSlug.String()
			}
			key := fmt.Sprintf("%s/%s/%d/%s/%s/%s", d.RootRunSlug, d.TriggerSlug, d.TriggerRevision, d.FinalAttemptRunSlug, d.DownstreamPipelineSlug, child)
			allowed, known := decisions[key]
			if !known {
				if len(decisions) >= 100 {
					evidence.Truncated = true
					result.Truncated = true
					return false, nil
				}
				var err error
				allowed, err = visible(d)
				if err != nil {
					return false, err
				}
				decisions[key] = allowed
			}
			if !allowed {
				evidence.ContextUnavailable = true
				result.ContextUnavailable = true
				return false, nil
			}
			if !seen[d.RootRunSlug] {
				queue = append(queue, d.RootRunSlug)
			}
			if d.ChildRunSlug != nil && !seen[*d.ChildRunSlug] {
				queue = append(queue, *d.ChildRunSlug)
			}
			return true, nil
		}
		if evidence.Parent != nil {
			allowed, err := visit(*evidence.Parent)
			if err != nil {
				return nil, err
			}
			if !allowed {
				evidence.Parent = nil
			}
		}
		for _, d := range evidence.Children {
			allowed, err := visit(d)
			if err != nil {
				return nil, err
			}
			if allowed {
				kept = append(kept, d)
			}
		}
		evidence.Children = kept
		result.Nodes = append(result.Nodes, evidence)
	}
	return result, nil
}
