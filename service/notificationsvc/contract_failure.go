package notificationsvc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ContractFailureParams identifies a durable enforced failure. Producers pass
// declaration identities and fixed diagnostic codes, never evaluated values.
type ContractFailureParams struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, PipelineSlug  slug.Slug
	RunSlug, ValidationSlug                              slug.Slug
	StepSlug                                             slug.Slug
	CheckID, ContractName, ContractRevision, Field, Rule string
	ReasonCode                                           string
	ObservedAt                                           time.Time
}

func ContractFailureCandidate(p ContractFailureParams) (CreateCandidateParams, error) {
	if p.OrgSlug == "" || p.ProjectSlug == "" || p.EnvironmentSlug == "" || p.PipelineSlug == "" ||
		(p.RunSlug == "") == (p.ValidationSlug == "") || p.CheckID == "" || p.ObservedAt.IsZero() {
		return CreateCandidateParams{}, ErrInvalidInput.Detail("contract failure requires a scoped pipeline, one run or validation identity, check identity and observation time")
	}
	identity, err := json.Marshal([]string{p.OrgSlug.String(), p.ProjectSlug.String(), p.EnvironmentSlug.String(), p.PipelineSlug.String(), p.RunSlug.String(), p.ValidationSlug.String(), p.CheckID})
	if err != nil {
		return CreateCandidateParams{}, xerror.Unexpected().With(err)
	}
	record := slug.Slug(fmt.Sprintf("contract-failure-%x", sha256.Sum256(identity)))
	refs := map[string]string{
		"project_slug": p.ProjectSlug.String(), "environment_slug": p.EnvironmentSlug.String(),
		"pipeline_slug": p.PipelineSlug.String(), "step_slug": p.StepSlug.String(), "check_id": p.CheckID,
	}
	kind, action := model.EventEntityTypePipelineRun, model.EventActionExecute
	if p.RunSlug != "" {
		refs["run_slug"] = p.RunSlug.String()
	} else {
		refs["validation_result_slug"] = p.ValidationSlug.String()
		kind, action = model.EventEntityTypePipeline, model.EventActionValidate
		refs["source_ref"] = p.PipelineSlug.String()
	}
	summary := "An enforced pipeline contract failed. Review the exact run or validation before retrying."
	if p.Field != "" {
		summary = fmt.Sprintf("Contract failure at step %q: field %q failed %q. Review the exact run or validation before retrying.", p.StepSlug, p.Field, p.Rule)
	}
	return CreateCandidateParams{
		OrgSlug: p.OrgSlug, RecordSlug: &record, ProjectSlug: &p.ProjectSlug, EnvironmentSlug: &p.EnvironmentSlug,
		NotificationType: model.NotificationTypePipelineContractFailure, Severity: model.NotificationSeverityWarning,
		ObservedAt:        p.ObservedAt,
		DedupeFingerprint: record.String(), SourceEntityType: kind, SourceAction: action, SourceRefs: refs,
		SafeMetadata: map[string]any{
			"summary":       summary,
			"contract_name": p.ContractName, "contract_revision": p.ContractRevision,
			"field": p.Field, "rule": p.Rule, "reason_code": p.ReasonCode,
			"failure_observed_at": p.ObservedAt.UTC().Format(time.RFC3339Nano),
		},
	}, nil
}
