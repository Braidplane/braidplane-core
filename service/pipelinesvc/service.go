package pipelinesvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	pipelineformat "github.com/Braidplane/braidplane-core/pkg/pipeline/format"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/proofidentity"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	structvalidate "github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	pipelinevalidationsrepo "github.com/Braidplane/braidplane-core/postgres/pipelinevalidations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var ErrPipelineNotFound = xerror.NotFound(xerror.Pipeline)

var ErrPipelineAlreadyExists = xerror.AlreadyExists(xerror.Pipeline)

var ErrPipelineRunNotFound = xerror.NotFound(xerror.PipelineRun)

// ErrPipelineExecutionNotStarted marks failures known to precede connector execution.
var ErrPipelineExecutionNotStarted = xerror.New(xerror.KindInternal, "PIPELINE_EXECUTION_NOT_STARTED", "pipeline execution not started")

var ErrPipelineValidationResultNotFound = xerror.NotFound(xerror.PipelineValidationResult)

var ErrPipelineValidationPrincipalRequired = xerror.New(
	xerror.KindUnauthorized,
	"PIPELINE_VALIDATION_PRINCIPAL_REQUIRED",
	"pipeline validation history requires an authenticated principal",
)

var ErrPipelineVersionNotFound = xerror.NotFound(xerror.PipelineVersion)

var ErrPipelineDeploymentNotFound = xerror.NotFound(xerror.PipelineDeployment)

var ErrProjectEnvironmentNotFound = xerror.NotFound(xerror.ProjectEnvironment)

var ErrPipelineDraftNotFound = xerror.NotFound(xerror.PipelineDraft)

var ErrPipelineDraftConflict = xerror.New(
	xerror.KindAlreadyExists,
	"PIPELINE_DRAFT_CONFLICT",
	"pipeline draft version conflict",
)

var ErrPipelineDeploymentConflict = xerror.New(
	xerror.KindAlreadyExists,
	"PIPELINE_DEPLOYMENT_CONFLICT",
	"pipeline deployment conflict",
)

var ErrProofReceiptLinkMismatch = xerror.New(
	xerror.KindInvalidInput,
	"PROOF_RECEIPT_LINK_MISMATCH",
	"proof receipt does not match the exact deployment target",
)

var ErrPipelineGitSourceRequired = xerror.New(
	xerror.KindInvalidInput,
	"PIPELINE_GIT_SOURCE_REQUIRED",
	"pipeline git source is required",
)

var ErrPipelineProposalAuthorityDenied = jobs.ErrPipelineProposalAuthorityDenied

var ErrPipelineExecutionFailed = xerror.ExecutionFailed()

const (
	shortSpecHashLength = 12

	pipelineRunStatusQueued    = "queued"
	pipelineRunStatusRunning   = "running"
	pipelineRunStatusSucceeded = "succeeded"
	pipelineRunStatusFailed    = "failed"

	pipelineRunEventQueued    = "run_queued"
	pipelineRunEventStarted   = "run_started"
	pipelineRunEventCompleted = "run_completed"

	pipelineRunFailureKindPlatform = "platform"
)

// Service exposes pipelines operations
type Service interface {
	AcceptQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinerun.RunSourceSummary, time.Time, int64) error
	StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error
	ReconcileInterruptedRuns(context.Context, time.Time) error
	FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error
	GetRunCompletion(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineRunCompletion, error)
	AcceptCompletionRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error)
	AcceptCompletionRetryRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot, slug.Slug, pipelinerun.RunSourceSummary, time.Time) (jobs.PipelineRunExecutionArgs, error)
	ExecuteCompletionPipelineRun(context.Context, jobs.PipelineRunExecutionArgs, CompletionAuthorityChecker) error
	ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error
	BootstrapCheckpoints(ctx context.Context, orgSlug, projectSlug, environmentSlug, pipelineSlug, requestSlug slug.Slug, confirmation *slug.Slug, actor model.EventActor, input map[string]any, seeds map[slug.Slug]json.RawMessage) error

	RecoverRun(ctx context.Context, orgSlug, projectSlug, environmentSlug, sourceRunSlug slug.Slug, confirmation *slug.Slug, actor model.EventActor, source pipelinerun.RunSourceSummary) (*PipelineRunDetail, error)
	List(
		ctx context.Context,
		orgSlug, projectSlug, memberSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*PipelineSummary], error)
	Get(ctx context.Context, orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug) (*PipelineDetail, error)
	GetDraft(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*PipelineDraftDetail, error)
	SaveDraft(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
		pipeline *pipelinespec.Pipeline,
		expectedSpecHash *string,
	) (*PipelineDraftDetail, error)
	PublishDraft(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
		expectedSpecHash *string,
		force bool,
	) (*PipelineDetail, error)
	AuthorizeProposalPublish(ctx context.Context, args jobs.PipelineGitPublishProposalArgs) error
	RefreshDraftProposal(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
	) (*PipelineDetail, error)
	DeleteDraft(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
	) error
	ListVersions(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) ([]*PipelineVersionSummary, error)
	GetVersion(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHash string,
	) (*PipelineVersionDetail, error)
	ListDeployments(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) ([]*PipelineDeploymentDetail, error)
	GetDeployment(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*PipelineDeploymentDetail, error)
	Deploy(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
		req DeployPipelineRequest,
	) (*PipelineDeploymentDetail, error)
	Update(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
		pipeline *pipelinespec.Pipeline,
	) (*PipelineDetail, error)
	ReconcilePipelines(
		ctx context.Context,
		orgSlug, projectSlug, memberSlug slug.Slug,
		actor model.EventActor,
		req ReconcilePipelinesRequest,
	) (*ReconcilePipelinesResult, error)
	Validate(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, memberSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
	) (*PipelineValidationResult, error)
	RecordValidationResult(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal ValidationHistoryPrincipal,
		source PipelineValidationResultSource,
		pipeline *pipelinespec.Pipeline,
		result *PipelineValidationResult,
	) (*PipelineValidationResultRecord, error)
	ListValidationResults(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal ValidationHistoryPrincipal,
		filters PipelineValidationResultFilters,
		req model.ListRequest,
	) (model.ListResponse[*PipelineValidationResultRecord], error)
	GetValidationResult(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal ValidationHistoryPrincipal,
		resultSlug slug.Slug,
	) (*PipelineValidationResultRecord, error)
	RevalidatePipeline(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal ValidationHistoryPrincipal,
		pipelineSlug, memberSlug slug.Slug,
	) (*PipelineValidationResultRecord, error)
	ListDriftImpactReports(
		ctx context.Context,
		orgSlug slug.Slug,
		filters DriftImpactReportFilters,
	) ([]*DriftImpactReport, error)
	GetDriftImpactReport(ctx context.Context, orgSlug, projectSlug, reportSlug slug.Slug) (*DriftImpactReport, error)
	Execute(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*PipelineRunDetail, error)
	StartRun(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*PipelineRunDetail, error)
	StartBackfill(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
		windows map[slug.Slug]pipelinerun.IncrementalSourceWindow,
	) (*PipelineRunDetail, error)
	ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error
	ExecuteVersion(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		specHash string,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*PipelineRunDetail, error)
	ExecuteVerifiedEvent(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
		eventInputJSON json.RawMessage,
	) (*PipelineRunDetail, error)
	ExecuteVerifiedEventVersion(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		specHash string,
		runSlug *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
		eventInputJSON json.RawMessage,
	) (*PipelineRunDetail, error)
	ListRuns(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		filters *RunFilters,
		req model.ListRequest,
	) (model.ListResponse[*PipelineRunMetadata], error)
	GetRun(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		runSlug string,
	) (*PipelineRunDetail, error)
	GetRunProgress(context.Context, slug.Slug, slug.Slug, slug.Slug, string, int64, int) (*RunProgress, error)
}

// PipelineSummary contains persisted pipeline metadata used in CRUD responses.
type PipelineSummary struct {
	Slug                   slug.Slug
	Name                   string
	HasDraft               bool
	SyncStatus             pipelinesrepo.SyncStatus
	SourcePath             *string
	SourceRef              *string
	LastAppliedCommit      *string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CurrentVersionID       *int64
	CurrentVersionSpecHash *string
}

// PipelineDetail contains persisted metadata plus the parsed pipeline spec.
type PipelineDetail struct {
	Summary *PipelineSummary
	Spec    *pipelinespec.Pipeline
	Draft   *PipelineDraftDetail
}

// PipelineDraftDetail contains mutable draft spec and metadata.
type PipelineDraftDetail struct {
	PipelineSlug          slug.Slug
	InitiatingMember      slug.Slug
	proposalAuthorityID   *int64
	BasedOnSpecHash       *string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Spec                  *pipelinespec.Pipeline
	DraftBasedOnVersionID *int64
	CurrentVersionID      *int64
	DraftState            string
	DraftSpecHash         string
	Repository            string
	ProposalSupported     bool
	BaseRef               *string
	BaseCommitSHA         *string
	BranchName            *string
	BranchHeadSHA         *string
	ProposalProvider      *string
	ProposalID            *string
	ProposalURL           *string
	ProposalState         *string
	ProposalIsStale       bool
	LastProposedDraftHash *string
	QueuedAt              *time.Time
	ProposalUpdatedAt     *time.Time
	FailedAt              *time.Time
	FailureCode           *string
	FailureMessage        *string
	PublicationMode       *string
	ProviderActor         *string
	AuthoritySource       *string
}

// PipelineDraftConflictError carries structured version mismatch details.
type PipelineDraftConflictError struct {
	PipelineSlug          slug.Slug
	ExpectedVersionID     *int64
	CurrentVersionID      *int64
	DraftBasedOnVersionID *int64
	ExpectedSpecHash      *string
	CurrentSpecHash       *string
	DraftBasedOnSpecHash  *string
	DraftPreserved        bool
	CanOverwrite          bool
}

// PipelineValidationResult contains structured validation diagnostics for API and CLI callers.
type PipelineValidationResult struct {
	Valid             bool
	Diagnostics       []PipelineValidationDiagnostic
	ImpactReport      *DriftImpactReport
	DataSourceRefSync *datasourcerefssvc.PipelineDataSourceRefSyncResult
}

// PipelineValidationDiagnostic is one validation finding for a submitted pipeline spec.
type PipelineValidationDiagnostic struct {
	Severity               string
	Code                   string
	Field                  string
	StepSlug               *string
	Message                string
	DataSourceRef          *string
	DataSourceRefSlug      *string
	ResolvedDataSourceSlug *string
	EnvironmentSlug        *string
	DataSourceType         *model.DataSourceType
	ExpectedDataSourceType *model.DataSourceType
	RequiredCapability     *model.DataSourceRefCapability
}

// PipelineValidationResultRecord is a durable non-run validation result for SSR triage.
type PipelineValidationResultRecord struct {
	Slug             slug.Slug
	ProjectSlug      slug.Slug
	EnvironmentSlug  slug.Slug
	PipelineSlug     slug.Slug
	Source           PipelineValidationResultSource
	Valid            bool
	SpecHash         *string
	Diagnostics      []PipelineValidationDiagnostic
	ImpactReportSlug *slug.Slug
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// PipelineValidationResultFilters narrows validation-history reads for SSR panels and API callers.
type PipelineValidationResultFilters struct {
	PipelineSlug *slug.Slug
	Limit        int32
}

// ValidationHistoryPrincipal carries the authenticated actor used for validation-history authorization.
type ValidationHistoryPrincipal struct {
	UserSlug       slug.Slug
	APIKeySlug     slug.Slug
	APIKeyOrgSlug  slug.Slug
	APIKeyRoleSlug slug.Slug
}

type DriftImpactReport struct {
	Slug           slug.Slug
	ReportType     pipelinedrift.ReportType
	Status         pipelinedrift.ReportStatus
	Severity       pipelinedrift.ReportSeverity
	Summary        string
	Changes        []pipelinedrift.Change
	Impacts        []pipelinedrift.Impact
	ProjectSlug    *slug.Slug
	PipelineSlug   *slug.Slug
	DataSourceSlug *slug.Slug
	CreatedAt      time.Time
	SupersededAt   *time.Time
}

type DriftImpactReportFilters struct {
	ProjectSlug    *slug.Slug
	PipelineSlug   *slug.Slug
	DataSourceSlug *slug.Slug
	Status         *pipelinedrift.ReportStatus
	Severity       *pipelinedrift.ReportSeverity
	Limit          int32
}

func (e *PipelineDraftConflictError) Error() string {
	if e == nil {
		return ErrPipelineDraftConflict.Error()
	}

	return ErrPipelineDraftConflict.Detailf(
		"pipeline=%s expected_spec_hash=%s current_spec_hash=%s draft_based_on_spec_hash=%s draft_preserved=%t can_overwrite=%t",
		e.PipelineSlug,
		formatOptionalSpecHash(e.ExpectedSpecHash),
		formatOptionalSpecHash(e.CurrentSpecHash),
		formatOptionalSpecHash(e.DraftBasedOnSpecHash),
		e.DraftPreserved,
		e.CanOverwrite,
	).Error()
}

func (e *PipelineDraftConflictError) Unwrap() error {
	return ErrPipelineDraftConflict
}

// PipelineVersionSummary contains immutable version metadata used in list responses.
type PipelineVersionSummary struct {
	SpecHash      string
	ShortSpecHash string
	PipelineSlug  slug.Slug
	GitCommitSHA  *string
	CreatedAt     time.Time
}

// PipelineVersionDetail contains immutable version metadata plus the stored spec snapshot.
type PipelineVersionDetail struct {
	Summary *PipelineVersionSummary
	Spec    *pipelinespec.Pipeline
}

// DeployPipelineRequest points an environment at an existing immutable version.
type DeployPipelineRequest struct {
	ResolutionInputs                 map[string]odcs.ResolutionValue
	ExpectedRevision                 *int64
	CompiledPlanDigest               *string
	ResolutionDigest                 *string
	SpecHash                         string
	ExpectedCurrentSpecHash          *string
	SourceEnvironmentSlug            *slug.Slug
	ProtectedEnvironmentConfirmation *slug.Slug
	ProofReceiptSlug                 *slug.Slug
}

type ProofLifecycleLink struct {
	Kind        model.ProofLifecycleLinkKind
	ReceiptSlug slug.Slug
	Identifier  string
	LinkedAt    time.Time
	RetainUntil time.Time
	Status      string
}

// PipelineDeploymentDetail contains the current deployed version for one environment.
type PipelineDeploymentDetail struct {
	Revision                     int64
	CurrentResolutionDigest      *string
	PreviousResolutionDigest     *string
	ProjectSlug                  slug.Slug
	EnvironmentSlug              slug.Slug
	PipelineSlug                 slug.Slug
	CurrentVersionID             int64
	CurrentVersionSpecHash       string
	CurrentVersionShortSpecHash  string
	PreviousVersionID            *int64
	PreviousVersionSpecHash      *string
	PreviousVersionShortSpecHash *string
	DeployedByActorType          string
	DeployedByActorSlug          *string
	DeployedAt                   time.Time
	UpdatedAt                    time.Time
	DataSourceRefSync            *datasourcerefssvc.PipelineDataSourceRefSyncResult
	ProofLink                    *ProofLifecycleLink
}

// RunFilters constrains list-runs results.
type RunFilters struct {
	Status             *string
	PipelineSlug       *slug.Slug
	Mode               *string
	BackfillSeriesSlug *string
}

// PipelineRunMetadata contains persisted run summary fields used by operators.
type PipelineRunMetadata struct {
	PipelineVersionID       int64
	PipelineVersionSpecHash string
	EnvironmentSlug         string
	PipelineSlug            string
	RunSlug                 string
	Source                  pipelinerun.RunSourceSummary
	RequestID               *string
	Status                  string
	DurationMs              int64
	StepCount               int32
	StepSlug                string
	ErrorSummary            *string
	FailureKind             *string
	StartedAt               time.Time
	EndedAt                 *time.Time
	ReplayOfRunSlug         *string
	ReplayFromStep          *string
	Incremental             pipelinerun.IncrementalRunSummary
	CreatedAt               time.Time
}

// PipelineRunDetail includes metadata plus diagnostics payload for a run.
type PipelineRunDetail struct {
	Composition            *RunCompositionInspection
	Metadata               PipelineRunMetadata
	Timeline               pipelinerun.RunTimeline
	IncrementalDiagnostics pipelinerun.IncrementalRunDiagnostics
	ProofLink              *ProofLifecycleLink
}

type queryExecutor interface {
	FindBySlug(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error)
	Execute(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error)
	GetExpectedInputContract(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*payload.Contract, error)
	ExecuteInline(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
}

type runnerQueryExecutorAdapter struct {
	executor queryExecutor
}

type incrementalQueryExecutor interface {
	ExecuteIncremental(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

type incrementalInlineQueryExecutor interface {
	ExecuteInlineIncremental(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

func (a runnerQueryExecutorAdapter) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	return a.executor.Execute(
		ctx,
		orgSlug,
		dsSlug,
		querySlug,
		model.SystemEventActor(),
		paramValues,
	)
}

func (a runnerQueryExecutorAdapter) GetExpectedInputContract(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*payload.Contract, error) {
	return a.executor.GetExpectedInputContract(ctx, orgSlug, dsSlug, querySlug)
}

func (a runnerQueryExecutorAdapter) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	return a.executor.ExecuteInline(ctx, orgSlug, dsSlug, operationInline, paramValues)
}

func (a runnerQueryExecutorAdapter) ExecuteIncremental(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	executor, ok := a.executor.(incrementalQueryExecutor)
	if !ok {
		return nil, nil, xerror.Unsupported(xerror.QueryExecutor).Detail(
			"incremental query executor is required for incremental source steps",
		)
	}

	return executor.ExecuteIncremental(ctx, orgSlug, dsSlug, querySlug, paramValues, checkpoint)
}

func (a runnerQueryExecutorAdapter) ExecuteInlineIncremental(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	executor, ok := a.executor.(incrementalInlineQueryExecutor)
	if !ok {
		return nil, nil, xerror.Unsupported(xerror.QueryExecutor).Detail(
			"incremental inline query executor is required for inline incremental source steps",
		)
	}

	return executor.ExecuteInlineIncremental(ctx, orgSlug, dsSlug, operationInline, paramValues, checkpoint)
}

type pipelineRuntime interface {
	PrepareCheckpointBootstrap(ctx context.Context, req pipelineruntime.RunRequest) ([]pipelinerun.IncrementalCheckpointLookup, error)
	Recover(context.Context, pipelineruntime.ReplayRequest) (*pipelineruntime.RunOutcome, error)
	Execute(ctx context.Context, req pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error)
	PreValidateTransforms(ctx context.Context, orgSlug slug.Slug, pipeline *pipelinespec.Pipeline) error
}

type pipelineDataSourceRefResolver interface {
	ResolvePipelineDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
	) (*datasourcerefssvc.ResolvedPipeline, error)
	SyncPipelineDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		actor model.EventActor,
	) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error)
	ReconcilePipelineDataSourceRefs(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		actor model.EventActor,
	) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error)
	DeleteDataSourceRefsForPipeline(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
	) error
}

