package proofcandidatesvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const maxSourceBytes = 10 * 1024 * 1024

// SourceVerifier reads pipeline bytes from the exact provider head after
// re-checking change state, repository, refs, and head SHA.
type SourceVerifier interface {
	VerifySource(context.Context, proofrepo.Mapping, gitprovider.VerifiedChange) ([]byte, error)
}

type Service interface {
	IngestTarget(context.Context, proofrepo.Mapping, model.EventActor, gitprovider.VerifiedChange) (*Result, error)
	CloseTarget(context.Context, proofrepo.Mapping, model.EventActor, gitprovider.VerifiedChange) error
	Ingest(context.Context, slug.Slug, model.EventActor, gitprovider.VerifiedChange) (*Result, error)
	Close(context.Context, slug.Slug, model.EventActor, gitprovider.VerifiedChange) error
}

type Result struct {
	CandidateSlug           slug.Slug
	OrganizationSlug        slug.Slug
	ProjectSlug             slug.Slug
	EnvironmentSlug         slug.Slug
	ProviderConnectionSlug  slug.Slug
	CandidateKey            string
	HeadSHA                 string
	SourceSHA256            string
	SpecHash                string
	SupersedesCandidateSlug *slug.Slug
	Replay                  bool
}

type service struct {
	txm     postgres.TransactionManager
	repo    proofrepo.Repository
	sources SourceVerifier
	events  workerproducer.EventProducer
}

func New(txm postgres.TransactionManager, repo proofrepo.Repository, sources SourceVerifier, events workerproducer.EventProducer) (Service, error) {
	if txm == nil || repo == nil || sources == nil || events == nil {
		return nil, xerror.Unexpected().Detail("proof candidate dependencies are required")
	}
	return &service{txm: txm, repo: repo, sources: sources, events: events}, nil
}

func (s *service) Ingest(
	ctx context.Context,
	organizationSlug slug.Slug,
	actor model.EventActor,
	change gitprovider.VerifiedChange,
) (*Result, error) {
	return s.ingest(ctx, organizationSlug, actor, change, 0)
}

func (s *service) IngestTarget(ctx context.Context, target proofrepo.Mapping, actor model.EventActor, change gitprovider.VerifiedChange) (*Result, error) {
	if target.ID <= 0 {
		return nil, ErrMappingNotFound
	}
	return s.ingest(ctx, target.OrganizationSlug, actor, change, target.ID)
}

