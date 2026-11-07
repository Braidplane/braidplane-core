package backend

import (
	"context"

	apischema "github.com/Braidplane/braidplane-core/api-schema"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	corehttp "github.com/Braidplane/braidplane-core/transport/http"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
)

type communityDistribution struct{ resources Resources }

// NewCommunityDistribution binds uncapped Community policy to the shared runtime.
func NewCommunityDistribution(resources Resources) (Distribution, error) {
	if resources.Config == nil || resources.Pool == nil || resources.TransactionManager == nil {
		return nil, xerror.Unexpected().Detail("Community runtime resources are required")
	}
	return communityDistribution{resources: resources}, nil
}

func (d communityDistribution) ConnectorFactory() connector.NewConnectorFunc {
	base := NewConnectorFactory(d.resources.Config, d.resources.TransactionManager)
	base = connector.NewConnectorWithTransportPolicy(base, d.resources.Config.Security.AllowPlaintextConnectors)
	return connector.NewConnectorWithTracing(base)
}

func (communityDistribution) WrapAdmission(base admissionsvc.Service) (admissionsvc.Service, error) {
	return base, nil
}

func (communityDistribution) WrapPipelineRuns(base pipelineruns.Repository) (pipelineruns.Repository, error) {
	return base, nil
}

func (communityDistribution) WithOperation(ctx context.Context, _, _ slug.Slug, execute func(context.Context) error) error {
	return execute(ctx)
}

func (communityDistribution) EncodeOperationError(error) (string, bool)              { return "", false }
func (communityDistribution) DecodeOperationError(string) (error, bool)              { return nil, false }
func (communityDistribution) CheckSetup(context.Context, slug.Slug, slug.Slug) error { return nil }
func (communityDistribution) CreateRepositorySource(ctx context.Context, sources projectssvc.RepositorySourceService, org, _ slug.Slug, actor model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
	return sources.Create(ctx, org, actor, params)
}

func (communityDistribution) NewExtension(context.Context, Services) (Extension, error) {
	return communityExtension{}, nil
}

type communityExtension struct{}

func (communityExtension) CheckMemberAdmission(context.Context, slug.Slug) error { return nil }
func (communityExtension) Document() (*openapi3.T, error) {
	document, err := openapi3.NewLoader().LoadFromData(apischema.Source())
	if err != nil {
		return nil, xerror.Wrapf("load Community API schema: %w", err)
	}
	return document, nil
}
func (communityExtension) RegisterRoutes(fiber.Router, *corehttp.Server) error { return nil }
func (communityExtension) RunCommand(context.Context, func() error) error {
	return xerror.ValidationFailed().Detail("Community has no distribution service command")
}

func (communityDistribution) WrapDataSources(base datasources.Repository) (datasources.Repository, error) {
	return base, nil
}

func (communityDistribution) AccountPolicy(workerproducer.EmailProducer) AccountPolicy {
	return AccountPolicy{RegistrationEnabled: true}
}