type pipelineRuntimeResolver interface {
	pipelineDataSourceRefResolver
	pipelineruntime.EnvironmentSecretResolver
}

type serviceOptions struct {
	transformLimits   transformsql.Limits
	transformExecutor pipelineruntime.TransformExecutor
}

// WithTransformExecutor routes transform materialization to an isolated executor.
func WithTransformExecutor(executor pipelineruntime.TransformExecutor) ServiceOption {
	return func(options *serviceOptions) {
		options.transformExecutor = executor
	}
}

// ServiceOption configures pipeline runtime safety boundaries.
type ServiceOption func(*serviceOptions)

// WithTransformLimits applies cumulative budgets to pipeline SQL transforms.
func WithTransformLimits(limits transformsql.Limits) ServiceOption {
	return func(options *serviceOptions) {
		options.transformLimits = limits
	}
}

type service struct {
	notificationProducer  notificationsvc.CandidateProducer
	compiledPlans         *compiledplans.Repository
	resolutions           *deploymentresolutions.Repository
	checkpoints           incrementalcheckpoints.Repository
	dbtx                  postgres.DBTX
	txm                   postgres.TransactionManager
	runRepo               pipelineruns.Repository
	pipelineStore         PipelineStore
	driftReports          driftimpactrepo.Repository
	validationResults     pipelinevalidationsrepo.Repository
	authorizationService  authorization.Service
	runtime               pipelineRuntime
	queryExecutor         queryExecutor
	dataSourceService     datasourcessvc.Service
	dataSourceRefResolver pipelineDataSourceRefResolver
	projectBackend        pipelinegit.ProjectBackendService
	gitSourceResolver     pipelinegit.SourceResolver
	pipelineGitProducer   workerproducer.PipelineGitProducer
	pipelineRunProducer   workerproducer.PipelineRunProducer
	eventProducer         workerproducer.EventProducer
	executionClock        clock.Clock
	admission             admissionsvc.Service
	proofLinks            proofreceipts.LifecycleRepository
}

var pipelineValidationStepPattern = regexp.MustCompile(`step\[(\d+)\] "([^"]+)"`)

func (s *service) List(
	ctx context.Context,
	orgSlug, projectSlug, memberSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*PipelineSummary], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	specs, err := s.pipelineStore.List(ctx, orgSlug, projectSlug, queryReq)
	if err != nil {
		return model.ListResponse[*PipelineSummary]{}, err
	}

	specs, hasMore := model.FinalizePage(specs, normalized)

	summaries := make([]*PipelineSummary, len(specs))
	for i, spec := range specs {
		summary := toPipelineSummary(spec)
		summary.HasDraft = false
		if memberSlug != "" {
			_, draftErr := s.pipelineStore.FindDraft(ctx, orgSlug, projectSlug, spec.Slug, memberSlug)
			switch {
			case draftErr == nil:
				summary.HasDraft = true
			case xerror.Is(draftErr, ErrPipelineDraftNotFound):
				// No member-scoped draft exists for this pipeline.
			default:
				return model.ListResponse[*PipelineSummary]{}, draftErr
			}
		}
		summaries[i] = summary
	}

	nextCursor, prevCursor := model.BuildBoundaryCursors(
		summaries,
		normalized,
		hasMore,
		func(item *PipelineSummary, _ string) (string, string) {
			return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)
	responseHasMore := hasMore
	if normalized.Direction == model.ListDirectionPrev &&
		normalized.Cursor != "" &&
		len(summaries) > 0 {
		responseHasMore = true
		last := summaries[len(summaries)-1]
		nextCursor = model.EncodeCursor(
			last.CreatedAt.Format(time.RFC3339Nano),
			last.Slug.String(),
		)
		if !hasMore {
			prevCursor = nil
		}
	}

	return model.ListResponse[*PipelineSummary]{
		Items:      summaries,
		HasMore:    responseHasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) Validate(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, memberSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*PipelineValidationResult, error) {
	if err := pipelinevalidate.Validate(pipeline); err != nil {
		return &PipelineValidationResult{
			Valid: false,
			Diagnostics: []PipelineValidationDiagnostic{
				pipelineValidationDiagnosticFromPipelineError(err, pipeline),
			},
		}, nil
	}

	// Compare executable contracts before resolving environment-specific data sources.
	authoredCandidate := pipeline
	runtimeCandidate, _, resolveErr := s.pipelineStore.ResolvePipelineContracts(ctx, orgSlug, projectSlug, pipeline)
	if resolveErr != nil {
		if !xerror.Is(resolveErr, ErrODCSContractReference) {
			return nil, resolveErr
		}
		var referenceError *odcsContractReferenceError
		if xerror.As(resolveErr, &referenceError) {
			return &PipelineValidationResult{Valid: false, Diagnostics: referenceError.diagnostics}, nil
		}
		return &PipelineValidationResult{Valid: false, Diagnostics: []PipelineValidationDiagnostic{pipelineValidationDiagnosticFromPipelineError(resolveErr, pipeline)}}, nil
	}
	if err := pipelinevalidate.Validate(runtimeCandidate); err != nil {
		return &PipelineValidationResult{Valid: false, Diagnostics: []PipelineValidationDiagnostic{pipelineValidationDiagnosticFromPipelineError(err, runtimeCandidate)}}, nil
	}
	report, hasReport, err := contractImpactReport(s, ctx, orgSlug, projectSlug, runtimeCandidate)
	if err != nil {
		return nil, err
	}

	var dataSourceRefSync *datasourcerefssvc.PipelineDataSourceRefSyncResult
	if environmentSlug != "" && s.dataSourceRefResolver != nil {
		resolved, err := s.dataSourceRefResolver.ResolvePipelineDataSourceRefs(
			ctx,
			orgSlug,
			projectSlug,
			environmentSlug,
			runtimeCandidate,
		)
		if err != nil {
			if !isPipelineValidationDiagnosticError(err) {
				return nil, err
			}
			return &PipelineValidationResult{
				Valid: false,
				Diagnostics: []PipelineValidationDiagnostic{
					pipelineValidationDiagnosticFromPipelineError(err, pipeline),
				},
				DataSourceRefSync: dataSourceRefSync,
			}, nil
		}
		if resolved != nil && resolved.Pipeline != nil {
			runtimeCandidate = resolved.Pipeline
		}
	}

	operationCandidate, err := s.resolveSavedWorkspaceOperations(ctx, orgSlug, environmentSlug, runtimeCandidate)
	if err != nil {
		if !xerror.Is(err, queriessvc.ErrQueryNotFound) && !xerror.Is(err, xerror.InvalidInput(xerror.Query)) {
			return nil, err
		}
		return &PipelineValidationResult{
			Valid: false, Diagnostics: []PipelineValidationDiagnostic{pipelineValidationDiagnosticFromPipelineError(err, runtimeCandidate)},
			DataSourceRefSync: dataSourceRefSync,
		}, nil
	}
	if err := pipelinevalidate.Validate(operationCandidate); err != nil {
		return &PipelineValidationResult{
			Valid: false, Diagnostics: []PipelineValidationDiagnostic{pipelineValidationDiagnosticFromPipelineError(err, operationCandidate)},
			DataSourceRefSync: dataSourceRefSync,
		}, nil
	}
	if err := s.runtime.PreValidateTransforms(ctx, orgSlug, runtimeCandidate); err != nil {
		if !isPipelineValidationDiagnosticError(err) {
			return nil, err
		}
		return &PipelineValidationResult{
			Valid: false,
			Diagnostics: []PipelineValidationDiagnostic{
				pipelineValidationDiagnosticFromPipelineError(err, runtimeCandidate),
			},
			DataSourceRefSync: dataSourceRefSync,
		}, nil
	}
	if err := pipelinevalidate.ValidateStepInputCompatibility(runtimeCandidate); err != nil {
		return &PipelineValidationResult{
			Valid: false,
			Diagnostics: []PipelineValidationDiagnostic{
				pipelineValidationDiagnosticFromPipelineError(err, runtimeCandidate),
			},
			DataSourceRefSync: dataSourceRefSync,
		}, nil
	}

	if !hasReport {
		return &PipelineValidationResult{
			Valid:             true,
			Diagnostics:       []PipelineValidationDiagnostic{},
			DataSourceRefSync: dataSourceRefSync,
		}, nil
	}

	diagnostic := pipelineValidationDiagnosticFromReport(report)
	persisted, persistErr := persistContractImpactReport(s, ctx, orgSlug, projectSlug, authoredCandidate, report)
	if persistErr != nil {
		slog.WarnContext(
			ctx,
			"persist contract impact report failed",
			slog.String("org_slug", orgSlug.String()),
			slog.String("project_slug", projectSlug.String()),
			slog.String("pipeline_slug", authoredCandidate.Metadata.Slug.String()),
			slog.Any("error", persistErr),
		)
	}
	return &PipelineValidationResult{
		Valid:             report.Severity != pipelinedrift.ReportSeverityBreaking,
		Diagnostics:       []PipelineValidationDiagnostic{diagnostic},
		ImpactReport:      persisted,
		DataSourceRefSync: dataSourceRefSync,
	}, nil
}

// RecordValidationResult stores one validation outcome without creating a run record.
func (s *service) RecordValidationResult(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal ValidationHistoryPrincipal,
	source PipelineValidationResultSource,
	pipeline *pipelinespec.Pipeline,
	result *PipelineValidationResult,
) (*PipelineValidationResultRecord, error) {
	if err := s.requireValidationHistoryPermissions(ctx, orgSlug, principal, validationHistoryPermission{
		action:   model.Update,
		resource: model.ResourcePipelines,
	}); err != nil {
		return nil, err
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*PipelineValidationResultRecord, error) {
		return s.recordValidationResultTx(ctx, tx, orgSlug, projectSlug, environmentSlug, source, pipeline, result)
	})
}

// ListValidationResults returns durable non-run validation history for one runtime target.
func (s *service) ListValidationResults(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal ValidationHistoryPrincipal,
	filters PipelineValidationResultFilters,
	req model.ListRequest,
) (model.ListResponse[*PipelineValidationResultRecord], error) {
	if err := s.requireValidationHistoryPermissions(ctx, orgSlug, principal, validationHistoryReadPermissions()...); err != nil {
		return model.ListResponse[*PipelineValidationResultRecord]{}, err
	}
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (model.ListResponse[*PipelineValidationResultRecord], error) {
		normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)
		filters.Limit = int32(queryReq.Limit)

		rows, err := s.validationResults.List(
			ctx,
			db,
			orgSlug,
			projectSlug,
			environmentSlug,
			pipelinevalidationsrepo.ListFilters{
				PipelineSlug: filters.PipelineSlug,
				Limit:        filters.Limit,
			},
		)
		if err != nil {
			return model.ListResponse[*PipelineValidationResultRecord]{}, err
		}
		records := make([]*PipelineValidationResultRecord, 0, len(rows))
		for _, row := range rows {
			record, err := validationResultRecordFromRepo(row)
			if err != nil {
				return model.ListResponse[*PipelineValidationResultRecord]{}, err
			}
			records = append(records, record)
		}

		records, hasMore := model.FinalizePage(records, normalized)
		return model.ListResponse[*PipelineValidationResultRecord]{
			Items:   records,
			HasMore: hasMore,
		}, nil
	})
}

// GetValidationResult returns one durable validation result scoped to a project runtime target.
func (s *service) GetValidationResult(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal ValidationHistoryPrincipal,
	resultSlug slug.Slug,
) (*PipelineValidationResultRecord, error) {
	if err := s.requireValidationHistoryPermissions(ctx, orgSlug, principal, validationHistoryReadPermissions()...); err != nil {
		return nil, err
	}
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*PipelineValidationResultRecord, error) {
		row, err := s.validationResults.FindBySlug(ctx, db, orgSlug, projectSlug, environmentSlug, resultSlug)
		if err != nil {
			if xerror.Is(err, pipelinevalidationsrepo.ErrResultNotFound) {
				return nil, ErrPipelineValidationResultNotFound.With(err).Detail(resultSlug.String())
			}
			return nil, err
		}
		return validationResultRecordFromRepo(row)
	})
}

// RevalidatePipeline validates the current canonical pipeline and stores the result for SSR triage.
func (s *service) RevalidatePipeline(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal ValidationHistoryPrincipal,
	pipelineSlug, memberSlug slug.Slug,
) (*PipelineValidationResultRecord, error) {
	if err := s.requireValidationHistoryPermissions(ctx, orgSlug, principal, validationHistoryRevalidatePermissions()...); err != nil {
		return nil, err
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*PipelineValidationResultRecord, error) {
		spec, err := s.pipelineStore.FindSpecForUpdate(ctx, orgSlug, projectSlug, pipelineSlug)
		if err != nil {
			return nil, err
		}
		pipeline, err := pipelineFromJSON(spec.SpecJSONB)
		if err != nil {
			return nil, xerror.Wrapf("decode pipeline spec %s: %w", pipelineSlug, err)
		}
		for _, port := range pipelineODCSPorts(pipeline) {
			if *port.ref != nil {
				if err := s.requireValidationHistoryPermissions(ctx, orgSlug, principal, validationHistoryPermission{action: model.Read, resource: model.ResourceProjects}); err != nil {
					return nil, err
				}
				break
			}
		}
		result, err := s.Validate(ctx, orgSlug, projectSlug, environmentSlug, memberSlug, pipeline)
		if err != nil {
			return nil, err
		}
		return s.recordValidationResultTx(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			PipelineValidationResultSourceRevalidate,
			pipeline,
			result,
		)
	})
}

