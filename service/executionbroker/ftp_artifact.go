package executionbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/textproto"
	"path"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

const (
	kindFTPArtifactPrepare = "ftp_artifact_prepare"
	kindFTPArtifactPublish = "ftp_artifact_publish"
	kindFTPArtifactRead    = "ftp_artifact_read"

	artifactReadUnavailable  = "artifact_read_unavailable"
	artifactReadChanged      = "artifact_changed"
	artifactReadTransient    = "artifact_read_transient"
	artifactBindingRefused   = "artifact_binding_refused"
	artifactPublishUncertain = "artifact_publication_uncertain"
)

// ErrFTPArtifactSubmissionUnknown means the submission transaction may have committed.
var ErrFTPArtifactSubmissionUnknown = xerror.New(xerror.KindInternal, "FTP_ARTIFACT_SUBMISSION_UNKNOWN", "FTP artifact submission outcome is unknown")

var errFTPArtifactSubmissionRefused = xerror.New(xerror.KindInvalidInput, "FTP_ARTIFACT_SUBMISSION_REFUSED", "FTP artifact submission refused")

// FTPArtifactError describes a local refusal or a completed broker outcome.
// RetryableRead identifies only completed transient provider failures.
type FTPArtifactError string

func (e FTPArtifactError) Error() string { return string(e) }

// PublicationUncertain reports that the executor could not prove the outcome after dispatch.
func (e FTPArtifactError) PublicationUncertain() bool { return e == artifactPublishUncertain }

func (e FTPArtifactError) RetryableRead() bool { return e == artifactReadTransient }

func isFTPArtifactKind(kind string) bool {
	return kind == kindFTPArtifactPrepare || kind == kindFTPArtifactPublish || kind == kindFTPArtifactRead
}

// FTPArtifactRequest is internal to trusted orchestration and the encrypted
// broker. The namespace, pinned operation and receipt are resolved by Core.
type FTPArtifactRequest struct {
	queriessvc.ConnectorExecutionRequest
	ftp.ArtifactEvidence
	Directory            string
	SavedQueryData       json.RawMessage `json:"artifact_saved_query_data,omitempty"`
	Content              json.RawMessage `json:"artifact_content,omitempty"`
	PublishRequestID     uuid.UUID
	ProjectEnvironmentID int64
	ReadQueryID          int64
	ReadQueryUpdatedAt   time.Time
}

type ftpArtifactResponse struct {
	Evidence *ftp.ArtifactEvidence
	Rows     *result.FileResult
}

func (b *Broker) PrepareFTPArtifact(ctx context.Context, req FTPArtifactRequest) (ftp.ArtifactEvidence, error) {
	req.CredentialsJSON, req.CredentialReference, req.CredentialVersion = nil, "", ""
	response, err := b.submitFTPArtifact(ctx, kindFTPArtifactPrepare, req)
	if err != nil {
		return ftp.ArtifactEvidence{}, err
	}
	if response.Evidence == nil || response.Evidence.DeliveryID != req.DeliveryID {
		return ftp.ArtifactEvidence{}, FTPArtifactError(artifactBindingRefused)
	}
	return *response.Evidence, nil
}

func (b *Broker) PublishFTPArtifact(ctx context.Context, req FTPArtifactRequest) error {
	if req.PublishRequestID == uuid.Nil || req.ProjectEnvironmentID < 1 {
		return FTPArtifactError(artifactBindingRefused)
	}
	response, err := b.submitFTPArtifact(ctx, kindFTPArtifactPublish, req)
	if err != nil {
		return err
	}
	if response.Evidence == nil || *response.Evidence != req.ArtifactEvidence {
		return ErrAmbiguousExecution
	}
	return nil
}

func (b *Broker) ReadFTPArtifact(ctx context.Context, req FTPArtifactRequest) (*result.FileResult, error) {
	response, err := b.submitFTPArtifact(ctx, kindFTPArtifactRead, req)
	if err != nil {
		return nil, err
	}
	if response.Rows == nil {
		return nil, FTPArtifactError(artifactBindingRefused)
	}
	return response.Rows, nil
}

