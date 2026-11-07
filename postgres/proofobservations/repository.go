package proofobservations

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = xerror.NotFound(xerror.Pipeline).Detail("Proof observation was not found")

type Repository struct{}

func New() *Repository { return &Repository{} }

func (*Repository) SavePolicy(ctx context.Context, db postgres.DBTX, scope Scope, policy proofevidence.Policy, identity string) (*Policy, error) {
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	row, err := dbgen.New(db).SaveProofObservationPolicy(ctx, dbgen.SaveProofObservationPolicyParams{
		OrgSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment,
		Version: policy.Version, Enabled: policy.Enabled, WorkerIdentity: identity, Policy: raw,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return decodePolicy(row)
}

func (*Repository) LockPolicy(ctx context.Context, db postgres.DBTX, scope Scope) (*Policy, error) {
	row, err := dbgen.New(db).LockProofObservationPolicy(ctx, dbgen.LockProofObservationPolicyParams{OrgSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment})
	if err != nil {
		return nil, mapError(err)
	}
	return decodePolicy(row)
}

// LockPolicyForCleanup includes deleted environments so deletion cannot prevent
// retention enforcement.
func (*Repository) LockPolicyForCleanup(ctx context.Context, db postgres.DBTX, scope Scope) (*Policy, error) {
	row, err := dbgen.New(db).LockProofObservationPolicyForCleanup(ctx, dbgen.LockProofObservationPolicyForCleanupParams{OrgSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment})
	if err != nil {
		return nil, mapError(err)
	}
	return decodePolicy(row)
}

func (*Repository) CancelObsolete(ctx context.Context, db postgres.DBTX, orgID int32, environmentID int64) ([]JobReference, error) {
	rows, err := dbgen.New(db).CancelObsoleteProofObservationJobs(ctx, dbgen.CancelObsoleteProofObservationJobsParams{OrganizationID: orgID, ProjectEnvironmentID: environmentID})
	if err != nil {
		return nil, mapError(err)
	}
	refs := make([]JobReference, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, JobReference{Slug: row.Slug, RequestDigest: row.RequestDigest})
	}
	return refs, nil
}

func (*Repository) Find(ctx context.Context, db postgres.DBTX, org, job slug.Slug) (*Job, error) {
	row, err := dbgen.New(db).FindProofObservationJob(ctx, dbgen.FindProofObservationJobParams{OrgSlug: org, JobSlug: job})
	if err != nil {
		return nil, mapError(err)
	}
	return decodeJob(row)
}

func (*Repository) Budget(ctx context.Context, db postgres.DBTX, policy *Policy, request proofevidence.Request) (Budget, error) {
	row, err := dbgen.New(db).ProofObservationBudgetUsage(ctx, dbgen.ProofObservationBudgetUsageParams{OrganizationID: policy.OrganizationID, ProjectEnvironmentID: policy.ProjectEnvironmentID, ConnectorType: request.ConnectorType.String(), Operation: request.Operation})
	return Budget{ActiveJobs: row.ActiveJobs, Requests: row.Requests, Bytes: row.Bytes, Rows: row.Rows, Cost: row.Cost}, mapError(err)
}

func (*Repository) IsActive(ctx context.Context, db postgres.DBTX, job *Job) (bool, error) {
	active, err := dbgen.New(db).IsProofObservationActive(ctx, dbgen.IsProofObservationActiveParams{
		OrgSlug: job.Request.OrganizationSlug, JobSlug: job.Slug, RequestDigest: job.RequestDigest,
	})
	return active, mapError(err)
}

func (*Repository) Create(ctx context.Context, db postgres.DBTX, policy *Policy, request proofevidence.Request, authorization proofevidence.Authorization, ciphertext []byte, deadline time.Time) (*Job, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	decision, err := json.Marshal(authorization)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	digest, err := request.Digest()
	if err != nil {
		return nil, err
	}
	row, err := dbgen.New(db).CreateProofObservationJob(ctx, dbgen.CreateProofObservationJobParams{
		JobSlug: request.JobID, RequestDigest: digest, Request: raw, Authorization: decision, TargetCiphertext: ciphertext,
		PolicyVersion: request.PolicyVersion, WorkerIdentity: request.Worker.Identity, Operation: request.Operation,
		ReservedRequests: request.Limits.MaxRequests, ReservedBytes: request.Limits.MaxBytes, ReservedRows: request.Limits.MaxRows, ReservedCost: request.Limits.MaxCostUnits,
		BudgetExpiresAt: postgres.ToTimestamptz(deadline.Add(request.Limits.RateWindow)), DeadlineAt: postgres.ToTimestamptz(deadline),
		TimeoutMicroseconds: time.Until(deadline).Microseconds(),
		EvidenceRetainUntil: postgres.ToTimestamptz(request.EvidenceRetainUntil), MetadataRetainUntil: postgres.ToTimestamptz(request.MetadataRetainUntil),
		OrgSlug: request.OrganizationSlug, CandidateSlug: request.CandidateSlug, HeadSha: request.Candidate.HeadSHA, SpecHash: request.Candidate.SpecHash,
		ProjectID: policy.ProjectID, ProjectEnvironmentID: policy.ProjectEnvironmentID, DataSourceSlug: request.DataSourceSlug, ConnectorType: request.ConnectorType.String(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return decodeJob(row)
}

func (*Repository) Cancel(ctx context.Context, db postgres.DBTX, org, job slug.Slug) error {
	_, err := dbgen.New(db).CancelProofObservationJob(ctx, dbgen.CancelProofObservationJobParams{OrgSlug: org, JobSlug: job})
	return mapError(err)
}

func (*Repository) Expire(ctx context.Context, db postgres.DBTX, orgID int32, environmentID int64) ([]JobReference, error) {
	rows, err := dbgen.New(db).ExpireProofObservationJobs(ctx, dbgen.ExpireProofObservationJobsParams{OrganizationID: orgID, ProjectEnvironmentID: environmentID})
	if err != nil {
		return nil, mapError(err)
	}
	references := make([]JobReference, 0, len(rows))
	for _, row := range rows {
		references = append(references, JobReference{Slug: row.Slug, RequestDigest: row.RequestDigest})
	}
	return references, nil
}

func (*Repository) PurgeSnapshots(ctx context.Context, db postgres.DBTX, orgID int32, environmentID int64) ([]JobReference, error) {
	rows, err := dbgen.New(db).PurgeProofObservationSnapshots(ctx, dbgen.PurgeProofObservationSnapshotsParams{OrganizationID: orgID, ProjectEnvironmentID: environmentID})
	if err != nil {
		return nil, mapError(err)
	}
	references := make([]JobReference, 0, len(rows))
	for _, row := range rows {
		references = append(references, JobReference{Slug: row.Slug, RequestDigest: row.RequestDigest})
	}
	return references, nil
}

func (*Repository) DeleteExpired(ctx context.Context, db postgres.DBTX, orgID int32, environmentID int64) ([]JobReference, error) {
	rows, err := dbgen.New(db).DeleteExpiredProofObservationJobs(ctx, dbgen.DeleteExpiredProofObservationJobsParams{OrganizationID: orgID, ProjectEnvironmentID: environmentID})
	if err != nil {
		return nil, mapError(err)
	}
	references := make([]JobReference, 0, len(rows))
	for _, row := range rows {
		references = append(references, JobReference{Slug: row.Slug, RequestDigest: row.RequestDigest})
	}
	return references, nil
}

func (*Repository) Start(ctx context.Context, db postgres.DBTX, job *Job) (bool, error) {
	n, err := dbgen.New(db).StartProofObservation(ctx, dbgen.StartProofObservationParams{
		OrgSlug: job.Request.OrganizationSlug, JobSlug: job.Slug, RequestDigest: job.RequestDigest,
	})
	return n == 1, mapError(err)
}

func (*Repository) Finish(ctx context.Context, db postgres.DBTX, job *Job, result proofevidence.Result, snapshot []byte) (bool, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	n, err := dbgen.New(db).FinishProofObservation(ctx, dbgen.FinishProofObservationParams{
		OrgSlug: job.Request.OrganizationSlug, JobSlug: job.Slug, RequestDigest: job.RequestDigest,
		Observation: raw, ProtectedSnapshot: snapshot,
	})
	return n == 1, mapError(err)
}

func (*Repository) FinishInterrupted(ctx context.Context, db postgres.DBTX, job *Job) (bool, error) {
	n, err := dbgen.New(db).FinishInterruptedProofObservation(ctx, dbgen.FinishInterruptedProofObservationParams{
		OrgSlug: job.Request.OrganizationSlug, JobSlug: job.Slug, RequestDigest: job.RequestDigest,
	})
	return n == 1, mapError(err)
}

func decodePolicy(row dbgen.ProofObservationPolicy) (*Policy, error) {
	var value proofevidence.Policy
	if err := json.Unmarshal(row.Policy, &value); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if value.Enabled != row.Enabled || value.Version != row.Version {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return &Policy{OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, ProjectEnvironmentID: row.ProjectEnvironmentID, WorkerIdentity: row.WorkerIdentity, Value: value}, nil
}

func decodeJob(row dbgen.ProofObservationJob) (*Job, error) {
	state, err := StateString(row.State)
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("invalid Proof observation state")
	}
	job := &Job{CancellationRequested: row.CanceledAt.Valid, Slug: row.Slug, State: state, RequestDigest: row.RequestDigest, TargetCiphertext: row.TargetCiphertext, SnapshotCiphertext: row.SnapshotCiphertext, Deadline: row.DeadlineAt.Time, StartedAt: row.StartedAt.Time}
	for _, item := range []struct {
		raw    []byte
		target any
	}{{row.Request, &job.Request}, {row.AuthorizationDecision, &job.Authorization}, {row.Result, &job.Result}} {
		if len(item.raw) > 0 {
			if err := json.Unmarshal(item.raw, item.target); err != nil {
				return nil, xerror.Unexpected().With(err)
			}
		}
	}
	return job, nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return xerror.Unexpected().With(err)
}

func (*Repository) FindScoped(ctx context.Context, db postgres.DBTX, scope Scope, job slug.Slug) (*Job, error) {
	row, err := dbgen.New(db).FindScopedProofObservationJob(ctx, dbgen.FindScopedProofObservationJobParams{OrgSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, JobSlug: job})
	if err != nil {
		return nil, mapError(err)
	}
	return decodeJob(row)
}

func (*Repository) FindSlugByDigest(ctx context.Context, db postgres.DBTX, org slug.Slug, digest string) (slug.Slug, error) {
	job, err := dbgen.New(db).FindProofObservationSlugByDigest(ctx, dbgen.FindProofObservationSlugByDigestParams{OrgSlug: org, RequestDigest: digest})
	return job, mapError(err)
}

func (*Repository) ListMaintenanceScopes(ctx context.Context, db postgres.DBTX, after MaintenanceScope) ([]MaintenanceScope, error) {
	rows, err := dbgen.New(db).ListProofObservationMaintenanceScopes(ctx, dbgen.ListProofObservationMaintenanceScopesParams{AfterOrganizationID: after.OrganizationID, AfterProjectEnvironmentID: after.ProjectEnvironmentID})
	if err != nil {
		return nil, mapError(err)
	}
	scopes := make([]MaintenanceScope, 0, len(rows))
	for _, row := range rows {
		scopes = append(scopes, MaintenanceScope{Scope: Scope{Organization: row.OrganizationSlug, Project: row.ProjectSlug, Environment: row.EnvironmentSlug}, OrganizationID: row.OrganizationID, ProjectEnvironmentID: row.ProjectEnvironmentID, Disabled: row.Disabled})
	}
	return scopes, nil
}

func (*Repository) LockMaintenanceScope(ctx context.Context, db postgres.DBTX, scope Scope) (*MaintenanceScope, error) {
	row, err := dbgen.New(db).LockProofObservationMaintenanceScope(ctx, dbgen.LockProofObservationMaintenanceScopeParams{OrgSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment})
	if err != nil {
		return nil, mapError(err)
	}
	return &MaintenanceScope{Scope: scope, OrganizationID: row.OrganizationID, ProjectEnvironmentID: row.ProjectEnvironmentID, Disabled: row.Disabled}, nil
}

func (*Repository) DisablePolicy(ctx context.Context, db postgres.DBTX, orgID int32, environmentID int64) error {
	return mapError(dbgen.New(db).DisableProofObservationPolicy(ctx, dbgen.DisableProofObservationPolicyParams{OrganizationID: orgID, ProjectEnvironmentID: environmentID}))
}

// MaintenanceHealth is an operator aggregate, not a tenant-scoped data-access path.
func (*Repository) MaintenanceHealth(ctx context.Context, db postgres.DBTX) (model.ProofMaintenanceHealth, error) {
	row, err := dbgen.New(db).ProofObservationMaintenanceHealth(ctx)
	if err != nil {
		return model.ProofMaintenanceHealth{}, mapError(err)
	}
	return model.ProofMaintenanceHealth{LastSuccessTimestamp: row.LastSuccessTimestamp, RecentFailures: row.RecentFailures, CancellationDue: row.CancellationDue, ExpiryDue: row.ExpiryDue, EvidenceDue: row.EvidenceDue, MetadataDue: row.MetadataDue}, nil
}

// Activity returns bounded platform aggregates, without job or provider identifiers.
func (*Repository) Activity(ctx context.Context, db postgres.DBTX) (proofevidence.ActivitySnapshot, error) {
	rows, err := dbgen.New(db).ProofObservationActivity(ctx)
	if err != nil {
		return proofevidence.ActivitySnapshot{}, mapError(err)
	}
	activity := proofevidence.ActivitySnapshot{Results: make(map[proofevidence.Outcome]int64)}
	for _, row := range rows {
		activity.Starts = row.Starts
		if row.Results == 0 {
			continue
		}
		outcome, err := proofevidence.OutcomeString(row.Outcome)
		if err != nil {
			outcome = proofevidence.OutcomeUnknown
		}
		activity.Results[outcome] += row.Results
	}
	return activity, nil
}
