package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	bqconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	mysqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	pgconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/gofiber/fiber/v3"
)

func mapProofObservationError(err error) error {
	for _, unavailable := range []*xerror.Error{proofreceiptsvc.ErrObservationAuditUnavailable, proofevidencesvc.ErrAdmissionLimit} {
		if xerror.Is(err, unavailable) {
			return apierror.New(fiber.StatusServiceUnavailable, unavailable.Code(), unavailable.Message())
		}
	}
	return mapServiceError(err)
}

func (s *server) observationScope(c fiber.Ctx, org, project, environment string, action model.Action, resource model.Resource) (proofreceiptsvc.ObservationScope, slug.Slug, error) {
	c.Set("Cache-Control", "no-store")
	if s.proofObservationService == nil {
		return proofreceiptsvc.ObservationScope{}, "", fiber.ErrNotFound
	}
	o, p, e, err := parseOrgProjectEnvironmentSlugs(org, project, environment)
	if err != nil {
		return proofreceiptsvc.ObservationScope{}, "", err
	}
	actor, err := s.canOrgUserPrincipal(c, o, action, resource)
	scope := proofreceiptsvc.ObservationScope{Organization: o, Project: p, Environment: e}
	if err != nil {
		s.recordObservationAccessRejection(c, scope, action, resource)
	}
	return scope, actor, err
}

func (s *server) recordObservationAccessRejection(c fiber.Ctx, scope proofreceiptsvc.ObservationScope, action model.Action, resource model.Resource) {
	c.Set("Cache-Control", "no-store")
	actor := s.eventActor(c)
	if actor.Slug == nil || *actor.Slug == "" {
		actor = model.ExternalEventActor()
	}
	if err := proofreceiptsvc.AuditObservationAccessRejected(c.Context(), s.eventsService, scope, actor, action, resource); err != nil {
		// Preserve the original denial even for a nonexistent requested tenant;
		// audit-store availability must not become an organization-existence oracle.
		slog.ErrorContext(c.Context(), "Proof observation access audit unavailable")
	}
}

func (s *server) canObservationResource(c fiber.Ctx, scope proofreceiptsvc.ObservationScope, action model.Action, resource model.Resource) error {
	if err := s.canOrg(c, scope.Organization, action, resource); err != nil {
		s.recordObservationAccessRejection(c, scope, action, resource)
		return err
	}
	return nil
}

func (s *server) GetProofObservationPolicy(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Read, model.ResourceEnvironments)
	if err != nil {
		return err
	}
	policy, err := s.proofObservationService.GetObservationPolicy(c.Context(), scope, actor)
	if err != nil {
		return mapProofObservationError(err)
	}
	return c.JSON(toAPIObservationPolicy(policy))
}

func (s *server) SetProofObservationPolicy(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Update, model.ResourceEnvironments)
	if err != nil {
		return err
	}
	var body api.ProofObservationPolicyInput
	if c.Bind().Body(&body) != nil {
		return apierror.InvalidRequestBody()
	}
	policy, err := s.proofObservationService.SetObservationPolicy(c.Context(), scope, actor, proofreceiptsvc.ObservationPolicySettings{Enabled: body.Enabled, TimeoutSeconds: body.TimeoutSeconds, EvidenceTTLSeconds: body.EvidenceTtlSeconds, MaxRequests: body.MaxRequests, MaxRows: body.MaxRows, MaxBytes: body.MaxBytes})
	if err != nil {
		return mapProofObservationError(err)
	}
	return c.JSON(toAPIObservationPolicy(policy))
}