func (s *service) ingest(ctx context.Context, organizationSlug slug.Slug, actor model.EventActor, change gitprovider.VerifiedChange, mappingID int64) (*Result, error) {
	if _, err := slug.New(organizationSlug.String()); err != nil {
		return nil, xerror.InvalidInput(xerror.OrganizationSlug).With(err)
	}
	if err := change.Validate(); err != nil {
		return nil, err
	}
	lookup := mappingLookup(organizationSlug, change)
	lookup.MappingID = mappingID
	mapping, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofrepo.Mapping, error) {
		return resolveOneMapping(ctx, s.repo, db, lookup)
	})
	if err != nil {
		return nil, err
	}
	source, err := s.sources.VerifySource(ctx, *mapping, change)
	if err != nil {
		return nil, err
	}
	if len(source) == 0 {
		return nil, ErrParseFailed.Detail("pipeline source is empty")
	}
	if len(source) > maxSourceBytes {
		return nil, ErrSourceTooLarge
	}
	pipeline, err := pipelinevalidate.Load(change.PipelinePath, source)
	if err != nil {
		return nil, ErrParseFailed.With(err)
	}
	// Match pipeline-version identity; editor formatting must not change its bytes or step order.
	canonical, err := json.Marshal(pipeline)
	if err != nil {
		return nil, ErrParseFailed.With(err)
	}
	sourceHash := digest(source)
	specHash := digest(canonical)
	candidateKey, err := identityDigest(*mapping, change, sourceHash, specHash)
	if err != nil {
		return nil, err
	}
	candidateSlug := slug.Slug("proof-candidate-" + strings.TrimPrefix(candidateKey, "sha256:")[:32])

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*Result, error) {
		if err := s.repo.LockSource(ctx, tx, mapping.OrganizationSlug, mapping.ProviderConnectionSlug); err != nil {
			return nil, err
		}
		locked, err := resolveOneMapping(ctx, s.repo, tx, lookup)
		if err != nil {
			return nil, err
		}
		if locked.ID != mapping.ID || locked.OrganizationSlug != mapping.OrganizationSlug ||
			locked.ProjectSlug != mapping.ProjectSlug || locked.EnvironmentSlug != mapping.EnvironmentSlug ||
			locked.ProviderConnectionSlug != mapping.ProviderConnectionSlug {
			return nil, ErrMappingAmbiguous.Detail("proof candidate mapping changed during ingestion")
		}
		existing, err := s.repo.FindByKey(ctx, tx, locked.OrganizationSlug, candidateKey)
		if err == nil {
			if existing.ClosedAt != nil {
				existing, err = s.repo.ReopenByKey(ctx, tx, locked.OrganizationSlug, candidateKey)
				if err != nil {
					return nil, xerror.Wrapf("reopen proof change candidate: %w", err)
				}
				if _, err := s.events.EnqueueRequired(ctx, tx, locked.OrganizationSlug, actor, candidateEvent{
					candidate: existing, mapping: *locked, pipelineSlug: pipeline.Metadata.Slug, action: model.EventActionRestore,
				}); err != nil {
					return nil, xerror.Wrapf("record reopened proof candidate audit event: %w", err)
				}
			} else if change.Rerequest {
				if _, err := s.events.EnqueueRequired(ctx, tx, locked.OrganizationSlug, actor, candidateEvent{
					candidate: existing, mapping: *locked, pipelineSlug: pipeline.Metadata.Slug, action: model.EventActionReconcile,
				}); err != nil {
					return nil, xerror.Wrapf("record re-requested proof candidate audit event: %w", err)
				}
			}
			var supersedes *proofrepo.Candidate
			if existing.SupersedesCandidateID != nil {
				supersedes, err = s.repo.FindByID(ctx, tx, locked.OrganizationSlug, *existing.SupersedesCandidateID)
				if err != nil {
					return nil, err
				}
			}
			return ingestResult(existing, locked, supersedes, true), nil
		}
		if !xerror.Is(err, proofrepo.ErrCandidateNotFound) {
			return nil, err
		}
		latest, err := s.repo.FindLatest(ctx, tx, locked.OrganizationSlug, locked.ID, change.ChangeRequestID)
		if err != nil && !xerror.Is(err, proofrepo.ErrCandidateNotFound) {
			return nil, err
		}
		var supersedes *int64
		if latest != nil {
			supersedes = &latest.ID
		}
		created, err := s.repo.Create(ctx, tx, locked.OrganizationSlug, proofrepo.CreateCandidateParams{
			MappingID: locked.ID, Slug: candidateSlug, CandidateKey: candidateKey,
			ChangeRequestID: change.ChangeRequestID, SourceRef: change.SourceRef,
			HeadSHA: change.HeadSHA, SourceSHA256: sourceHash, SpecHash: specHash,
			CanonicalSpec: canonical, SupersedesCandidateID: supersedes,
		})
		if err != nil {
			return nil, xerror.Wrapf("create proof change candidate: %w", err)
		}
		if _, err := s.events.EnqueueRequired(ctx, tx, locked.OrganizationSlug, actor, candidateEvent{
			candidate: created, mapping: *locked, pipelineSlug: pipeline.Metadata.Slug, action: model.EventActionReconcile,
		}); err != nil {
			return nil, xerror.Wrapf("record proof candidate audit event: %w", err)
		}
		return ingestResult(created, locked, latest, false), nil
	})
}

func (s *service) Close(
	ctx context.Context,
	organizationSlug slug.Slug,
	actor model.EventActor,
	change gitprovider.VerifiedChange,
) error {
	return s.close(ctx, organizationSlug, actor, change, 0)
}

func (s *service) CloseTarget(ctx context.Context, target proofrepo.Mapping, actor model.EventActor, change gitprovider.VerifiedChange) error {
	if target.ID <= 0 {
		return ErrMappingNotFound
	}
	return s.close(ctx, target.OrganizationSlug, actor, change, target.ID)
}

func (s *service) close(ctx context.Context, organizationSlug slug.Slug, actor model.EventActor, change gitprovider.VerifiedChange, mappingID int64) error {
	if _, err := slug.New(organizationSlug.String()); err != nil {
		return xerror.InvalidInput(xerror.OrganizationSlug).With(err)
	}
	if err := change.Validate(); err != nil {
		return err
	}
	lookup := mappingLookup(organizationSlug, change)
	lookup.MappingID = mappingID
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		mapping, err := resolveOneMapping(ctx, s.repo, tx, lookup)
		if err != nil {
			return err
		}
		latest, err := s.repo.FindLatest(ctx, tx, mapping.OrganizationSlug, mapping.ID, change.ChangeRequestID)
		if err != nil {
			if xerror.Is(err, proofrepo.ErrCandidateNotFound) {
				return nil
			}
			return err
		}
		if latest.HeadSHA != change.HeadSHA {
			return gitprovider.ErrChangeStale
		}
		if latest.ClosedAt != nil {
			return nil
		}
		candidate, err := s.repo.CloseLatest(
			ctx, tx, mapping.OrganizationSlug, mapping.ID, change.ChangeRequestID, change.HeadSHA,
		)
		if err != nil {
			return err
		}
		pipeline, err := pipelinevalidate.Load(change.PipelinePath, candidate.CanonicalSpec)
		if err != nil {
			return ErrParseFailed.With(err)
		}
		if _, err := s.events.EnqueueRequired(ctx, tx, mapping.OrganizationSlug, actor, candidateEvent{
			candidate: candidate, mapping: *mapping, pipelineSlug: pipeline.Metadata.Slug, action: model.EventActionDelete,
		}); err != nil {
			return xerror.Wrapf("record closed proof candidate audit event: %w", err)
		}
		return nil
	})
}

