package pipelinesvc

import (
	"context"
	"encoding/json"
	"time"

	pipelinediff "github.com/Braidplane/braidplane-core/pkg/pipeline/diff"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
)

// PipelineDeploymentComparison identifies the observed baseline and its semantic impact.
type PipelineDeploymentComparison struct {
	DeploymentRevision int64
	DeployedAt         time.Time
	BaselineSpecHash   string
	CandidateSpecHash  string
	Impact             *pipelinediff.CheckResult
}

func (s *service) CompareDeployment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	candidateJSON json.RawMessage,
) (*PipelineDeploymentComparison, error) {
	candidate, err := pipelinevalidate.Load("candidate.json", candidateJSON)
	if err != nil {
		// Validation errors can quote candidate values.
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("invalid candidate pipeline specification")
	}
	if candidate.Metadata.Slug != pipelineSlug {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("candidate pipeline slug must match the requested pipeline")
	}
	candidateHash, err := validationSpecHash(candidate)
	if err != nil {
		return nil, err
	}

	deployment, err := s.pipelineStore.FindDeployment(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}
	// The exact hash and version ID belong to the same deployment read. A later
	// deployment must not replace the baseline while this comparison is running.
	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, deployment.CurrentVersionSpecHash)
	if err != nil {
		return nil, err
	}
	impact, err := CheckDeployedVersion(deployment, version, candidate)
	if err != nil {
		return nil, err
	}
	return &PipelineDeploymentComparison{
		DeploymentRevision: deployment.Revision,
		DeployedAt:         deployment.DeployedAt,
		BaselineSpecHash:   version.SpecHash,
		CandidateSpecHash:  *candidateHash,
		Impact:             impact,
	}, nil
}

// CheckDeployedVersion compares a parsed candidate with the deployment's exact immutable version.
func CheckDeployedVersion(deployment *pipelinesrepo.PipelineDeployment, version *pipelinesrepo.PipelineVersion, candidate *pipelinespec.Pipeline) (*pipelinediff.CheckResult, error) {
	if version == nil || version.ID != deployment.CurrentVersionID || version.SpecHash != deployment.CurrentVersionSpecHash {
		return nil, ErrPipelineVersionNotFound
	}
	baseline, err := pipelineFromJSON(version.SpecJSONB)
	if err != nil {
		return nil, xerror.Unexpected().Detail("could not decode the deployed pipeline version")
	}

	return pipelinediff.CheckRedacted(pipelinediff.Diff(baseline, candidate)), nil
}
