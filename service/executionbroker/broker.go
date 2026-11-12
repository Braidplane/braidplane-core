package executionbroker

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/postgres/retainedfiles"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	connectortypes "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/filetransfersvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

const (
	kindConnectionTest                = "connection_test"
	kindQuery                         = "query"
	kindPrepareS3Write                = "prepare_s3_write"
	kindSchema                        = "schema_introspection"
	kindProofMetadata                 = "proof_metadata"
	kindTransform                     = "transform"
	kindGitWrite                      = "git_write"
	kindGitDelete                     = "git_delete"
	kindGitProposal                   = "git_proposal"
	kindGitCreationProposal           = "git_creation_proposal"
	kindGitProposalRefresh            = "git_proposal_refresh"
	kindGitRepository                 = "git_repository"
	kindGitSource                     = "git_source_snapshot"
	kindGitBranchList                 = "git_branch_list"
	requestRetention                  = 15 * time.Minute
	cancelGracePeriod                 = time.Second
	pollInterval                      = 25 * time.Millisecond
	executorPollInterval              = 100 * time.Millisecond
	admissionRetryInterval            = 100 * time.Millisecond
	maxAdmissionRetryDelay            = time.Second
	heartbeatInterval                 = 2 * time.Second
	executorMetricsInterval           = time.Second
	maintenanceInterval               = time.Minute
	errorCodeInvalidRequest           = "invalid_request"
	errorCodeQueryInvalid             = "source_query_invalid"
	errorCodeSchemaReferenceInvalid   = "source_schema_reference_invalid"
	errorCodeDestinationConstraint    = "destination_constraint_violation"
	errorCodeQueryFailed              = "query_failed"
	errorCodeDataSourceCredentials    = "data_source_credentials_failed" // #nosec G101 -- Error classification code, not a credential.
	errorCodeDataSourcePermission     = "data_source_permission_denied"
	errorCodeDataSourceConnection     = "data_source_connection_failed"
	errorCodeDataSourceTimeout        = "data_source_timeout"
	errorCodeQueryRowLimit            = "query_row_limit_exceeded"
	errorCodeAcknowledgementRejected  = "http_acknowledgement_rejected"
	errorCodeAcknowledgementUncertain = "http_acknowledgement_uncertain"
	errorCodeResourceScope            = "resource_scope_rejected"
	errorCodeGitFailed                = "git_failed"
	errorCodeCreationTargetExists     = "git_creation_target_exists"
	errorCodeRepositoryForbidden      = "repository_forbidden"
	errorCodeRepositoryConflict       = "repository_conflict"
	errorCodeRepositoryInvalid        = "repository_invalid"
	errorCodeRepositoryCleanup        = "repository_cleanup_failed"
	errorCodeResponseSize             = "response_size_exceeded"
)

var ErrAmbiguousExecution = xerror.ExecutionUnavailable().Detail(
	"execution outcome is ambiguous; automatic retry is forbidden",
)

type Dispatcher interface {
	Dispatch(context.Context, postgres.DBTX, uuid.UUID) error
}

// operationAdmission fences connector work even after its caller stops waiting.
type operationAdmission interface {
	WithOperation(context.Context, slug.Slug, slug.Slug, func(context.Context) error) error
	EncodeOperationError(error) (string, bool)
	DecodeOperationError(string) (error, bool)
}

// Broker owns persisted request submission and the shared request processor.
// factory is required only in compositions that process execution requests.
type Broker struct {
	admission        operationAdmission
	txm              postgres.TransactionManager
	repo             requestRepository
	cipher           crypto.AES256
	dispatcher       Dispatcher
	requestTimeout   time.Duration
	factory          connector.NewConnectorFunc
	runtimePool      *connector.Manager
	logger           *slog.Logger
	heartbeatEvery   time.Duration
	maintenanceEvery time.Duration
	active           atomic.Int64
	capacity         atomic.Int64
}

type requestRepository interface {
	CheckDispatch(context.Context, postgres.DBTX, requestrepo.DispatchAuthority) (bool, error)
	Preflight(context.Context, postgres.DBTX) error
	PreflightDelivery(context.Context, postgres.DBTX) error
	Create(context.Context, postgres.DBTX, requestrepo.Request, time.Time) error
	BindUser(context.Context, postgres.DBTX, uuid.UUID, string, slug.Slug) error
	BindSource(context.Context, postgres.DBTX, uuid.UUID, string, slug.Slug) error
	Find(context.Context, postgres.DBTX, uuid.UUID, string) (requestrepo.Request, error)
	Claim(context.Context, postgres.DBTX, uuid.UUID) (requestrepo.Request, bool, error)
	FindForDelivery(context.Context, postgres.DBTX, uuid.UUID) (requestrepo.DeliverySnapshot, bool, error)
	ClaimNext(context.Context, postgres.DBTX) (requestrepo.Request, bool, error)
	Heartbeat(context.Context, postgres.DBTX, uuid.UUID, uuid.UUID) (bool, error)
	Complete(context.Context, postgres.DBTX, uuid.UUID, uuid.UUID, []byte, bool) error
	Cancel(context.Context, postgres.DBTX, uuid.UUID) (bool, error)
	DeleteExpired(context.Context, postgres.DBTX) (int64, error)
	Metrics(context.Context, postgres.DBTX) (requestrepo.Metrics, error)
}

func New(
	txm postgres.TransactionManager,
	cipher crypto.AES256,
	dispatcher Dispatcher,
	requestTimeout time.Duration,
	factory connector.NewConnectorFunc,
	logger *slog.Logger,
	admission operationAdmission,
) (*Broker, error) {
	if txm == nil || cipher == nil || dispatcher == nil || admission == nil {
		return nil, xerror.Unexpected().Detail("execution broker dependencies are required")
	}
	if requestTimeout <= 0 {
		return nil, xerror.Unexpected().Detail("execution request timeout must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	broker := &Broker{
		admission: admission,
		txm:       txm, repo: requestrepo.New(), cipher: cipher,
		dispatcher: dispatcher, requestTimeout: requestTimeout,
		factory: factory, logger: logger, heartbeatEvery: heartbeatInterval,
		maintenanceEvery: maintenanceInterval,
	}
	if factory != nil {
		runtimePool, err := connector.NewManager(factory, connector.RuntimePoolSettings{})
		if err != nil {
			return nil, xerror.Wrapf("initialize connector runtime pool: %w", err)
		}
		broker.runtimePool = runtimePool
	}
	return broker, nil
}

// Close releases process-owned connector pools after execution processing stops.
func (b *Broker) Close() error {
	if b.runtimePool == nil {
		return nil
	}
	return b.runtimePool.Close()
}

func (b *Broker) TestConnection(ctx context.Context, request datasourcessvc.ConnectionTestRequest) error {
	_, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindConnectionTest, request)
	if err != nil {
		if code, ok := b.admission.EncodeOperationError(err); ok && code != "" {
			return executiondelivery.MarkNotExecuted(err)
		}
	}
	return err
}

func (b *Broker) ExecuteConnector(
	ctx context.Context,
	request queriessvc.ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	if writegate.HasGuard(ctx) {
		if request.DataSourceType != model.DataSourceTypeS3 || !request.SinkExecution {
			return nil, writegate.ErrPreparationFailed
		}
		prepared, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindPrepareS3Write, request)
		if err != nil {
			return nil, err
		}
		if !prepared.WritePrepared {
			return nil, writegate.ErrPreparationFailed
		}
		if err := writegate.BeforeWrite(ctx); err != nil {
			return nil, err
		}
	}
	response, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindQuery, request)
	if err != nil {
		return nil, err
	}
	if response.Result == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no query result")
	}
	return response.Result, nil
}

type encryptedResponse struct {
	WritePrepared             bool                           `json:"write_prepared,omitempty"`
	Result                    *result.ExecutionResult        `json:"result,omitempty"`
	ResultRuntime             *executionResultRuntimePayload `json:"result_runtime,omitempty"`
	resultRuntimeMaxBytes     int64
	resultRuntimeMaxFileBytes int64
	SchemaJSON                []byte                                 `json:"schema_json,omitempty"`
	TransformOutput           *pipepayload.Output                    `json:"transform_output,omitempty"`
	ProposalResult            *pipelinegit.ProposalResult            `json:"proposal_result,omitempty"`
	SourceSnapshot            *pipelinegit.RemoteSourceSnapshot      `json:"source_snapshot,omitempty"`
	RepositoryResult          *pipelinegit.RepositoryBootstrapResult `json:"repository_result,omitempty"`
	Branches                  []datasourcessvc.DataSourceBranch      `json:"branches,omitempty"`
	Proof                     *proofExecutionResponse                `json:"proof,omitempty"`
	ErrorCode                 string                                 `json:"error_code,omitempty"`
	PublicDetail              string                                 `json:"public_detail,omitempty"`
}

