package pipelinesvc

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

var errArtifactHandoffDisabled = xerror.InvalidInput(xerror.Pipeline).Detail("artifact handoff is not enabled")

// QualifiedFTPStorage identifies an administrator-qualified namespace for one
// project environment, binding revision, source configuration and credential.
type QualifiedFTPStorage struct {
	Organization, Project, Environment, Source slug.Slug
	EnvironmentID                              int64
	BindingRevision, ConfigDigest              string
	CredentialReference, Directory             string
}

type ftpArtifactQueryResolver interface {
	ResolveFTPArtifactWrite(context.Context, slug.Slug, slug.Slug, contractplan.OperationContent) (queriessvc.ConnectorExecutionRequest, error)
}

type ftpArtifactBroker interface {
	PrepareFTPArtifact(context.Context, executionbroker.FTPArtifactRequest) (ftp.ArtifactEvidence, error)
	PublishFTPArtifact(context.Context, executionbroker.FTPArtifactRequest) error
}

// NewFTPArtifactProducer assembles the dormant sink path. Its qualification
// resolver must read trusted operator state. No production caller installs it in this phase.
func NewFTPArtifactProducer(
	txm postgres.TransactionManager, repo pipelinesinkeffects.Repository,
	queries ftpArtifactQueryResolver, broker ftpArtifactBroker,
	qualification func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (QualifiedFTPStorage, error),
) pipelineruntime.ArtifactProducer {
	return func(ctx context.Context, input *pipelineruntime.SinkRuntimeInput, rows []map[string]any, effect pipelinerun.SinkEffectAttempt) error {
		if txm == nil || repo == nil || queries == nil || broker == nil || qualification == nil {
			return deploymentplan.ErrInvalid
		}
		org, project, environment, source := input.OrgSlug, input.ProjectSlug, input.EnvironmentSlug, input.SinkDataSourceSlug
		pin := input.OperationSnapshot
		if pin == nil || input.OperationRevision == "" || input.BindingRevision == "" {
			return deploymentplan.ErrInvalid
		}
		target, _ := input.SinkConfig["path"].(string)
		if err := validateArtifactOperation(pin, path.Base(target), true); err != nil {
			return err
		}
		storage, err := qualification(ctx, org, project, environment, source)
		if err != nil || storage.Organization != org || storage.Project != project || storage.Environment != environment ||
			storage.Source != source || storage.EnvironmentID <= 0 || storage.Directory != path.Dir(target) ||
			storage.BindingRevision != input.BindingRevision || storage.ConfigDigest == "" || storage.CredentialReference == "" {
			return deploymentplan.ErrInvalid.Detail("artifact storage binding is not qualified").With(err)
		}
		request, err := queries.ResolveFTPArtifactWrite(ctx, org, source, *pin)
		if err != nil || request.CredentialReference != storage.CredentialReference || request.PinnedConfigDigest != storage.ConfigDigest {
			return deploymentplan.ErrInvalid.Detail("artifact storage credentials or configuration do not match qualification").With(err)
		}
		// The same serialized rows are sent for preparation and for publication. The
		// executor independently regenerates and verifies the bounded encoded bytes.
		body, err := json.Marshal(rows)
		if err != nil {
			return xerror.InvalidInput(xerror.PipelineRun).With(err)
		}
		req := executionbroker.FTPArtifactRequest{
			ConnectorExecutionRequest: request, SavedQueryData: pin.Data, Content: body, Directory: storage.Directory,
			ArtifactEvidence: ftp.ArtifactEvidence{DeliveryID: uuid.New()}, PublishRequestID: uuid.New(), ProjectEnvironmentID: storage.EnvironmentID,
		}
		req.ArtifactEvidence, err = broker.PrepareFTPArtifact(ctx, req)
		if err != nil {
			return err
		}
		ledger := sinkEffectLedgerService{txm: txm, repo: repo}
		if err := ledger.beginFTPArtifact(ctx, input, effect, req); err != nil {
			return err
		}
		input.EffectState = pipelinerun.SinkEffectStateInProgress
		if err := broker.PublishFTPArtifact(ctx, req); err != nil {
			if ledger.ftpArtifactPublishUncertain(ctx, org, req, err) {
				input.ReplayDecision = "artifact_publication_uncertain"
				input.DeliveryOutcome = "unknown"
			}
			return err
		}
		if err := ledger.confirmFTPArtifact(ctx, org, req); err != nil {
			input.ReplayDecision = "artifact_publication_uncertain"
			input.DeliveryOutcome = "unknown"
			return err
		}
		input.EffectState = pipelinerun.SinkEffectStateSucceeded
		input.DeliveryOutcome = "confirmed"
		return nil
	}
}

func validateArtifactOperation(pin *contractplan.OperationContent, filename string, write bool) error {
	if pin == nil || pin.ResourceScope != nil || len(pin.Parameters) != 0 {
		return deploymentplan.ErrInvalid
	}
	raw, err := queriessvc.ExtractQueryStatement(pin.Data)
	if err != nil {
		return deploymentplan.ErrInvalid.With(err)
	}
	stmt, err := remotefile.DecodeStatement(raw)
	if err != nil || stmt.Format != filedata.QueryFormatCSV || stmt.Overwrite != nil || stmt.ContentSet ||
		stmt.Path != filename || strings.ContainsAny(filename, "/\\{}") || filename == ".csv" || !strings.HasSuffix(filename, ".csv") {
		return deploymentplan.ErrInvalid.With(err)
	}
	wanted := remotefile.OperationRead
	if write {
		wanted = remotefile.OperationWrite
	}
	if remotefile.OperationOrDefault(stmt.Operation) != wanted || (write && stmt.Operation != wanted) {
		return deploymentplan.ErrInvalid
	}
	return nil
}