func validationSpecHash(pipeline *pipelinespec.Pipeline) (*string, error) {
	if pipeline == nil {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("pipeline validation result requires a pipeline")
	}
	data, err := json.Marshal(pipeline)
	if err != nil {
		return nil, xerror.Wrapf("hash pipeline validation spec: %w", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	return &hash, nil
}

func validationResultRecordFromRepo(
	row *pipelinevalidationsrepo.Result,
) (*PipelineValidationResultRecord, error) {
	if row == nil {
		return nil, ErrPipelineValidationResultNotFound
	}
	diagnostics := []PipelineValidationDiagnostic{}
	if len(row.DiagnosticsJSONB) > 0 {
		if err := json.Unmarshal(row.DiagnosticsJSONB, &diagnostics); err != nil {
			return nil, xerror.Wrapf("decode pipeline validation diagnostics: %w", err)
		}
	}
	source, err := PipelineValidationResultSourceString(row.Source)
	if err != nil {
		source = PipelineValidationResultSourceUnknown
	}
	return &PipelineValidationResultRecord{
		Slug:             row.Slug,
		ProjectSlug:      row.ProjectSlug,
		EnvironmentSlug:  row.EnvironmentSlug,
		PipelineSlug:     row.PipelineSlug,
		Source:           source,
		Valid:            row.Valid,
		SpecHash:         row.SpecHash,
		Diagnostics:      diagnostics,
		ImpactReportSlug: row.ImpactReportSlug,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

func pipelineValidationResultFromExecutionError(
	err error,
	pipeline *pipelinespec.Pipeline,
) (*PipelineValidationResult, bool) {
	if !isExecutionValidationFailure(err) {
		return nil, false
	}
	return &PipelineValidationResult{
		Valid: false,
		Diagnostics: []PipelineValidationDiagnostic{
			pipelineValidationDiagnosticFromPipelineError(err, pipeline),
		},
	}, true
}

func isExecutionValidationFailure(err error) bool {
	if err == nil {
		return false
	}
	if isPipelineValidationDiagnosticError(err) {
		return true
	}
	return xerror.Is(err, xerror.InvalidInput(xerror.Pipeline)) ||
		xerror.Is(err, pipelinevalidate.ErrOrphanStep) ||
		xerror.Is(err, pipelinevalidate.ErrMissingField) ||
		xerror.Is(err, pipelinevalidate.ErrContractMismatch) ||
		xerror.Is(err, pipelinevalidate.ErrInvalidFieldPath) ||
		xerror.Is(err, pipelineruntime.ErrTransformUpstreamRequired) ||
		xerror.Is(err, pipelineruntime.ErrSinkUpstreamRequired) ||
		xerror.Is(err, pipelineruntime.ErrQueryOutputContractSchemaMismatch)
}

func pipelineValidationDiagnosticFromReport(report *pipelinedrift.Report) PipelineValidationDiagnostic {
	severity := "warning"
	code := "PIPELINE_CONTRACT_IMPACT_WARNING"
	if report.Severity == pipelinedrift.ReportSeverityBreaking {
		severity = "error"
		code = "PIPELINE_CONTRACT_IMPACT_BREAKING"
	}

	field := "metadata"
	if len(report.Changes) > 0 {
		field = report.Changes[0].Path
	}
	var stepSlug *string
	if len(report.Impacts) > 0 && report.Impacts[0].StepSlug != "" {
		value := report.Impacts[0].StepSlug
		stepSlug = &value
	}
	return PipelineValidationDiagnostic{
		Severity: severity,
		Code:     code,
		Field:    field,
		StepSlug: stepSlug,
		Message:  report.Summary,
	}
}

func (s *service) ListDriftImpactReports(
	ctx context.Context,
	orgSlug slug.Slug,
	filters DriftImpactReportFilters,
) ([]*DriftImpactReport, error) {
	if s.driftReports == nil {
		return nil, nil
	}

	rows, err := s.driftReports.List(ctx, s.dbtx, orgSlug, driftimpactrepo.ListFilters{
		ProjectSlug:    filters.ProjectSlug,
		PipelineSlug:   filters.PipelineSlug,
		DataSourceSlug: filters.DataSourceSlug,
		Status:         filters.Status,
		Severity:       filters.Severity,
		Limit:          filters.Limit,
	})
	if err != nil {
		return nil, err
	}

	reports := make([]*DriftImpactReport, 0, len(rows))
	for _, row := range rows {
		report, err := driftImpactReportFromRepo(row)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (s *service) GetDriftImpactReport(
	ctx context.Context,
	orgSlug, projectSlug, reportSlug slug.Slug,
) (*DriftImpactReport, error) {
	if s.driftReports == nil {
		return nil, driftimpactrepo.ErrReportNotFound
	}

	row, err := s.driftReports.FindBySlug(ctx, s.dbtx, orgSlug, projectSlug, reportSlug)
	if err != nil {
		return nil, err
	}
	return driftImpactReportFromRepo(row)
}

func contractImpactReport(
	s *service,
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	candidate *pipelinespec.Pipeline,
) (*pipelinedrift.Report, bool, error) {
	if candidate == nil {
		return nil, false, nil
	}

	current, err := s.pipelineStore.Get(ctx, orgSlug, projectSlug, candidate.Metadata.Slug)
	if err != nil {
		if xerror.Is(err, ErrPipelineNotFound) {
			return nil, false, nil
		}
		return nil, false, xerror.Wrapf("load deployed pipeline for contract impact: %w", err)
	}

	if slices.ContainsFunc(pipelineODCSPorts(current), func(port odcsPort) bool { return *port.ref != nil }) {
		record, err := s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, candidate.Metadata.Slug)
		if err != nil {
			return nil, false, err
		}
		version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, candidate.Metadata.Slug, record.SpecHash)
		if err != nil {
			return nil, false, err
		}
		current, err = pipelineFromVersion(version)
		if err != nil {
			return nil, false, err
		}
	}

	report := pipelinedrift.ContractImpact(current, candidate)
	if report == nil {
		return nil, false, nil
	}
	return report, true, nil
}

func persistContractImpactReport(
	s *service,
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	candidate *pipelinespec.Pipeline,
	report *pipelinedrift.Report,
) (*DriftImpactReport, error) {
	createdSlug, err := slug.WithRandomSuffix(
		slug.Slug("contract-impact-"+candidate.Metadata.Slug.String()),
		6,
	)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detailf("generate drift impact report slug: %w", err)
	}

	projectSlugPtr := projectSlug
	pipelineSlugPtr := candidate.Metadata.Slug
	params, err := driftImpactCreateParams(createdSlug, &projectSlugPtr, &pipelineSlugPtr, nil, report)
	if err != nil {
		return nil, err
	}

	var created *DriftImpactReport
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.driftReports.SupersedeOpen(ctx, tx, orgSlug, params); err != nil {
			return err
		}
		row, err := s.driftReports.Create(ctx, tx, orgSlug, params)
		if err != nil {
			return err
		}
		created, err = driftImpactReportFromRepo(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func driftImpactCreateParams(
	reportSlug slug.Slug,
	projectSlug *slug.Slug,
	pipelineSlug *slug.Slug,
	dataSourceSlug *slug.Slug,
	report *pipelinedrift.Report,
) (driftimpactrepo.CreateParams, error) {
	changes, err := json.Marshal(report.Changes)
	if err != nil {
		return driftimpactrepo.CreateParams{}, xerror.Wrapf("encode drift impact changes: %w", err)
	}
	impacts, err := json.Marshal(report.Impacts)
	if err != nil {
		return driftimpactrepo.CreateParams{}, xerror.Wrapf("encode drift impact impacts: %w", err)
	}
	return driftimpactrepo.CreateParams{
		Slug:           reportSlug,
		ReportType:     report.Type,
		Status:         report.Status,
		Severity:       report.Severity,
		Summary:        report.Summary,
		ChangesJSONB:   changes,
		ImpactsJSONB:   impacts,
		ProjectSlug:    projectSlug,
		PipelineSlug:   pipelineSlug,
		DataSourceSlug: dataSourceSlug,
	}, nil
}

func driftImpactReportFromRepo(row *driftimpactrepo.Report) (*DriftImpactReport, error) {
	if row == nil {
		return nil, driftimpactrepo.ErrReportNotFound
	}

	var changes []pipelinedrift.Change
	if err := json.Unmarshal(row.ChangesJSONB, &changes); err != nil {
		return nil, xerror.Wrapf("decode drift impact changes: %w", err)
	}
	var impacts []pipelinedrift.Impact
	if err := json.Unmarshal(row.ImpactsJSONB, &impacts); err != nil {
		return nil, xerror.Wrapf("decode drift impact impacts: %w", err)
	}
	return &DriftImpactReport{
		Slug:           row.Slug,
		ReportType:     row.ReportType,
		Status:         row.Status,
		Severity:       row.Severity,
		Summary:        row.Summary,
		Changes:        changes,
		Impacts:        impacts,
		ProjectSlug:    row.ProjectSlug,
		PipelineSlug:   row.PipelineSlug,
		DataSourceSlug: row.DataSourceSlug,
		CreatedAt:      row.CreatedAt,
		SupersededAt:   row.SupersededAt,
	}, nil
}

func isPipelineValidationDiagnosticError(err error) bool {
	return xerror.Is(err, pipelineruntime.ErrQueryOutputContractSchemaMismatch) ||
		xerror.Is(err, transformsql.ErrContractMissing) ||
		xerror.Is(err, transformsql.ErrContractType) ||
		xerror.Is(err, transformsql.ErrTypeInput) ||
		xerror.Is(err, datasourcerefssvc.ErrDataSourceBindingNotFound) ||
		xerror.Is(err, datasourcerefssvc.ErrDataSourceRefNotFound) ||
		xerror.Is(err, datasourcerefssvc.ErrProjectEnvironmentNotEnabled) ||
		xerror.Is(err, datasourcerefssvc.ErrDataSourceBindingUnavailable) ||
		xerror.Is(err, datasourcerefssvc.ErrDataSourceRefIncompatible) ||
		xerror.Is(err, xerror.InvalidInput(xerror.RuntimeVariable)) ||
		xerror.Is(err, datasourcessvc.ErrDataSourceReadOnly)
}

func pipelineValidationDiagnosticFromError(err error) PipelineValidationDiagnostic {
	diagnostic := PipelineValidationDiagnostic{
		Severity: "error",
		Code:     "PIPELINE_VALIDATION_FAILED",
		Field:    "spec",
		Message:  strings.TrimSpace(err.Error()),
	}

	var xe *xerror.Error
	if xerror.As(err, &xe) {
		diagnostic.Code = xe.Code()
		diagnostic.Message = validationDiagnosticMessage(err, xe)
	}

	errorText := err.Error()
	matches := pipelineValidationStepPattern.FindStringSubmatch(errorText)
	// Match groups are: full step marker, step index, step slug.
	if len(matches) == 3 {
		diagnostic.Field = validationDiagnosticField(errorText, matches[1])
		diagnostic.StepSlug = ptrString(matches[2])
	}
	if resolution, ok := datasourcerefssvc.AsResolutionDiagnostic(err); ok {
		applyDataSourceRefResolutionDiagnostic(&diagnostic, resolution)
	}

	return diagnostic
}

func pipelineValidationDiagnosticFromPipelineError(
	err error,
	pipeline *pipelinespec.Pipeline,
) PipelineValidationDiagnostic {
	diagnostic := pipelineValidationDiagnosticFromError(err)
	matches := pipelineValidationStepPattern.FindStringSubmatch(err.Error())
	if len(matches) != 3 || pipeline == nil {
		return diagnostic
	}

	stepIndex, parseErr := strconv.Atoi(matches[1])
	if parseErr != nil || stepIndex < 0 || stepIndex >= len(pipeline.Steps) {
		return diagnostic
	}

	errorText := err.Error()
	if pipeline.Steps[stepIndex].Kind == "transform" &&
		(strings.Contains(errorText, "input_contract") || strings.Contains(errorText, "output_contract")) {
		diagnostic.Field = "steps[" + matches[1] + "].transform"
	}
	return diagnostic
}

func applyDataSourceRefResolutionDiagnostic(
	diagnostic *PipelineValidationDiagnostic,
	resolution datasourcerefssvc.ResolutionDiagnostic,
) {
	if resolution.Field != "" {
		diagnostic.Field = resolution.Field
	}
	if resolution.StepSlug != nil {
		diagnostic.StepSlug = ptrString(resolution.StepSlug.String())
	}
	if resolution.DataSourceRef != "" {
		diagnostic.DataSourceRef = ptrString(resolution.DataSourceRef.String())
	}
	if resolution.DataSourceRefSlug != "" {
		diagnostic.DataSourceRefSlug = ptrString(resolution.DataSourceRefSlug.String())
	}
	if resolution.ResolvedDataSourceSlug != "" {
		diagnostic.ResolvedDataSourceSlug = ptrString(resolution.ResolvedDataSourceSlug.String())
	}
	if resolution.EnvironmentSlug != "" {
		diagnostic.EnvironmentSlug = ptrString(resolution.EnvironmentSlug.String())
	}
	if resolution.DataSourceType != model.DataSourceTypeUnknown {
		diagnostic.DataSourceType = &resolution.DataSourceType
	}
	if resolution.ExpectedDataSourceType != model.DataSourceTypeUnknown {
		diagnostic.ExpectedDataSourceType = &resolution.ExpectedDataSourceType
	}
	if resolution.RequiredCapability != model.DataSourceRefCapabilityUnknown {
		diagnostic.RequiredCapability = &resolution.RequiredCapability
	}
}

func validationDiagnosticField(errorText, stepIndex string) string {
	switch {
	case strings.Contains(errorText, "query target"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "data source binding"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "data source ref"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "query parameters"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "query operation_inline"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "query output_contract"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "transform"):
		return "steps[" + stepIndex + "].transform"
	case strings.Contains(errorText, "output_contract"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "incremental contract"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "incremental mode"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "lookback_window"):
		return "steps[" + stepIndex + "].query"
	case strings.Contains(errorText, "sink target"), strings.Contains(errorText, "sink.config."):
		return "steps[" + stepIndex + "].sink"
	default:
		return "steps[" + stepIndex + "]"
	}
}

func validationDiagnosticMessage(err error, xe *xerror.Error) string {
	message := strings.TrimSpace(err.Error())
	if mismatchMessage, ok := validationPrevalidationMessage(message); ok {
		return mismatchMessage
	}

	message = trimPipelineValidationPrefixes(message)
	marker := xe.Message() + ": "
	if strings.Contains(message, marker) {
		parts := strings.Split(message, marker)
		message = parts[len(parts)-1]
	}
	message = trimPipelineValidationPrefixes(message)
	message = trimPipelineValidationStepMarker(message)
	if message == "" {
		return xe.Message()
	}

	return message
}

func trimPipelineValidationPrefixes(message string) string {
	for _, prefix := range []string{"validate pipeline: ", "validate steps: "} {
		message = strings.TrimPrefix(message, prefix)
	}
	return message
}

func trimPipelineValidationStepMarker(message string) string {
	matches := pipelineValidationStepPattern.FindStringIndex(message)
	if len(matches) != 2 {
		return message
	}

	message = strings.TrimSpace(message[matches[1]:])
	message = strings.TrimPrefix(message, ": ")
	message = strings.TrimPrefix(message, "output_contract: ")
	return message
}

func validationPrevalidationMessage(message string) (string, bool) {
	for _, marker := range []string{
		"query output_contract mismatch",
		"validate step inputs:",
	} {
		index := strings.Index(message, marker)
		if index >= 0 {
			return message[index:], true
		}
	}

	return "", false
}

func ptrString(value string) *string {
	return &value
}

func (s *service) Get(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*PipelineDetail, error) {
	meta, err := s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}

	spec, err := pipelineFromJSON(meta.SpecJSONB)
	if err != nil {
		return nil, err
	}

	var draftDetail *PipelineDraftDetail
	if memberSlug != "" {
		draft, err := s.pipelineStore.FindDraft(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
		if err != nil && !xerror.Is(err, ErrPipelineDraftNotFound) {
			return nil, err
		}
		if draft != nil {
			var decodeErr error
			draftDetail, decodeErr = pipelineDraftDetailFromRepository(draft)
			if decodeErr != nil {
				return nil, xerror.Wrapf("decode pipeline draft %s: %w", pipelineSlug, decodeErr)
			}
			if decorateErr := s.decorateDraftProposalContext(
				ctx, orgSlug, projectSlug, memberSlug, draftDetail,
			); decorateErr != nil {
				return nil, decorateErr
			}
		}
	}

	summary := toPipelineSummary(meta)
	summary.HasDraft = draftDetail != nil

	return &PipelineDetail{
		Summary: summary,
		Spec:    spec,
		Draft:   draftDetail,
	}, nil
}

func (s *service) GetDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*PipelineDraftDetail, error) {
	draft, err := s.pipelineStore.FindDraft(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	if err != nil {
		return nil, err
	}

	detail, err := pipelineDraftDetailFromRepository(draft)
	if err != nil {
		return nil, xerror.Wrapf("decode pipeline draft %s: %w", pipelineSlug, err)
	}
	if err := s.decorateDraftProposalContext(ctx, orgSlug, projectSlug, memberSlug, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

func pipelineDraftDetailFromRepository(draft *pipelinesrepo.PipelineDraft) (*PipelineDraftDetail, error) {
	if draft == nil {
		return nil, xerror.InvalidRequired(xerror.Pipeline)
	}
	spec, err := pipelineFromJSON(draft.SpecJSONB)
	if err != nil {
		return nil, err
	}
	draftSpecHash := fmt.Sprintf("%x", sha256.Sum256(draft.SpecJSONB))
	draftState := pipelineDraftStateEditing
	if draft.DraftState != nil && strings.TrimSpace(*draft.DraftState) != "" {
		draftState = strings.TrimSpace(*draft.DraftState)
	}
	return &PipelineDraftDetail{
		PipelineSlug:          draft.PipelineSlug,
		proposalAuthorityID:   draft.ProposalAuthorityID,
		BasedOnSpecHash:       draft.BasedOnSpecHash,
		DraftBasedOnVersionID: draft.BasedOnVersionID,
		CurrentVersionID:      draft.CurrentVersionID,
		DraftState:            draftState,
		DraftSpecHash:         draftSpecHash,
		BaseRef:               draft.BaseRef,
		BaseCommitSHA:         draft.BaseCommitSHA,
		BranchName:            draft.BranchName,
		BranchHeadSHA:         draft.BranchHeadSHA,
		ProposalProvider:      draft.ProposalProvider,
		ProposalID:            draft.ProposalID,
		ProposalURL:           draft.ProposalURL,
		ProposalState:         draft.ProposalState,
		ProposalIsStale: draft.LastProposedDraftHash != nil &&
			strings.TrimSpace(*draft.LastProposedDraftHash) != draftSpecHash,
		LastProposedDraftHash: draft.LastProposedDraftHash,
		QueuedAt:              draft.QueuedAt,
		ProposalUpdatedAt:     draft.ProposalUpdatedAt,
		FailedAt:              draft.FailedAt,
		FailureCode:           draft.FailureCode,
		FailureMessage:        draft.FailureMessage,
		PublicationMode:       draft.PublicationMode,
		ProviderActor:         draft.ExecutingPrincipalDisplay,
		AuthoritySource:       draft.AuthoritySource,
		CreatedAt:             draft.CreatedAt,
		UpdatedAt:             draft.UpdatedAt,
		Spec:                  spec,
	}, nil
}

func pipelineProposalSupported(repository string) bool {
	switch repostore.DetectProvider(repository) {
	case repostore.ProviderGitHub, repostore.ProviderGitLab:
		return true
	default:
		return false
	}
}

func (s *service) SaveDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
	pipeline *pipelinespec.Pipeline,
	expectedSpecHash *string,
) (*PipelineDraftDetail, error) {
	if pipeline == nil {
		return nil, xerror.InvalidRequired(xerror.Pipeline)
	}
	if pipeline.Metadata.Slug != pipelineSlug {
		return nil, xerror.InvalidInput(xerror.Pipeline).With(
			xerror.Unexpected().Detail("pipeline slug does not match path parameter"),
		)
	}

	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.pipelineStore.SaveDraft(
			ctx,
			orgSlug,
			projectSlug,
			memberSlug,
			pipeline,
			expectedSpecHash,
		); err != nil {
			return err
		}
		return s.recordPipelineEventWithMetadata(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			actor,
			pipelineSlug,
			model.EventActionUpdate,
			map[string]any{"mutation": "draft_saved"},
		)
	}); err != nil {
		return nil, err
	}

	return s.GetDraft(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
}

func (s *service) PublishDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
	expectedSpecHash *string,
	force bool,
) (*PipelineDetail, error) {
	if err := authorizeProposalMember(memberSlug, actor); err != nil {
		return nil, err
	}
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		backend, err := s.resolveProjectPipelineBackend(ctx, orgSlug, projectSlug)
		if err != nil {
			return err
		}
		baseRef := "refs/heads/" + strings.TrimSpace(backend.TrackedBranch)

		publishReq, err := s.pipelineStore.PrepareGitDraftPublish(
			ctx,
			orgSlug,
			projectSlug,
			pipelineSlug,
			memberSlug,
			&baseRef,
			expectedSpecHash,
			force,
		)
		if err != nil {
			return err
		}

		_, err = s.pipelineGitProducer.EnqueuePublishProposal(
			ctx,
			tx,
			jobs.PipelineGitPublishProposalArgs{
				OrganizationSlug: orgSlug,
				ProjectSlug:      publishReq.ProjectSlug,
				DataSourceSlug:   backend.DataSourceSlug,
				PipelineSlug:     publishReq.PipelineSlug,
				MemberSlug:       publishReq.MemberSlug,
				BaseRef:          publishReq.BaseRef,
				BaseCommitSHA:    publishReq.BaseCommitSHA,
				ExpectedHeadSHA:  publishReq.ExpectedHeadSHA,
				BranchName:       publishReq.BranchName,
				Path:             publishReq.SourcePath,
				Content:          publishReq.Content,
				CommitMessage:    publishReq.CommitMessage,
				ProposalTitle:    publishReq.ProposalTitle,
				ProposalBody:     publishReq.ProposalBody,
				DraftSpecHash:    publishReq.DraftSpecHash,
				PublishAttemptID: publishReq.PublishAttemptID,
				Authority:        publishReq.Authority.Payload(),
			},
		)
		if err != nil {
			return err
		}
		return s.recordPipelineEventWithMetadata(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			actor,
			pipelineSlug,
			model.EventActionUpdate,
			map[string]any{
				"mutation":            "git_update_proposed",
				"source_path":         publishReq.SourcePath,
				"publication_mode":    publishReq.Authority.PublicationMode,
				"provider_host":       publishReq.Authority.ProviderHost,
				"provider_repository": publishReq.Authority.ProviderRepositoryID,
				"provider_actor":      publishReq.Authority.ExecutingPrincipalDisplay,
				"authority_source":    publishReq.Authority.AuthoritySource,
			},
		)
	}); err != nil {
		return nil, err
	}

	return s.Get(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
}

// AuthorizeProposalPublish rechecks the exact member, repository, draft, and provider identity before mutation.
func (s *service) AuthorizeProposalPublish(
	ctx context.Context,
	args jobs.PipelineGitPublishProposalArgs,
) error {
	if err := authorizeProposalMember(
		args.MemberSlug,
		model.UserEventActor(args.MemberSlug),
	); err != nil {
		return err
	}
	allowed, err := s.authorizationService.Can(ctx, authorization.Input{
		UserSlug: args.MemberSlug,
		OrgSlug:  &args.OrganizationSlug,
		Action:   model.Update,
		Resource: model.ResourcePipelines,
	})
	if err != nil {
		return xerror.Wrapf("authorize pipeline proposal member: %w", err)
	}
	if !allowed {
		return ErrPipelineProposalAuthorityDenied.Detail(
			"initiating member no longer has pipeline update permission",
		)
	}
	backend, err := s.resolveProjectPipelineBackend(ctx, args.OrganizationSlug, args.ProjectSlug)
	if err != nil {
		return err
	}
	if backend.DataSourceSlug != args.DataSourceSlug {
		return ErrPipelineProposalAuthorityDenied.Detail("project repository binding changed")
	}
	authority, err := s.pipelineStore.ResolveProposalAuthority(
		ctx,
		args.OrganizationSlug,
		args.ProjectSlug,
	)
	if err != nil {
		return err
	}
	if authority.Payload() != args.Authority {
		return ErrPipelineProposalAuthorityDenied.Detail("provider authority changed or was revoked")
	}
	draft, err := s.pipelineStore.FindDraft(
		ctx,
		args.OrganizationSlug,
		args.ProjectSlug,
		args.PipelineSlug,
		args.MemberSlug,
	)
	if err != nil {
		if xerror.Is(err, ErrPipelineDraftNotFound) {
			return ErrPipelineProposalAuthorityDenied.With(err).Detail("queued proposal draft is unavailable")
		}
		return err
	}
	spec, err := s.pipelineStore.FindSpec(
		ctx,
		args.OrganizationSlug,
		args.ProjectSlug,
		args.PipelineSlug,
	)
	if err != nil {
		if xerror.Is(err, ErrPipelineNotFound) {
			return ErrPipelineProposalAuthorityDenied.With(err).Detail("proposal pipeline is unavailable")
		}
		return err
	}
	if spec.SourcePath == nil || strings.TrimSpace(*spec.SourcePath) == "" {
		return ErrPipelineProposalAuthorityDenied.Detail("proposal source path is unavailable")
	}
	draftHash := fmt.Sprintf("%x", sha256.Sum256(draft.SpecJSONB))
	pipeline, err := pipelinevalidate.Load(args.PipelineSlug.String()+".pipeline.json", draft.SpecJSONB)
	if err != nil {
		return ErrPipelineProposalAuthorityDenied.With(err)
	}
	content, err := renderPipelineSource(strings.TrimSpace(*spec.SourcePath), pipeline)
	if err != nil {
		return ErrPipelineProposalAuthorityDenied.With(err)
	}
	if draft.PublishAttemptID == nil || *draft.PublishAttemptID != args.PublishAttemptID ||
		draft.BranchName == nil || *draft.BranchName != args.BranchName ||
		draft.ProposalAuthorityID == nil || *draft.ProposalAuthorityID != args.Authority.ID ||
		strings.TrimSpace(*spec.SourcePath) != args.Path ||
		strings.TrimSpace(ptr.Value(draft.BaseRef)) != strings.TrimSpace(ptr.Value(args.BaseRef)) ||
		strings.TrimSpace(ptr.Value(draft.BaseCommitSHA)) != strings.TrimSpace(ptr.Value(args.BaseCommitSHA)) ||
		strings.TrimSpace(ptr.Value(draft.BranchHeadSHA)) != strings.TrimSpace(ptr.Value(args.ExpectedHeadSHA)) ||
		draftHash != args.DraftSpecHash || !bytes.Equal(content, args.Content) {
		return ErrPipelineProposalAuthorityDenied.Detail("proposal target changed after publication was queued")
	}
	return nil
}

func (s *service) RefreshDraftProposal(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
) (*PipelineDetail, error) {
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		backend, err := s.resolveProjectPipelineBackend(ctx, orgSlug, projectSlug)
		if err != nil {
			return err
		}
		refreshReq, err := s.pipelineStore.PrepareGitProposalRefresh(
			ctx, orgSlug, projectSlug, pipelineSlug, memberSlug,
		)
		if err != nil {
			return err
		}
		if _, err := s.pipelineGitProducer.EnqueueRefreshProposal(ctx, tx, jobs.PipelineGitRefreshProposalArgs{
			OrganizationSlug: orgSlug,
			ProjectSlug:      refreshReq.ProjectSlug,
			DataSourceSlug:   backend.DataSourceSlug,
			PipelineSlug:     refreshReq.PipelineSlug,
			MemberSlug:       refreshReq.MemberSlug,
			ProposalID:       refreshReq.ProposalID,
			RefreshAttemptID: refreshReq.RefreshAttemptID,
		}); err != nil {
			return err
		}
		return s.recordPipelineEventWithMetadata(ctx, tx, orgSlug, projectSlug, actor, pipelineSlug,
			model.EventActionUpdate, map[string]any{"mutation": "git_proposal_refresh_queued"})
	}); err != nil {
		return nil, err
	}
	return s.Get(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
}

func (s *service) DeleteDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.pipelineStore.DeleteDraft(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug); err != nil {
			return err
		}
		return s.recordPipelineEventWithMetadata(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			actor,
			pipelineSlug,
			model.EventActionUpdate,
			map[string]any{"mutation": "draft_deleted"},
		)
	})
}