type connectorExecutionRequestEnvelope struct {
	queriessvc.ConnectorExecutionRequest
	RuntimeParameterBytes map[string][]byte                `json:"runtime_parameter_bytes,omitempty"`
	RuntimeParameterFiles map[string]filepayload.Reference `json:"runtime_parameter_files,omitempty"`
}

// Runtime byte sidecars are an encrypted broker protocol. During its initial
// rollout, do not start native-file work until every submitting and processing
// role runs this protocol: an older role ignores the sidecar and cannot recover
// the omitted bytes.
//
// executionResultRuntimePayload carries byte-preserving connector output only
// inside the encrypted worker response. These bytes remain encrypted in the
// broker request row until requestRetention expires. ExecutionResult's normal
// JSON shape intentionally excludes them from durable query and run records and
// from API responses.
type executionResultRuntimePayload struct {
	HTTPRawBody   []byte                 `json:"http_raw_body,omitempty"`
	FileContent   []byte                 `json:"file_content,omitempty"`
	FileReference *filepayload.Reference `json:"file_reference,omitempty"`
}

func marshalRequestPlaintext(kind string, payload any) ([]byte, error) {
	if kind != kindQuery && kind != kindPrepareS3Write {
		return json.Marshal(payload)
	}

	request, ok := payload.(queriessvc.ConnectorExecutionRequest)
	if !ok {
		return nil, xerror.Unexpected().
			Detailf("query execution payload has unexpected type %T", payload)
	}

	runtimeParameterBytes := make(map[string][]byte)
	for name, parameter := range request.Parameters {
		if value, isBytes := parameter.Value.([]byte); isBytes {
			runtimeParameterBytes[name] = value
		}
	}
	if err := validateRuntimeParameterBytes(
		runtimeParameterBytes,
		request.Limits.MaxResponseBytes,
	); err != nil {
		return nil, err
	}

	envelope := connectorExecutionRequestEnvelope{ConnectorExecutionRequest: request}
	envelope.Parameters = make(
		map[string]connectortypes.TypedParam,
		len(request.Parameters),
	)
	for name, parameter := range request.Parameters {
		if value, isBytes := parameter.Value.([]byte); isBytes {
			if envelope.RuntimeParameterBytes == nil {
				envelope.RuntimeParameterBytes = make(map[string][]byte)
			}
			envelope.RuntimeParameterBytes[name] = append([]byte(nil), value...)
			parameter.Value = nil
		}
		if ref, ok := parameter.Value.(filepayload.Reference); ok {
			if _, err := filestream.ParseReference(ref, request.Limits.MaxFileBytes); err != nil {
				return nil, err
			}
			if envelope.RuntimeParameterFiles == nil {
				envelope.RuntimeParameterFiles = make(map[string]filepayload.Reference)
			}
			envelope.RuntimeParameterFiles[name] = ref
			parameter.Value = nil
		}
		envelope.Parameters[name] = parameter
	}

	return json.Marshal(envelope)
}

func unmarshalConnectorExecutionRequest(
	plaintext []byte,
) (queriessvc.ConnectorExecutionRequest, error) {
	var envelope connectorExecutionRequestEnvelope
	if err := jsonvalue.Unmarshal(plaintext, &envelope); err != nil {
		return queriessvc.ConnectorExecutionRequest{}, err
	}
	for name, parameter := range envelope.Parameters {
		parameter.Value = jsonvalue.Normalize(parameter.Value)
		envelope.Parameters[name] = parameter
	}

	for name := range envelope.RuntimeParameterBytes {
		if _, ok := envelope.Parameters[name]; !ok {
			return queriessvc.ConnectorExecutionRequest{}, xerror.Unexpected().
				Detailf("runtime byte parameter %q is missing from query execution request", name)
		}
	}
	if err := validateRuntimeParameterBytes(
		envelope.RuntimeParameterBytes,
		envelope.Limits.MaxResponseBytes,
	); err != nil {
		return queriessvc.ConnectorExecutionRequest{}, err
	}
	for name, value := range envelope.RuntimeParameterBytes {
		parameter := envelope.Parameters[name]
		parameter.Value = append([]byte(nil), value...)
		envelope.Parameters[name] = parameter
	}
	for name, ref := range envelope.RuntimeParameterFiles {
		parameter, ok := envelope.Parameters[name]
		if !ok || parameter.Value != nil {
			return queriessvc.ConnectorExecutionRequest{}, filestream.ErrUnavailable
		}
		if _, exists := envelope.RuntimeParameterBytes[name]; exists {
			return queriessvc.ConnectorExecutionRequest{}, filestream.ErrUnavailable
		}
		if _, err := filestream.ParseReference(ref, envelope.Limits.MaxFileBytes); err != nil {
			return queriessvc.ConnectorExecutionRequest{}, err
		}
		parameter.Value = ref
		envelope.Parameters[name] = parameter
	}
	if parameter, ok := envelope.Parameters[sinkbinding.InternalInputRows]; ok {
		var rows []map[string]any
		if parameter.Value != nil {
			rawRows, ok := parameter.Value.([]any)
			if !ok {
				return queriessvc.ConnectorExecutionRequest{}, xerror.Unexpected().
					Detail("runtime input rows have an invalid query execution envelope")
			}
			rows = make([]map[string]any, len(rawRows))
			for i, rawRow := range rawRows {
				row, ok := rawRow.(map[string]any)
				if !ok {
					return queriessvc.ConnectorExecutionRequest{}, xerror.Unexpected().
						Detail("runtime input rows have an invalid query execution envelope")
				}
				rows[i] = row
			}
		}
		parameter.Value = rows
		envelope.Parameters[sinkbinding.InternalInputRows] = parameter
	}

	return envelope.ConnectorExecutionRequest, nil
}

func validateRuntimeParameterBytes(parameters map[string][]byte, maxBytes int64) error {
	if len(parameters) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return xerror.Unexpected().
			Detail("query runtime byte parameter limit is required")
	}
	var totalBytes int64
	for _, value := range parameters {
		valueBytes := int64(len(value))
		if valueBytes > maxBytes-totalBytes {
			return xerror.ExecutionRequestSizeExceeded().Detailf(
				"query runtime byte parameters exceed %d byte limit",
				maxBytes,
			)
		}
		totalBytes += valueBytes
	}
	return nil
}