func (s sinkEffectLedgerService) beginFTPArtifact(ctx context.Context, input *pipelineruntime.SinkRuntimeInput, attempt pipelinerun.SinkEffectAttempt, req executionbroker.FTPArtifactRequest) error {
	credential, revision, ok := strings.Cut(req.CredentialReference, "/")
	credentialUpdatedAt, err := time.Parse(time.RFC3339Nano, revision)
	if !ok || err != nil {
		return deploymentplan.ErrInvalid.With(err)
	}
	writeUpdatedAt, err := time.Parse(time.RFC3339Nano, input.OperationRevision)
	if err != nil {
		return deploymentplan.ErrInvalid.With(err)
	}
	return s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		query, err := pipelinesinkeffects.FindFTPArtifactWriteQuery(ctx, tx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationRef)
		if err != nil || !query.UpdatedAt.Time.Equal(writeUpdatedAt) {
			return deploymentplan.ErrInvalid.With(err)
		}
		effect, err := s.repo.BeginOrGet(ctx, tx, input.OrgSlug, input.ProjectSlug, input.EnvironmentSlug, attempt)
		if err != nil {
			return err
		}
		if effect == nil || !effect.Claimed {
			return xerror.ReplayUnsafe().WithPublicDetail("This artifact attempt already holds a write claim. Resolve the original publication before starting a new run.")
		}
		created, err := pipelinesinkeffects.CreateFTPArtifactIntent(ctx, tx, dbgen.CreateFTPArtifactIntentParams{
			DeliveryID: pgtype.UUID{Bytes: req.DeliveryID.UUID, Valid: true}, PublishRequestID: pgtype.UUID{Bytes: req.PublishRequestID.UUID, Valid: true},
			OrgSlug: input.OrgSlug.String(), ProjectEnvironmentID: req.ProjectEnvironmentID, AttemptSlug: input.RunSlug,
			SinkStepSlug: attempt.SinkStepSlug.String(), EffectKey: attempt.IdempotencyKey,
			SourceSlug: input.SinkDataSourceSlug.String(), ExpectedConfig: req.ConfigJSON, CredentialSlug: credential,
			CredentialRevision: pgtype.Timestamptz{Time: credentialUpdatedAt, Valid: true},
			WriteQueryID:       int64(query.ID), WriteQueryRevision: query.UpdatedAt, Statement: req.SavedQueryData,
			ConfigDigest: req.PinnedConfigDigest, FinalPath: req.FinalPath, StagingPath: req.StagingPath, SizeBytes: req.SizeBytes, Sha256: req.SHA256,
		})
		if err != nil || !created {
			return deploymentplan.ErrInvalid.With(err)
		}
		return nil
	})
}

func (s sinkEffectLedgerService) confirmFTPArtifact(ctx context.Context, org slug.Slug, req executionbroker.FTPArtifactRequest) error {
	err := s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		ok, err := pipelinesinkeffects.ConfirmFTPArtifactDelivery(ctx, tx, org, req.ProjectEnvironmentID, req.DeliveryID, req.PublishRequestID)
		if err != nil || !ok {
			return xerror.Unexpected().Detail("artifact receipt confirmation refused").With(err)
		}
		return nil
	})
	if err == nil {
		return nil
	}
	// A commit reply can be lost. Resolve only the original immutable receipt.
	receipt, probeErr := s.probeFTPArtifact(ctx, org, req)
	if probeErr == nil && receipt.ConfirmedAt.Valid {
		return nil
	}
	return xerror.Join(err, probeErr)
}

func (s sinkEffectLedgerService) probeFTPArtifact(ctx context.Context, org slug.Slug, req executionbroker.FTPArtifactRequest) (dbgen.PipelineArtifactDelivery, error) {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(postgres.WithoutTransaction(ctx)), 2*time.Second)
	defer cancel()
	return postgres.InRead(probeCtx, s.txm, func(ctx context.Context, db postgres.DBTX) (dbgen.PipelineArtifactDelivery, error) {
		row, err := pipelinesinkeffects.FindFTPArtifactIntent(ctx, db, org, req.ProjectEnvironmentID, req.DeliveryID)
		if err == nil && (!row.PublishRequestID.Valid || row.PublishRequestID.Bytes != req.PublishRequestID.UUID) {
			return dbgen.PipelineArtifactDelivery{}, deploymentplan.ErrInvalid
		}
		return row, err
	})
}

func (s sinkEffectLedgerService) ftpArtifactPublishUncertain(ctx context.Context, org slug.Slug, req executionbroker.FTPArtifactRequest, err error) bool {
	if xerror.Is(err, executionbroker.ErrFTPArtifactSubmissionUnknown) {
		return true
	}
	row, probeErr := s.probeFTPArtifact(ctx, org, req)
	if probeErr != nil || row.PermitSpentAt.Valid {
		return true
	}
	var outcome executionbroker.FTPArtifactError
	if xerror.As(err, &outcome) {
		return outcome.PublicationUncertain()
	}
	return row.SubmittedAt.Valid
}