func (s *service) ListVersions(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*PipelineVersionSummary, error) {
	if _, err := s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug); err != nil {
		return nil, err
	}

	versions, err := s.pipelineStore.ListVersions(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}

	summaries := make([]*PipelineVersionSummary, len(versions))
	for i, version := range versions {
		summaries[i] = toPipelineVersionSummary(version)
	}

	return summaries, nil
}

func (s *service) GetVersion(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (*PipelineVersionDetail, error) {
	if _, err := s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug); err != nil {
		return nil, err
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
			version, err = s.findVersionByShortSpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	spec, err := pipelineFromJSON(version.SpecJSONB)
	if err != nil {
		return nil, xerror.Wrapf("decode pipeline version %s: %w", version.SpecHash, err)
	}

	return &PipelineVersionDetail{
		Summary: toPipelineVersionSummary(version),
		Spec:    spec,
	}, nil
}

func (s *service) ListDeployments(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]*PipelineDeploymentDetail, error) {
	enabled, err := s.pipelineStore.ProjectEnvironmentEnabled(ctx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrProjectEnvironmentNotFound
	}

	deployments, err := s.pipelineStore.ListDeployments(ctx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return nil, err
	}

	ret := make([]*PipelineDeploymentDetail, len(deployments))
	for i, deployment := range deployments {
		ret[i] = toPipelineDeploymentDetail(deployment)
	}
	return ret, nil
}

func (s *service) GetDeployment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*PipelineDeploymentDetail, error) {
	deployment, err := s.pipelineStore.FindDeployment(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
	)
	if err != nil {
		return nil, err
	}
	detail := toPipelineDeploymentDetail(deployment)
	if err := s.attachDeploymentProofLink(ctx, orgSlug, projectSlug, environmentSlug, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *service) Deploy(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
	req DeployPipelineRequest,
) (*PipelineDeploymentDetail, error) {
	if req.ExpectedRevision != nil && *req.ExpectedRevision < 0 {
		return nil, xerror.InvalidInput(xerror.PipelineDeployment).Detail("deployment revision must be nonnegative")
	}
	specHash := strings.TrimSpace(req.SpecHash)
	if specHash == "" {
		return nil, xerror.InvalidRequired(xerror.PipelineVersion)
	}
	protection, err := s.pipelineStore.ProjectEnvironmentProtection(ctx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		if xerror.Is(err, ErrProjectEnvironmentNotFound) {
			return nil, ErrProjectEnvironmentNotFound.With(err)
		}
		return nil, err
	}
	if err := protectedenv.Confirm(protection, req.ProtectedEnvironmentConfirmation); err != nil {
		return nil, err
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
			version, err = s.findVersionByShortSpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	var versionPipeline *pipelinespec.Pipeline
	if req.CompiledPlanDigest != nil || req.ResolutionDigest != nil {
		versionPipeline, err = pipelineFromJSON(version.SpecJSONB)
	} else {
		versionPipeline, err = pipelineFromVersion(version)
	}
	if err != nil {
		return nil, err
	}
	if err := pipelinevalidate.Validate(versionPipeline); err != nil {
		return nil, xerror.Wrapf("invalid deployment pipeline: %w", err)
	}

	var expectedCurrentSpecHash *string
	if req.ExpectedCurrentSpecHash != nil {
		expectedCurrentSpecHash, err = s.resolveExpectedDeploymentSpecHash(
			ctx,
			orgSlug,
			projectSlug,
			pipelineSlug,
			*req.ExpectedCurrentSpecHash,
		)
		if err != nil {
			return nil, err
		}
	}

	var dataSourceRefSync *datasourcerefssvc.PipelineDataSourceRefSyncResult
	var deploymentSpec *pipelinesrepo.PipelineSpec
	if s.dataSourceRefResolver != nil {
		dataSourceRefSync, err = s.dataSourceRefResolver.SyncPipelineDataSourceRefs(
			ctx,
			orgSlug,
			projectSlug,
			versionPipeline,
			actor,
		)
		if err != nil {
			return nil, err
		}
	}
	if req.ProofReceiptSlug != nil {
		deploymentSpec, err = s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
		if err != nil {
			return nil, xerror.Wrapf("find pipeline spec for proof receipt link: %w", xerror.Unexpected().With(err))
		}
	}

	var deployment *pipelinesrepo.PipelineDeployment
	var proofLink *proofreceipts.LifecycleLink
	err = s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.admission.CheckPipelineDeploymentAdmission(ctx, orgSlug, projectSlug, pipelineSlug); err != nil {
			return err
		}
		resolutionID, err := s.resolveDeployment(ctx, tx, orgSlug, projectSlug, environmentSlug, version, req, false)
		if err != nil {
			return err
		}
		var resolutionDigest *string
		if resolutionID != "" {
			resolutionDigest = &resolutionID
		}
		if resolutionDigest != nil && req.ProofReceiptSlug != nil {
			return ErrProofReceiptLinkMismatch
		}
		var resolved *datasourcerefssvc.ResolvedPipeline
		if s.dataSourceRefResolver != nil {
			resolved, err = s.dataSourceRefResolver.ResolvePipelineDataSourceRefs(
				ctx,
				orgSlug,
				projectSlug,
				environmentSlug,
				versionPipeline,
			)
			if err != nil {
				return err
			}
		}
		var receipt *proofreceipts.Receipt
		if req.ProofReceiptSlug != nil {
			receipt, err = s.proofLinks.FindBySlug(ctx, tx, orgSlug, projectSlug, environmentSlug, *req.ProofReceiptSlug)
			if xerror.Is(err, proofreceipts.ErrReceiptNotFound) {
				return ErrProofReceiptLinkMismatch.With(err)
			}
			if err != nil {
				return xerror.Wrapf("find proof receipt for deployment link: %w", xerror.Unexpected().With(err))
			}
			bindingFingerprint, fingerprintErr := proofBindingFingerprint(resolved)
			if fingerprintErr != nil {
				return fingerprintErr
			}
			capabilityFingerprint, fingerprintErr := proofCapabilityFingerprint(resolved)
			if fingerprintErr != nil {
				return fingerprintErr
			}
			if !proofReceiptMatchesDeployment(receipt, orgSlug, projectSlug, environmentSlug, version, deploymentSpec, bindingFingerprint, capabilityFingerprint) {
				return ErrProofReceiptLinkMismatch
			}
		}

		deployed, deployErr := s.pipelineStore.Deploy(
			ctx,
			orgSlug,
			projectSlug,
			environmentSlug,
			pipelinesrepo.DeployParams{
				PipelineSlug:            pipelineSlug,
				VersionID:               version.ID,
				ExpectedCurrentSpecHash: expectedCurrentSpecHash,
				ExpectedRevision:        req.ExpectedRevision,
				ResolutionDigest:        resolutionDigest,
				DeployedByActorType:     actor.Type.String(),
				DeployedByActorSlug:     actorSlugPtr(actor),
			},
		)
		if deployErr != nil {
			if xerror.Is(deployErr, pipelinesrepo.ErrPipelineDeploymentConflict) {
				return ErrPipelineDeploymentConflict.With(deployErr)
			}
			if xerror.Is(deployErr, ErrPipelineDeploymentNotFound) {
				return ErrPipelineDeploymentConflict.With(deployErr)
			}
			return deployErr
		}
		deployment = deployed
		if req.ProofReceiptSlug != nil {
			proofLink, err = s.proofLinks.LinkDeployment(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelineSlug, proofreceipts.LinkDeploymentParams{
				ReceiptSlug: *req.ProofReceiptSlug, SpecHash: version.SpecHash,
				Identifier: deploymentLinkIdentifier(pipelineSlug, deployed), LinkedAt: deployed.DeployedAt,
			})
			if xerror.Is(err, proofreceipts.ErrLifecycleLinkNotFound) {
				return ErrProofReceiptLinkMismatch.With(err)
			}
			if err != nil {
				return xerror.Wrapf("link proof receipt to deployment: %w", xerror.Unexpected().With(err))
			}
		}
		return s.recordPipelineDeploymentEvent(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			pipelineSlug,
			actor,
			deployed,
			req.SourceEnvironmentSlug,
			protection.Protected,
			req.ProofReceiptSlug,
		)
	})
	if err != nil {
		return nil, err
	}

	detail := toPipelineDeploymentDetail(deployment)
	detail.DataSourceRefSync = dataSourceRefSync
	if proofLink != nil {
		detail.ProofLink = toProofLifecycleLink(proofLink, deployment.CurrentVersionSpecHash, deploymentLinkIdentifier(pipelineSlug, deployment), s.executionClock.Now())
	}
	return detail, nil
}

func (s *service) Update(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
	pipeline *pipelinespec.Pipeline,
) (*PipelineDetail, error) {
	if err := authorizeProposalMember(memberSlug, actor); err != nil {
		return nil, err
	}
	if pipeline == nil {
		return nil, xerror.InvalidRequired(xerror.Pipeline)
	}
	if pipeline.Metadata.Slug != pipelineSlug {
		return nil, xerror.InvalidInput(xerror.Pipeline).With(
			xerror.Unexpected().Detail("pipeline slug does not match path parameter"),
		)
	}

	spec, err := s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return nil, err
	}
	if spec.SourcePath == nil || strings.TrimSpace(*spec.SourcePath) == "" {
		return nil, ErrPipelineGitSourceRequired
	}
	if _, err := s.SaveDraft(
		ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor, pipeline, spec.CurrentVersionSpecHash,
	); err != nil {
		return nil, err
	}
	return s.PublishDraft(
		ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor, spec.CurrentVersionSpecHash, false,
	)
}

func renderPipelineSource(sourcePath string, pipeline *pipelinespec.Pipeline) ([]byte, error) {
	_, data, err := renderCanonicalPipelineSource(sourcePath, pipeline)
	return data, err
}

func renderCanonicalPipelineSource(sourcePath string, pipeline *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []byte, error) {
	formatted, err := pipelineformat.Format(pipeline)
	if err != nil {
		return nil, nil, xerror.Wrapf("format pipeline %s: %w", pipeline.Metadata.Slug, err)
	}
	canonical := formatted.Spec

	switch strings.ToLower(filepath.Ext(sourcePath)) {
	case ".json":
		data, err := json.MarshalIndent(canonical, "", "  ")
		if err != nil {
			return nil, nil, xerror.InvalidInput(xerror.JSON).With(err)
		}
		return &canonical, append(data, '\n'), nil
	case ".yaml", ".yml":
		data, err := yaml.Marshal(canonical)
		if err != nil {
			return nil, nil, xerror.InvalidInput(xerror.JSON).With(err)
		}
		return &canonical, data, nil
	default:
		return nil, nil, pipelinespec.ErrUnsupportedFormat.Detailf("%q", sourcePath)
	}
}

func (s *service) Execute(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*PipelineRunDetail, error) {
	deployment, protection, source, runtimeInput, err := s.preparePipelineExecution(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		protectedEnvironmentConfirmation,
		source,
		runtimeInput,
	)
	if err != nil {
		return nil, err
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		deployment.CurrentVersionSpecHash,
	)
	if err != nil {
		return nil, err
	}
	return s.executeResolvedVersion(ctx, orgSlug, projectSlug, environmentSlug, version, deployment, runSlug, actor, source, runtimeInput, protection.Protected, nil, pipelinerun.IncrementalRunOptions{}, deployment.CurrentResolutionDigest)
}

// StartRun accepts one manual/API run, persists its queued status, and enqueues execution.
func (s *service) StartRun(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*PipelineRunDetail, error) {
	return s.startRun(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug, protectedEnvironmentConfirmation, actor, source, runtimeInput, nil)
}

func (s *service) StartBackfill(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
	windows map[slug.Slug]pipelinerun.IncrementalSourceWindow,
) (*PipelineRunDetail, error) {
	if len(windows) == 0 {
		return nil, xerror.InvalidInput(xerror.PipelineRun).Detail("backfill requires source windows")
	}
	return s.startRun(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug, protectedEnvironmentConfirmation, actor, source, runtimeInput, windows)
}