func marshalResponsePlaintext(response encryptedResponse) ([]byte, error) {
	if err := response.captureResultRuntimePayload(); err != nil {
		return nil, err
	}
	if err := validateResultFileReference(response.ResultRuntime, response.resultRuntimeMaxFileBytes); err != nil {
		return nil, err
	}
	if err := validateResultRuntimePayloadSize(
		response.ResultRuntime,
		response.resultRuntimeMaxBytes,
	); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func unmarshalResponsePlaintext(
	plaintext []byte,
	resultRuntimeMaxBytes int64,
) (encryptedResponse, error) {
	response, err := decodeResponsePlaintext(plaintext)
	if err != nil {
		return encryptedResponse{}, err
	}
	if err := validateResultRuntimePayloadSize(
		response.ResultRuntime,
		resultRuntimeMaxBytes,
	); err != nil {
		return encryptedResponse{}, err
	}
	if err := response.restoreResultRuntimePayload(); err != nil {
		return encryptedResponse{}, err
	}
	return response, nil
}

func decodeResponsePlaintext(plaintext []byte) (encryptedResponse, error) {
	var response encryptedResponse
	if err := jsonvalue.Unmarshal(plaintext, &response); err != nil {
		return encryptedResponse{}, err
	}
	if response.TransformOutput != nil {
		jsonvalue.Normalize(response.TransformOutput.Rows)
	}
	return response, nil
}

func (response *encryptedResponse) captureResultRuntimePayload() error {
	if response == nil || response.Result == nil {
		return nil
	}
	if response.ResultRuntime != nil {
		return validateResultRuntimePayloadSize(
			response.ResultRuntime,
			response.resultRuntimeMaxBytes,
		)
	}

	runtimePayload := &executionResultRuntimePayload{}
	if httpResult, ok := response.Result.HTTP(); ok && httpResult != nil {
		runtimePayload.HTTPRawBody = httpResult.RawBody
		runtimePayload.FileReference = filepayload.CloneReference(httpResult.Reference)
	}
	if fileResult, ok := response.Result.File(); ok && fileResult != nil {
		runtimePayload.FileContent = fileResult.Content
		runtimePayload.FileReference = filepayload.CloneReference(fileResult.Reference)
	}
	if len(runtimePayload.HTTPRawBody) == 0 && len(runtimePayload.FileContent) == 0 && runtimePayload.FileReference == nil {
		return nil
	}
	if err := validateResultRuntimePayloadSize(
		runtimePayload,
		response.resultRuntimeMaxBytes,
	); err != nil {
		return err
	}
	if err := validateResultFileReference(runtimePayload, response.resultRuntimeMaxFileBytes); err != nil {
		return err
	}
	response.ResultRuntime = &executionResultRuntimePayload{
		HTTPRawBody:   append([]byte(nil), runtimePayload.HTTPRawBody...),
		FileContent:   append([]byte(nil), runtimePayload.FileContent...),
		FileReference: filepayload.CloneReference(runtimePayload.FileReference),
	}
	return nil
}

func validateResultRuntimePayloadSize(
	payload *executionResultRuntimePayload,
	maxBytes int64,
) error {
	if payload == nil {
		return nil
	}
	if maxBytes <= 0 {
		return xerror.Unexpected().
			Detail("execution runtime payload byte limit is required")
	}
	if payload.FileReference != nil && (len(payload.HTTPRawBody) != 0 || len(payload.FileContent) != 0) {
		return filestream.ErrUnavailable
	}
	httpBytes := int64(len(payload.HTTPRawBody))
	fileBytes := int64(len(payload.FileContent))
	if httpBytes > maxBytes || fileBytes > maxBytes-httpBytes {
		return xerror.ExecutionResponseSizeExceeded().Detailf(
			"execution runtime payload exceeds %d byte broker limit",
			maxBytes,
		)
	}
	return nil
}

func (response *encryptedResponse) restoreResultRuntimePayload() error {
	if response == nil || response.ResultRuntime == nil {
		return nil
	}
	if response.Result == nil {
		return xerror.Unexpected().
			Detail("execution runtime payload has no matching execution result")
	}

	runtimePayload := response.ResultRuntime
	if len(runtimePayload.HTTPRawBody) == 0 && len(runtimePayload.FileContent) == 0 && runtimePayload.FileReference == nil {
		return xerror.Unexpected().Detail("execution runtime payload has no bytes to restore")
	}

	var httpResult *result.HTTPResult
	if len(runtimePayload.HTTPRawBody) > 0 {
		var ok bool
		httpResult, ok = response.Result.HTTP()
		if !ok || httpResult == nil {
			return xerror.Unexpected().
				Detail("execution HTTP runtime payload does not match execution result")
		}
	}
	var fileResult *result.FileResult
	if len(runtimePayload.FileContent) > 0 {
		var ok bool
		fileResult, ok = response.Result.File()
		if !ok || fileResult == nil {
			return xerror.Unexpected().
				Detail("execution file runtime payload does not match execution result")
		}
	}

	if httpResult != nil {
		httpResult.RawBody = append([]byte(nil), runtimePayload.HTTPRawBody...)
	}
	if fileResult != nil {
		fileResult.Content = append([]byte(nil), runtimePayload.FileContent...)
	}
	if runtimePayload.FileReference != nil {
		ref := runtimePayload.FileReference
		var metadata *filepayload.FilePayloadMetadata
		if output, ok := response.Result.File(); ok && output != nil {
			metadata = output.File
			output.Reference = filepayload.CloneReference(ref)
		} else if output, ok := response.Result.HTTP(); ok && output != nil {
			metadata = output.File
			output.Reference = filepayload.CloneReference(ref)
		} else {
			return filestream.ErrUnavailable
		}
		if metadata == nil || metadata.SizeBytes != ref.SizeBytes || metadata.DigestAlgorithm != filepayload.FilePayloadDigestSHA256 || metadata.Digest != ref.SHA256 {
			return filestream.ErrUnavailable
		}
	}
	response.ResultRuntime = nil
	return nil
}

type requestAdmission struct {
	DataSourceSlug   string        `json:"data_source_slug"`
	ConcurrencyLimit int           `json:"concurrency_limit"`
	QueueCapacity    int           `json:"concurrency_queue_limit"`
	QueueTimeout     time.Duration `json:"concurrency_queue_timeout"`
}

func (b *Broker) ListGitBranches(
	ctx context.Context,
	request datasourcessvc.GitBranchRequest,
) ([]datasourcessvc.DataSourceBranch, error) {
	response, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindGitBranchList, request)
	if err != nil {
		return nil, err
	}
	if response.Branches == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no Git branches")
	}
	return response.Branches, nil
}

func (b *Broker) ExecuteGitSourceSnapshot(
	ctx context.Context,
	request pipelinegit.RemoteSourceRequest,
) (*pipelinegit.RemoteSourceSnapshot, error) {
	response, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindGitSource, request)
	if err != nil {
		return nil, err
	}
	if response.SourceSnapshot == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no Git source snapshot")
	}
	return response.SourceSnapshot, nil
}

func (b *Broker) ExecuteGitWrite(ctx context.Context, request pipelinegit.RemoteWriteRequest) error {
	_, err := b.submitAndWait(ctx, request.Operation.OrganizationSlug.String(), kindGitWrite, request)
	return err
}

func (b *Broker) ExecuteGitDelete(ctx context.Context, request pipelinegit.RemoteDeleteRequest) error {
	_, err := b.submitAndWait(ctx, request.Operation.OrganizationSlug.String(), kindGitDelete, request)
	return err
}

func (b *Broker) ExecuteGitProposal(
	ctx context.Context,
	request pipelinegit.RemoteProposalRequest,
) (*pipelinegit.ProposalResult, error) {
	kind := kindGitProposal
	if request.Operation.CreateOnly {
		// Older executors must reject creation rather than ignore its overwrite guard.
		kind = kindGitCreationProposal
	}
	response, err := b.submitAndWait(ctx, request.Operation.OrganizationSlug.String(), kind, request)
	if err != nil {
		return nil, err
	}
	if response.ProposalResult == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no proposal result")
	}
	return response.ProposalResult, nil
}

func (b *Broker) ExecuteGitProposalRefresh(
	ctx context.Context,
	request pipelinegit.RemoteProposalRefreshRequest,
) (*pipelinegit.ProposalResult, error) {
	response, err := b.submitAndWait(ctx, request.Operation.OrganizationSlug.String(), kindGitProposalRefresh, request)
	if err != nil {
		return nil, err
	}
	if response.ProposalResult == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no proposal refresh result")
	}
	return response.ProposalResult, nil
}

func (b *Broker) ExecuteGitRepository(
	ctx context.Context,
	request pipelinegit.RemoteRepositoryRequest,
) (*pipelinegit.RepositoryBootstrapResult, error) {
	response, err := b.submitAndWait(ctx, request.Operation.OrganizationSlug.String(), kindGitRepository, request)
	if err != nil {
		return nil, err
	}
	if request.Action == pipelinegit.RepositoryActionBootstrap && response.RepositoryResult == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no repository result")
	}
	return response.RepositoryResult, nil
}

func (b *Broker) IntrospectSchema(
	ctx context.Context,
	request datasourcessvc.SchemaIntrospectionRequest,
) ([]byte, error) {
	response, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindSchema, request)
	if err != nil {
		return nil, err
	}
	if len(response.SchemaJSON) == 0 {
		return nil, xerror.Unexpected().Detail("execution worker returned no schema result")
	}
	return response.SchemaJSON, nil
}

