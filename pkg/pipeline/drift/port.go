package drift

import (
	"maps"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// PortImpact uses the same compatibility rules as an inline contract edit.
func PortImpact(pipeline *pipelinespec.Pipeline, step slug.Slug, port string, before, after *payload.Contract) *Report {
	withShape := func(shape *payload.Contract) *pipelinespec.Pipeline {
		candidate := *pipeline
		candidate.Steps = append([]pipelinespec.Step(nil), pipeline.Steps...)
		for i := range candidate.Steps {
			s := &candidate.Steps[i]
			if s.Slug != step {
				continue
			}
			switch {
			case s.Input != nil && port == "output":
				input := *s.Input
				input.OutputContract, input.OutputContractRef = shape, nil
				s.Input = &input
			case s.Query != nil && port == "output":
				query := *s.Query
				query.OutputContract, query.OutputContractRef = shape, nil
				s.Query = &query
			case s.Transform != nil:
				transform := *s.Transform
				if port == "input" {
					transform.InputContract, transform.InputContractRef = shape, nil
				} else if name, named := strings.CutPrefix(port, "input:"); named {
					transform.InputContracts = maps.Clone(transform.InputContracts)
					transform.InputContracts[name] = &pipelinespec.InputContractAttachment{Contract: shape}
				} else {
					transform.OutputContract, transform.OutputContractRef = shape, nil
				}
				s.Transform = &transform
			case s.Sink != nil && port == "input":
				sink := *s.Sink
				sink.InputContract, sink.InputContractRef = shape, nil
				s.Sink = &sink
			}
		}
		return &candidate
	}
	report := ContractImpact(withShape(before), withShape(after))
	if report == nil {
		return nil
	}
	report.Impacts = PortImpacts(pipeline, step, report.Severity)
	return report
}

// PortImpacts follows actual graph edges, including transitive sink consumers.
func PortImpacts(pipeline *pipelinespec.Pipeline, step slug.Slug, severity ReportSeverity) []Impact {
	impacts := []Impact{
		{Kind: "pipeline", PipelineSlug: pipeline.Metadata.Slug.String(), Severity: severity, Message: "pipeline consumes the changed contract"},
		{Kind: "step", PipelineSlug: pipeline.Metadata.Slug.String(), StepSlug: step.String(), Severity: severity, Message: "step consumes the changed contract"},
		{Kind: "unknown_consumer", PipelineSlug: pipeline.Metadata.Slug.String(), StepSlug: step.String(), Severity: ReportSeverityWarning, Message: "external consumer impact is unknown"},
	}
	queue := []string{step.String()}
	seen := map[string]bool{step.String(): true}
	for len(queue) > 0 {
		producer := queue[0]
		queue = queue[1:]
		for _, downstream := range downstreamSteps(pipeline, producer) {
			impacts = append(impacts, Impact{Kind: "edge", PipelineSlug: pipeline.Metadata.Slug.String(), StepSlug: downstream, EdgeSlug: producer + "->" + downstream, Severity: severity, Message: "downstream contract may be affected"})
			if !seen[downstream] {
				seen[downstream] = true
				queue = append(queue, downstream)
			}
		}
	}
	for _, affected := range pipeline.Steps {
		if affected.Sink != nil && seen[affected.Slug.String()] {
			impacts = append(impacts, Impact{Kind: "sink", PipelineSlug: pipeline.Metadata.Slug.String(), StepSlug: affected.Slug.String(), Severity: severity, Message: "sink consumes the affected contract path"})
		}
	}
	return dedupeImpacts(impacts)
}