func (s *service) startRun(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
	windows map[slug.Slug]pipelinerun.IncrementalSourceWindow,
) (*PipelineRunDetail, error) {
	deployment, protection, source, runtimeInput, err := s.preparePipelineExecution(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		protectedEnvironmentConfirmation,
		source,
		runtimeInput,
	)
	if err != nil {
		return nil, err
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, deployment.CurrentVersionSpecHash)
	if err != nil {
		return nil, err
	}
	pipeline, _, err := s.executionPipeline(ctx, orgSlug, projectSlug, environmentSlug, version, deployment.CurrentResolutionDigest)
	if err != nil {
		return nil, err
	}
	if windows != nil {
		if err := validateBackfillWindows(pipeline, windows); err != nil {
			return nil, err
		}
	}

	acceptedAt := s.executionClock.Now().UTC()
	acceptedRunSlug, err := acceptedPipelineRunSlug(runSlug, pipelineSlug, acceptedAt)
	if err != nil {
		return nil, err
	}
	incremental := pipelinerun.IncrementalRunOptions{}
	if windows != nil {
		incremental = pipelinerun.IncrementalRunOptions{
			SourceWindows:        windows,
			SkipCheckpointCommit: true,
			BackfillSeriesSlug:   acceptedRunSlug,
			BackfillChunkCount:   1,
		}
	}
	source = pipelinerun.NormalizeAttemptSource(source, acceptedRunSlug)

	var run *pipelineruns.PipelineRun
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		created, _, err := s.createQueuedRun(ctx, tx, orgSlug, projectSlug, environmentSlug, pipelineSlug,
			acceptedRunSlug, deployment.CurrentVersionID, source, acceptedAt, deployment, incremental)
		if err != nil {
			return err
		}

		_, err = s.pipelineRunProducer.EnqueuePipelineRunExecution(ctx, tx, jobs.PipelineRunExecutionArgs{
			OrganizationSlug:           orgSlug,
			ProjectSlug:                projectSlug,
			EnvironmentSlug:            environmentSlug,
			PipelineSlug:               pipelineSlug,
			PipelineVersionHash:        deployment.CurrentVersionSpecHash,
			DeploymentResolutionDigest: deployment.CurrentResolutionDigest,
			RunSlug:                    acceptedRunSlug,
			AcceptedAt:                 acceptedAt,
			Actor:                      actor,
			Source:                     source,
			RuntimeInput:               runtimeInput,
			Incremental:                incremental,
			ProtectedEnvironment:       protection.Protected,
		})
		if err != nil {
			return err
		}

		if windows != nil {
			_, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, pipelineEvent{
				securityAudit: true, projectSlug: projectSlug, pipelineSlug: pipelineSlug, action: model.EventActionUpdate,
				metadata: map[string]any{"operation": "backfill_requested", "environment_slug": environmentSlug.String(), "run_slug": acceptedRunSlug.String(), "source_count": len(windows), "checkpoint_policy": "unchanged"},
			})
			if err != nil {
				return err
			}
		}
		run = created
		return nil
	})
	if err != nil {
		return nil, err
	}

	detail, err := pipelineRunDetailFromRecord(run)
	if err != nil {
		return nil, err
	}
	if err := s.attachRunProofLink(ctx, orgSlug, projectSlug, environmentSlug, acceptedRunSlug, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *service) createQueuedRun(
	ctx context.Context, tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug slug.Slug,
	versionID int64, source pipelinerun.RunSourceSummary, acceptedAt time.Time,
	deployment *pipelinesrepo.PipelineDeployment, incremental pipelinerun.IncrementalRunOptions,
) (*pipelineruns.PipelineRun, *proofreceipts.LifecycleLink, error) {
	if err := s.admission.CheckPipelineRunAdmission(ctx, orgSlug); err != nil {
		return nil, nil, err
	}
	timelineJSON, err := json.Marshal(queuedPipelineRunTimeline(runSlug, pipelineSlug, projectSlug, environmentSlug, acceptedAt))
	if err != nil {
		return nil, nil, xerror.Wrapf("encode queued pipeline run timeline: %w", err)
	}
	summary, diagnostics := requestedIncrementalState(incremental)
	diagnosticsJSON, err := json.Marshal(diagnostics)
	if err != nil {
		return nil, nil, xerror.Wrapf("encode queued pipeline run diagnostics: %w", err)
	}
	run, err := s.runRepo.CreateRun(ctx, tx, orgSlug, projectSlug, environmentSlug, &pipelineruns.CreateRunParams{
		DeploymentResolutionDigest: deployment.CurrentResolutionDigest,
		DeploymentID:               &deployment.ID,
		DeploymentRevision:         &deployment.Revision,
		PipelineVersionID:          versionID, PipelineSlug: pipelineSlug.String(), RunSlug: runSlug.String(),
		Source: source, RequestID: reqctx.GetRequestID(ctx), Status: pipelineRunStatusQueued,
		StartedAt: acceptedAt, Timeline: timelineJSON, Incremental: summary,
		IncrementalDiagnostics: diagnosticsJSON,
	})
	if err != nil {
		return nil, nil, err
	}
	link, err := s.proofLinks.LinkRun(ctx, tx, orgSlug, projectSlug, environmentSlug, runSlug,
		deployment.ID, deploymentLinkIdentifier(pipelineSlug, deployment), acceptedAt)
	if err != nil && !xerror.Is(err, proofreceipts.ErrLifecycleLinkNotFound) {
		return nil, nil, xerror.Wrapf("link proof receipt to pipeline run: %w", xerror.Unexpected().With(err))
	}
	return run, link, nil
}

// ExecuteQueuedPipelineRun runs one accepted job and writes terminal diagnostics to the run row.
func (s *service) ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error {
	if args.CompletionSnapshotID != 0 || args.Source.Type == model.PipelineRunSourceTypeCompletion {
		return xerror.InvalidInput(xerror.PipelineRun).Detail("completion execution requires authority revalidation")
	}
	return s.executeQueuedPipelineRun(ctx, args, nil)
}

