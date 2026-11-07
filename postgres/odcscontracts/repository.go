package odcscontracts

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

var (
	ErrNotFound = xerror.New(xerror.KindNotFound, "ODCS_NOT_FOUND", "ODCS catalog resource not found")
	ErrConflict = xerror.New(xerror.KindAlreadyExists, "ODCS_CONFLICT", "ODCS identity, revision, or digest already exists")
)

type Repository interface {
	FindArtifact(context.Context, postgres.DBTX, slug.Slug, slug.Slug, model.ODCSArtifact) (*model.ODCSArtifact, error)
	FindContractByIdentity(context.Context, postgres.DBTX, slug.Slug, slug.Slug, string) (*model.ODCSContract, error)
	FindRevisionByDigest(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*model.ODCSRevision, error)
	CreateArtifact(context.Context, postgres.DBTX, slug.Slug, slug.Slug, model.ODCSArtifact) error

	CreateContract(context.Context, postgres.DBTX, slug.Slug, slug.Slug, model.NewODCSContract) (*model.ODCSContract, error)
	GetContract(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSContract, error)
	LockContract(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSContract, error)
	UpdateContract(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, model.UpdateODCSContract) (*model.ODCSContract, error)
	ListContracts(context.Context, postgres.DBTX, slug.Slug, slug.Slug, model.ListRequest) ([]model.ODCSContract, error)
	CreateRevision(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, model.ODCSRevision) (*model.ODCSRevision, error)
	GetRevision(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.ODCSRevision, error)
	ListRevisions(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) ([]model.ODCSRevision, error)
	ListConsumerSnapshots(context.Context, postgres.DBTX, slug.Slug, slug.Slug) ([]ConsumerSnapshot, error)
}

type ConsumerSnapshot struct {
	Origin, Environment, SpecHash string
	Spec, Pins                    []byte
}

type repository struct{}

func NewRepository() Repository { return &repository{} }
