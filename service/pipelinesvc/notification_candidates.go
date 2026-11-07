package pipelinesvc

import (
	"fmt"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

func validationNotificationCandidates(org slug.Slug, pipeline *pipelinespec.Pipeline, result *PipelineValidationResultRecord) ([]model.NotificationCandidate, error) {
	candidates := []model.NotificationCandidate{}
	if result.Valid {
		return candidates, nil
	}
	for i, diagnostic := range result.Diagnostics {
		if !blockingContractDiagnostic(diagnostic) {
			continue
		}
		p := notificationsvc.ContractFailureParams{
			OrgSlug: org, ProjectSlug: result.ProjectSlug, EnvironmentSlug: result.EnvironmentSlug,
			PipelineSlug: result.PipelineSlug, ValidationSlug: result.Slug,
			CheckID: fmt.Sprintf("diagnostic-%d", i),
			Field:   diagnostic.Field, Rule: diagnostic.Code, ReasonCode: diagnostic.Code, ObservedAt: result.CreatedAt,
		}
		if diagnostic.StepSlug != nil {
			p.StepSlug = slug.Slug(*diagnostic.StepSlug)
		}
		for _, port := range pipelineODCSPorts(pipeline) {
			if port.field == diagnostic.Field && *port.ref != nil {
				p.ContractName, p.ContractRevision = (*port.ref).ContractSlug.String(), (*port.ref).RevisionSlug.String()
				break
			}
		}
		candidate, err := notificationsvc.ContractFailureCandidate(p)
		if err != nil {
			return nil, err
		}
		if result.SpecHash != nil {
			candidate.SafeMetadata["spec_hash"] = *result.SpecHash
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func blockingContractDiagnostic(diagnostic PipelineValidationDiagnostic) bool {
	if diagnostic.Severity != "error" {
		return false
	}
	if strings.HasPrefix(diagnostic.Code, "ODCS_") {
		return true
	}
	switch diagnostic.Code {
	case "PIPELINE_QUERY_OUTPUT_CONTRACT_SCHEMA_MISMATCH", "PIPELINE_CONTRACT_MISMATCH",
		"PIPELINE_TRANSFORM_CONTRACT_INVALID", "PIPELINE_TRANSFORM_CONTRACT_TYPE_MISMATCH",
		"PIPELINE_TRANSFORM_CONTRACT_MISSING_FIELD", "PIPELINE_ODCS_REFERENCE_INVALID":
		return true
	default:
		return false
	}
}