func (b *Broker) submitFTPArtifact(ctx context.Context, kind string, req FTPArtifactRequest) (*ftpArtifactResponse, error) {
	if req.DataSourceType != model.DataSourceTypeFTP || req.OrganizationSlug == "" || req.DataSourceSlug == "" ||
		req.DeliveryID == uuid.Nil || req.Limits.MaxFileBytes <= 0 || req.Limits.MaxResponseBytes <= 0 ||
		req.ResourceScope != nil || len(req.Parameters) != 0 || req.ReconcileOnly || req.HTTPRecordUpdate != nil {
		return nil, FTPArtifactError(artifactBindingRefused)
	}
	if int64(len(req.Content)) > min(req.Limits.MaxResponseBytes, ftp.MaxArtifactBytes) {
		return nil, FTPArtifactError(artifactBindingRefused)
	}
	response, err := b.submitAndWait(ctx, req.OrganizationSlug.String(), kind, req)
	if err != nil {
		return nil, err
	}
	if response.FTPArtifact == nil {
		return nil, FTPArtifactError(artifactBindingRefused)
	}
	return response.FTPArtifact, nil
}

func (b *Broker) processFTPArtifact(ctx context.Context, request requestrepo.Request, plaintext []byte) (encryptedResponse, bool) {
	fail := func(code string) (encryptedResponse, bool) { return encryptedResponse{ErrorCode: code}, false }
	var req FTPArtifactRequest
	if err := json.Unmarshal(plaintext, &req); err != nil || request.OrganizationSlug != req.OrganizationSlug.String() ||
		req.DataSourceType != model.DataSourceTypeFTP || req.ResourceScope != nil || len(req.Parameters) != 0 ||
		req.ReconcileOnly || req.HTTPRecordUpdate != nil || req.Limits.MaxResponseBytes <= 0 || req.Limits.MaxFileBytes <= 0 {
		return fail(artifactBindingRefused)
	}
	digest, err := connector.PublicConfigDigest(req.DataSourceType, req.ConfigJSON)
	if err != nil || digest != req.PinnedConfigDigest {
		return fail(artifactBindingRefused)
	}
	var cfg ftp.Config
	if err := json.Unmarshal(req.ConfigJSON, &cfg); err != nil || cfg.ValidateBasePath() != nil {
		return fail(artifactBindingRefused)
	}
	statement, err := queriessvc.ExtractQueryStatement(req.SavedQueryData)
	if err != nil || !sameArtifactStatement(statement, req.StatementJSON) {
		return fail(artifactBindingRefused)
	}
	stmt, err := remotefile.DecodeStatement(req.StatementJSON)
	if err != nil || stmt.Format != filedata.QueryFormatCSV {
		return fail(artifactBindingRefused)
	}
	options := filedata.ParseCSVOptions{Delimiter: stmt.Delimiter, HasHeader: stmt.HasHeader, MaxRows: req.Limits.MaxResultRows}
	if request.Kind != kindFTPArtifactRead {
		if !req.SinkExecution || req.Limits.ReadOnly || stmt.Operation != remotefile.OperationWrite || stmt.Overwrite != nil {
			return fail(artifactBindingRefused)
		}
		var content []any
		dec := json.NewDecoder(bytes.NewReader(req.Content))
		dec.UseNumber()
		if int64(len(req.Content)) > min(req.Limits.MaxResponseBytes, ftp.MaxArtifactBytes) || dec.Decode(&content) != nil ||
			content == nil || dec.Decode(new(any)) != io.EOF {
			return fail(artifactBindingRefused)
		}
		evidence, body, err := ftp.PrepareArtifactCSV(cfg, req.Directory, stmt.Path, req.DeliveryID, req.Limits.MaxFileBytes, content, options)
		if err != nil {
			return fail(artifactBindingRefused)
		}
		if request.Kind == kindFTPArtifactPrepare {
			return encryptedResponse{FTPArtifact: &ftpArtifactResponse{Evidence: &evidence}}, true
		}
		if evidence != req.ArtifactEvidence || req.PublishRequestID != request.ID || b.factory == nil {
			return fail(artifactBindingRefused)
		}
		return b.publishFTPArtifact(ctx, request, req, body)
	}
	if req.SinkExecution || remotefile.OperationOrDefault(stmt.Operation) != remotefile.OperationRead ||
		len(req.Content) != 0 || b.factory == nil || path.Base(stmt.Path) != stmt.Path || !strings.HasSuffix(stmt.Path, ".csv") {
		return fail(artifactBindingRefused)
	}
	return b.readFTPArtifact(ctx, request, req, options)
}

