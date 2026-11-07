package pipelinesvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinecreations"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type creationRepository interface {
	List(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, int) ([]*pipelinecreations.Row, error)
	Create(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *pipelinecreations.Row) (bool, error)
	Get(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*pipelinecreations.Row, error)
	Find(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*pipelinecreations.Row, error)
	Update(context.Context, postgres.DBTX, slug.Slug, slug.Slug, *pipelinecreations.Row) (bool, error)
}

type creationPipelineStore interface {
	Exists(context.Context, slug.Slug, slug.Slug, slug.Slug) (bool, error)
	ResolveProposalAuthority(context.Context, slug.Slug, slug.Slug) (model.PipelineProposalAuthority, error)
}

type creationProjectService interface {
	GetProject(context.Context, slug.Slug, slug.Slug) (*model.Project, error)
}

// CreationProposal is pending authoring state, never a deployable canonical pipeline.
type CreationProposal struct {
	Slug         slug.Slug
	ProjectSlug  slug.Slug
	PipelineSlug slug.Slug
	MemberSlug   slug.Slug
	SourcePath   string
	SpecHash     string
	Spec         *pipelinespec.Pipeline
	Status       string
	FailureCode  string
	Proposal     *pipelinegit.ProposalResult
}

type CreationService struct {
	txm           postgres.TransactionManager
	repo          creationRepository
	projects      creationProjectService
	store         creationPipelineStore
	authorization authorization.Service
	producer      workerproducer.PipelineGitProducer
	events        workerproducer.EventProducer
}

type CreationProposalPage struct {
	Items   []*CreationProposal
	HasMore bool
}

func (s *CreationService) List(ctx context.Context, org, project, after slug.Slug, limit int) (*CreationProposalPage, error) {
	if limit < 1 || limit > 100 {
		return nil, xerror.ValidationFailed().Detail("proposal page limit must be between 1 and 100")
	}
	rows, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]*pipelinecreations.Row, error) {
		return s.repo.List(ctx, db, org, project, after, limit+1)
	})
	if err != nil {
		return nil, err
	}
	page := &CreationProposalPage{Items: make([]*CreationProposal, 0, min(len(rows), limit)), HasMore: len(rows) > limit}
	if page.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		proposal, err := creationDetail(project, row)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, proposal)
	}
	return page, nil
}

func NewCreationService(txm postgres.TransactionManager, repo creationRepository, projects creationProjectService, store creationPipelineStore, auth authorization.Service, producer workerproducer.PipelineGitProducer, events workerproducer.EventProducer) (*CreationService, error) {
	if txm == nil || repo == nil || projects == nil || store == nil || auth == nil || producer == nil || events == nil {
		return nil, xerror.Unexpected().Detail("pipeline creation dependencies are required")
	}
	return &CreationService{txm, repo, projects, store, auth, producer, events}, nil
}