func (s *server) PrepareProofObservation(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Execute, model.ResourcePipelines)
	if err != nil {
		return err
	}
	if err := s.canObservationResource(c, scope, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canObservationResource(c, scope, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	var body api.ProofObservationPreviewInput
	if c.Bind().Body(&body) != nil {
		return apierror.InvalidRequestBody()
	}
	candidate, err := slug.New(body.CandidateSlug)
	if err != nil {
		return apierror.ValidationFailed("invalid Proof candidate slug")
	}
	step, err := slug.New(body.StepSlug)
	if err != nil {
		return apierror.ValidationFailed("invalid Proof step slug")
	}
	target, err := proofObservationTargetPlan(body.Target)
	if err != nil {
		return err
	}
	preview, err := s.proofObservationService.PrepareObservation(c.Context(), scope, actor, candidate, step, target)
	if err != nil {
		return mapProofObservationError(err)
	}
	c.Set("Cache-Control", "no-store")
	r := preview.Request
	return c.JSON(api.ProofObservationPreview{IntentToken: preview.IntentToken, RequestDigest: preview.RequestDigest, Candidate: toAPIProofCandidateIdentity(r.Candidate), Claim: api.ProofClaimIdentity{Family: api.ProofClaimFamily(r.Claim.Family.String()), PipelineVersionHash: r.Claim.PipelineVersionHash, SemanticPath: r.Claim.SemanticPath}, Operation: api.ProofObservationPreviewOperation(r.Operation), MetadataExpiresAt: r.MetadataRetainUntil, RateWindowSeconds: int(r.Limits.RateWindow / time.Second), Target: body.Target, DataSourceSlug: r.DataSourceSlug.String(), CredentialVersion: r.CredentialVersion, PolicyVersion: r.PolicyVersion, ExpiresAt: preview.ExpiresAt, EvidenceExpiresAt: r.EvidenceRetainUntil, TimeoutSeconds: int(r.Limits.Timeout / time.Second), MaxRows: r.Limits.MaxRows, MaxBytes: r.Limits.MaxBytes, MaxRequests: r.Limits.MaxRequests})
}

func proofObservationTargetPlan(target api.ProofObservationTarget) (proofevidence.MetadataPlan, error) {
	invalid := apierror.ValidationFailed("invalid Proof observation target")
	raw, err := target.MarshalJSON()
	if err != nil {
		return proofevidence.MetadataPlan{}, invalid
	}
	kind, err := target.Discriminator()
	if err != nil {
		return proofevidence.MetadataPlan{}, invalid
	}
	var observer proofevidence.MetadataObserver
	switch kind {
	case "hubspot":
		var resource api.ProofHubSpotTarget
		if !decodeProofTarget(raw, &resource, "connector_type", "account_id") {
			return proofevidence.MetadataPlan{}, invalid
		}
		observer = &hubspot.ContactMetadataObserver{AccountID: resource.AccountId}
	case "bigquery":
		var resource api.ProofBigQueryTarget
		if !decodeProofTarget(raw, &resource, "connector_type", "project_id", "dataset_id", "table_id") {
			return proofevidence.MetadataPlan{}, invalid
		}
		observer = &bqconnector.TableMetadataObserver{ProjectID: resource.ProjectId, DatasetID: resource.DatasetId, TableID: resource.TableId}
	case "mongodb":
		var resource api.ProofMongoDBTarget
		if !decodeProofTarget(raw, &resource, "connector_type", "host", "port", "database", "collection") || resource.Port < 1 || resource.Port > 65535 {
			return proofevidence.MetadataPlan{}, invalid
		}
		observer = &mongoconnector.CollectionMetadataObserver{Host: resource.Host, Port: uint16(resource.Port), Database: resource.Database, Collection: resource.Collection}
	case "s3":
		var resource api.ProofS3Target
		if !decodeProofTarget(raw, &resource, "connector_type", "endpoint", "region", "bucket", "key") {
			return proofevidence.MetadataPlan{}, invalid
		}
		observer = &s3connector.ObjectMetadataObserver{Endpoint: resource.Endpoint, Region: resource.Region, Bucket: resource.Bucket, Key: resource.Key}
	case "postgres":
		var resource api.ProofPostgresTarget
		if !decodeProofTarget(raw, &resource, "connector_type", "host", "port", "database", "schema", "table") || resource.Port < 1 || resource.Port > 65535 {
			return proofevidence.MetadataPlan{}, invalid
		}
		observer = &pgconnector.TableMetadataObserver{TableMetadataResource: sqlconnector.TableMetadataResource{Host: resource.Host, Port: uint16(resource.Port), Database: resource.Database, Schema: resource.Schema, Table: resource.Table}}
	case "mysql":
		var resource api.ProofMySQLTarget
		if !decodeProofTarget(raw, &resource, "connector_type", "host", "port", "database", "schema", "table") || resource.Port < 1 || resource.Port > 65535 {
			return proofevidence.MetadataPlan{}, invalid
		}
		observer = &mysqlconnector.TableMetadataObserver{TableMetadataResource: sqlconnector.TableMetadataResource{Host: resource.Host, Port: uint16(resource.Port), Database: resource.Database, Schema: resource.Schema, Table: resource.Table}}
	case "google_workspace":
		workspace, err := target.AsProofWorkspaceTarget()
		if err != nil {
			return proofevidence.MetadataPlan{}, invalid
		}
		kind, err := workspace.Discriminator()
		if err != nil {
			return proofevidence.MetadataPlan{}, invalid
		}
		switch kind {
		case "spreadsheet":
			var resource api.ProofWorksheetTarget
			if !decodeProofTarget(raw, &resource, "connector_type", "resource", "spreadsheet_id", "worksheet_id", "range") {
				return proofevidence.MetadataPlan{}, invalid
			}
			observer = &googleworkspace.WorksheetMetadataObserver{SpreadsheetID: resource.SpreadsheetId, WorksheetID: resource.WorksheetId, Range: resource.Range}
		case "file", "document", "presentation":
			var resource api.ProofDriveFileTarget
			if !decodeProofTarget(raw, &resource, "connector_type", "resource", "file_id", "operation") {
				return proofevidence.MetadataPlan{}, invalid
			}
			nativeResource := kind
			if kind == "file" {
				nativeResource = ""
			}
			observer = &googleworkspace.FileMetadataObserver{Resource: nativeResource, FileID: resource.FileId, Operation: string(resource.Operation)}
		default:
			return proofevidence.MetadataPlan{}, invalid
		}
	default:
		return proofevidence.MetadataPlan{}, invalid
	}
	plan, err := observer.Plan()
	if err != nil {
		return proofevidence.MetadataPlan{}, invalid
	}
	return plan, nil
}

// Generated unions preserve raw JSON; enforce each concrete variant before admission.
func decodeProofTarget(raw []byte, target any, required ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, field := range required {
		if len(fields[field]) == 0 || bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}

func (s *server) AuthorizeProofObservation(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Execute, model.ResourcePipelines)
	if err != nil {
		return err
	}
	if err := s.canObservationResource(c, scope, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canObservationResource(c, scope, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	var body api.ProofObservationIntent
	if c.Bind().Body(&body) != nil {
		return apierror.InvalidRequestBody()
	}
	job, err := s.proofObservationService.AuthorizeObservation(c.Context(), scope, actor, body.IntentToken, body.RequestDigest)
	if err != nil {
		return mapProofObservationError(err)
	}
	return writeObservationJob(c, job, fiber.StatusOK)
}

func toAPIObservationPolicy(policy *proofreceiptsvc.ObservationPolicy) api.ProofObservationPolicy {
	p := policy.Settings
	return api.ProofObservationPolicy{Version: policy.Version, Settings: api.ProofObservationPolicyInput{Enabled: p.Enabled, TimeoutSeconds: p.TimeoutSeconds, EvidenceTtlSeconds: p.EvidenceTTLSeconds, MaxRequests: p.MaxRequests, MaxRows: p.MaxRows, MaxBytes: p.MaxBytes}}
}

func (s *server) GetProofObservation(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam, observation api.ProofObservationSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Read, model.ResourcePipelines)
	if err != nil {
		return err
	}
	jobSlug, err := slug.New(observation)
	if err != nil {
		return apierror.ValidationFailed("invalid Proof observation slug")
	}
	job, err := s.proofObservationService.GetObservation(c.Context(), scope, actor, jobSlug)
	if err != nil {
		return mapProofObservationError(err)
	}
	return writeObservationJob(c, job, fiber.StatusOK)
}

func (s *server) CancelProofObservation(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam, observation api.ProofObservationSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Execute, model.ResourcePipelines)
	if err != nil {
		return err
	}
	jobSlug, err := slug.New(observation)
	if err != nil {
		return apierror.ValidationFailed("invalid Proof observation slug")
	}
	job, err := s.proofObservationService.CancelObservation(c.Context(), scope, actor, jobSlug)
	if err != nil {
		return mapProofObservationError(err)
	}
	return writeObservationJob(c, job, fiber.StatusOK)
}

func (s *server) CompileObservedProofReceipt(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam) error {
	scope, actor, err := s.observationScope(c, org, project, environment, model.Execute, model.ResourcePipelines)
	if err != nil {
		return err
	}
	if err := s.canObservationResource(c, scope, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canObservationResource(c, scope, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	var body api.CompileObservedProofReceiptInput
	if c.Bind().Body(&body) != nil {
		return apierror.InvalidRequestBody()
	}
	candidate, err := slug.New(body.CandidateSlug)
	if err != nil {
		return apierror.ValidationFailed("invalid Proof candidate slug")
	}
	jobs := make([]slug.Slug, len(body.ObservationSlugs))
	for i, raw := range body.ObservationSlugs {
		jobs[i], err = slug.New(raw)
		if err != nil {
			return apierror.ValidationFailed("invalid Proof observation slug")
		}
	}
	receipt, err := s.proofObservationService.CompileObserved(c.Context(), scope, actor, candidate, jobs)
	if err != nil {
		return mapProofObservationError(err)
	}
	// Read the current projection so replaying old evidence cannot hide supersession.
	detail, err := s.proofObservationService.Get(c.Context(), scope.Organization, scope.Project, scope.Environment, receipt.Value.ID)
	if err != nil {
		return mapProofObservationError(err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(toAPIProofReceipt(detail))
}

func writeObservationJob(c fiber.Ctx, job *proofreceiptsvc.ObservationJob, status int) error {
	result := api.ProofObservationJob{Slug: job.Slug.String(), State: api.ProofObservationJobState(job.State), RequestDigest: job.RequestDigest, DeadlineAt: job.Deadline, CancellationRequested: job.CancellationRequested}
	if job.Outcome != nil {
		outcome := api.ProofObservationOutcome(job.Outcome.String())
		result.Outcome = &outcome
	}
	c.Set("Cache-Control", "no-store")
	return c.Status(status).JSON(result)
}
