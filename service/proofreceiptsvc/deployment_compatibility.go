package proofreceiptsvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinediff "github.com/Braidplane/braidplane-core/pkg/pipeline/diff"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func (s *Compiler) findDeployment(ctx context.Context, db postgres.DBTX, org, project, env, pipeline slug.Slug, path string) (*pipelinesrepo.PipelineDeployment, error) {
	deployment, err := s.deployments.FindDeployment(ctx, db, org, project, env, pipeline)
	// The candidate's slug cannot select a deployment for another configured file.
	if err == nil && deployment != nil && path != "" && deployment.PipelineSourcePath != path {
		return nil, pipelinesrepo.ErrPipelineDeploymentNotFound
	}
	return deployment, err
}

func (s *Compiler) readDeployedCompatibility(ctx context.Context, db postgres.DBTX, target *proofreceipts.CandidateTarget, pipeline *pipelinespec.Pipeline, deployment *pipelinesrepo.PipelineDeployment) (*model.ProofDeployedCompatibility, error) {
	comparison := &model.ProofDeployedCompatibility{PipelineSlug: pipeline.Metadata.Slug}
	if deployment == nil {
		return comparison, nil
	}
	comparison.DeploymentRevision = deployment.Revision
	comparison.BaselineSpecHash = deployment.CurrentVersionSpecHash
	comparison.DeployedAt = deployment.DeployedAt.UTC()
	version, err := s.deployments.FindVersionBySpecHash(ctx, db, target.OrganizationSlug, target.ProjectSlug, pipeline.Metadata.Slug, comparison.BaselineSpecHash)
	if xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
		return comparison, nil
	}
	if err != nil {
		return nil, err
	}
	// A mismatched or unreadable immutable version cannot supply compatibility evidence.
	comparison.Impact, _ = pipelinesvc.CheckDeployedVersion(deployment, version, pipeline)
	return comparison, nil
}

func matchesCompatibilityDeployment(comparison *model.ProofDeployedCompatibility, deployment *pipelinesrepo.PipelineDeployment) bool {
	if deployment == nil {
		return comparison.DeploymentRevision == 0
	}
	return comparison.DeploymentRevision == deployment.Revision &&
		comparison.BaselineSpecHash == deployment.CurrentVersionSpecHash && comparison.DeployedAt.Equal(deployment.DeployedAt)
}

func compatibilityClaim(comparison *model.ProofDeployedCompatibility, specHash string, ref model.ProofEvidenceReference, now time.Time) model.ProofClaim {
	claim := model.ProofClaim{
		Identity:              model.ProofClaimIdentity{Family: model.ProofClaimFamilyDriftImpact, PipelineVersionHash: specHash, SemanticPath: model.ProofDeployedCompatibilityPath},
		Description:           "Static compatibility with the deployed version",
		RequiredEvidenceLevel: model.ProofEvidenceLevelStatic,
	}
	if comparison.Impact == nil {
		claim.Verdict = model.ProofVerdictUnverified
		claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.deployed_baseline_unavailable", Summary: "The target has no readable deployed pipeline version.", Remediation: "Re-run Proof when the target has a deployed pipeline version.", Retryable: true}}
		return claim
	}
	claim.ActualEvidenceLevel, claim.ObservedAt = new(model.ProofEvidenceLevelStatic), &now
	claim.Evidence = []model.ProofEvidenceReference{ref}
	claim.Verdict = model.ProofVerdictPassed
	if comparison.Impact.Severity == pipelinediff.SeverityBreaking {
		claim.Verdict = model.ProofVerdictFailed
	}
	return claim
}