func (s *CreationService) Create(ctx context.Context, org, project, member slug.Slug, actor model.EventActor, spec *pipelinespec.Pipeline) (*CreationProposal, error) {
	if err := authorizeProposalMember(member, actor); err != nil {
		return nil, err
	}
	if err := pipelinevalidate.Validate(spec); err != nil {
		return nil, err
	}
	creation, err := slug.WithRandomSuffix("creation", 18)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var saved *pipelinecreations.Row
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		exists, err := s.store.Exists(ctx, org, project, spec.Metadata.Slug)
		if err != nil {
			return err
		}
		if exists {
			return xerror.AlreadyExists(xerror.Pipeline).Detail("pipeline already exists; use its draft workflow")
		}
		backend, err := s.backend(ctx, org, project)
		if err != nil {
			return err
		}
		authority, err := s.store.ResolveProposalAuthority(ctx, org, project)
		if err != nil {
			return err
		}
		sourcePath := path.Join(backend.PipelineDir, spec.Metadata.Slug.String()+".pipeline.yaml")
		canonical, content, err := renderCanonicalPipelineSource(sourcePath, spec)
		if err != nil {
			return err
		}
		canonicalJSON, err := json.Marshal(canonical)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		base := "refs/heads/" + backend.TrackedBranch
		args := jobs.PipelineGitPublishProposalArgs{CreationSlug: creation, OrganizationSlug: org, ProjectSlug: project, PipelineSlug: spec.Metadata.Slug, MemberSlug: member, DataSourceSlug: backend.DataSourceSlug, BaseRef: &base, BranchName: "braidplane/create/" + creation.String(), Path: sourcePath, Content: content, CommitMessage: "create pipeline " + spec.Metadata.Slug.String(), ProposalTitle: "Create pipeline " + spec.Metadata.Slug.String(), ProposalBody: "New pipeline proposed by Braidplane. Review and merge before syncing it into Braidplane.", DraftSpecHash: fmt.Sprintf("%x", sha256.Sum256(canonicalJSON)), PublishAttemptID: creation.String(), Authority: authority.Payload()}
		request, err := json.Marshal(args)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		inserted, err := s.repo.Create(ctx, tx, org, project, &pipelinecreations.Row{Slug: creation, PipelineSlug: spec.Metadata.Slug, MemberSlug: member, Request: request, PublishAttemptID: args.PublishAttemptID})
		if err != nil {
			return err
		}
		saved, err = s.repo.Find(ctx, tx, org, project, spec.Metadata.Slug)
		if err != nil {
			return err
		}
		previous, err := creationRequest(saved)
		if err != nil {
			return err
		}
		if previous.MemberSlug != member || previous.DraftSpecHash != args.DraftSpecHash || previous.Path != args.Path || previous.DataSourceSlug != args.DataSourceSlug || previous.Authority != args.Authority || ptr.Value(previous.BaseRef) != base {
			return xerror.AlreadyExists(xerror.Pipeline).Detail("another creation proposal exists for this pipeline; inspect or retry its recorded definition, or choose a different pipeline name/slug for a new definition or repository")
		}
		if (saved.Status == "proposal_open" || saved.Status == "branch_pushed") || !inserted && saved.Status == "publish_queued" {
			return nil
		}
		saved.Status, saved.FailureCode = "publish_queued", ""
		if _, err := s.repo.Update(ctx, tx, org, project, saved); err != nil {
			return err
		}
		if _, err := s.producer.EnqueuePublishProposal(ctx, tx, previous); err != nil {
			return err
		}
		_, err = s.events.EnqueueRequired(ctx, tx, org, actor, pipelineEvent{securityAudit: true, projectSlug: project, pipelineSlug: spec.Metadata.Slug, action: model.EventActionUpdate, metadata: map[string]any{"mutation": "pipeline_creation_proposed", "creation_slug": saved.Slug.String(), "source_path": previous.Path}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return creationDetail(project, saved)
}

func (s *CreationService) Get(ctx context.Context, org, project, creation slug.Slug) (*CreationProposal, error) {
	row, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*pipelinecreations.Row, error) {
		return s.repo.Get(ctx, db, org, project, creation)
	})
	if err != nil {
		return nil, err
	}
	return creationDetail(project, row)
}

func (s *CreationService) backend(ctx context.Context, org, project slug.Slug) (*model.ProjectPipelineBackend, error) {
	p, err := s.projects.GetProject(ctx, org, project)
	if err != nil {
		return nil, err
	}
	if p == nil || p.PipelineBackend == nil {
		return nil, ErrPipelineGitSourceRequired.Detail("connect the project repository before proposing a pipeline")
	}
	b := p.PipelineBackend
	dir := strings.TrimSpace(b.PipelineDir)
	if dir == "" || path.IsAbs(dir) || path.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") || strings.Contains(dir, "\\") || strings.TrimSpace(b.TrackedBranch) == "" {
		return nil, ErrPipelineGitSourceRequired.Detail("project repository branch or pipeline path is invalid")
	}
	return b, nil
}

func (s *CreationService) AuthorizeProposalPublish(ctx context.Context, args jobs.PipelineGitPublishProposalArgs) error {
	if err := authorizeProposalMember(args.MemberSlug, model.UserEventActor(args.MemberSlug)); err != nil {
		return err
	}
	allowed, err := s.authorization.Can(ctx, authorization.Input{UserSlug: args.MemberSlug, OrgSlug: &args.OrganizationSlug, Action: model.Create, Resource: model.ResourcePipelines})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrPipelineProposalAuthorityDenied.Detail("initiating member no longer has pipeline create permission")
	}
	row, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*pipelinecreations.Row, error) {
		return s.repo.Get(ctx, db, args.OrganizationSlug, args.ProjectSlug, args.CreationSlug)
	})
	if err != nil {
		return ErrPipelineProposalAuthorityDenied.With(err)
	}
	if row.Status == "proposal_open" || row.Status == "branch_pushed" {
		return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation was already published")
	}
	stored, err := creationRequest(row)
	if err != nil {
		return err
	}
	storedJSON, err := json.Marshal(stored)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	queuedJSON, err := json.Marshal(args)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if row.MemberSlug != args.MemberSlug || row.PublishAttemptID != args.PublishAttemptID || !bytes.Equal(storedJSON, queuedJSON) {
		return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation request changed after queueing")
	}
	backend, err := s.backend(ctx, args.OrganizationSlug, args.ProjectSlug)
	if err != nil {
		return ErrPipelineProposalAuthorityDenied.With(err)
	}
	authority, err := s.store.ResolveProposalAuthority(ctx, args.OrganizationSlug, args.ProjectSlug)
	if err != nil {
		return ErrPipelineProposalAuthorityDenied.With(err)
	}
	if backend.DataSourceSlug != args.DataSourceSlug || path.Join(backend.PipelineDir, args.PipelineSlug.String()+".pipeline.yaml") != args.Path || "refs/heads/"+backend.TrackedBranch != ptr.Value(args.BaseRef) || authority.Payload() != args.Authority {
		return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation repository or provider authority changed")
	}
	exists, err := s.store.Exists(ctx, args.OrganizationSlug, args.ProjectSlug, args.PipelineSlug)
	if err != nil {
		return err
	}
	if exists {
		return ErrPipelineProposalAuthorityDenied.Detail("pipeline already exists; creation cannot overwrite it")
	}
	return nil
}

