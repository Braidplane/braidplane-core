package odcscontractsvc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
)

var ErrArchived = xerror.New(xerror.KindAlreadyExists, "ODCS_CONTRACT_ARCHIVED", "cannot add revisions to an archived contract")

type Service interface {
	InspectRevision(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*Inspection, error)
	PreviewRevisionImpact(context.Context, slug.Slug, slug.Slug, slug.Slug, RevisionImpactRequest) (*RevisionImpact, error)
	CreateContract(context.Context, slug.Slug, slug.Slug, model.NewODCSContract) (*model.ODCSContract, error)
	GetContract(context.Context, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSContract, error)
	UpdateContract(context.Context, slug.Slug, slug.Slug, slug.Slug, model.UpdateODCSContract) (*model.ODCSContract, error)
	ListContracts(context.Context, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.ODCSContract], error)
	CreateRevision(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, json.RawMessage) (*model.ODCSRevision, error)
	GetRevision(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error)
	ValidateRevision(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error)
	ListRevisions(context.Context, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.ODCSRevision], error)
}

type projectRepository interface {
	FindBySlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*projects.Project, error)
}

type service struct {
	txm      postgres.TransactionManager
	repo     odcscontracts.Repository
	projects projectRepository
	plans    compiledPlanRepository
}

type compiledPlanRepository interface {
	Find(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*compiledplans.Snapshot, error)
}

func New(txm postgres.TransactionManager, repo odcscontracts.Repository, projects projectRepository, plans compiledPlanRepository) Service {
	if txm == nil || repo == nil || projects == nil || plans == nil {
		panic("ODCS catalog requires transaction manager, catalog, project and compiled plan repositories")
	}
	return &service{txm: txm, repo: repo, projects: projects, plans: plans}
}

func (s *service) CreateContract(ctx context.Context, org, project slug.Slug, input model.NewODCSContract) (*model.ODCSContract, error) {
	if err := validate.Default().Struct(input); err != nil {
		return nil, xerror.ValidationFailed()
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.ODCSID) == "" {
		return nil, xerror.ValidationFailed()
	}
	provided := input.Slug != ""
	base, err := input.Slug.ValidateOrGenerate(input.Name)
	if err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	input.Slug = base
	var result *model.ODCSContract
	err = s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		result, err = s.repo.CreateContract(ctx, db, org, project, input)
		return err
	})
	if !provided && xerror.Is(err, odcscontracts.ErrConflict) {
		input.Slug, err = slug.WithRandomSuffix(base, 6)
		if err != nil {
			return nil, err
		}
		err = s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
			var createErr error
			result, createErr = s.repo.CreateContract(ctx, db, org, project, input)
			return createErr
		})
	}
	return result, err
}

func (s *service) GetContract(ctx context.Context, org, project, identity slug.Slug) (*model.ODCSContract, error) {
	var result *model.ODCSContract
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		result, err = s.repo.GetContract(ctx, db, org, project, identity)
		return err
	})
	return result, err
}

func (s *service) UpdateContract(ctx context.Context, org, project, identity slug.Slug, input model.UpdateODCSContract) (*model.ODCSContract, error) {
	if err := validate.Default().Struct(input); err != nil {
		return nil, xerror.ValidationFailed()
	}
	if strings.TrimSpace(input.Name) == "" {
		return nil, xerror.ValidationFailed()
	}
	var result *model.ODCSContract
	err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		// Revision creation takes the same lock so archival cannot race past it.
		if _, err := s.repo.LockContract(ctx, db, org, project, identity); err != nil {
			return err
		}
		var err error
		result, err = s.repo.UpdateContract(ctx, db, org, project, identity, input)
		return err
	})
	return result, err
}