func artifactAuthority(request requestrepo.Request, req FTPArtifactRequest) (requestrepo.FTPArtifactPermit, error) {
	credential, revision, ok := strings.Cut(req.CredentialReference, "/")
	at, err := time.Parse(time.RFC3339Nano, revision)
	if _, slugErr := slug.New(credential); !ok || slugErr != nil || err != nil {
		return requestrepo.FTPArtifactPermit{}, FTPArtifactError(artifactBindingRefused)
	}
	return requestrepo.FTPArtifactPermit{
		DeliveryID: req.DeliveryID, ConfigDigest: req.PinnedConfigDigest, Statement: req.SavedQueryData,
		FinalPath: req.FinalPath, StagingPath: req.StagingPath, SizeBytes: req.SizeBytes, SHA256: req.SHA256,
		DispatchAuthority: requestrepo.DispatchAuthority{
			RequestID: request.ID, ClaimToken: request.ClaimToken, Organization: request.OrganizationSlug,
			DataSource: req.DataSourceSlug.String(), Type: req.DataSourceType.String(), Config: req.ConfigJSON,
			CredentialSlug: &credential, CredentialUpdatedAt: &at, ReadOnly: req.Limits.ReadOnly,
		},
	}, nil
}

func (b *Broker) checkFTPArtifactDispatch(ctx context.Context, authority requestrepo.FTPArtifactPermit, read *FTPArtifactRequest) error {
	return b.txm.RunRead(postgres.WithoutTransaction(ctx), func(ctx context.Context, db postgres.DBTX) error {
		allowed, err := b.repo.CheckDispatch(ctx, db, authority.DispatchAuthority)
		if err != nil {
			return err
		}
		if !allowed {
			return FTPArtifactError(artifactBindingRefused)
		}
		if read != nil {
			allowed, err = requestrepo.New().CheckFTPArtifactRead(ctx, db, authority, read.ProjectEnvironmentID, read.ReadQueryID, read.ReadQueryUpdatedAt)
			if err != nil {
				return err
			}
			if !allowed {
				return FTPArtifactError(artifactBindingRefused)
			}
		}
		return nil
	})
}

func (b *Broker) openFTPArtifact(ctx context.Context, req FTPArtifactRequest) (*ftp.Connector, func(), error) {
	ctx = connector.WithDataSourceIdentity(ctx, req.OrganizationSlug, req.DataSourceSlug)
	conn, err := b.factory(ctx, req.DataSourceType, req.ConfigJSON, req.CredentialsJSON)
	if err != nil {
		return nil, nil, err
	}
	ftpConn, ok := connector.FTPArtifactConnector(conn)
	if !ok {
		_ = conn.Close()
		return nil, nil, FTPArtifactError(artifactBindingRefused)
	}
	return ftpConn, func() { _ = conn.Close() }, nil
}