func (b *Broker) ExecuteTransform(
	ctx context.Context,
	request pipelineruntime.TransformExecutionRequest,
) (*pipepayload.Output, error) {
	response, err := b.submitAndWait(ctx, request.OrganizationSlug.String(), kindTransform, request)
	if err != nil {
		return nil, err
	}
	if response.TransformOutput == nil {
		return nil, xerror.Unexpected().Detail("execution worker returned no transform output")
	}
	return response.TransformOutput, nil
}

// Start launches independent database pollers. The executor role has no River
// grants, so compromised tenant execution cannot inspect or mutate control jobs.
func (b *Broker) Start(ctx context.Context, concurrency int) error {
	if b.factory == nil {
		return xerror.Unexpected().Detail("connector factory is required by execution poller")
	}
	if concurrency < 1 {
		return xerror.Unexpected().Detail("execution concurrency must be positive")
	}
	if err := b.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		return b.repo.Preflight(ctx, db)
	}); err != nil {
		return xerror.Wrapf("preflight execution request poller: %w", err)
	}
	b.capacity.Store(int64(concurrency))
	go b.observeQueue(ctx)
	for range concurrency {
		go b.poll(ctx)
	}
	return nil
}

// StartMaintenance schedules deletion of expired encrypted envelopes on the
// control-plane connection. Executor credentials intentionally cannot delete
// or scan envelope rows.
func (b *Broker) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(b.maintenanceEvery)
		defer ticker.Stop()
		for {
			if err := b.deleteExpired(ctx); err != nil && ctx.Err() == nil {
				b.logger.ErrorContext(ctx, "delete expired execution requests", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// PreflightProcessor verifies the River worker's request-processing
// capabilities before it starts consuming execution jobs.
func (b *Broker) PreflightProcessor(ctx context.Context) error {
	if err := b.validateProcessorConfiguration(); err != nil {
		return err
	}
	return b.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		return b.repo.PreflightDelivery(ctx, db)
	})
}

// StartProcessorObservability publishes the shared persisted-envelope signals
// for River processors. River's own queue metrics remain the source of job and
// worker heartbeat state.
func (b *Broker) StartProcessorObservability(ctx context.Context, concurrency int) error {
	if concurrency < 1 {
		return xerror.Unexpected().Detail("execution concurrency must be positive")
	}
	b.capacity.Store(int64(concurrency))
	go b.observeQueue(ctx)
	return nil
}

// Process claims and executes one opaque delivery without acknowledging a
// duplicate while another worker still owns its live lease.
func (b *Broker) Process(ctx context.Context, id uuid.UUID) (executiondelivery.ProcessResult, error) {
	if err := b.validateProcessorConfiguration(); err != nil {
		return executiondelivery.ProcessResult{}, err
	}

	claim, err := postgres.InTx(ctx, b.txm, func(
		ctx context.Context,
		tx postgres.DBTX,
	) (deliveryClaim, error) {
		request, claimed, err := b.repo.Claim(ctx, tx, id)
		if err != nil {
			return deliveryClaim{}, err
		}
		if claimed {
			if request.State == requestrepo.StateRunning {
				return deliveryClaim{request: request, claimed: true}, nil
			}
			return deliveryClaim{}, nil
		}

		snapshot, found, err := b.repo.FindForDelivery(ctx, tx, id)
		if err != nil {
			return deliveryClaim{}, err
		}
		if !found {
			return deliveryClaim{}, nil
		}
		if snapshot.Request.State == requestrepo.StatePending {
			if snapshot.Request.CancelRequestedAt != nil ||
				!snapshot.Request.DeadlineAt.After(snapshot.ObservedAt) ||
				(snapshot.Request.QueueDeadlineAt != nil &&
					!snapshot.Request.QueueDeadlineAt.After(snapshot.ObservedAt)) {
				return deliveryClaim{}, xerror.Unexpected().
					Detail("expired pending execution request was not terminalized")
			}
			return deliveryClaim{retryAt: pendingAdmissionRetryAt(snapshot)}, nil
		}
		if snapshot.Request.State != requestrepo.StateRunning {
			return deliveryClaim{}, nil
		}
		if snapshot.Request.LeaseExpiresAt == nil {
			return deliveryClaim{}, xerror.Unexpected().Detail("running execution request lease is missing")
		}
		if !snapshot.Request.LeaseExpiresAt.After(snapshot.ObservedAt) {
			return deliveryClaim{}, xerror.Unexpected().Detail("expired execution request was not reclaimed")
		}
		return deliveryClaim{retryAt: *snapshot.Request.LeaseExpiresAt}, nil
	})
	if err != nil {
		return executiondelivery.ProcessResult{}, err
	}

	if !claim.retryAt.IsZero() {
		return executiondelivery.ProcessResult{
			Disposition: executiondelivery.ProcessRetryAt,
			RetryAt:     claim.retryAt,
		}, nil
	}
	if claim.claimed {
		if err := b.processClaimed(ctx, claim.request); err != nil {
			return executiondelivery.ProcessResult{}, err
		}
	}
	return executiondelivery.ProcessResult{Disposition: executiondelivery.ProcessDone}, nil
}

func (b *Broker) validateProcessorConfiguration() error {
	if b.factory == nil {
		return xerror.Unexpected().Detail("connector factory is required by execution processor")
	}
	if b.runtimePool == nil {
		return xerror.Unexpected().Detail("connector runtime pool is required by execution processor")
	}
	return nil
}

func pendingAdmissionRetryAt(snapshot requestrepo.DeliverySnapshot) time.Time {
	delay := admissionRetryInterval
	age := snapshot.ObservedAt.Sub(snapshot.CreatedAt)
	for age >= delay && delay < maxAdmissionRetryDelay {
		delay *= 2
		if delay > maxAdmissionRetryDelay {
			delay = maxAdmissionRetryDelay
		}
	}

	retryAt := snapshot.ObservedAt.Add(delay)
	if retryAt.After(snapshot.Request.DeadlineAt) {
		retryAt = snapshot.Request.DeadlineAt
	}
	if snapshot.Request.QueueDeadlineAt != nil &&
		retryAt.After(*snapshot.Request.QueueDeadlineAt) {
		retryAt = *snapshot.Request.QueueDeadlineAt
	}
	return retryAt
}

type deliveryClaim struct {
	request requestrepo.Request
	retryAt time.Time
	claimed bool
}

func (b *Broker) deleteExpired(ctx context.Context) error {
	return b.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		_, err := b.repo.DeleteExpired(ctx, db)
		return err
	})
}