func (s *service) executeQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs, checker CompletionAuthorityChecker) error {
	startedAt := s.executionClock.Now().UTC()
	var refusal error
	if err := s.admission.CheckWriteAccess(ctx, args.OrganizationSlug); err != nil {
		if xerror.Is(err, admissionsvc.ErrAdmissionDenied) {
			if checker == nil {
				return s.completeQueuedRunFailure(ctx, args, startedAt, err)
			}
			refusal = err
		} else {
			return xerror.Wrapf("check organization admission: %w", err)
		}
	}
	timelineJSON, err := json.Marshal(runningPipelineRunTimeline(args, startedAt))
	if err != nil {
		return xerror.Wrapf("encode running pipeline run timeline: %w", err)
	}

	claimed := true
	claimCtx := ctx
	if checker != nil {
		claimCtx = postgres.WithConsistentSnapshot(ctx)
	}
	if err := s.txm.RunInTx(claimCtx, func(ctx context.Context, tx postgres.DBTX) error {
		if checker != nil {
			snapshot, err := checker.CheckCompletionSnapshot(ctx, args.OrganizationSlug, args.ProjectSlug, args.EnvironmentSlug, args.CompletionSnapshotID)
			if err != nil {
				var typed *xerror.Error
				if snapshot == nil || !xerror.As(err, &typed) || typed.Kind() != xerror.KindInvalidInput {
					return err
				}
				refusal = err
			}
			expectedRun := snapshot.ChildRunSlug
			if args.Source.AttemptNumber > 1 {
				expectedRun = slug.Slug(fmt.Sprintf("%s-retry-%d", snapshot.ChildRunSlug, args.Source.AttemptNumber))
			}
			if args.Source.AttemptNumber < 1 || args.Source.RootRunSlug != snapshot.ChildRunSlug || expectedRun != args.RunSlug || snapshot.VersionHash != args.PipelineVersionHash ||
				snapshot.PipelineSlug != args.PipelineSlug || args.DeploymentResolutionDigest == nil || snapshot.ResolutionDigest != *args.DeploymentResolutionDigest {
				return xerror.InvalidInput(xerror.PipelineRun).Detail("completion execution does not match its accepted snapshot")
			}
			if snapshot.State != "accepted" {
				claimed = false
				return nil
			}
			if refusal == nil && args.Source.AttemptNumber > 1 && snapshot.RetryPolicy.MaxElapsedSeconds != nil {
				root, err := s.runRepo.FindRunBySlug(ctx, tx, args.OrganizationSlug, args.ProjectSlug, args.EnvironmentSlug, snapshot.ChildRunSlug.String())
				if err != nil {
					return err
				}
				if startedAt.After(root.StartedAt.Add(time.Duration(*snapshot.RetryPolicy.MaxElapsedSeconds) * time.Second)) {
					refusal = xerror.InvalidInput(xerror.PipelineRun).Detail("completion retry elapsed-time cap exceeded")
				}
			}
		}
		_, err := s.runRepo.MarkRunRunning(
			ctx,
			tx,
			args.OrganizationSlug,
			args.ProjectSlug,
			args.EnvironmentSlug,
			args.PipelineSlug,
			args.RunSlug.String(),
			startedAt,
			timelineJSON,
		)
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			claimed = false
			return nil
		}
		return err
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if refusal != nil {
		return s.completeQueuedRunFailure(ctx, args, startedAt, refusal)
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(
		ctx,
		args.OrganizationSlug,
		args.ProjectSlug,
		args.PipelineSlug,
		args.PipelineVersionHash,
	)
	if err != nil {
		return s.completeQueuedRunFailure(ctx, args, startedAt, err)
	}

	run, err := s.executeResolvedVersion(
		ctx,
		args.OrganizationSlug,
		args.ProjectSlug,
		args.EnvironmentSlug,
		version,
		nil,
		&args.RunSlug,
		args.Actor,
		args.Source,
		normalizeRuntimeInput(args.RuntimeInput),
		args.ProtectedEnvironment,
		nil,
		args.Incremental,
		args.DeploymentResolutionDigest,
	)
	if err != nil {
		if run != nil {
			// The runtime persisted a terminal failed run. Treat that as completed
			// worker work so River does not retry an already-observable domain failure.
			slog.WarnContext(
				ctx,
				"pipeline run persisted terminal failure",
				slog.String("org_slug", args.OrganizationSlug.String()),
				slog.String("project_slug", args.ProjectSlug.String()),
				slog.String("environment_slug", args.EnvironmentSlug.String()),
				slog.String("pipeline_slug", args.PipelineSlug.String()),
				slog.String("run_slug", args.RunSlug.String()),
				slog.Any("error", err),
			)
			return nil
		}
		return s.completeQueuedRunFailure(ctx, args, startedAt, err)
	}
	return nil
}

// acceptedPipelineRunSlug resolves the externally visible run slug before enqueue.
func acceptedPipelineRunSlug(explicit *slug.Slug, pipelineSlug slug.Slug, acceptedAt time.Time) (slug.Slug, error) {
	if explicit != nil && *explicit != "" {
		return *explicit, nil
	}
	base, err := slug.FromName(fmt.Sprintf("%s-%s", pipelineSlug, acceptedAt.UTC().Format("20060102150405")))
	if err != nil {
		return "", err
	}
	return slug.WithRandomSuffix(base, 6)
}

// queuedPipelineRunTimeline captures the durable state returned by run-start requests.
func queuedPipelineRunTimeline(
	runSlug, pipelineSlug, projectSlug, environmentSlug slug.Slug,
	acceptedAt time.Time,
) pipelinerun.RunTimeline {
	return pipelinerun.RunTimeline{
		RunSlug:         runSlug.String(),
		PipelineSlug:    pipelineSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		Status:          pipelineRunStatusQueued,
		Events: []pipelinerun.TimelineEvent{
			{At: acceptedAt, Type: pipelineRunEventQueued, Status: pipelineRunStatusQueued},
		},
		Snapshots: []pipelinerun.StepSnapshot{},
	}
}

// runningPipelineRunTimeline records that the background worker has claimed a queued run.
func runningPipelineRunTimeline(
	args jobs.PipelineRunExecutionArgs,
	startedAt time.Time,
) pipelinerun.RunTimeline {
	acceptedAt := args.AcceptedAt
	if acceptedAt.IsZero() {
		acceptedAt = startedAt
	}
	return pipelinerun.RunTimeline{
		RunSlug:         args.RunSlug.String(),
		PipelineSlug:    args.PipelineSlug,
		ProjectSlug:     args.ProjectSlug,
		EnvironmentSlug: args.EnvironmentSlug,
		Status:          pipelineRunStatusRunning,
		Events: []pipelinerun.TimelineEvent{
			{At: acceptedAt, Type: pipelineRunEventQueued, Status: pipelineRunStatusQueued},
			{At: startedAt, Type: pipelineRunEventStarted, Status: pipelineRunStatusRunning},
		},
		Snapshots: []pipelinerun.StepSnapshot{},
	}
}

// failedQueuedPipelineRunTimeline preserves failure context when execution fails before runtime persistence.
func failedQueuedPipelineRunTimeline(
	args jobs.PipelineRunExecutionArgs,
	startedAt time.Time,
	endedAt time.Time,
	errorSummary string,
) pipelinerun.RunTimeline {
	acceptedAt := args.AcceptedAt
	if acceptedAt.IsZero() {
		acceptedAt = startedAt
	}
	return pipelinerun.RunTimeline{
		RunSlug:         args.RunSlug.String(),
		PipelineSlug:    args.PipelineSlug,
		ProjectSlug:     args.ProjectSlug,
		EnvironmentSlug: args.EnvironmentSlug,
		Status:          pipelineRunStatusFailed,
		Events: []pipelinerun.TimelineEvent{
			{At: acceptedAt, Type: pipelineRunEventQueued, Status: pipelineRunStatusQueued},
			{At: startedAt, Type: pipelineRunEventStarted, Status: pipelineRunStatusRunning},
			{At: endedAt, Type: pipelineRunEventCompleted, Status: pipelineRunStatusFailed},
		},
		Snapshots: []pipelinerun.StepSnapshot{},
		Failure: &pipelinerun.FailureContext{
			Error: errorSummary,
			At:    endedAt,
			Kind:  pipelineRunFailureKindPlatform,
		},
	}
}

// pipelineRunDetailFromRecord decodes the JSON timeline stored on a pipeline run row.
func pipelineRunDetailFromRecord(run *pipelineruns.PipelineRun) (*PipelineRunDetail, error) {
	if run == nil {
		return nil, ErrPipelineRunNotFound
	}
	var timeline pipelinerun.RunTimeline
	if err := json.Unmarshal(run.Timeline, &timeline); err != nil {
		return nil, xerror.Wrapf("decode pipeline run timeline: %w", err)
	}
	return &PipelineRunDetail{
		Metadata:               *toPipelineRunMetadata(run),
		Timeline:               timeline,
		IncrementalDiagnostics: run.IncrementalDiagnostics,
	}, nil
}

// emptyPipelineRunSummary provides the neutral incremental state for pre-runtime rows.
func emptyPipelineRunSummary() pipelinerun.IncrementalRunSummary {
	return pipelinerun.IncrementalRunSummary{
		Mode:               "full",
		CheckpointStatus:   "not_applicable",
		ReplaySafetyStatus: "safe",
	}
}

// emptyPipelineRunDiagnostics provides the neutral incremental diagnostics for pre-runtime rows.
func emptyPipelineRunDiagnostics() pipelinerun.IncrementalRunDiagnostics {
	return pipelinerun.IncrementalRunDiagnostics{
		Sources: []pipelinerun.IncrementalSourceDiagnostic{},
		ReplaySafety: pipelinerun.IncrementalReplaySafety{
			Status: "safe",
		},
	}
}

// durationMilliseconds returns a non-negative whole millisecond duration.
func durationMilliseconds(startedAt, completedAt time.Time) int64 {
	if completedAt.Before(startedAt) {
		return 0
	}
	return completedAt.Sub(startedAt).Milliseconds()
}

func (s *service) ExecuteVersion(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	specHash string,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*PipelineRunDetail, error) {
	specHash = strings.TrimSpace(specHash)
	if specHash == "" {
		return nil, xerror.InvalidRequired(xerror.PipelineVersion)
	}

	deployment, protection, source, runtimeInput, err := s.preparePipelineExecution(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		protectedEnvironmentConfirmation,
		source,
		runtimeInput,
	)
	if err != nil {
		return nil, err
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		specHash,
	)
	if err != nil {
		if !xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
			return nil, err
		}
		version, err = s.findVersionByShortSpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
		if err != nil {
			return nil, err
		}
	}
	var resolutionDigest *string
	if version.SpecHash == deployment.CurrentVersionSpecHash {
		resolutionDigest = deployment.CurrentResolutionDigest
	}
	return s.executeResolvedVersion(ctx, orgSlug, projectSlug, environmentSlug, version, deployment, runSlug, actor, source, runtimeInput, protection.Protected, nil, pipelinerun.IncrementalRunOptions{}, resolutionDigest)
}

// ExecuteVerifiedEvent runs one externally verified JSON event against the latest deployed version.
func (s *service) ExecuteVerifiedEvent(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
	eventInputJSON json.RawMessage,
) (*PipelineRunDetail, error) {
	deployment, protection, source, runtimeInput, err := s.preparePipelineExecution(
		ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, nil, source, runtimeInput,
	)
	if err != nil {
		return nil, xerror.Join(err, ErrPipelineExecutionNotStarted)
	}
	version, err := s.pipelineStore.FindVersionBySpecHash(
		ctx, orgSlug, projectSlug, pipelineSlug, deployment.CurrentVersionSpecHash,
	)
	if err != nil {
		return nil, xerror.Join(err, ErrPipelineExecutionNotStarted)
	}
	return s.executeResolvedVersion(
		ctx, orgSlug, projectSlug, environmentSlug, version, deployment, runSlug, actor, source, runtimeInput,
		protection.Protected, eventInputJSON, pipelinerun.IncrementalRunOptions{}, deployment.CurrentResolutionDigest,
	)
}

// ExecuteVerifiedEventVersion runs one externally verified JSON event against a pinned version.
func (s *service) ExecuteVerifiedEventVersion(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	specHash string,
	runSlug *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
	eventInputJSON json.RawMessage,
) (*PipelineRunDetail, error) {
	specHash = strings.TrimSpace(specHash)
	if specHash == "" {
		return nil, xerror.Join(xerror.InvalidRequired(xerror.PipelineVersion), ErrPipelineExecutionNotStarted)
	}
	deployment, protection, source, runtimeInput, err := s.preparePipelineExecution(
		ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, nil, source, runtimeInput,
	)
	if err != nil {
		return nil, xerror.Join(err, ErrPipelineExecutionNotStarted)
	}
	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
	if err != nil {
		return nil, xerror.Join(err, ErrPipelineExecutionNotStarted)
	}
	var resolutionDigest *string
	if version.SpecHash == deployment.CurrentVersionSpecHash {
		resolutionDigest = deployment.CurrentResolutionDigest
	}
	return s.executeResolvedVersion(
		ctx, orgSlug, projectSlug, environmentSlug, version, deployment, runSlug, actor, source, runtimeInput,
		protection.Protected, eventInputJSON, pipelinerun.IncrementalRunOptions{}, resolutionDigest,
	)
}

func normalizeSourceParameterKeys(keys []string) []string {
	if len(keys) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeRuntimeInput(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

// validateRunSourceSummary enforces that callers provide the immutable run source fields
// needed for launch triage and normalizes safe parameter names before persistence.
func validateRunSourceSummary(source pipelinerun.RunSourceSummary) (pipelinerun.RunSourceSummary, error) {
	source.Summary = strings.TrimSpace(source.Summary)
	source.ParameterKeys = normalizeSourceParameterKeys(source.ParameterKeys)
	if source.AttemptNumber <= 0 {
		source.AttemptNumber = 1
	}

	if err := structvalidate.Default().Struct(source); err != nil {
		return pipelinerun.RunSourceSummary{}, xerror.InvalidInput(xerror.PipelineRun).
			Detail(runSourceValidationDetail(err))
	}
	if err := validateRunAttemptSource(source); err != nil {
		return pipelinerun.RunSourceSummary{}, err
	}
	if err := validateScheduledRunSource(source); err != nil {
		return pipelinerun.RunSourceSummary{}, err
	}
	if err := validateWebhookRunSource(source); err != nil {
		return pipelinerun.RunSourceSummary{}, err
	}

	return source, nil
}

// validateRunAttemptSource enforces immutable retry grouping invariants before persistence.
func validateRunAttemptSource(source pipelinerun.RunSourceSummary) error {
	if source.AttemptNumber < 1 {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("run source attempt_number must be positive")
	}
	if source.AttemptNumber == 1 && source.RetryOfRunSlug != nil {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("first run attempt must not include retry_of_run_slug")
	}
	if source.AttemptNumber > 1 {
		if source.RootRunSlug == "" {
			return xerror.InvalidInput(xerror.PipelineRun).
				Detail("retry run source root_run_slug is required")
		}
		if _, err := slug.New(source.RootRunSlug.String()); err != nil {
			return xerror.InvalidInput(xerror.PipelineRun).With(err).Detail("retry run source root_run_slug is invalid")
		}
		if source.RetryOfRunSlug == nil || *source.RetryOfRunSlug == "" {
			return xerror.InvalidInput(xerror.PipelineRun).
				Detail("retry run source retry_of_run_slug is required")
		}
		if _, err := slug.New(source.RetryOfRunSlug.String()); err != nil {
			return xerror.InvalidInput(xerror.PipelineRun).With(err).Detail("retry run source retry_of_run_slug is invalid")
		}
	}
	return nil
}

// validateScheduledRunSource enforces schedule-specific source invariants before persistence.
func validateScheduledRunSource(source pipelinerun.RunSourceSummary) error {
	if source.Type != model.PipelineRunSourceTypeSchedule {
		if source.Schedule != nil {
			return xerror.InvalidInput(xerror.PipelineRun).
				Detail("run source schedule summary is only valid for scheduled runs")
		}
		return nil
	}
	if source.Entrypoint != model.PipelineRunSourceEntrypointScheduler {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("scheduled run source entrypoint must be scheduler")
	}
	if source.ActorType != model.EventActorTypeSystem {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("scheduled run source actor type must be system")
	}
	if source.ActorSlug != nil {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("scheduled run source must not include an actor slug")
	}
	if source.Schedule == nil {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("scheduled run source schedule summary is required")
	}
	if source.Schedule.TriggerSlug == "" ||
		source.Schedule.TriggerRevision <= 0 ||
		source.Schedule.Cron == "" ||
		source.Schedule.Timezone == "" ||
		source.Schedule.ScheduledFireAt.IsZero() ||
		source.Schedule.ActualFireAt.IsZero() ||
		source.Schedule.MisfireResult == model.PipelineTriggerScheduleMisfireResultUnknown ||
		!source.Schedule.MisfireResult.IsAPipelineTriggerScheduleMisfireResult() ||
		source.Schedule.MisfireResult == model.PipelineTriggerScheduleMisfireResultSkipped {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("scheduled run source schedule summary is invalid")
	}
	return nil
}

func validateWebhookRunSource(source pipelinerun.RunSourceSummary) error {
	if source.Type != model.PipelineRunSourceTypeWebhook {
		if source.Webhook != nil {
			return xerror.InvalidInput(xerror.PipelineRun).
				Detail("run source webhook summary is only valid for webhook runs")
		}
		return nil
	}
	if source.Entrypoint != model.PipelineRunSourceEntrypointWebhook ||
		source.ActorType != model.EventActorTypeExternal ||
		source.ActorSlug != nil ||
		source.Webhook == nil ||
		source.Webhook.DeliveryRecordID <= 0 ||
		source.Webhook.TriggerSlug == "" ||
		source.Webhook.TriggerRevision <= 0 ||
		source.Webhook.DeliveryID == "" ||
		len(source.Webhook.DeliveryID) > 128 ||
		len(source.Webhook.PayloadDigest) != sha256.Size*2 ||
		source.Webhook.PayloadSizeBytes < 0 ||
		source.Webhook.ReceivedAt.IsZero() ||
		source.Webhook.VerificationStatus != "verified" {
		return xerror.InvalidInput(xerror.PipelineRun).
			Detail("webhook run source summary is invalid")
	}
	return nil
}

// runSourceValidationDetail maps shared validator failures to stable pipeline-run errors.
func runSourceValidationDetail(err error) string {
	return structvalidate.DetailForFirstError(
		err,
		"run source summary is invalid",
		structvalidate.ErrorDetail{Field: "Type", Tag: "required", Detail: "run source type is required"},
		structvalidate.ErrorDetail{Field: "Type", Detail: "run source type must be manual, api, schedule, or webhook"},
		structvalidate.ErrorDetail{Field: "Summary", Detail: "run source summary is required"},
		structvalidate.ErrorDetail{Field: "Entrypoint", Tag: "required", Detail: "run source entrypoint is required"},
		structvalidate.ErrorDetail{Field: "Entrypoint", Detail: "run source entrypoint must be ui, api, scheduler, or webhook"},
		structvalidate.ErrorDetail{Field: "ActorType", Tag: "required", Detail: "run source actor type is required"},
		structvalidate.ErrorDetail{Field: "ActorType", Detail: "run source actor type must be a known event actor type"},
	)
}

func (s *service) ListRuns(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	filters *RunFilters,
	req model.ListRequest,
) (model.ListResponse[*PipelineRunMetadata], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var repoFilters *pipelineruns.RunListFilters
	if filters != nil {
		repoFilters = &pipelineruns.RunListFilters{
			Status:             filters.Status,
			Mode:               filters.Mode,
			BackfillSeriesSlug: filters.BackfillSeriesSlug,
		}
		if filters.PipelineSlug != nil {
			pipelineSlug := filters.PipelineSlug.String()
			repoFilters.PipelineSlug = &pipelineSlug
		}
	}

	runs, err := s.runRepo.ListRuns(
		ctx,
		s.dbtx,
		orgSlug,
		projectSlug,
		environmentSlug,
		repoFilters,
		queryReq,
	)
	if err != nil {
		return model.ListResponse[*PipelineRunMetadata]{}, xerror.Wrapf(
			"list pipeline runs: %w",
			err,
		)
	}

	metadata := make([]*PipelineRunMetadata, len(runs))
	for i, run := range runs {
		metadata[i] = toPipelineRunMetadata(run)
	}

	metadata, hasMore := model.FinalizePage(metadata, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		metadata,
		normalized,
		hasMore,
		func(item *PipelineRunMetadata, _ string) (string, string) {
			return item.CreatedAt.Format(time.RFC3339Nano), item.RunSlug
		},
	)

	return model.ListResponse[*PipelineRunMetadata]{
		Items:      metadata,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) GetRun(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*PipelineRunDetail, error) {
	run, err := s.runRepo.FindRunBySlug(
		ctx,
		s.dbtx,
		orgSlug,
		projectSlug,
		environmentSlug,
		runSlug,
	)
	if err != nil {
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			return nil, ErrPipelineRunNotFound.With(err).Detail(runSlug)
		}
		return nil, xerror.Wrapf("find pipeline run: %w", err)
	}

	detail, err := pipelineRunDetailFromRecord(run)
	if err != nil {
		return nil, err
	}
	parsedRunSlug, err := slug.New(runSlug)
	if err != nil {
		return nil, ErrPipelineRunNotFound.With(err).Detail(runSlug)
	}
	detail.Composition, err = s.inspectRunComposition(ctx, orgSlug, projectSlug, environmentSlug, detail, run.DeploymentResolutionDigest)
	if err != nil {
		return nil, err
	}
	detail.Composition.AdmissionEnabled = true
	if err := s.attachRunProofLink(ctx, orgSlug, projectSlug, environmentSlug, parsedRunSlug, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

// completeQueuedRunFailure closes an accepted run when execution fails before runtime persistence.
func (s *service) completeQueuedRunFailure(
	ctx context.Context,
	args jobs.PipelineRunExecutionArgs,
	startedAt time.Time,
	runErr error,
) error {
	endedAt := s.executionClock.Now().UTC()
	errorSummary := strings.TrimSpace(runErr.Error())
	if errorSummary == "" {
		errorSummary = ErrPipelineExecutionFailed.Error()
	}
	failureKind := pipelineRunFailureKindPlatform
	var failureSide *string
	var stepCount int32
	var stepSlug string
	summary, requestedDiagnostics := requestedIncrementalState(args.Incremental)
	var replayOfRunSlug, replayFromStep *string
	timeline := failedQueuedPipelineRunTimeline(args, startedAt, endedAt, errorSummary)
	if xerror.Is(runErr, errWorkerOwnershipLost) {
		timeline.Failure.ReasonCode = pipelinerun.FailureReasonCodeWorkerOwnershipLost
		timeline.Failure.ActionHint = "After native queue rescue, recover retained inputs only when ownership is fenced and every sink is repeat-safe. Reconcile provider outcomes if repair inputs are unavailable."
	}
	// Failure before runtime admission must preserve completed checks from a
	// previous delivery of this attempt, including after a worker interruption.
	if err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		existing, err := s.runRepo.FindRunBySlug(ctx, tx, args.OrganizationSlug, args.ProjectSlug, args.EnvironmentSlug, args.RunSlug.String())
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if existing == nil || len(existing.Timeline) == 0 {
			return nil
		}
		var previous pipelinerun.RunTimeline
		if err := json.Unmarshal(existing.Timeline, &previous); err != nil {
			return xerror.Unexpected().With(err)
		}
		admitted := previous.Revision > 0 || len(previous.StructuralChecks) > 0
		if len(previous.Events) > 0 || admitted {
			startedAt = existing.StartedAt
			stepCount, stepSlug = existing.StepCount, existing.StepSlug
			failure := timeline.Failure
			timeline = previous
			if admitted {
				timeline.Revision++
				summary, requestedDiagnostics = existing.Incremental, existing.IncrementalDiagnostics
				replayOfRunSlug, replayFromStep = existing.ReplayOfRunSlug, existing.ReplayFromStep
			}
			timeline.Status = pipelineRunStatusFailed
			if timeline.Failure == nil {
				timeline.Failure = failure
			}
			timeline.Failure.OutcomeUncertain = admitted
			if timeline.Failure.OutcomeUncertain {
				timeline.Failure.AutomaticRetryProhibited = true
			}
			errorSummary = timeline.Failure.Error
			if timeline.Failure.Kind != "" {
				failureKind = timeline.Failure.Kind
			}
			if timeline.Failure.Side != "" {
				failureSide = &timeline.Failure.Side
			}
			timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{At: endedAt, Type: pipelineRunEventCompleted, Status: pipelineRunStatusFailed})
		}
		if len(previous.StructuralChecks) > 0 {
			timeline.StructuralChecks = previous.StructuralChecks
			timeline.DeploymentResolutionDigest = previous.DeploymentResolutionDigest
			timeline.Admissions = previous.Admissions
			pipelinerun.FinalizeStructuralChecks(timeline.StructuralChecks, endedAt)
		}
		return nil
	}); err != nil {
		return err
	}
	timelineJSON, err := json.Marshal(timeline)
	if err != nil {
		return xerror.Wrapf("encode failed pipeline run timeline: %w", err)
	}

	incrementalDiagnosticsJSON, err := json.Marshal(requestedDiagnostics)
	if err != nil {
		return xerror.Wrapf("encode failed pipeline run diagnostics: %w", err)
	}

	var run *PipelineRunDetail
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		row, err := s.runRepo.CompleteRun(ctx, tx, args.OrganizationSlug, args.ProjectSlug, args.EnvironmentSlug, args.RunSlug.String(), &pipelineruns.CompleteRunParams{
			PipelineSlug:    args.PipelineSlug.String(),
			ReplayOfRunSlug: replayOfRunSlug, ReplayFromStep: replayFromStep,
			Status:                 pipelineRunStatusFailed,
			DurationMs:             durationMilliseconds(startedAt, endedAt),
			StepCount:              stepCount,
			StepSlug:               stepSlug,
			ErrorSummary:           &errorSummary,
			FailureKind:            &failureKind,
			FailureSide:            failureSide,
			StartedAt:              startedAt,
			EndedAt:                &endedAt,
			Timeline:               timelineJSON,
			Incremental:            summary,
			IncrementalDiagnostics: incrementalDiagnosticsJSON,
		})
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		run, err = pipelineRunDetailFromRecord(row)
		if err != nil {
			return err
		}
		return s.recordPipelineRunEvent(ctx, tx, args.OrganizationSlug, args.ProjectSlug, args.Actor, run, args.ProtectedEnvironment)
	})
	if err != nil {
		return err
	}
	return nil
}

type validationHistoryPermission struct {
	action   model.Action
	resource model.Resource
}

// validationHistoryReadPermissions returns the permission split required to inspect validation evidence.
func validationHistoryReadPermissions() []validationHistoryPermission {
	return []validationHistoryPermission{
		{action: model.Read, resource: model.ResourcePipelines},
		{action: model.Read, resource: model.ResourceDataSources},
	}
}

// validationHistoryRevalidatePermissions returns the permission split required to revalidate a pipeline.
func validationHistoryRevalidatePermissions() []validationHistoryPermission {
	return []validationHistoryPermission{
		{action: model.Update, resource: model.ResourcePipelines},
		{action: model.Read, resource: model.ResourceDataSources},
	}
}

// requireValidationHistoryPermissions enforces validation-history permissions at the service boundary.
func (s *service) requireValidationHistoryPermissions(
	ctx context.Context,
	orgSlug slug.Slug,
	principal ValidationHistoryPrincipal,
	permissions ...validationHistoryPermission,
) error {
	if s.authorizationService == nil {
		return xerror.ExecutionUnavailable().Detail("authorization service is required for pipeline validation history")
	}
	if err := validateValidationHistoryPrincipal(orgSlug, principal); err != nil {
		return err
	}
	for _, permission := range permissions {
		input := authorization.Input{
			Action:   permission.action,
			Resource: permission.resource,
			OrgSlug:  &orgSlug,
		}
		if principal.APIKeySlug != "" {
			input.APIKeySlug = principal.APIKeySlug
			input.APIKeyRoleSlug = principal.APIKeyRoleSlug
		} else {
			input.UserSlug = principal.UserSlug
		}

		can, err := s.authorizationService.Can(ctx, input)
		if err != nil {
			return xerror.Wrapf("authorize pipeline validation history: %w", err)
		}
		if !can {
			return xerror.Forbidden(xerror.PipelineValidationResult).
				Detailf("permission denied for %s %s", permission.action, permission.resource)
		}
	}
	return nil
}

// validateValidationHistoryPrincipal rejects missing or cross-organization validation-history actors.
func validateValidationHistoryPrincipal(orgSlug slug.Slug, principal ValidationHistoryPrincipal) error {
	switch {
	case principal.APIKeySlug != "":
		if principal.APIKeyOrgSlug == "" || principal.APIKeyRoleSlug == "" {
			return ErrPipelineValidationPrincipalRequired
		}
		if principal.APIKeyOrgSlug != orgSlug {
			return xerror.Forbidden(xerror.PipelineValidationResult).
				Detail("api key principal does not belong to the organization")
		}
		return nil
	case principal.UserSlug != "":
		return nil
	default:
		return ErrPipelineValidationPrincipalRequired
	}
}

// recordValidationResultTx validates and persists a validation result inside the caller's unit of work.
func (s *service) recordValidationResultTx(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	source PipelineValidationResultSource,
	pipeline *pipelinespec.Pipeline,
	result *PipelineValidationResult,
) (*PipelineValidationResultRecord, error) {
	if pipeline == nil {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("pipeline validation result requires a pipeline")
	}
	if pipeline.Metadata.Slug == "" {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("pipeline validation result requires a pipeline slug")
	}
	if result == nil {
		result = &PipelineValidationResult{Diagnostics: []PipelineValidationDiagnostic{}}
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []PipelineValidationDiagnostic{}
	}
	if !source.IsAPipelineValidationResultSource() || source == PipelineValidationResultSourceUnknown {
		return nil, xerror.InvalidInput(xerror.PipelineValidationResult).
			Detail("pipeline validation result source is required")
	}

	resultSlug, err := slug.WithRandomSuffix(slug.Slug("validation-"+pipeline.Metadata.Slug.String()), 6)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.PipelineValidationResult).
			Detailf("generate validation result slug: %w", err)
	}
	diagnostics, err := json.Marshal(result.Diagnostics)
	if err != nil {
		return nil, xerror.Wrapf("encode pipeline validation diagnostics: %w", err)
	}
	specHash, err := validationSpecHash(pipeline)
	if err != nil {
		return nil, err
	}
	var impactReportSlug *slug.Slug
	if result.ImpactReport != nil && result.ImpactReport.Slug != "" {
		value := result.ImpactReport.Slug
		impactReportSlug = &value
	}

	row, err := s.validationResults.Create(
		ctx,
		tx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelinevalidationsrepo.CreateParams{
			Slug:             resultSlug,
			PipelineSlug:     pipeline.Metadata.Slug,
			Source:           source.String(),
			Valid:            result.Valid,
			SpecHash:         specHash,
			DiagnosticsJSONB: diagnostics,
			ImpactReportSlug: impactReportSlug,
		},
	)
	if err != nil {
		return nil, err
	}
	record, err := validationResultRecordFromRepo(row)
	if err != nil {
		return nil, err
	}
	candidates, err := validationNotificationCandidates(orgSlug, pipeline, record)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if err := s.notificationProducer.EnqueueCandidate(ctx, tx, candidate); err != nil {
			return nil, xerror.Wrapf("enqueue validation notification candidate: %w", err)
		}
	}
	return record, nil
}