func (b *Broker) publishFTPArtifact(ctx context.Context, request requestrepo.Request, req FTPArtifactRequest, body []byte) (encryptedResponse, bool) {
	authority, err := artifactAuthority(request, req)
	if err != nil {
		return encryptedResponse{ErrorCode: artifactBindingRefused}, false
	}
	var spent bool
	err = b.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		spent, err = requestrepo.New().SpendFTPArtifactPermit(ctx, tx, authority)
		return err
	})
	if err != nil {
		return encryptedResponse{ErrorCode: artifactPublishUncertain}, false
	}
	if !spent {
		return encryptedResponse{ErrorCode: artifactBindingRefused}, false
	}
	providerCtx, cancel := artifactProviderContext(ctx, req.Limits.ConnectorTimeout)
	defer cancel()
	conn, closeConn, err := b.openFTPArtifact(providerCtx, req)
	if err != nil {
		return encryptedResponse{ErrorCode: artifactPublishUncertain}, false
	}
	defer closeConn()
	if err := conn.PublishArtifact(providerCtx, req.ArtifactEvidence, bytes.NewReader(body), func(ctx context.Context) error {
		return b.checkFTPArtifactDispatch(ctx, authority, nil)
	}); err != nil {
		return encryptedResponse{ErrorCode: artifactPublishUncertain}, false
	}
	return encryptedResponse{FTPArtifact: &ftpArtifactResponse{Evidence: &req.ArtifactEvidence}}, true
}

func (b *Broker) readFTPArtifact(ctx context.Context, request requestrepo.Request, req FTPArtifactRequest, options filedata.ParseCSVOptions) (encryptedResponse, bool) {
	authority, err := artifactAuthority(request, req)
	if err != nil {
		return encryptedResponse{ErrorCode: artifactBindingRefused}, false
	}
	if err := b.checkFTPArtifactDispatch(ctx, authority, &req); err != nil {
		return encryptedResponse{ErrorCode: artifactReadDispatchError(err)}, false
	}
	providerCtx, cancel := artifactProviderContext(ctx, req.Limits.ConnectorTimeout)
	defer cancel()
	conn, closeConn, err := b.openFTPArtifact(providerCtx, req)
	if err != nil {
		return encryptedResponse{ErrorCode: artifactReadError(ctx, err)}, false
	}
	defer closeConn()
	options.MaxResultBytes = req.Limits.MaxResponseBytes
	rows, err := conn.FetchArtifactCSV(providerCtx, req.ArtifactEvidence, req.Limits.MaxFileBytes, options)
	if err != nil {
		return encryptedResponse{ErrorCode: artifactReadError(ctx, err)}, false
	}
	if err := b.checkFTPArtifactDispatch(ctx, authority, &req); err != nil {
		return encryptedResponse{ErrorCode: artifactReadDispatchError(err)}, false
	}
	encoded, err := json.Marshal(rows)
	if err != nil || int64(len(encoded)) > req.Limits.MaxResponseBytes {
		return encryptedResponse{ErrorCode: errorCodeResponseSize}, false
	}
	return encryptedResponse{FTPArtifact: &ftpArtifactResponse{Rows: rows}}, true
}

func artifactProviderContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func artifactReadError(ctx context.Context, err error) string {
	if xerror.Is(err, ftp.ErrArtifactChanged) {
		return artifactReadChanged
	}
	if xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
		return errorCodeResponseSize
	}
	var transport net.Error
	var reply *textproto.Error
	transferAborted := xerror.As(err, &reply) && reply.Code == 426
	if ctx.Err() == nil && (transferAborted || xerror.As(err, &transport) ||
		xerror.Is(err, io.EOF) || xerror.Is(err, io.ErrUnexpectedEOF) || xerror.Is(err, context.DeadlineExceeded)) {
		return artifactReadTransient
	}
	return artifactReadUnavailable
}

func artifactReadDispatchError(err error) string {
	if xerror.Is(err, FTPArtifactError(artifactBindingRefused)) {
		return artifactBindingRefused
	}
	// Infrastructure failure does not prove that the FTP provider completed a transient read.
	return errorCodeQueryFailed
}

func sameArtifactStatement(a, b []byte) bool {
	var left, right bytes.Buffer
	return json.Compact(&left, a) == nil && json.Compact(&right, b) == nil && bytes.Equal(left.Bytes(), right.Bytes())
}