func (b *Broker) submitAndWait(
	ctx context.Context,
	organizationSlug string,
	kind string,
	payload any,
) (encryptedResponse, error) {
	startedAt := time.Now()
	ctx = postgres.WithoutTransaction(ctx)
	waitCtx, cancelWait := context.WithTimeout(ctx, b.requestTimeout)
	defer cancelWait()

	plaintext, err := marshalRequestPlaintext(kind, payload)
	defer clear(plaintext)
	if err != nil {
		recordBrokerOutcome(kind, "error", startedAt)
		return encryptedResponse{}, xerror.Wrapf("marshal execution request: %w", err)
	}
	ciphertext, err := b.cipher.Encrypt(plaintext)
	if err != nil {
		recordBrokerOutcome(kind, "error", startedAt)
		return encryptedResponse{}, xerror.Wrapf("encrypt execution request: %w", err)
	}
	admission := requestAdmission{}
	if kind == kindQuery || kind == kindPrepareS3Write {
		if err := json.Unmarshal(plaintext, &admission); err != nil {
			recordBrokerOutcome(kind, "error", startedAt)
			return encryptedResponse{}, xerror.Wrapf("decode execution admission: %w", err)
		}
	}

	deadline, _ := waitCtx.Deadline()
	retainUntil := deadline.Add(requestRetention)
	if kind == kindProofMetadata {
		proof, ok := payload.(proofevidencesvc.ExecutionRequest)
		if !ok || proof.Request.EvidenceRetainUntil.Before(deadline) {
			recordBrokerOutcome(kind, "error", startedAt)
			return encryptedResponse{}, xerror.Unexpected().Detail("invalid Proof execution retention")
		}
		if proof.Request.EvidenceRetainUntil.Before(retainUntil) {
			retainUntil = proof.Request.EvidenceRetainUntil
		}
	}
	var queueDeadline *time.Time
	if admission.DataSourceSlug != "" {
		if admission.ConcurrencyLimit < 1 ||
			admission.QueueCapacity < 1 ||
			admission.QueueTimeout <= 0 {
			recordBrokerOutcome(kind, "error", startedAt)
			return encryptedResponse{}, xerror.Unexpected().
				Detail("execution admission limits must be positive")
		}
		value := time.Now().Add(admission.QueueTimeout)
		if value.After(deadline) {
			value = deadline
		}
		queueDeadline = &value
	}
	id := uuid.New()
	err = b.txm.RunInTx(waitCtx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := b.repo.Create(ctx, tx, requestrepo.Request{
			ID: id, OrganizationSlug: organizationSlug, Kind: kind,
			RequestCiphertext: ciphertext, DeadlineAt: deadline,
			DataSourceSlug:   admission.DataSourceSlug,
			ConcurrencyLimit: admission.ConcurrencyLimit,
			QueueCapacity:    admission.QueueCapacity,
			QueueDeadlineAt:  queueDeadline,
		}, retainUntil); err != nil {
			return err
		}
		if user := executiondelivery.InitiatingUser(ctx); user != "" {
			if err := b.repo.BindUser(ctx, tx, id, organizationSlug, user); err != nil {
				return err
			}
		}
		if kind == kindConnectionTest || kind == kindSchema {
			var source struct {
				DataSourceSlug slug.Slug `json:"data_source_slug"`
			}
			if err := json.Unmarshal(plaintext, &source); err != nil {
				return xerror.Unexpected().With(err)
			}
			if source.DataSourceSlug != "" {
				if err := b.repo.BindSource(ctx, tx, id, organizationSlug, source.DataSourceSlug); err != nil {
					return err
				}
			}
		}
		return b.dispatcher.Dispatch(ctx, tx, id)
	})
	if err != nil {
		if kind == kindQuery && xerror.Is(err, xerror.ExecutionConcurrencyLimitExceeded()) {
			metrics.RecordConnectorAdmission("rejected")
		}
		recordBrokerOutcome(kind, "error", startedAt)
		return encryptedResponse{}, xerror.Wrapf("persist execution request: %w", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			cancelledBeforeExecution, cancelErr := b.cancel(ctx, id)
			if cancelErr != nil {
				if executionMayHaveExternalSideEffects(kind) {
					recordBrokerOutcome(kind, "ambiguous", startedAt)
					return encryptedResponse{}, xerror.Wrapf(
						"%w: cancel execution request: %w",
						ErrAmbiguousExecution,
						cancelErr,
					)
				}
				recordBrokerOutcome(kind, "error", startedAt)
				return encryptedResponse{}, xerror.Wrapf(
					"cancel execution request: %w",
					cancelErr,
				)
			}
			if executionMayHaveExternalSideEffects(kind) && !cancelledBeforeExecution {
				recordBrokerOutcome(kind, "ambiguous", startedAt)
				return encryptedResponse{}, ErrAmbiguousExecution
			}
			if kind == kindQuery {
				metrics.RecordConnectorAdmission(contextOutcome(waitCtx.Err()))
			}
			recordBrokerOutcome(kind, contextOutcome(waitCtx.Err()), startedAt)
			return encryptedResponse{}, waitCtx.Err()
		case <-ticker.C:
			response, done, readErr := b.readResponse(waitCtx, id, organizationSlug)
			if readErr != nil {
				if kind == kindQuery &&
					xerror.Is(readErr, xerror.ExecutionConcurrencyLimitExceeded()) {
					metrics.RecordConnectorAdmission("timeout")
				}
				recordBrokerOutcome(kind, terminalOutcome(readErr), startedAt)
				return encryptedResponse{}, readErr
			}
			if done {
				responseErr := b.responseError(response)
				if kind == kindConnectionTest && responseErr != nil {
					attrs := []slog.Attr{
						slog.String("braidplane.event", "execution.connection_test.request_failed"),
						slog.String("execution_request_id", id.String()),
					}
					attrs = append(attrs, logging.ContextAttrs(waitCtx)...)
					b.logger.LogAttrs(
						waitCtx,
						slog.LevelWarn,
						"connection test execution request failed",
						attrs...,
					)
				}
				recordBrokerOutcome(kind, terminalOutcome(responseErr), startedAt)
				return response, responseErr
			}
		}
	}
}

func recordBrokerOutcome(kind, outcome string, startedAt time.Time) {
	metrics.RecordExecutionBroker(metrics.ExecutionBrokerMetric{
		Kind: kind, Outcome: outcome, Duration: time.Since(startedAt),
	})
}

func terminalOutcome(err error) string {
	if err == nil {
		return "success"
	}
	if xerror.Is(err, ErrAmbiguousExecution) {
		return "ambiguous"
	}
	return contextOutcome(err)
}

func contextOutcome(err error) string {
	switch {
	case xerror.Is(err, context.DeadlineExceeded):
		return "timeout"
	case xerror.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "error"
	}
}

func executionMayHaveExternalSideEffects(kind string) bool {
	switch kind {
	case "connection_test", "schema_introspection", "transform", "git_source_snapshot", "git_branch_list", kindPrepareS3Write:
		return false
	default:
		return true
	}
}

func (b *Broker) readResponse(
	ctx context.Context,
	id uuid.UUID,
	organizationSlug string,
) (encryptedResponse, bool, error) {
	request, err := postgres.InRead(ctx, b.txm, func(ctx context.Context, db postgres.DBTX) (requestrepo.Request, error) {
		return b.repo.Find(ctx, db, id, organizationSlug)
	})
	if err != nil {
		return encryptedResponse{}, false, err
	}
	switch request.State {
	case requestrepo.StatePending, requestrepo.StateRunning:
		return encryptedResponse{}, false, nil
	case requestrepo.StateCancelled:
		if request.CancelRequestedAt == nil &&
			request.QueueDeadlineAt != nil &&
			!request.QueueDeadlineAt.After(time.Now()) {
			return encryptedResponse{}, true, xerror.ExecutionConcurrencyLimitExceeded().
				Detail("execution concurrency queue wait timed out")
		}
		return encryptedResponse{}, true, context.Canceled
	case requestrepo.StateAmbiguous:
		return encryptedResponse{}, true, ErrAmbiguousExecution
	case requestrepo.StateSucceeded, requestrepo.StateFailed:
		plaintext, err := b.cipher.Decrypt(request.ResultCiphertext)
		defer clear(plaintext)
		if err != nil {
			return encryptedResponse{}, true, xerror.Wrapf("decrypt execution response: %w", err)
		}
		response, err := decodeResponsePlaintext(plaintext)
		if err != nil {
			return encryptedResponse{}, true, xerror.Wrapf("decode execution response: %w", err)
		}
		if response.ResultRuntime != nil {
			runtimeLimits, err := b.resultRuntimeLimits(request)
			if err != nil {
				return encryptedResponse{}, true, err
			}
			if err := validateResultRuntimePayloadSize(
				response.ResultRuntime,
				runtimeLimits.MaxResponseBytes,
			); err != nil {
				return encryptedResponse{}, true, err
			}
			if err := validateResultFileReference(response.ResultRuntime, runtimeLimits.MaxFileBytes); err != nil {
				return encryptedResponse{}, true, err
			}
			if err := response.restoreResultRuntimePayload(); err != nil {
				return encryptedResponse{}, true, xerror.Wrapf(
					"decode execution response runtime payload: %w",
					err,
				)
			}
		}
		return response, true, nil
	default:
		return encryptedResponse{}, true, xerror.Unexpected().Detail("invalid execution request state")
	}
}

func (b *Broker) resultRuntimeLimits(request requestrepo.Request) (connectortypes.ExecutionLimits, error) {
	if request.Kind != kindQuery {
		return connectortypes.ExecutionLimits{}, nil
	}

	plaintext, err := b.cipher.Decrypt(request.RequestCiphertext)
	if err != nil {
		return connectortypes.ExecutionLimits{}, xerror.Wrapf("decrypt execution request limits: %w", err)
	}
	var envelope struct {
		Limits connectortypes.ExecutionLimits `json:"limits"`
	}
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return connectortypes.ExecutionLimits{}, xerror.Wrapf("decode execution request limits: %w", err)
	}
	if envelope.Limits.MaxResponseBytes <= 0 {
		return connectortypes.ExecutionLimits{}, xerror.Unexpected().
			Detail("execution runtime payload byte limit must be positive")
	}
	return envelope.Limits, nil
}