func (s *CreationService) CompleteProposal(ctx context.Context, completed pipelinegit.ProposalCompletion) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		row, err := s.repo.Get(ctx, tx, completed.OrganizationSlug, completed.ProjectSlug, completed.CreationSlug)
		if err != nil {
			return err
		}
		args, err := creationRequest(row)
		if err != nil {
			return err
		}
		if args.MemberSlug != completed.MemberSlug || args.PipelineSlug != completed.PipelineSlug || args.DraftSpecHash != completed.DraftSpecHash || args.Authority != completed.Authority || args.BranchName != completed.BranchName || row.PublishAttemptID != completed.PublishAttemptID {
			return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation completion does not match the queued proposal")
		}
		result := pipelinegit.ProposalResult{Provider: completed.Provider, ProposalID: completed.ProposalID, ProposalURL: completed.ProposalURL, ProposalState: completed.ProposalState, BranchName: completed.BranchName, BranchHeadSHA: completed.BranchHeadSHA}
		if err := result.ValidatePublication(args.Authority, args.BranchName); err != nil {
			return err
		}
		if row.Status == "proposal_open" || row.Status == "branch_pushed" {
			var recorded pipelinegit.ProposalResult
			if err := json.Unmarshal(row.Result, &recorded); err != nil {
				return xerror.Unexpected().With(err)
			}
			if recorded != result {
				return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation already recorded a different provider result")
			}
			return nil
		}
		row.Result, err = json.Marshal(result)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		row.Status, row.FailureCode = "proposal_open", ""
		if completed.Provider == "git" {
			row.Status = "branch_pushed"
		}
		updated, err := s.repo.Update(ctx, tx, completed.OrganizationSlug, completed.ProjectSlug, row)
		if err != nil {
			return err
		}
		if !updated {
			return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation completed concurrently; its recorded result was kept")
		}
		return nil
	})
}

func (s *CreationService) FailProposal(ctx context.Context, failed pipelinegit.ProposalFailure) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		row, err := s.repo.Get(ctx, tx, failed.OrganizationSlug, failed.ProjectSlug, failed.CreationSlug)
		if err != nil {
			return err
		}
		if row.MemberSlug != failed.MemberSlug || row.PipelineSlug != failed.PipelineSlug || row.PublishAttemptID != failed.PublishAttemptID {
			return ErrPipelineProposalAuthorityDenied.Detail("pipeline creation failure does not match its queued attempt")
		}
		row.Status, row.FailureCode = "publish_failed", failed.FailureCode
		_, err = s.repo.Update(ctx, tx, failed.OrganizationSlug, failed.ProjectSlug, row)
		return err
	})
}

func creationRequest(row *pipelinecreations.Row) (jobs.PipelineGitPublishProposalArgs, error) {
	var args jobs.PipelineGitPublishProposalArgs
	if err := json.Unmarshal(row.Request, &args); err != nil {
		return args, xerror.Unexpected().With(err)
	}
	if args.CreationSlug != row.Slug || args.PipelineSlug != row.PipelineSlug || args.MemberSlug != row.MemberSlug || args.PublishAttemptID != row.PublishAttemptID {
		return args, xerror.Unexpected().Detail("stored pipeline creation identity is inconsistent")
	}
	return args, nil
}

func creationDetail(project slug.Slug, row *pipelinecreations.Row) (*CreationProposal, error) {
	args, err := creationRequest(row)
	if err != nil {
		return nil, err
	}
	result := &CreationProposal{Slug: row.Slug, ProjectSlug: project, PipelineSlug: row.PipelineSlug, MemberSlug: row.MemberSlug, SourcePath: args.Path, SpecHash: args.DraftSpecHash, Status: row.Status, FailureCode: row.FailureCode}
	spec, err := pipelinespec.Load(args.Path, args.Content)
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("stored pipeline creation definition is invalid")
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if spec.Metadata.Slug != row.PipelineSlug || fmt.Sprintf("%x", sha256.Sum256(encoded)) != args.DraftSpecHash {
		return nil, xerror.Unexpected().Detail("stored pipeline creation definition does not match its recorded identity")
	}
	result.Spec = spec
	if len(row.Result) > 0 {
		var proposal pipelinegit.ProposalResult
		if err := json.Unmarshal(row.Result, &proposal); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		result.Proposal = &proposal
	}
	return result, nil
}
