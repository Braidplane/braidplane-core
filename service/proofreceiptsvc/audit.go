package proofreceiptsvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
)

var ErrObservationAuditUnavailable = xerror.New(xerror.KindUnavailable, "PROOF_OBSERVATION_AUDIT_UNAVAILABLE", "Proof observation audit unavailable")

// Only server-generated identities and persisted job references enter the audit
// stream. Intent tokens, supplied digests, targets, and error text never do.
type observationAudit struct {
	scope         ObservationScope
	action        model.EventAction
	accepted      bool
	limited       bool
	job           *proofobservations.Job
	policyVersion string
	enabled       *bool
	receiptSlug   slug.Slug
}

func (e observationAudit) EventRecord() (eventrecorder.Record, error) {
	metadata := map[string]any{
		"project_slug": e.scope.Project.String(), "environment_slug": e.scope.Environment.String(),
		"accepted": e.accepted, "admission_limit_reached": e.limited,
	}
	if e.job != nil {
		metadata["proof_observation_job_slug"] = e.job.Slug.String()
		record, err := (proofevidencesvc.JobAudit{Scope: e.scope, Reference: proofobservations.JobReference{Slug: e.job.Slug, RequestDigest: e.job.RequestDigest}, Action: e.action}).EventRecord()
		if err != nil {
			return eventrecorder.Record{}, err
		}
		if digest, ok := record.Metadata["request_digest"]; ok {
			metadata["request_digest"] = digest
		}
		metadata["observation_state"] = e.job.State.String()
	}
	if e.receiptSlug != "" {
		metadata["proof_receipt_slug"] = e.receiptSlug.String()
	}
	if e.policyVersion != "" {
		metadata["policy_version"] = e.policyVersion
	}
	if e.enabled != nil {
		metadata["enabled"] = *e.enabled
	}
	return eventrecorder.Record{
		SlugPrefix: "evt-proof-observation", EventClass: model.EventClassSecurityAudit,
		EntityType: model.EventEntityTypeProjectEnvironment, EntitySlug: e.scope.Environment,
		Action: e.action, SchemaVersion: model.CurrentEventSchemaVersion, Metadata: metadata,
	}, nil
}

func (s *LiveCompiler) audit(ctx context.Context, actor slug.Slug, event observationAudit) error {
	if _, err := s.events.Record(ctx, event.scope.Organization, model.UserEventActor(actor), event); err != nil {
		return ErrObservationAuditUnavailable
	}
	return nil
}

func (s *LiveCompiler) auditRejectedControl(ctx context.Context, scope ObservationScope, actor slug.Slug, action model.EventAction, cause error) error {
	// A rejected transaction must not erase its denial audit. The independent
	// bounded transaction also survives the requesting client's disconnect.
	ctx, cancel := context.WithTimeout(postgres.WithoutTransaction(context.WithoutCancel(ctx)), 2*time.Second)
	defer cancel()
	if err := s.base.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		return s.audit(ctx, actor, observationAudit{scope: scope, action: action, limited: xerror.Is(cause, proofevidencesvc.ErrAdmissionLimit)})
	}); err != nil {
		return ErrObservationAuditUnavailable
	}
	return cause
}

type observationAccessRejected struct {
	scope    ObservationScope
	action   model.Action
	resource model.Resource
}

func (e observationAccessRejected) EventRecord() (eventrecorder.Record, error) {
	record, err := (observationAudit{scope: e.scope, action: model.EventActionDecline}).EventRecord()
	record.Metadata["requested_action"] = e.action.String()
	record.Metadata["resource"] = e.resource.String()
	return record, err
}

// AuditObservationAccessRejected records the HTTP authorization boundary without
// constructing the admitted compiler or reading an observation target. The caller
// keeps its original denial response regardless of audit persistence outcome.
func AuditObservationAccessRejected(ctx context.Context, events eventssvc.Service, scope ObservationScope, actor model.EventActor, action model.Action, resource model.Resource) (retErr error) {
	defer func() {
		outcome := model.ProofControlOutcomeDenied
		if retErr != nil {
			outcome = model.ProofControlOutcomeAuditUnavailable
		}
		metrics.RecordProofControl(model.EventActionDecline, outcome)
	}()
	if events == nil || !action.IsAAction() || action == model.ActionUnknown || !resource.IsAResource() || resource == model.ResourceUnknown {
		return ErrObservationAuditUnavailable
	}
	ctx, cancel := context.WithTimeout(postgres.WithoutTransaction(context.WithoutCancel(ctx)), 2*time.Second)
	defer cancel()
	if _, err := eventrecorder.New(events).Record(ctx, scope.Organization, actor, observationAccessRejected{scope: scope, action: action, resource: resource}); err != nil {
		return ErrObservationAuditUnavailable
	}
	return nil
}

// AuditReceiptRead runs before the HTTP response exposes the protected receipt.
func AuditReceiptRead(ctx context.Context, events eventssvc.Service, scope ObservationScope, actor model.EventActor, receipt slug.Slug) (retErr error) {
	defer func() { recordObservationControl(model.EventActionRead, retErr) }()
	if events == nil {
		return ErrObservationAuditUnavailable
	}
	if _, err := eventrecorder.New(events).Record(ctx, scope.Organization, actor, observationAudit{scope: scope, action: model.EventActionRead, accepted: true, receiptSlug: receipt}); err != nil {
		return ErrObservationAuditUnavailable
	}
	return nil
}

// Record only the returned control result: an audit record can still roll back
// with its enclosing transaction and must not count as an accepted admission.
func recordObservationControl(action model.EventAction, err error) {
	outcome := model.ProofControlOutcomeError
	switch {
	case err == nil:
		outcome = model.ProofControlOutcomeAccepted
	case xerror.Is(err, ErrObservationAuditUnavailable):
		outcome = model.ProofControlOutcomeAuditUnavailable
	case xerror.Is(err, proofevidencesvc.ErrAdmissionLimit):
		outcome = model.ProofControlOutcomeLimited
	default:
		var typed *xerror.Error
		if xerror.As(err, &typed) {
			switch typed.Kind() {
			case xerror.KindInvalidInput, xerror.KindUnauthorized, xerror.KindForbidden, xerror.KindNotFound:
				outcome = model.ProofControlOutcomeDenied
			default:
				outcome = model.ProofControlOutcomeError
			}
		}
	}
	metrics.RecordProofControl(action, outcome)
}