func (b *Broker) responseError(response encryptedResponse) error {
	if err, ok := b.admission.DecodeOperationError(response.ErrorCode); ok && err != nil {
		return err
	}
	return responseError(response)
}

func responseError(response encryptedResponse) error {
	if response.ErrorCode == "" {
		return nil
	}
	switch response.ErrorCode {
	case "sink_preparation_failed":
		return writegate.ErrPreparationFailed.WithPublicDetail(response.PublicDetail)
	case "connection_failed":
		return datasourcessvc.ErrConnectionFailed.
			WithPublicDetail(response.PublicDetail).
			Detail("connection test failed")
	case errorCodeDataSourceCredentials:
		return xerror.InvalidCredentials()
	case errorCodeDataSourcePermission:
		return xerror.Forbidden(xerror.DataSource)
	case errorCodeDataSourceConnection:
		return xerror.ConnectionFailed(xerror.DataSource)
	case errorCodeDataSourceTimeout:
		return xerror.Timeout(xerror.DataSource)
	case errorCodeQueryInvalid:
		return xerror.SourceQueryInvalid()
	case errorCodeSchemaReferenceInvalid:
		return xerror.SourceSchemaReferenceInvalid()
	case errorCodeDestinationConstraint:
		return xerror.DestinationConstraintViolation().WithPublicDetail(response.PublicDetail)
	case errorCodeQueryFailed:
		return xerror.ExecutionUnavailable().WithPublicDetail(response.PublicDetail).Detail("query execution failed")
	case errorCodeQueryRowLimit:
		return xerror.ExecutionRowLimitExceeded()
	case errorCodeAcknowledgementRejected:
		return xerror.HTTPAcknowledgementRejected().WithPublicDetail(response.PublicDetail)
	case errorCodeAcknowledgementUncertain:
		return xerror.HTTPAcknowledgementUncertain().WithPublicDetail(response.PublicDetail)
	case errorCodeResourceScope:
		return resourcescope.ErrInvalid
	case errorCodeResponseSize:
		return xerror.ExecutionResponseSizeExceeded().
			Detail("execution response exceeded the byte limit")
	case errorCodeRepositoryForbidden:
		return xerror.Forbidden(xerror.DataSource)
	case errorCodeRepositoryConflict:
		return xerror.AlreadyExists(xerror.RepoFile)
	case errorCodeRepositoryInvalid:
		return xerror.InvalidInput(xerror.RepoFile)
	case errorCodeRepositoryCleanup:
		return gitprovider.ErrRepositoryCleanupFailed
	case errorCodeCreationTargetExists:
		return pipelinegit.ErrCreationTargetExists
	case "transform_work":
		return transformsql.ErrWorkBudgetExceeded
	case "transform_intermediate_rows":
		return transformsql.ErrIntermediateRowsExceeded
	case "transform_intermediate_bytes":
		return transformsql.ErrIntermediateBytesExceeded
	case "transform_output_rows":
		return transformsql.ErrOutputRowsExceeded
	case "transform_output_bytes":
		return transformsql.ErrOutputBytesExceeded
	case "transform_depth":
		return transformsql.ErrDepthBudgetExceeded
	case "transform_interrupted":
		return transformsql.ErrExecutionBudgetInterrupted
	default:
		return xerror.ExecutionUnavailable().Detail("isolated execution failed")
	}
}

func (b *Broker) cancel(ctx context.Context, id uuid.UUID) (bool, error) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelGracePeriod)
	defer cancel()
	cancelledBeforeExecution := false
	err := b.txm.RunRead(cancelCtx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		cancelledBeforeExecution, err = b.repo.Cancel(ctx, db, id)
		return err
	})
	if err != nil {
		return false, err
	}
	return cancelledBeforeExecution, nil
}

