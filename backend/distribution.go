package backend

import (
	"context"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	corehttp "github.com/Braidplane/braidplane-core/transport/http"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Resources belong to the process and remain valid until Run returns.
type Resources struct {
	Config             *backendconfig.Config
	Pool               *pgxpool.Pool
	TransactionManager postgres.TransactionManager
}

// Services are the existing core graph required by distribution lifecycle composition.
type Services struct {
	Resources
	Queue          queue.Enqueuer
	Cipher         crypto.AES256
	EncryptionKey  []byte
	Authorization  authorization.Service
	Organizations  orgsvc.Service
	Projects       projectssvc.Service
	Members        memberssvc.Service
	Roles          rolessvc.Service
	DataSources    datasourcessvc.Service
	Queries        queriessvc.Service
	DataSourceRefs datasourcerefssvc.Service
	Pipelines      pipelinesvc.Service
	CompiledPlans  *contractplansvc.Service
}

// DistributionFactory binds explicit deployment policy after database initialization.
type DistributionFactory func(Resources) (Distribution, error)

// Distribution owns deployment policy while core owns service implementation and lifecycle.
type AccountPolicy struct {
	RegistrationEnabled bool
	Users               []userssvc.Option
	Members             []memberssvc.Option
	Organizations       []orgsvc.Option
	AccountClosure      []accountclosuresvc.Option
}

type Distribution interface {
	AccountPolicy(workerproducer.EmailProducer) AccountPolicy
	WithOperation(context.Context, slug.Slug, slug.Slug, func(context.Context) error) error
	EncodeOperationError(error) (string, bool)
	DecodeOperationError(string) (error, bool)
	CheckSetup(context.Context, slug.Slug, slug.Slug) error
	CreateRepositorySource(context.Context, projectssvc.RepositorySourceService, slug.Slug, slug.Slug, model.EventActor, *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error)
	ConnectorFactory() connector.NewConnectorFunc
	WrapAdmission(admissionsvc.Service) (admissionsvc.Service, error)
	WrapDataSources(datasources.Repository) (datasources.Repository, error)
	WrapPipelineRuns(pipelineruns.Repository) (pipelineruns.Repository, error)
	NewExtension(context.Context, Services) (Extension, error)
}

// Extension registers distribution routes and runs only entrypoint-selected private commands.
type Extension interface {
	CheckMemberAdmission(context.Context, slug.Slug) error
	Document() (*openapi3.T, error)
	RegisterRoutes(fiber.Router, *corehttp.Server) error
	RunCommand(context.Context, func() error) error
}
