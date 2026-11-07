package proofevidencesvc

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

// JobAudit contains only safe persisted references shared by lifecycle and retention controls.
type JobAudit struct {
	Scope     proofobservations.Scope
	Reference proofobservations.JobReference
	Action    model.EventAction
	Outcome   proofevidence.Outcome
}

func (e JobAudit) EventRecord() (eventrecorder.Record, error) {
	metadata := map[string]any{"project_slug": e.Scope.Project.String(), "environment_slug": e.Scope.Environment.String(), "proof_observation_job_slug": e.Reference.Slug.String()}
	if raw, err := hex.DecodeString(strings.TrimPrefix(e.Reference.RequestDigest, "sha256:")); err == nil && len(raw) == 32 && e.Reference.RequestDigest == "sha256:"+hex.EncodeToString(raw) {
		metadata["request_digest"] = e.Reference.RequestDigest
	}
	prefix := slug.Slug("evt-proof-retention")
	if e.Action == model.EventActionRevoke || e.Action == model.EventActionExecute || e.Action == model.EventActionAcknowledge {
		prefix = "evt-proof-observation"
	}
	if e.Outcome.IsAOutcome() && e.Outcome != proofevidence.OutcomeUnknown {
		metadata["observation_outcome"] = e.Outcome.String()
	}
	return eventrecorder.Record{SlugPrefix: prefix, EventClass: model.EventClassSecurityAudit, EntityType: model.EventEntityTypeProjectEnvironment, EntitySlug: e.Scope.Environment, Action: e.Action, SchemaVersion: model.CurrentEventSchemaVersion, Metadata: metadata}, nil
}

func (s *Ledger) auditExecution(ctx context.Context, job *proofobservations.Job, action model.EventAction, outcome proofevidence.Outcome) error {
	event := JobAudit{
		Scope:     proofobservations.Scope{Organization: job.Request.OrganizationSlug, Project: job.Request.ProjectSlug, Environment: job.Request.EnvironmentSlug},
		Reference: proofobservations.JobReference{Slug: job.Slug, RequestDigest: job.RequestDigest},
		Action:    action, Outcome: outcome,
	}
	if _, err := s.audit.Record(ctx, job.Request.OrganizationSlug, model.SystemEventActor(), event); err != nil {
		return xerror.Unavailable(xerror.Event).Detail("Proof execution audit unavailable")
	}
	return nil
}