func (s *service) recordExecutionValidationFailure(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	err error,
) {
	result, ok := pipelineValidationResultFromExecutionError(err, pipeline)
	if !ok {
		return
	}
	if _, recordErr := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*PipelineValidationResultRecord, error) {
		return s.recordValidationResultTx(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			PipelineValidationResultSourceExecute,
			pipeline,
			result,
		)
	}); recordErr != nil {
		pipelineSlug := ""
		if pipeline != nil {
			pipelineSlug = pipeline.Metadata.Slug.String()
		}
		slog.WarnContext(
			ctx,
			"record execution preflight validation result failed",
			slog.String("org_slug", orgSlug.String()),
			slog.String("project_slug", projectSlug.String()),
			slog.String("environment_slug", environmentSlug.String()),
			slog.String("pipeline_slug", pipelineSlug),
			slog.Any("error", recordErr),
		)
	}
}

// preparePipelineExecution validates common runtime inputs and environment deployment state.
func (s *service) preparePipelineExecution(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*pipelinesrepo.PipelineDeployment, *model.ProjectEnvironmentProtection, pipelinerun.RunSourceSummary, map[string]any, error) {
	if err := s.admission.CheckRunAccess(ctx, orgSlug); err != nil {
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, err
	}
	if s.runtime == nil {
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, xerror.ExecutionUnavailable().Detail("pipeline runtime is required")
	}
	runtimeInput = normalizeRuntimeInput(runtimeInput)
	if err := pipelineruntimevars.ValidateInput(runtimeInput); err != nil {
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, err
	}
	source.EnvironmentSlug = environmentSlug
	source.ParameterKeys = append(source.ParameterKeys, pipelineruntimevars.InputParameterKeys(runtimeInput)...)
	var err error
	source, err = validateRunSourceSummary(source)
	if err != nil {
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, err
	}

	deployment, err := s.pipelineStore.FindDeployment(
		ctx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
	)
	if err != nil {
		if xerror.Is(err, ErrPipelineDeploymentNotFound) {
			if _, specErr := s.pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug); specErr != nil {
				return nil, nil, pipelinerun.RunSourceSummary{}, nil, specErr
			}
		}
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, err
	}

	protection, err := s.pipelineStore.ProjectEnvironmentProtection(ctx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, err
	}
	if err := protectedenv.Confirm(protection, protectedEnvironmentConfirmation); err != nil {
		return nil, nil, pipelinerun.RunSourceSummary{}, nil, err
	}

	return deployment, protection, source, runtimeInput, nil
}

// executeResolvedVersion runs one already-selected immutable pipeline version.
func (s *service) executeResolvedVersion(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	version *pipelinesrepo.PipelineVersion,
	deployment *pipelinesrepo.PipelineDeployment,
	runSlug *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
	protected bool,
	eventInputJSON json.RawMessage,
	incremental pipelinerun.IncrementalRunOptions,
	resolutionDigest *string,
) (detail *PipelineRunDetail, executionErr error) {
	runtimeStarted := false
	defer func() {
		if !runtimeStarted && executionErr != nil {
			executionErr = xerror.Join(executionErr, ErrPipelineExecutionNotStarted)
		}
	}()
	ctx = executiondelivery.WithInitiatingUser(ctx, actor.UserSlugValue())
	pipeline, deploymentRaw, err := s.executionPipeline(ctx, orgSlug, projectSlug, environmentSlug, version, resolutionDigest)
	if err != nil {
		return nil, xerror.Wrapf("decode pipeline version %s: %w", version.SpecHash, err)
	}
	// Keep validation-history hashes tied to the stored version, not an environment-resolved runtime copy.
	canonicalPipeline, err := pipelineFromJSON(version.SpecJSONB)
	if err != nil {
		return nil, xerror.Wrapf("decode canonical pipeline version %s: %w", version.SpecHash, err)
	}

	if resolutionDigest == nil && s.dataSourceRefResolver != nil {
		resolved, err := s.dataSourceRefResolver.ResolvePipelineDataSourceRefs(
			ctx,
			orgSlug,
			projectSlug,
			environmentSlug,
			pipeline,
		)
		if err != nil {
			s.recordExecutionValidationFailure(
				ctx,
				orgSlug,
				projectSlug,
				environmentSlug,
				canonicalPipeline,
				err,
			)
			return nil, err
		}
		if resolved != nil && resolved.Pipeline != nil {
			pipeline = resolved.Pipeline
		}
	}
	var eventInput *pipelinerun.EventInput
	if len(eventInputJSON) > 0 {
		eventInput, err = verifiedEventInput(pipeline, eventInputJSON)
		if err != nil {
			return nil, err
		}
	}

	var proofLink *proofreceipts.LifecycleLink
	var acceptedAt time.Time
	accepted := false
	if deployment != nil {
		link, err := s.proofLinks.FindDeploymentLink(ctx, s.dbtx, orgSlug, projectSlug, environmentSlug, pipeline.Metadata.Slug)
		if err != nil && !xerror.Is(err, proofreceipts.ErrLifecycleLinkNotFound) {
			return nil, xerror.Wrapf("find deployment proof lifecycle link: %w", xerror.Unexpected().With(err))
		}
		if link != nil && ProofLifecycleLinkStatus(link, version.SpecHash, deploymentLinkIdentifier(pipeline.Metadata.Slug, deployment), s.executionClock.Now()) == "matched" {
			acceptedAt = s.executionClock.Now().UTC()
			acceptedSlug, err := acceptedPipelineRunSlug(runSlug, pipeline.Metadata.Slug, acceptedAt)
			if err != nil {
				return nil, err
			}
			source = pipelinerun.NormalizeAttemptSource(source, acceptedSlug)
			if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
				_, link, err := s.createQueuedRun(ctx, tx, orgSlug, projectSlug, environmentSlug, pipeline.Metadata.Slug,
					acceptedSlug, version.ID, source, acceptedAt, deployment, incremental)
				proofLink = link
				return err
			}); err != nil {
				return nil, err
			}
			runSlug = &acceptedSlug
			accepted = true
		}
	}

	runReq := pipelineruntime.RunRequest{
		DeploymentPlan:    deploymentRaw,
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		EnvironmentSlug:   environmentSlug,
		Pipeline:          pipeline,
		PipelineVersionID: version.ID,
		Source:            source,
		RuntimeInput:      runtimeInput,
		EventInput:        eventInput,
	}
	if deployment != nil && version.ID == deployment.CurrentVersionID {
		runReq.DeploymentID = &deployment.ID
		runReq.DeploymentRevision = &deployment.Revision
	}
	if resolutionDigest != nil {
		runReq.DeploymentResolutionDigest = *resolutionDigest
		runReq.Pipeline = nil
	}
	runReq.Incremental = incremental
	if runSlug != nil {
		runReq.RunSlug = runSlug.String()
	}
	if source.RetryOfRunSlug != nil {
		// Keep retries in the original external-effect namespace even though this
		// delivery has a new pipeline run slug.
		replayOfRunSlug := source.RetryOfRunSlug.String()
		runReq.ReplayOfRunSlug = &replayOfRunSlug
	}

	var runOutcome *pipelineruntime.RunOutcome
	runErr := s.admission.WithPipelineExecution(ctx, orgSlug, deployment == nil, func(ctx context.Context) error {
		runtimeStarted = true
		var err error
		runOutcome, err = s.runtime.Execute(ctx, runReq)
		return err
	})
	if runErr != nil && runOutcome == nil {
		if accepted {
			if err := s.completeQueuedRunFailure(ctx, jobs.PipelineRunExecutionArgs{
				OrganizationSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug,
				PipelineSlug: pipeline.Metadata.Slug, PipelineVersionHash: version.SpecHash, RunSlug: *runSlug,
				Actor: actor, Source: source, AcceptedAt: acceptedAt, ProtectedEnvironment: protected, Incremental: incremental,
			}, acceptedAt, runErr); err != nil {
				return nil, err
			}
		}
		s.recordExecutionValidationFailure(
			ctx,
			orgSlug,
			projectSlug,
			environmentSlug,
			canonicalPipeline,
			runErr,
		)
		return nil, xerror.Wrapf("%w: %w", ErrPipelineExecutionFailed, runErr)
	}

	run, err := runDetailFromOutcome(ctx, s.executionClock, runOutcome, version.SpecHash)
	if err != nil {
		return nil, xerror.Wrapf("%w: build run detail: %w", ErrPipelineExecutionFailed, err)
	}

	run.ProofLink = toProofLifecycleLink(proofLink, version.SpecHash, "", s.executionClock.Now())

	if runErr != nil {
		// Run was persisted but the pipeline itself failed (e.g. data source error).
		// Return the run detail alongside the error so the caller can surface full
		// failure context (kind, side, data_source_slug, action_hint) to the user.
		wrappedRunErr := xerror.Wrapf("%w: %w", ErrPipelineExecutionFailed, runErr)
		_ = s.recordPipelineRunEventInTx(ctx, orgSlug, projectSlug, actor, run, protected)
		return run, wrappedRunErr
	}

	_ = s.recordPipelineRunEventInTx(ctx, orgSlug, projectSlug, actor, run, protected)
	return run, nil
}

func verifiedEventInput(
	pipeline *pipelinespec.Pipeline,
	document json.RawMessage,
) (*pipelinerun.EventInput, error) {
	inputStep := -1
	for i := range pipeline.Steps {
		if pipeline.Steps[i].Input == nil {
			continue
		}
		if inputStep >= 0 {
			return nil, xerror.InvalidInput(xerror.Event).Detail("pipeline declares more than one input step")
		}
		inputStep = i
	}
	if inputStep < 0 {
		return nil, xerror.InvalidInput(xerror.Event).Detail("pipeline does not declare an input step")
	}
	return pipelinerun.NewVerifiedJSONEventInput(pipeline.Steps[inputStep].Slug, document)
}

func (s *service) findVersionByShortSpecHash(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHashPrefix string,
) (*pipelinesrepo.PipelineVersion, error) {
	version, err := s.pipelineStore.FindVersionBySpecHashPrefix(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		specHashPrefix,
	)
	if err == nil {
		return version, nil
	}
	if xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
		return nil, ErrPipelineVersionNotFound.With(err)
	}
	if xerror.Is(err, pipelinesrepo.ErrPipelineVersionAmbiguous) {
		return nil, xerror.InvalidInput(xerror.PipelineVersion).With(err).Detail(fmt.Sprintf("ambiguous pipeline version hash prefix %q", specHashPrefix)).WithPublicDetail(fmt.Sprintf("ambiguous pipeline version hash prefix %q", specHashPrefix))
	}

	return nil, err
}

func (s *service) resolveExpectedDeploymentSpecHash(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	expected string,
) (*string, error) {
	specHash := strings.TrimSpace(expected)
	if specHash == "" {
		return nil, xerror.InvalidRequired(xerror.PipelineVersion)
	}

	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
	if err == nil {
		return &version.SpecHash, nil
	}
	if !xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
		return nil, err
	}

	version, err = s.pipelineStore.FindVersionBySpecHashPrefix(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		specHash,
	)
	if err == nil {
		return &version.SpecHash, nil
	}
	if xerror.Is(err, pipelinesrepo.ErrPipelineVersionAmbiguous) {
		return nil, xerror.InvalidInput(xerror.PipelineVersion).With(err).Detail(fmt.Sprintf("ambiguous pipeline version hash prefix %q", specHash)).WithPublicDetail(fmt.Sprintf("ambiguous pipeline version hash prefix %q", specHash))
	}
	if xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
		return &specHash, nil
	}

	return nil, err
}

func (s *service) resolveProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (*model.ProjectPipelineBackend, error) {
	project, err := s.projectBackend.GetProject(ctx, orgSlug, projectSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve project pipeline backend: %w", err)
	}
	if project == nil || project.PipelineBackend == nil {
		return nil, ErrPipelineGitSourceRequired.Detail(
			"project pipeline backend is required for Git-authored pipeline routing",
		)
	}

	if strings.TrimSpace(project.PipelineBackend.TrackedBranch) == "" {
		return nil, ErrPipelineGitSourceRequired.Detail(
			"project pipeline backend tracked branch is required for Git-authored pipeline routing",
		)
	}

	return project.PipelineBackend, nil
}

func (s *service) resolveProjectPipelineRepository(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (string, error) {
	project, err := s.projectBackend.GetProject(ctx, orgSlug, projectSlug)
	if err != nil {
		return "", xerror.Wrapf("resolve project pipeline repository: %w", err)
	}
	if project == nil || project.PipelineBackend == nil {
		return "", nil
	}
	return repostore.SanitizeRemoteURL(project.PipelineBackend.Repo), nil
}

func (s *service) recordPipelineRunEventInTx(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	actor model.EventActor,
	run *PipelineRunDetail,
	protectedEnvironment bool,
) error {
	return s.withEventTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.recordPipelineRunEvent(ctx, tx, orgSlug, projectSlug, actor, run, protectedEnvironment)
	})
}

func (s *service) recordPipelineEventWithMetadata(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	actor model.EventActor,
	pipelineSlug slug.Slug,
	action model.EventAction,
	metadata map[string]any,
) error {
	switch action {
	case model.EventActionUpdate, model.EventActionReconcile:
	default:
		return xerror.InvalidInput(xerror.Event).Detail("unsupported pipeline event action")
	}

	s.eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, pipelineEvent{
		projectSlug:  projectSlug,
		pipelineSlug: pipelineSlug,
		action:       action,
		metadata:     metadata,
	})
	return nil
}

func (s *service) recordPipelineEventWithMetadataInTx(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	actor model.EventActor,
	pipelineSlug slug.Slug,
	action model.EventAction,
	metadata map[string]any,
) error {
	return s.withEventTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.recordPipelineEventWithMetadata(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			actor,
			pipelineSlug,
			action,
			metadata,
		)
	})
}

func (s *service) recordPipelineDeploymentEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
	deployment *pipelinesrepo.PipelineDeployment,
	sourceEnvironmentSlug *slug.Slug,
	protectedEnvironment bool,
	proofReceiptSlug *slug.Slug,
) error {
	if deployment == nil {
		return xerror.Unexpected().Detail("pipeline deployment event requires deployment")
	}
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, pipelineDeploymentEvent{
		projectSlug:             projectSlug,
		environmentSlug:         environmentSlug,
		pipelineSlug:            pipelineSlug,
		currentVersionSpecHash:  deployment.CurrentVersionSpecHash,
		previousVersionSpecHash: deployment.PreviousVersionSpecHash,
		sourceEnvironmentSlug:   sourceEnvironmentSlug,
		protectedEnvironment:    protectedEnvironment,
		proofReceiptSlug:        proofReceiptSlug,
	}); err != nil {
		return xerror.Wrapf("enqueue required pipeline deployment audit event: %w", err)
	}
	return nil
}

func (s *service) recordPipelineRunEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	actor model.EventActor,
	run *PipelineRunDetail,
	protectedEnvironment bool,
) error {
	if run == nil {
		return xerror.Unexpected().Detail("pipeline run event requires run")
	}

	runSlug, err := slug.New(run.Metadata.RunSlug)
	if err != nil {
		return xerror.Wrapf("parse pipeline run slug: %w", err)
	}
	event := pipelineRunExecutedEvent{
		projectSlug:          projectSlug,
		environmentSlug:      slug.FromString(run.Metadata.EnvironmentSlug),
		pipelineSlug:         slug.FromString(run.Metadata.PipelineSlug),
		runSlug:              runSlug,
		versionSpecHash:      run.Metadata.PipelineVersionSpecHash,
		status:               run.Metadata.Status,
		durationMs:           run.Metadata.DurationMs,
		stepCount:            run.Metadata.StepCount,
		lastStepSlug:         run.Metadata.StepSlug,
		sourceType:           run.Metadata.Source.Type.String(),
		sourceSummary:        run.Metadata.Source.Summary,
		sourceEntrypoint:     run.Metadata.Source.Entrypoint.String(),
		sourceParameterKeys:  run.Metadata.Source.ParameterKeys,
		requestID:            run.Metadata.Source.RequestID,
		traceID:              run.Metadata.Source.TraceID,
		protectedEnvironment: protectedEnvironment,
	}
	// Scheduled retries share effect identity but do not reuse pinned step outputs.
	if run.Metadata.ReplayFromStep != nil {
		event.replayOfRunSlug = run.Metadata.ReplayOfRunSlug
	}
	if schedule := run.Metadata.Source.Schedule; schedule != nil {
		event.scheduleTriggerSlug = schedule.TriggerSlug
		event.scheduleTriggerRevision = schedule.TriggerRevision
		event.scheduleCron = schedule.Cron
		event.scheduleTimezone = schedule.Timezone
		event.scheduleScheduledFireAt = schedule.ScheduledFireAt
		event.scheduleActualFireAt = schedule.ActualFireAt
		event.scheduleMisfireResult = schedule.MisfireResult.String()
	}
	if event.replayOfRunSlug != nil {
		_, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event)
		return err
	}
	job, ok := s.eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, event)
	if !ok || job.EventSlug == "" {
		return nil
	}

	if err := s.runRepo.UpdateRunSourceEvent(
		ctx,
		tx,
		orgSlug,
		projectSlug,
		slug.FromString(run.Metadata.EnvironmentSlug),
		run.Metadata.RunSlug,
		job.EventSlug,
	); err != nil {
		slog.WarnContext(ctx,
			"pipeline run source event link failed",
			slog.String("org_slug", orgSlug.String()),
			slog.String("project_slug", projectSlug.String()),
			slog.String("environment_slug", run.Metadata.EnvironmentSlug),
			slog.String("pipeline_slug", run.Metadata.PipelineSlug),
			slog.String("run_slug", run.Metadata.RunSlug),
			slog.String("event_slug", job.EventSlug.String()),
			slog.Any("error", err),
		)
		return nil
	}
	run.Metadata.Source.SourceEventSlug = &job.EventSlug
	return nil
}

func (s *service) withEventTx(
	ctx context.Context,
	fn func(context.Context, postgres.DBTX) error,
) error {
	if s.txm == nil {
		return xerror.Unexpected().Detail("pipeline event transaction manager is required")
	}
	return s.txm.RunInTx(ctx, fn)
}