func ingestResult(candidate *proofrepo.Candidate, mapping *proofrepo.Mapping, supersedes *proofrepo.Candidate, replay bool) *Result {
	result := &Result{
		CandidateSlug: candidate.Slug, OrganizationSlug: mapping.OrganizationSlug,
		ProjectSlug: mapping.ProjectSlug, EnvironmentSlug: mapping.EnvironmentSlug,
		ProviderConnectionSlug: mapping.ProviderConnectionSlug, CandidateKey: candidate.CandidateKey,
		HeadSHA: candidate.HeadSHA, SourceSHA256: candidate.SourceSHA256, SpecHash: candidate.SpecHash,
		Replay: replay,
	}
	if supersedes != nil {
		result.SupersedesCandidateSlug = &supersedes.Slug
	}
	return result
}

func resolveOneMapping(ctx context.Context, repo proofrepo.Repository, db postgres.DBTX, lookup proofrepo.MappingLookup) (*proofrepo.Mapping, error) {
	mappings, err := repo.ResolveMappings(ctx, db, lookup)
	if err != nil {
		return nil, err
	}
	switch len(mappings) {
	case 0:
		return nil, ErrMappingNotFound
	case 1:
		return &mappings[0], nil
	default:
		return nil, ErrMappingAmbiguous.Detail("configure one exact project and environment mapping")
	}
}

func mappingLookup(organizationSlug slug.Slug, change gitprovider.VerifiedChange) proofrepo.MappingLookup {
	return proofrepo.MappingLookup{
		OrganizationSlug: organizationSlug, Provider: string(change.Provider), ProviderConnectionID: change.ProviderConnectionID,
		RepositoryID: change.RepositoryID, TargetRef: change.TargetRef, PipelinePath: change.PipelinePath,
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func identityDigest(mapping proofrepo.Mapping, change gitprovider.VerifiedChange, sourceHash, specHash string) (string, error) {
	identity := struct {
		MappingID            int64  `json:"mapping_id"`
		Organization         string `json:"organization"`
		Project              string `json:"project"`
		Environment          string `json:"environment"`
		Connection           string `json:"connection"`
		Provider             string `json:"provider"`
		ProviderConnectionID string `json:"provider_connection_id"`
		RepositoryID         string `json:"repository_id"`
		ChangeRequestID      string `json:"change_request_id"`
		HeadSHA              string `json:"head_sha"`
		SourceRef            string `json:"source_ref"`
		TargetRef            string `json:"target_ref"`
		PipelinePath         string `json:"pipeline_path"`
		SourceHash           string `json:"source_hash"`
		SpecHash             string `json:"spec_hash"`
	}{
		MappingID: mapping.ID, Organization: mapping.OrganizationSlug.String(), Project: mapping.ProjectSlug.String(),
		Environment: mapping.EnvironmentSlug.String(), Connection: mapping.ProviderConnectionSlug.String(),
		Provider: mapping.Provider, ProviderConnectionID: mapping.ProviderConnectionID, RepositoryID: mapping.RepositoryID,
		ChangeRequestID: change.ChangeRequestID, HeadSHA: change.HeadSHA,
		SourceRef: change.SourceRef, TargetRef: change.TargetRef,
		PipelinePath: change.PipelinePath, SourceHash: sourceHash, SpecHash: specHash,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return digest(encoded), nil
}

type candidateEvent struct {
	candidate    *proofrepo.Candidate
	mapping      proofrepo.Mapping
	pipelineSlug slug.Slug
	action       model.EventAction
}

func (e candidateEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix: candidateEventSlugPrefix(e.action), EventClass: model.EventClassSecurityAudit,
		EntityType: model.EventEntityTypePipeline, EntitySlug: e.pipelineSlug,
		Action: e.action, SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata: map[string]any{
			"project_slug": e.mapping.ProjectSlug.String(), "environment_slug": e.mapping.EnvironmentSlug.String(),
			"provider": e.mapping.Provider, "provider_connection_id": e.mapping.ProviderConnectionID,
			"repository_id": e.mapping.RepositoryID, "change_request_id": e.candidate.ChangeRequestID,
			"source_ref": e.candidate.SourceRef, "target_ref": e.mapping.TargetRef,
			"proof_candidate_slug": e.candidate.Slug.String(), "head_sha": e.candidate.HeadSHA,
			"pipeline_path": e.mapping.PipelinePath, "source_sha256": e.candidate.SourceSHA256,
			"pipeline_version_spec_hash": e.candidate.SpecHash,
		},
	}, nil
}

func candidateEventSlugPrefix(action model.EventAction) slug.Slug {
	switch action {
	case model.EventActionDelete:
		return "evt-proof-candidate-close"
	case model.EventActionRestore:
		return "evt-proof-candidate-reopen"
	default:
		return "evt-proof-candidate-ingest"
	}
}