func (b *Broker) poll(ctx context.Context) {
	ticker := time.NewTicker(executorPollInterval)
	defer ticker.Stop()
	for {
		var request requestrepo.Request
		var claimed bool
		err := b.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
			var err error
			request, claimed, err = b.repo.ClaimNext(ctx, db)
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.logger.ErrorContext(ctx, "claim execution request", "error", err)
		} else if claimed && request.State == requestrepo.StateRunning {
			if err := b.processClaimed(ctx, request); err != nil {
				b.logger.ErrorContext(ctx, "execution request failed", "request_id", request.ID.String(), "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Broker) observeQueue(ctx context.Context) {
	ticker := time.NewTicker(executorMetricsInterval)
	defer ticker.Stop()
	for {
		queueMetrics, err := postgres.InRead(ctx, b.txm, func(
			ctx context.Context,
			db postgres.DBTX,
		) (requestrepo.Metrics, error) {
			return b.repo.Metrics(ctx, db)
		})
		if err != nil {
			if ctx.Err() == nil {
				b.logger.ErrorContext(ctx, "read execution queue metrics", "error", err)
			}
		} else {
			metrics.RecordExecutionBrokerSnapshot(metrics.ExecutionBrokerSnapshot{
				PendingCount: queueMetrics.PendingCount, RunningCount: queueMetrics.RunningCount,
				OldestPendingAgeSeconds: queueMetrics.OldestPendingAgeSeconds,
				Active:                  b.active.Load(), Capacity: b.capacity.Load(), LastSuccessfulPoll: time.Now(),
				ConnectorActive: queueMetrics.ConnectorActiveCount,
				ConnectorQueued: queueMetrics.ConnectorQueuedCount,
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Broker) processClaimed(ctx context.Context, request requestrepo.Request) error {
	b.active.Add(1)
	defer b.active.Add(-1)
	id := request.ID
	claimToken := request.ClaimToken

	// Removal can commit after claim but before provider work begins. Do not wait
	// for the periodic heartbeat to observe an already-cancelled capability.
	cancelled, err := postgres.InRead(ctx, b.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
		return b.repo.Heartbeat(ctx, db, id, claimToken)
	})
	if err != nil {
		return err
	}
	if cancelled {
		return b.finish(ctx, id, claimToken, encryptedResponse{}, false)
	}

	plaintext, err := b.cipher.Decrypt(request.RequestCiphertext)
	defer clear(plaintext)
	if err != nil {
		return b.finish(ctx, id, claimToken, encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false)
	}
	deadline := request.DeadlineAt
	var authorityTime time.Time
	if request.Kind == kindProofMetadata {
		if request.ObservedAt.IsZero() || request.ObservedLocallyAt.IsZero() {
			return b.finish(ctx, id, claimToken, encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false)
		}
		// Charge the claim round trip conservatively; executor wall time is irrelevant.
		authorityTime = request.ObservedAt.Add(time.Since(request.ObservedLocallyAt))
		deadline = time.Now().Add(request.DeadlineAt.Sub(authorityTime))
	}
	execCtx, cancelExec := context.WithDeadline(ctx, deadline)
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go b.heartbeat(execCtx, cancelExec, done, heartbeatErr, id, claimToken)
	defer func() {
		close(done)
		cancelExec()
	}()

	response := encryptedResponse{}
	succeeded := true
	execCtx = executiondelivery.WithRequest(execCtx, id)
	execute := func(execCtx context.Context) error {
		switch request.Kind {
		case kindProofMetadata:
			response, succeeded = processProofMetadata(execCtx, request.OrganizationSlug, plaintext, authorityTime)
		case kindConnectionTest:
			var payload datasourcessvc.ConnectionTestRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			execCtx = connector.WithDataSourceIdentity(execCtx, payload.OrganizationSlug, payload.DataSourceSlug)
			conn, err := b.factory(execCtx, payload.DataSourceType, payload.ConfigJSON, payload.CredentialsJSON)
			if err == nil {
				err = conn.TestConnection(execCtx)
				_ = conn.Close()
			}
			if err != nil {
				response.ErrorCode, succeeded = "connection_failed", false
				response.PublicDetail = datasourcessvc.ConnectionTestDiagnostic(err)
				attrs := []slog.Attr{
					slog.String("braidplane.event", "execution.connection_test.worker_failed"),
					slog.String("execution_request_id", id.String()),
					slog.String("data_source.type", payload.DataSourceType.String()),
				}
				var typedErr *xerror.Error
				if xerror.As(err, &typedErr) && typedErr != nil {
					attrs = append(attrs,
						slog.String("error.type", typedErr.Code()),
						slog.String("error.kind", typedErr.Kind().String()),
					)
				}
				switch payload.DataSourceType {
				case model.DataSourceTypeGithub, model.DataSourceTypeGitlab:
					attrs = append(attrs, slog.String("error", repostore.SanitizeGitErrorDetail(err.Error())))
				default:
				}
				attrs = append(attrs, logging.ContextAttrs(execCtx)...)
				b.logger.LogAttrs(
					execCtx,
					slog.LevelWarn,
					"connection test worker failed",
					attrs...,
				)
			}
		case kindQuery, kindPrepareS3Write:
			payload, err := unmarshalConnectorExecutionRequest(plaintext)
			if err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}

			if payload.Limits.MaxResponseBytes <= 0 {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.resultRuntimeMaxBytes = payload.Limits.MaxResponseBytes
			response.resultRuntimeMaxFileBytes = payload.Limits.MaxFileBytes
			if b.runtimePool == nil {
				response.ErrorCode, succeeded = errorCodeQueryFailed, false
				break
			}
			var prepared bool
			if request.Kind == kindPrepareS3Write {
				if payload.DataSourceType != model.DataSourceTypeS3 || !payload.SinkExecution {
					response.ErrorCode, succeeded = errorCodeInvalidRequest, false
					break
				}
				execCtx = writegate.WithBeforeWrite(execCtx, func(context.Context) error {
					prepared = true
					return writegate.ErrSkipped
				})
			}
			response.Result, err = b.executeConnector(execCtx, id, claimToken, payload)
			if request.Kind == kindPrepareS3Write && prepared {
				response = encryptedResponse{WritePrepared: true}
				break
			}
			if err != nil {
				response.ErrorCode, succeeded = safeQueryErrorCode(err), false
				response.PublicDetail = xerror.PublicDetail(err)
			} else if response.Result != nil && response.Result.Error() != nil {
				response.ErrorCode, succeeded = safeQueryErrorCode(response.Result.Error()), false
				response.PublicDetail = xerror.PublicDetail(response.Result.Error())
				response.Result = nil
			} else if err := response.captureResultRuntimePayload(); err != nil {
				if !xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
					return err
				}
				response = encryptedResponse{ErrorCode: errorCodeResponseSize}
				succeeded = false
			}
			if response.ErrorCode == errorCodeResourceScope {
				response = encryptedResponse{ErrorCode: errorCodeResourceScope}
			}
			if request.Kind == kindPrepareS3Write && (response.ErrorCode == "" || response.ErrorCode == errorCodeQueryFailed) {
				response = encryptedResponse{ErrorCode: "sink_preparation_failed", PublicDetail: "S3 sink preparation failed before provider dispatch. Review the selected operation fields and payload format."}
				succeeded = false
			}
		case kindSchema:
			var payload datasourcessvc.SchemaIntrospectionRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.SchemaJSON, err = datasourcessvc.ExecuteSchemaIntrospection(execCtx, b.factory, payload)
			if err != nil {
				response.ErrorCode, succeeded = "schema_failed", false
			}
		case kindTransform:
			var payload pipelineruntime.TransformExecutionRequest
			if err := jsonvalue.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}

			for _, upstream := range payload.UpstreamPayloads {
				if upstream != nil {
					jsonvalue.Normalize(upstream.Rows)
				}
			}
			runtime := transformsql.NewRuntime(payload.Limits).WithObserver(recordTransformExecution)
			response.TransformOutput, err = runtime.ExecuteContext(
				execCtx,
				payload.Transform,
				payload.UpstreamPayloads,
			)
			if err != nil {
				response.ErrorCode, succeeded = safeTransformErrorCode(err), false
			}
		case kindGitWrite:
			var payload pipelinegit.RemoteWriteRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			if err := pipelinegit.ExecuteResolvedGitWrite(execCtx, payload); err != nil {
				response.ErrorCode, succeeded = errorCodeGitFailed, false
			}
		case kindGitDelete:
			var payload pipelinegit.RemoteDeleteRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			if err := pipelinegit.ExecuteResolvedGitDelete(execCtx, payload); err != nil {
				response.ErrorCode, succeeded = errorCodeGitFailed, false
			}
		case kindGitProposal, kindGitCreationProposal:
			var payload pipelinegit.RemoteProposalRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			if payload.Operation.CreateOnly != (request.Kind == kindGitCreationProposal) {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.ProposalResult, err = pipelinegit.ExecuteResolvedGitProposal(execCtx, payload)
			if err != nil {
				response.ErrorCode, succeeded = errorCodeGitFailed, false
				if xerror.Is(err, pipelinegit.ErrCreationTargetExists) {
					response.ErrorCode = errorCodeCreationTargetExists
				}
				attrs := []slog.Attr{
					slog.String("braidplane.event", "execution.git_proposal.worker_failed"),
					slog.String("execution_request_id", id.String()),
					slog.String("data_source.type", payload.Auth.DataSourceType.String()),
					slog.String("error", repostore.SanitizeGitErrorDetail(err.Error())),
				}
				attrs = append(attrs, logging.ContextAttrs(execCtx)...)
				b.logger.LogAttrs(execCtx, slog.LevelError, "isolated Git proposal execution failed", attrs...)
			}
		case kindGitProposalRefresh:
			var payload pipelinegit.RemoteProposalRefreshRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.ProposalResult, err = pipelinegit.ExecuteResolvedGitProposalRefresh(execCtx, payload)
			if err != nil {
				response.ErrorCode, succeeded = errorCodeGitFailed, false
			}
		case kindGitRepository:
			var payload pipelinegit.RemoteRepositoryRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.RepositoryResult, err = pipelinegit.ExecuteResolvedGitRepository(execCtx, payload)
			if err != nil {
				response.ErrorCode, succeeded = safeRepositoryErrorCode(err), false
			}
		case kindGitSource:
			var payload pipelinegit.RemoteSourceRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.SourceSnapshot, err = pipelinegit.ExecuteGitSourceSnapshot(execCtx, payload)
			if err != nil {
				response.ErrorCode, succeeded = errorCodeGitFailed, false
			}
		case kindGitBranchList:
			var payload datasourcessvc.GitBranchRequest
			if err := json.Unmarshal(plaintext, &payload); err != nil {
				response.ErrorCode, succeeded = errorCodeInvalidRequest, false
				break
			}
			response.Branches, err = datasourcessvc.ExecuteGitBranchList(execCtx, payload)
			if err != nil {
				response.ErrorCode, succeeded = safeQueryErrorCode(err), false
			}
		default:
			response.ErrorCode, succeeded = errorCodeInvalidRequest, false
		}
		return nil
	}
	var identity struct {
		OrganizationSlug slug.Slug `json:"organization_slug"`
		DataSourceSlug   slug.Slug `json:"data_source_slug"`
	}
	if request.Kind == kindQuery || request.Kind == kindPrepareS3Write || request.Kind == kindConnectionTest || request.Kind == kindSchema {
		if err := json.Unmarshal(plaintext, &identity); err != nil {
			return b.finish(ctx, id, claimToken, encryptedResponse{ErrorCode: errorCodeInvalidRequest}, false)
		}
	}
	if identity.DataSourceSlug != "" {
		err = b.admission.WithOperation(execCtx, identity.OrganizationSlug, identity.DataSourceSlug, execute)
	} else {
		err = execute(execCtx)
	}
	if err != nil {
		succeeded = false
		response = encryptedResponse{}
		response.ErrorCode = errorCodeQueryFailed
		if code, ok := b.admission.EncodeOperationError(err); ok && code != "" {
			response.ErrorCode = code
		}
	}

	select {
	case err := <-heartbeatErr:
		if err != nil {
			return err
		}
	default:
	}
	return b.finish(ctx, id, claimToken, response, succeeded)
}

func safeQueryErrorCode(err error) string {
	switch {
	case xerror.Is(err, xerror.DestinationConstraintViolation()):
		return errorCodeDestinationConstraint
	case xerror.Is(err, xerror.SourceQueryInvalid()):
		return errorCodeQueryInvalid
	case xerror.Is(err, xerror.SourceSchemaReferenceInvalid()):
		return errorCodeSchemaReferenceInvalid
	case xerror.Is(err, xerror.InvalidCredentials()):
		return errorCodeDataSourceCredentials
	case xerror.Is(err, xerror.Forbidden(xerror.DataSource)):
		return errorCodeDataSourcePermission
	case xerror.Is(err, xerror.Timeout(xerror.DataSource)):
		return errorCodeDataSourceTimeout
	case xerror.Is(err, xerror.ConnectionFailed(xerror.DataSource)):
		return errorCodeDataSourceConnection
	case xerror.Is(err, resourcescope.ErrInvalid):
		return errorCodeResourceScope
	case xerror.Is(err, xerror.ExecutionResponseSizeExceeded()):
		return errorCodeResponseSize
	case xerror.Is(err, xerror.ExecutionRowLimitExceeded()):
		return errorCodeQueryRowLimit
	case xerror.Is(err, xerror.HTTPAcknowledgementRejected()):
		return errorCodeAcknowledgementRejected
	case xerror.Is(err, xerror.HTTPAcknowledgementUncertain()):
		return errorCodeAcknowledgementUncertain
	default:
		return errorCodeQueryFailed
	}
}

func safeRepositoryErrorCode(err error) string {
	switch {
	case xerror.Is(err, gitprovider.ErrRepositoryCleanupFailed):
		return errorCodeRepositoryCleanup
	case xerror.Is(err, xerror.Forbidden(xerror.DataSource)):
		return errorCodeRepositoryForbidden
	case xerror.Is(err, xerror.AlreadyExists(xerror.RepoFile)):
		return errorCodeRepositoryConflict
	case xerror.Is(err, xerror.InvalidInput(xerror.RepoFile)):
		return errorCodeRepositoryInvalid
	default:
		return errorCodeGitFailed
	}
}

func (b *Broker) executeConnector(
	ctx context.Context,
	requestID, claimToken uuid.UUID,
	payload queriessvc.ConnectorExecutionRequest,
) (*result.ExecutionResult, error) {
	if payload.Limits.MaxFileBytes < 0 || payload.Limits.MaxFileBytes > filestream.MaxRetainedBytes {
		return nil, filestream.ErrUnavailable
	}
	expected := requestrepo.DispatchAuthority{RequestID: requestID, ClaimToken: claimToken, Organization: payload.OrganizationSlug.String(), DataSource: payload.DataSourceSlug.String(), Type: payload.DataSourceType.String(), ReadOnly: payload.Limits.ReadOnly}
	if payload.PinnedConfigDigest != "" {
		current, err := connector.PublicConfigDigest(payload.DataSourceType, payload.ConfigJSON)
		if err != nil || current != payload.PinnedConfigDigest {
			return nil, deploymentplan.ErrInvalid.With(err)
		}
		expected.Config = payload.ConfigJSON
	}
	reference := payload.CredentialReference
	if reference == "" {
		// Older standard-connector envelopes already carried this lifecycle reference.
		reference = payload.CredentialVersion
	}
	credential, revision, ok := strings.Cut(reference, "/")
	if !ok {
		return nil, deploymentplan.ErrInvalid
	}
	if _, err := slug.New(credential); err != nil {
		return nil, deploymentplan.ErrInvalid
	}
	at, err := time.Parse(time.RFC3339Nano, revision)
	if err != nil {
		return nil, deploymentplan.ErrInvalid.With(err)
	}
	expected.CredentialSlug, expected.CredentialUpdatedAt = &credential, &at
	if err := b.txm.RunRead(postgres.WithoutTransaction(ctx), func(ctx context.Context, db postgres.DBTX) error {
		allowed, err := b.repo.CheckDispatch(ctx, db, expected)
		if err != nil {
			return err
		}
		if !allowed {
			return deploymentplan.ErrInvalid
		}
		return nil
	}); err != nil {
		return nil, err
	}
	conn, release, err := b.runtimePool.Acquire(
		ctx,
		connector.RuntimeIdentity{
			OrganizationSlug:  payload.OrganizationSlug,
			DataSourceSlug:    payload.DataSourceSlug,
			DataSourceType:    payload.DataSourceType,
			ConfigVersion:     payload.ConfigVersion,
			CredentialVersion: payload.CredentialVersion,
		},
		payload.ConfigJSON,
		payload.CredentialsJSON,
	)
	if err != nil {
		metrics.RecordConnectorAdmission("error")
		return nil, err
	}
	defer func() { _ = release() }()

	metrics.RecordConnectorAdmission("acquired")
	if payload.SinkExecution {
		ctx = runtimevars.WithSinkExecution(ctx)
	}
	if payload.Limits.MaxFileBytes > 0 {
		expires := time.Now().Add(requestRetention)
		if deadline, ok := ctx.Deadline(); ok {
			expires = deadline.Add(requestRetention)
		}
		store, err := filetransfersvc.NewRetainedStore(b.txm, b.cipher, retainedfiles.New(), filestream.Claim{RequestID: requestID, ClaimToken: claimToken, Organization: payload.OrganizationSlug}, expires)
		if err != nil {
			return nil, err
		}
		ctx = filestream.WithStore(ctx, store)
	}
	return connector.Execute(ctx, conn, payload.StatementJSON, payload.Parameters, payload.ResourceScope, payload.Limits)
}

func recordTransformExecution(usage transformsql.Usage, err error) {
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	metrics.RecordTransformExecution(metrics.TransformExecutionMetric{
		Outcome:           outcome,
		LimitType:         transformLimitType(err),
		Duration:          usage.Duration,
		Work:              usage.Work,
		IntermediateRows:  usage.IntermediateRows,
		IntermediateBytes: usage.IntermediateBytes,
		OutputRows:        usage.OutputRows,
		OutputBytes:       usage.OutputBytes,
		MaxDepth:          usage.MaxDepth,
	})
}

func transformLimitType(err error) string {
	switch {
	case xerror.Is(err, transformsql.ErrWorkBudgetExceeded):
		return "work"
	case xerror.Is(err, transformsql.ErrIntermediateRowsExceeded):
		return "intermediate_rows"
	case xerror.Is(err, transformsql.ErrIntermediateBytesExceeded):
		return "intermediate_bytes"
	case xerror.Is(err, transformsql.ErrOutputRowsExceeded):
		return "output_rows"
	case xerror.Is(err, transformsql.ErrOutputBytesExceeded):
		return "output_bytes"
	case xerror.Is(err, transformsql.ErrDepthBudgetExceeded):
		return "depth"
	case xerror.Is(err, transformsql.ErrExecutionBudgetInterrupted):
		return "timeout_or_cancellation"
	default:
		return "none"
	}
}

func (b *Broker) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	done <-chan struct{},
	errCh chan<- error,
	id, claimToken uuid.UUID,
) {
	ticker := time.NewTicker(b.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			cancelled, err := postgres.InRead(ctx, b.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
				return b.repo.Heartbeat(ctx, db, id, claimToken)
			})
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
				return
			}
			if cancelled {
				cancel()
				return
			}
		}
	}
}

func safeTransformErrorCode(err error) string {
	switch {
	case xerror.Is(err, transformsql.ErrWorkBudgetExceeded):
		return "transform_work"
	case xerror.Is(err, transformsql.ErrIntermediateRowsExceeded):
		return "transform_intermediate_rows"
	case xerror.Is(err, transformsql.ErrIntermediateBytesExceeded):
		return "transform_intermediate_bytes"
	case xerror.Is(err, transformsql.ErrOutputRowsExceeded):
		return "transform_output_rows"
	case xerror.Is(err, transformsql.ErrOutputBytesExceeded):
		return "transform_output_bytes"
	case xerror.Is(err, transformsql.ErrDepthBudgetExceeded):
		return "transform_depth"
	case xerror.Is(err, transformsql.ErrExecutionBudgetInterrupted):
		return "transform_interrupted"
	default:
		return "transform_failed"
	}
}

func (b *Broker) finish(ctx context.Context, id, claimToken uuid.UUID, response encryptedResponse, succeeded bool) error {
	plaintext, err := marshalResponsePlaintext(response)
	defer clear(plaintext)
	if err != nil {
		return err
	}
	ciphertext, err := b.cipher.Encrypt(plaintext)
	if err != nil {
		return err
	}
	return b.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		return b.repo.Complete(ctx, db, id, claimToken, ciphertext, succeeded)
	})
}
