package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (s *server) CreateDataSourceRef(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
) error {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if err != nil {
		return err
	}
	pipelineSlug, err := parsePipelineSlug(pipelineSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceDataSources); err != nil {
		return err
	}

	var req api.NewDataSourceRef
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	newRef, err := newDataSourceRefFromAPI(req)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source ref request: %v", err))
	}
	ref, err := s.dataSourceRefsService.CreateDataSourceRef(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		newRef,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(fiber.StatusCreated).JSON(dataSourceRefToAPI(ref))
}

func (s *server) ListDataSourceRefs(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	params api.ListDataSourceRefsParams,
) error {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	response, err := s.dataSourceRefsService.ListDataSourceRefs(
		c.Context(),
		orgSlug,
		projectSlug,
		listRequestFromDataSourceRefParams(params),
	)
	if err != nil {
		return mapServiceError(err)
	}
	items := make([]api.DataSourceRef, len(response.Items))
	for i := range response.Items {
		items[i] = dataSourceRefToAPI(&response.Items[i])
	}
	return c.JSON(api.DataSourceRefListResponse{
		DataSourceRefs: items,
		HasMore:        response.HasMore,
		NextCursor:     response.NextCursor,
		PrevCursor:     response.PrevCursor,
	})
}

func (s *server) GetDataSourceRef(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) error {
	orgSlug, projectSlug, pipelineSlug, refSlug, err := parseDataSourceRefSlugs(slugParam, projectSlugParam, pipelineSlugParam, dataSourceRefSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	ref, err := s.dataSourceRefsService.GetDataSourceRef(c.Context(), orgSlug, projectSlug, pipelineSlug, refSlug)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(dataSourceRefToAPI(ref))
}

func (s *server) UpdateDataSourceRef(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) error {
	orgSlug, projectSlug, pipelineSlug, refSlug, err := parseDataSourceRefSlugs(slugParam, projectSlugParam, pipelineSlugParam, dataSourceRefSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	var req api.UpdateDataSourceRef
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	update, err := updateDataSourceRefFromAPI(req)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source ref request: %v", err))
	}
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &update.ProtectedEnvironmentConfirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}
	ref, err := s.dataSourceRefsService.UpdateDataSourceRef(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		refSlug,
		update,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(dataSourceRefToAPI(ref))
}

func (s *server) DeleteDataSourceRef(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) error {
	orgSlug, projectSlug, pipelineSlug, refSlug, err := parseDataSourceRefSlugs(slugParam, projectSlugParam, pipelineSlugParam, dataSourceRefSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceDataSources); err != nil {
		return err
	}
	if err := s.dataSourceRefsService.DeleteDataSourceRef(c.Context(), orgSlug, projectSlug, pipelineSlug, refSlug, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) ListDataSourceBindings(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.ListDataSourceBindingsParams,
) error {
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(slugParam, projectSlugParam, environmentSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	response, err := s.dataSourceRefsService.ListEnvironmentBindings(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		listRequestFromDataSourceBindingParams(params),
	)
	if err != nil {
		return mapServiceError(err)
	}
	items := make([]api.DataSourceBinding, len(response.Items))
	for i := range response.Items {
		items[i] = dataSourceBindingToAPI(&response.Items[i])
	}
	return c.JSON(api.DataSourceBindingListResponse{
		DataSourceBindings: items,
		HasMore:            response.HasMore,
		NextCursor:         response.NextCursor,
		PrevCursor:         response.PrevCursor,
	})
}

func (s *server) GetDataSourceBinding(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) error {
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, err := parseBindingSlugs(slugParam, projectSlugParam, pipelineSlugParam, environmentSlugParam, dataSourceRefSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	binding, err := s.dataSourceRefsService.GetEnvironmentBinding(c.Context(), orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(dataSourceBindingToAPI(binding))
}

func (s *server) UpsertDataSourceBinding(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) error {
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, err := parseBindingSlugs(slugParam, projectSlugParam, pipelineSlugParam, environmentSlugParam, dataSourceRefSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	var req api.UpsertDataSourceBinding
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	dsSlug, err := slug.New(req.DataSourceSlug)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}
	binding, err := s.dataSourceRefsService.UpsertEnvironmentBinding(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		environmentSlug,
		refSlug,
		model.UpsertDataSourceBinding{
			DataSourceSlug:                   dsSlug,
			ProtectedEnvironmentConfirmation: confirmation,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(dataSourceBindingToAPI(binding))
}

func (s *server) DeleteDataSourceBinding(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
	params api.DeleteDataSourceBindingParams,
) error {
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, err := parseBindingSlugs(slugParam, projectSlugParam, pipelineSlugParam, environmentSlugParam, dataSourceRefSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceDataSources); err != nil {
		return err
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromParam(params.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}
	if err := s.dataSourceRefsService.DeleteEnvironmentBinding(
		c.Context(),
		orgSlug,
		projectSlug,
		pipelineSlug,
		environmentSlug,
		refSlug,
		confirmation,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func newDataSourceRefFromAPI(req api.NewDataSourceRef) (model.NewDataSourceRef, error) {
	var refSlug slug.Slug
	if req.Slug != nil {
		parsed, err := slug.New(*req.Slug)
		if err != nil {
			return model.NewDataSourceRef{}, err
		}
		refSlug = parsed
	}
	dsType, err := model.DataSourceTypeString(string(req.DataSourceType))
	if err != nil {
		return model.NewDataSourceRef{}, err
	}
	requiredCapabilities, err := dataSourceRefCapabilitiesFromAPI(req.RequiredCapabilities)
	if err != nil {
		return model.NewDataSourceRef{}, err
	}
	return model.NewDataSourceRef{
		Name:                 req.Name,
		Slug:                 refSlug,
		DataSourceType:       dsType,
		RequiredCapabilities: requiredCapabilities,
	}, nil
}

func updateDataSourceRefFromAPI(req api.UpdateDataSourceRef) (model.UpdateDataSourceRef, error) {
	dsType, err := model.DataSourceTypeString(string(req.DataSourceType))
	if err != nil {
		return model.UpdateDataSourceRef{}, err
	}
	requiredCapabilities, err := dataSourceRefCapabilitiesFromAPI(req.RequiredCapabilities)
	if err != nil {
		return model.UpdateDataSourceRef{}, err
	}
	return model.UpdateDataSourceRef{
		Name:                 req.Name,
		DataSourceType:       dsType,
		RequiredCapabilities: requiredCapabilities,
	}, nil
}

func dataSourceRefCapabilitiesFromAPI(values []api.DataSourceRefCapability) ([]model.DataSourceRefCapability, error) {
	if len(values) == 0 {
		return nil, xerror.InvalidInput(xerror.DataSourceRef).Detail(
			"required_capabilities must include at least one capability",
		)
	}
	result := make([]model.DataSourceRefCapability, 0, len(values))
	for _, value := range values {
		parsed, err := model.DataSourceRefCapabilityString(string(value))
		if err != nil || parsed == model.DataSourceRefCapabilityUnknown {
			return nil, xerror.InvalidInput(xerror.DataSourceRef).Detailf(
				"unsupported data source ref capability %q",
				value,
			)
		}
		result = append(result, parsed)
	}
	return result, nil
}

func dataSourceRefCapabilitiesToAPI(values []model.DataSourceRefCapability) []api.DataSourceRefCapability {
	result := make([]api.DataSourceRefCapability, len(values))
	for i, value := range values {
		result[i] = api.DataSourceRefCapability(value.String())
	}
	return result
}

func dataSourceRefToAPI(ref *model.DataSourceRef) api.DataSourceRef {
	if ref == nil {
		return api.DataSourceRef{}
	}
	return api.DataSourceRef{
		PipelineSlug:         ref.PipelineSlug.String(),
		Slug:                 ref.Slug.String(),
		Name:                 ref.Name,
		DataSourceType:       dataSourceTypeToNullableAPI(ref.DataSourceType),
		RequiredCapabilities: dataSourceRefCapabilitiesToAPI(ref.RequiredCapabilities),
		CreatedAt:            ref.CreatedAt,
		UpdatedAt:            ref.UpdatedAt,
	}
}

func dataSourceTypeToNullableAPI(dsType model.DataSourceType) *api.DataSourceType {
	if dsType == model.DataSourceTypeUnknown {
		return nil
	}
	value := api.DataSourceType(dsType.String())
	return &value
}

func dataSourceBindingToAPI(binding *model.DataSourceBinding) api.DataSourceBinding {
	if binding == nil {
		return api.DataSourceBinding{}
	}
	return api.DataSourceBinding{
		PipelineSlug:       binding.PipelineSlug.String(),
		DataSourceRefSlug:  binding.DataSourceRefSlug.String(),
		DataSourceRefName:  binding.DataSourceRefName,
		EnvironmentSlug:    binding.EnvironmentSlug.String(),
		DataSourceSlug:     binding.DataSourceSlug.String(),
		DataSourceName:     binding.DataSourceName,
		DataSourceType:     api.DataSourceType(binding.DataSourceType.String()),
		DataSourceActive:   binding.DataSourceActive,
		DataSourceReadOnly: binding.DataSourceReadOnly,
		BoundAt:            binding.BoundAt,
		UpdatedAt:          binding.UpdatedAt,
	}
}

func listRequestFromDataSourceRefParams(params api.ListDataSourceRefsParams) model.ListRequest {
	return model.ListRequest{
		Limit:  int(ptr.Value(params.Limit)),
		Cursor: ptr.Value(params.Cursor),
	}
}

func listRequestFromDataSourceBindingParams(params api.ListDataSourceBindingsParams) model.ListRequest {
	return model.ListRequest{
		Limit:  int(ptr.Value(params.Limit)),
		Cursor: ptr.Value(params.Cursor),
	}
}

func parseDataSourceRefSlugs(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) (slug.Slug, slug.Slug, slug.Slug, slug.Slug, error) {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if err != nil {
		return "", "", "", "", err
	}
	pipelineSlug, err := parsePipelineSlug(pipelineSlugParam)
	if err != nil {
		return "", "", "", "", err
	}
	refSlug, err := slug.New(dataSourceRefSlugParam)
	if err != nil {
		return "", "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid data source ref slug: %v", err))
	}
	return orgSlug, projectSlug, pipelineSlug, refSlug, nil
}

func parseOrgProjectEnvironmentSlugs(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) (slug.Slug, slug.Slug, slug.Slug, error) {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(slugParam, projectSlugParam)
	if err != nil {
		return "", "", "", err
	}
	environmentSlug, err := slug.New(environmentSlugParam)
	if err != nil {
		return "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
	}
	return orgSlug, projectSlug, environmentSlug, nil
}

func parseBindingSlugs(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	pipelineSlugParam api.PipelineSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	dataSourceRefSlugParam api.DataSourceRefSlugParam,
) (slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, error) {
	orgSlug, projectSlug, environmentSlug, err := parseOrgProjectEnvironmentSlugs(slugParam, projectSlugParam, environmentSlugParam)
	if err != nil {
		return "", "", "", "", "", err
	}
	pipelineSlug, err := parsePipelineSlug(pipelineSlugParam)
	if err != nil {
		return "", "", "", "", "", err
	}
	refSlug, err := slug.New(dataSourceRefSlugParam)
	if err != nil {
		return "", "", "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid data source ref slug: %v", err))
	}
	return orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, nil
}

func parsePipelineSlug(pipelineSlugParam api.PipelineSlugParam) (slug.Slug, error) {
	pipelineSlug, err := slug.New(pipelineSlugParam)
	if err != nil {
		return "", apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}
	return pipelineSlug, nil
}