func (s *service) CreateRevision(ctx context.Context, org, project, identity, revisionSlug slug.Slug, document json.RawMessage) (*model.ODCSRevision, error) {
	if len(document) > odcs.MaxDocumentBytes {
		return nil, odcs.ErrDocument.WithPublicDetail("document exceeds 1 MiB")
	}
	var result *model.ODCSRevision
	err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		contract, err := s.repo.LockContract(ctx, db, org, project, identity)
		if err != nil {
			return err
		}
		if contract.Status == "archived" {
			return ErrArchived
		}
		revision, err := odcs.Record(document, contract.ODCSID)
		if err != nil {
			return err
		}
		base, err := revisionSlug.ValidateOrGenerate("revision-" + revision.DocumentVersion)
		if err != nil {
			return xerror.ValidationFailed().With(err)
		}
		if revisionSlug == "" {
			if _, findErr := s.repo.GetRevision(ctx, db, org, project, identity, base); findErr == nil {
				base, err = slug.WithRandomSuffix(base, 6)
				if err != nil {
					return err
				}
			} else if !xerror.Is(findErr, odcscontracts.ErrNotFound) {
				return findErr
			}
		}
		revision.Slug = base
		result, err = s.repo.CreateRevision(ctx, db, org, project, identity, *revision)
		return err
	})
	return result, err
}

func (s *service) GetRevision(ctx context.Context, org, project, identity, revisionSlug slug.Slug) (*model.ODCSRevision, error) {
	var result *model.ODCSRevision
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		result, err = s.repo.GetRevision(ctx, db, org, project, identity, revisionSlug)
		return err
	})
	return result, err
}

func (s *service) ValidateRevision(ctx context.Context, org, project, identity, revisionSlug slug.Slug, objectID string) (*odcs.ConversionResult, error) {
	revision, err := s.GetRevision(ctx, org, project, identity, revisionSlug)
	if err != nil {
		return nil, err
	}
	result, err := odcs.Convert(revision.Document, objectID)
	if err != nil {
		return nil, err
	}
	if result.Digest != "" && (result.Digest != revision.Digest || revision.ODCSVersion != odcs.PinnedVersion) {
		return nil, xerror.Unexpected().Detail("ODCS revision digest or version does not match its immutable document")
	}
	return result, nil
}

func (s *service) ListContracts(ctx context.Context, org, project slug.Slug, req model.ListRequest) (model.ListResponse[model.ODCSContract], error) {
	req.SortBy = model.ListSortCreatedAt
	normalized, query := model.PrepareListQuery(req, model.ListSortCreatedAt)
	var items []model.ODCSContract
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if _, err := s.projects.FindBySlug(ctx, db, org, project); err != nil {
			return err
		}
		var err error
		items, err = s.repo.ListContracts(ctx, db, org, project, query)
		return err
	})
	if err != nil {
		return model.ListResponse[model.ODCSContract]{}, err
	}
	items, more := model.FinalizePage(items, normalized)
	next, prev := model.BuildBoundaryCursors(items, normalized, more, func(item model.ODCSContract, _ string) (string, string) {
		return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
	})
	return model.ListResponse[model.ODCSContract]{Items: items, HasMore: more, NextCursor: next, PrevCursor: prev}, nil
}

func (s *service) ListRevisions(ctx context.Context, org, project, identity slug.Slug, req model.ListRequest) (model.ListResponse[model.ODCSRevision], error) {
	req.SortBy = model.ListSortCreatedAt
	normalized, query := model.PrepareListQuery(req, model.ListSortCreatedAt)
	var items []model.ODCSRevision
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if _, err := s.repo.GetContract(ctx, db, org, project, identity); err != nil {
			return err
		}
		var err error
		items, err = s.repo.ListRevisions(ctx, db, org, project, identity, query)
		return err
	})
	if err != nil {
		return model.ListResponse[model.ODCSRevision]{}, err
	}
	items, more := model.FinalizePage(items, normalized)
	next, prev := model.BuildBoundaryCursors(items, normalized, more, func(item model.ODCSRevision, _ string) (string, string) {
		return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
	})
	return model.ListResponse[model.ODCSRevision]{Items: items, HasMore: more, NextCursor: next, PrevCursor: prev}, nil
}