// New creates a pipeline service with strict dependency requirements.
func New(
	compiledPlans *compiledplans.Repository,
	resolutions *deploymentresolutions.Repository,
	pipelineStore PipelineStore,
	dbtx postgres.DBTX,
	txm postgres.TransactionManager,
	runRepo pipelineruns.Repository,
	checkpoints incrementalcheckpoints.Repository,
	runStore pipelinerun.RunStore,
	driftReports driftimpactrepo.Repository,
	queryExecutor queryExecutor,
	authorizationService authorization.Service,
	admission admissionsvc.Service,
	dataSourceService datasourcessvc.Service,
	projectBackend pipelinegit.ProjectBackendService,
	gitSourceResolver pipelinegit.SourceResolver,
	clk clock.Clock,
	pipelineRunProducer workerproducer.PipelineRunProducer,
	pipelineGitProducer workerproducer.PipelineGitProducer,
	eventProducer workerproducer.EventProducer,
	runtimeResolver pipelineRuntimeResolver,
	sinkEffectLedger pipelinerun.SinkEffectLedger,
	proofLinks proofreceipts.LifecycleRepository,
	notificationProducer notificationsvc.CandidateProducer,
	options ...ServiceOption,
) (Service, error) {
	if notificationProducer == nil {
		return nil, xerror.Unexpected().Detail("notification candidate producer is required")
	}
	if compiledPlans == nil || resolutions == nil {
		return nil, xerror.Unexpected().Detail("compiled plan and deployment resolution repositories are required")
	}
	if pipelineStore == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("pipeline store is required for pipeline execution"),
		)
	}
	if dbtx == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("dbtx is required when run repository is configured"),
		)
	}
	if txm == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("txm is required for pipeline writes"),
		)
	}
	if runRepo == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail(
				"run repository is required when persistence is configured",
			),
		)
	}
	if checkpoints == nil {
		return nil, xerror.UnexpectedRequired(xerror.PipelineCheckpoint)
	}
	if runStore == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("run store is required for pipeline execution"),
		)
	}
	if driftReports == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("drift impact repository is required"),
		)
	}
	if queryExecutor == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("query executor is required for pipeline execution"),
		)
	}
	if authorizationService == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("authorization service is required for pipeline validation history"),
		)
	}
	if admission == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("admission service is required for pipeline execution"),
		)
	}
	if dataSourceService == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("datasource service is required for pipeline execution"),
		)
	}
	if projectBackend == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("project backend service is required for pipeline git routing"),
		)
	}
	if gitSourceResolver == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("git source resolver is required for pipeline reconcile"),
		)
	}
	if eventProducer == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("event producer is required for pipeline event recording"),
		)
	}
	if clk == nil {
		clk = clock.New()
	}
	if pipelineGitProducer == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("pipeline git producer is required"),
		)
	}
	if pipelineRunProducer == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("pipeline run producer is required"),
		)
	}
	if runtimeResolver == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("pipeline runtime resolver is required"),
		)
	}
	if sinkEffectLedger == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("sink effect ledger is required for pipeline execution"),
		)
	}
	if proofLinks == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("proof lifecycle repository is required"),
		)
	}
	dataSourceRefResolver := pipelineDataSourceRefResolver(runtimeResolver)
	resolvedOptions := serviceOptions{}
	for _, option := range options {
		if option != nil {
			option(&resolvedOptions)
		}
	}
	runnerOptions := []pipelineruntime.RunnerOption{
		pipelineruntime.WithEnvironmentSecretResolver(runtimeResolver),
		pipelineruntime.WithTransformLimits(resolvedOptions.transformLimits),
	}
	if resolvedOptions.transformExecutor != nil {
		runnerOptions = append(
			runnerOptions,
			pipelineruntime.WithTransformExecutor(
				resolvedOptions.transformExecutor,
				resolvedOptions.transformLimits,
			),
		)
	}
	runtime, err := pipelineruntime.NewRunner(
		runnerQueryExecutorAdapter{executor: queryExecutor},
		clk,
		runStore,
		dataSourceService,
		pipelineruntime.NewSinkRuntime(queryExecutor, sinkEffectLedger),
		runnerOptions...,
	)
	if err != nil {
		return nil, xerror.Wrapf("%w: initialize runner: %w", ErrPipelineExecutionFailed, err)
	}

	return &service{
		notificationProducer: notificationProducer,
		compiledPlans:        compiledPlans, resolutions: resolutions,
		dbtx:                  dbtx,
		txm:                   txm,
		runRepo:               runRepo,
		checkpoints:           checkpoints,
		pipelineStore:         pipelineStore,
		driftReports:          driftReports,
		validationResults:     pipelinevalidationsrepo.New(),
		authorizationService:  authorizationService,
		admission:             admission,
		proofLinks:            proofLinks,
		runtime:               runtime,
		queryExecutor:         queryExecutor,
		dataSourceService:     dataSourceService,
		dataSourceRefResolver: dataSourceRefResolver,
		projectBackend:        projectBackend,
		gitSourceResolver:     gitSourceResolver,
		pipelineGitProducer:   pipelineGitProducer,
		pipelineRunProducer:   pipelineRunProducer,
		eventProducer:         eventProducer,
		executionClock:        clk,
	}, nil
}

func toPipelineSummary(
	spec *pipelinesrepo.PipelineSpec,
) *PipelineSummary {
	if spec == nil {
		return nil
	}

	return &PipelineSummary{
		Slug:                   spec.Slug,
		Name:                   spec.Name,
		HasDraft:               spec.HasDraft,
		SyncStatus:             spec.SyncStatus,
		SourcePath:             spec.SourcePath,
		SourceRef:              spec.SourceRef,
		LastAppliedCommit:      spec.LastAppliedCommit,
		CreatedAt:              spec.CreatedAt,
		UpdatedAt:              spec.UpdatedAt,
		CurrentVersionID:       spec.CurrentVersionID,
		CurrentVersionSpecHash: spec.CurrentVersionSpecHash,
	}
}

func toPipelineVersionSummary(version *pipelinesrepo.PipelineVersion) *PipelineVersionSummary {
	if version == nil {
		return nil
	}

	return &PipelineVersionSummary{
		SpecHash:      version.SpecHash,
		ShortSpecHash: shortPipelineSpecHash(version.SpecHash),
		PipelineSlug:  version.PipelineSlug,
		GitCommitSHA:  version.GitCommitSHA,
		CreatedAt:     version.CreatedAt,
	}
}

func toPipelineDeploymentDetail(
	deployment *pipelinesrepo.PipelineDeployment,
) *PipelineDeploymentDetail {
	if deployment == nil {
		return nil
	}

	return &PipelineDeploymentDetail{
		Revision:                     deployment.Revision,
		CurrentResolutionDigest:      deployment.CurrentResolutionDigest,
		PreviousResolutionDigest:     deployment.PreviousResolutionDigest,
		ProjectSlug:                  deployment.ProjectSlug,
		EnvironmentSlug:              deployment.EnvironmentSlug,
		PipelineSlug:                 deployment.PipelineSlug,
		CurrentVersionID:             deployment.CurrentVersionID,
		CurrentVersionSpecHash:       deployment.CurrentVersionSpecHash,
		CurrentVersionShortSpecHash:  shortPipelineSpecHash(deployment.CurrentVersionSpecHash),
		PreviousVersionID:            deployment.PreviousVersionID,
		PreviousVersionSpecHash:      deployment.PreviousVersionSpecHash,
		PreviousVersionShortSpecHash: optionalShortPipelineSpecHash(deployment.PreviousVersionSpecHash),
		DeployedByActorType:          deployment.DeployedByActorType,
		DeployedByActorSlug:          deployment.DeployedByActorSlug,
		DeployedAt:                   deployment.DeployedAt,
		UpdatedAt:                    deployment.UpdatedAt,
	}
}

func (s *service) attachDeploymentProofLink(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, detail *PipelineDeploymentDetail) error {
	if detail == nil {
		return nil
	}
	link, err := s.proofLinks.FindDeploymentLink(ctx, s.dbtx, orgSlug, projectSlug, environmentSlug, detail.PipelineSlug)
	if xerror.Is(err, proofreceipts.ErrLifecycleLinkNotFound) {
		return nil
	}
	if err != nil {
		return xerror.Wrapf("find deployment proof lifecycle link: %w", xerror.Unexpected().With(err))
	}
	detail.ProofLink = toProofLifecycleLink(link, detail.CurrentVersionSpecHash, deploymentLinkIdentifierValues(detail.PipelineSlug, detail.CurrentVersionSpecHash, detail.DeployedAt), s.executionClock.Now())
	return nil
}

func (s *service) attachRunProofLink(ctx context.Context, orgSlug, projectSlug, environmentSlug, runSlug slug.Slug, detail *PipelineRunDetail) error {
	if detail == nil {
		return nil
	}
	link, err := s.proofLinks.FindRunLink(ctx, s.dbtx, orgSlug, projectSlug, environmentSlug, runSlug)
	if xerror.Is(err, proofreceipts.ErrLifecycleLinkNotFound) {
		return nil
	}
	if err != nil {
		return xerror.Wrapf("find run proof lifecycle link: %w", xerror.Unexpected().With(err))
	}
	detail.ProofLink = toProofLifecycleLink(link, detail.Metadata.PipelineVersionSpecHash, "", s.executionClock.Now())
	return nil
}

func toProofLifecycleLink(link *proofreceipts.LifecycleLink, currentSpecHash, deploymentIdentifier string, now time.Time) *ProofLifecycleLink {
	if link == nil {
		return nil
	}
	status := ProofLifecycleLinkStatus(link, currentSpecHash, deploymentIdentifier, now)
	return &ProofLifecycleLink{Kind: link.Kind, ReceiptSlug: link.ReceiptSlug, Identifier: link.Identifier, LinkedAt: link.LinkedAt, RetainUntil: link.RetainUntil, Status: status}
}

func ProofLifecycleLinkStatus(link *proofreceipts.LifecycleLink, currentSpecHash, deploymentIdentifier string, now time.Time) string {
	status := "matched"
	switch {
	case deploymentIdentifier != "" && link.Identifier != deploymentIdentifier:
		status = "deployment_mismatch"
	case link.SpecHash != currentSpecHash:
		status = "version_mismatch"
	case !link.RetainUntil.IsZero() && !link.RetainUntil.After(now):
		status = "expired"
	case link.Superseded:
		status = "superseded"
	}
	return status
}

func proofReceiptMatchesDeployment(
	receipt *proofreceipts.Receipt,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	version *pipelinesrepo.PipelineVersion,
	spec *pipelinesrepo.PipelineSpec,
	bindingFingerprint, capabilityFingerprint string,
) bool {
	if receipt == nil || receipt.Superseded || version == nil || spec == nil || version.GitCommitSHA == nil || spec.SourcePath == nil || spec.SourceRef == nil {
		return false
	}
	candidate := receipt.Value.Candidate
	return candidate.OrganizationSlug == orgSlug &&
		candidate.ProjectSlug == projectSlug &&
		candidate.EnvironmentSlug == environmentSlug &&
		candidate.HeadSHA == *version.GitCommitSHA &&
		candidate.TargetRef == *spec.SourceRef &&
		candidate.PipelinePath == *spec.SourcePath &&
		candidate.SpecHash == "sha256:"+version.SpecHash &&
		candidate.BindingFingerprint == bindingFingerprint &&
		candidate.CapabilityFingerprint == capabilityFingerprint
}

func proofBindingFingerprint(resolved *datasourcerefssvc.ResolvedPipeline) (string, error) {
	if resolved == nil {
		return "", xerror.ExecutionUnavailable().Detail("proof data source resolver is required")
	}
	return proofidentity.BindingFingerprint(resolved.Resolutions)
}

func proofCapabilityFingerprint(resolved *datasourcerefssvc.ResolvedPipeline) (string, error) {
	if resolved == nil || resolved.Pipeline == nil {
		return "", xerror.ExecutionUnavailable().Detail("proof data source resolver is required")
	}
	return proofidentity.CapabilityFingerprint(resolved.Pipeline, resolved.Resolutions)
}

func deploymentLinkIdentifier(pipelineSlug slug.Slug, deployment *pipelinesrepo.PipelineDeployment) string {
	return deploymentLinkIdentifierValues(pipelineSlug, deployment.CurrentVersionSpecHash, deployment.DeployedAt)
}

func deploymentLinkIdentifierValues(pipelineSlug slug.Slug, specHash string, deployedAt time.Time) string {
	return pipelineSlug.String() + ":" + specHash + ":" + deployedAt.UTC().Format(time.RFC3339Nano)
}

func shortPipelineSpecHash(specHash string) string {
	if len(specHash) <= shortSpecHashLength {
		return specHash
	}
	return specHash[:shortSpecHashLength]
}

func optionalShortPipelineSpecHash(specHash *string) *string {
	if specHash == nil {
		return nil
	}
	short := shortPipelineSpecHash(*specHash)
	return &short
}

func actorSlugPtr(actor model.EventActor) *slug.Slug {
	if actor.Slug == nil || *actor.Slug == "" {
		return nil
	}
	return actor.Slug
}

func toPipelineRunMetadata(run *pipelineruns.PipelineRun) *PipelineRunMetadata {
	if run == nil {
		return nil
	}

	return &PipelineRunMetadata{
		PipelineVersionID:       run.PipelineVersionID,
		PipelineVersionSpecHash: run.PipelineVersionSpecHash,
		EnvironmentSlug:         run.EnvironmentSlug,
		PipelineSlug:            run.PipelineSlug,
		RunSlug:                 run.RunSlug,
		Source:                  run.Source,
		RequestID:               run.RequestID,
		Status:                  run.Status,
		DurationMs:              run.DurationMs,
		StepCount:               run.StepCount,
		StepSlug:                run.StepSlug,
		ErrorSummary:            run.ErrorSummary,
		FailureKind:             run.FailureKind,
		StartedAt:               run.StartedAt,
		EndedAt:                 run.EndedAt,
		ReplayOfRunSlug:         run.ReplayOfRunSlug,
		ReplayFromStep:          run.ReplayFromStep,
		Incremental:             run.Incremental,
		CreatedAt:               run.CreatedAt,
	}
}

// runDetailFromOutcome builds a PipelineRunDetail from in-memory run data, avoiding a DB read-back
// after execution. The persisted run record already exists at this point — this just avoids
// a redundant read.
func runDetailFromOutcome(
	ctx context.Context,
	clk clock.Clock,
	outcome *pipelineruntime.RunOutcome,
	pipelineVersionSpecHash string,
) (*PipelineRunDetail, error) {
	if outcome == nil {
		return nil, xerror.Unexpected().Detail("run outcome is required")
	}

	createdAt := clk.Now().UTC()
	if outcome.Metadata.StepCount > math.MaxInt32 {
		return nil, xerror.InvalidInput(xerror.PipelineRun).Detailf("step count %d exceeds int32 range", outcome.Metadata.StepCount)
	}

	return &PipelineRunDetail{
		Metadata: PipelineRunMetadata{
			PipelineVersionID:       outcome.Metadata.PipelineVersionID,
			PipelineVersionSpecHash: pipelineVersionSpecHash,
			EnvironmentSlug:         outcome.Metadata.EnvironmentSlug.String(),
			PipelineSlug:            string(outcome.Metadata.PipelineSlug),
			RunSlug:                 outcome.Metadata.RunSlug,
			Source:                  outcome.Metadata.Source,
			RequestID:               reqctx.GetRequestID(ctx),
			Status:                  outcome.Metadata.Status,
			DurationMs:              outcome.Metadata.DurationMs,
			StepCount:               int32(outcome.Metadata.StepCount),
			StepSlug:                outcome.Metadata.StepSlug,
			ErrorSummary:            outcome.Metadata.ErrorSummary,
			FailureKind:             outcome.Metadata.FailureKind,
			StartedAt:               outcome.Metadata.StartedAt,
			EndedAt:                 outcome.Metadata.EndedAt,
			ReplayOfRunSlug:         outcome.Metadata.ReplayOfRunSlug,
			ReplayFromStep:          outcome.Metadata.ReplayFromStep,
			Incremental:             outcome.Metadata.Incremental,
			CreatedAt:               createdAt,
		},
		Timeline:               outcome.Timeline,
		IncrementalDiagnostics: outcome.IncrementalDiagnostics,
	}, nil
}

func pipelineFromJSON(data []byte) (*pipelinespec.Pipeline, error) {
	pipeline, err := pipelinespec.Load("pipeline.json", data)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return pipeline, nil
}

func (s *service) decorateDraftProposalContext(
	ctx context.Context,
	orgSlug, projectSlug, memberSlug slug.Slug,
	detail *PipelineDraftDetail,
) error {
	detail.InitiatingMember = memberSlug
	repository, err := s.resolveProjectPipelineRepository(ctx, orgSlug, projectSlug)
	if err != nil {
		return err
	}
	detail.Repository = repository
	detail.ProposalSupported = pipelineProposalSupported(repository)
	authority, err := s.pipelineStore.ResolveProposalAuthority(ctx, orgSlug, projectSlug)
	if err != nil {
		if xerror.Is(err, ErrPipelineProposalAuthorityDenied) {
			detail.ProposalSupported = false
			return nil
		}
		return err
	}
	if authority.PublicationMode == model.PipelineProposalModeRepositoryCredential {
		detail.ProposalSupported = true
	}
	if detail.PublicationMode != nil {
		if detail.proposalAuthorityID == nil || *detail.proposalAuthorityID != authority.ID {
			detail.ProposalSupported = false
		}
		return nil
	}
	detail.PublicationMode = ptrString(authority.PublicationMode)
	detail.ProviderActor = ptrString(authority.ExecutingPrincipalDisplay)
	detail.AuthoritySource = ptrString(authority.AuthoritySource)
	return nil
}

func authorizeProposalMember(
	memberSlug slug.Slug,
	actor model.EventActor,
) error {
	if actor.Type != model.EventActorTypeUser || actor.Slug == nil || *actor.Slug != memberSlug {
		return ErrPipelineProposalAuthorityDenied.Detail(
			"proposal publication requires the initiating authenticated member",
		)
	}
	return nil
}

func (a runnerQueryExecutorAdapter) ExecuteSnapshot(ctx context.Context, org, source slug.Slug, content contractplan.OperationContent, params map[string]any, checkpoint *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	executor, ok := a.executor.(interface {
		ExecuteSnapshot(context.Context, slug.Slug, slug.Slug, contractplan.OperationContent, map[string]any, *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
	})
	if !ok {
		return nil, nil, xerror.Unsupported(xerror.QueryExecutor).Detail("immutable operation executor is required")
	}
	return executor.ExecuteSnapshot(ctx, org, source, content, params, checkpoint)
}
