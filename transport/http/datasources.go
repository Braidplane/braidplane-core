package http

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	snowflakeconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake"
)

type createDataSourcePayload struct {
	Name        string
	Slug        *string
	Type        model.DataSourceType
	Config      any
	Credentials any
	ReadOnly    bool
}

type testDataSourceConnectionPayload struct {
	Type        model.DataSourceType
	Config      any
	Credentials any
}

// CreateDataSource creates a new data source.
func (s *server) CreateDataSource(c fiber.Ctx, slugParam api.SlugParam) error {
	// Parse organization slug
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	// Check authorization
	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceDataSources); err != nil {
		return err
	}
	if err := s.admissionService.CheckDataSourceAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}

	// Parse request body
	if !isJSONObjectPayload(c.BodyRaw()) {
		return apierror.InvalidRequestBody()
	}

	var req api.CreateDataSourceRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	payload, err := createDataSourcePayloadFromRequest(req)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source request: %v", err))
	}

	var dsSlug slug.Slug
	if payload.Slug != nil {
		parsed, err := slug.New(*payload.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
		}
		dsSlug = parsed
	}

	// Extract config as JSON bytes
	configBytes, err := json.Marshal(payload.Config)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid config format: %v", err))
	}

	if err := validateCredentialsByType(payload.Type, payload.Credentials); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid credentials: %v", err))
	}

	credentials, err := credentialsToMap(payload.Credentials)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Failed to process credentials: %v", err))
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.connection_test", false); err != nil {
		return err
	}

	ds, err := s.datasourcesService.Create(
		c.Context(),
		orgSlug,
		s.eventActor(c),
		&datasourcessvc.CreateParams{
			Name:        payload.Name,
			Slug:        dsSlug,
			Type:        payload.Type,
			ConfigJSON:  configBytes,
			Credentials: credentials,
			ReadOnly:    payload.ReadOnly,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(dataSourceToHTTP(ds))
}

// TestDataSourceConnection tests draft data source connection without creating it.
func (s *server) TestDataSourceConnection(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceDataSources); err != nil {
		return err
	}

	if !isJSONObjectPayload(c.BodyRaw()) {
		return apierror.InvalidRequestBody()
	}

	var req api.TestDataSourceConnectionRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	payload, err := testDataSourceConnectionPayloadFromRequest(req)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source request: %v", err))
	}

	configBytes, err := json.Marshal(payload.Config)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid config format: %v", err))
	}

	if err := validateCredentialsByType(payload.Type, payload.Credentials); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid credentials: %v", err))
	}

	credentials, err := credentialsToMap(payload.Credentials)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Failed to process credentials: %v", err))
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.connection_test", false); err != nil {
		return err
	}

	if err := s.datasourcesService.TestConnection(
		c.Context(),
		orgSlug,
		&datasourcessvc.TestConnectionParams{
			Type:        payload.Type,
			ConfigJSON:  configBytes,
			Credentials: credentials,
		},
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// TestStoredDataSourceConnection tests persisted data source config and stored credentials.
func (s *server) TestStoredDataSourceConnection(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.connection_test", false); err != nil {
		return err
	}

	if err := s.datasourcesService.TestExistingConnection(c.Context(), orgSlug, dsSlug); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ListDataSourceBranches lists remote branches for a git-compatible data source.
func (s *server) ListDataSourceBranches(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.remote_metadata", false); err != nil {
		return err
	}

	branches, err := s.datasourcesService.ListBranches(c.Context(), orgSlug, dsSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(api.DataSourceBranchListResponse{
		Branches: dataSourceBranchesToHTTP(branches),
	})
}

func (s *server) ListDataSourceProviderRepositories(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, dsSlug, err := dataSourceSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.remote_metadata", false); err != nil {
		return err
	}
	repositories, err := s.datasourcesService.ListProviderRepositories(c.Context(), orgSlug, dsSlug)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(api.ProviderRepositoryListResponse{Repositories: providerRepositoriesToHTTP(repositories)})
}

func (s *server) SelectDataSourceProviderRepository(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, dsSlug, err := dataSourceSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.connection_test", false); err != nil {
		return err
	}
	if !isJSONObjectPayload(c.BodyRaw()) {
		return apierror.InvalidRequestBody()
	}
	var req api.SelectProviderRepositoryRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	dataSource, err := s.datasourcesService.SelectProviderRepository(
		c.Context(),
		orgSlug,
		dsSlug,
		s.eventActor(c),
		datasourcessvc.SelectProviderRepositoryParams{RepositoryID: req.RepositoryId},
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(dataSourceToHTTP(dataSource))
}

func dataSourceSlugs(slugParam api.SlugParam, dsSlugParam api.DataSourceSlugParam) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}
	return orgSlug, dsSlug, nil
}

func providerRepositoriesToHTTP(repositories []datasourcessvc.ProviderRepository) []api.ProviderRepository {
	result := make([]api.ProviderRepository, 0, len(repositories))
	for _, repository := range repositories {
		mapped := api.ProviderRepository{
			Id:       repository.ID,
			Name:     repository.Name,
			FullName: repository.FullName,
			WebUrl:   repository.WebURL,
		}
		if repository.DefaultBranch != "" {
			mapped.DefaultBranch = &repository.DefaultBranch
		}
		result = append(result, mapped)
	}
	return result
}

// ListDataSources lists all data sources in an organization.
func (s *server) ListDataSources(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListDataSourcesParams,
) error {
	// Parse organization slug
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	// Check authorization
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}

	listReq := toDataSourcesListRequest(params)

	// Call service
	dsList, err := s.datasourcesService.ListByOrg(c.Context(), orgSlug, listReq)
	if err != nil {
		return mapServiceError(err)
	}

	apiList := make([]api.DataSource, 0, len(dsList.Items))
	for i := range dsList.Items {
		apiList = append(apiList, dataSourceToHTTP(&dsList.Items[i]))
	}

	return c.JSON(api.DataSourceListResponse{
		DataSources: apiList,
		HasMore:     dsList.HasMore,
		NextCursor:  dsList.NextCursor,
		PrevCursor:  dsList.PrevCursor,
	})
}

// GetDataSource retrieves a single data source by slug.
func (s *server) GetDataSource(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	// Parse organization slug
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	// Parse data source slug
	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	// Check authorization
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}

	// Call service
	ds, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, dsSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(dataSourceToHTTP(ds))
}

// dataSourceBranchesToHTTP maps service branch records into API response records.
func dataSourceBranchesToHTTP(branches []datasourcessvc.DataSourceBranch) []api.DataSourceBranch {
	apiBranches := make([]api.DataSourceBranch, 0, len(branches))
	for _, branch := range branches {
		apiBranches = append(apiBranches, api.DataSourceBranch{
			Name:      branch.Name,
			IsDefault: branch.IsDefault,
		})
	}
	return apiBranches
}

// UpdateDataSource updates an existing data source.
func (s *server) UpdateDataSource(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	// Parse organization slug
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	// Parse data source slug
	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	// Check authorization
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}

	// Parse request body
	var req api.UpdateDataSourceRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	if req.IsActive != nil {
		ds, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, dsSlug)
		if err != nil {
			return mapServiceError(err)
		}
		if ds.Type != model.DataSourceTypeSnowflake {
			return apierror.ValidationFailed("is_active updates are supported only for Snowflake connections")
		}
	}

	var configBytes []byte
	if req.Config != nil {
		configBytes, err = req.Config.MarshalJSON()
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid config format: %v", err))
		}
	}

	params := &datasourcessvc.UpdateParams{
		ConfigJSON:         configBytes,
		ConfigFromCustomer: req.Config != nil,
		ReadOnly:           req.ReadOnly != nil && *req.ReadOnly,
		IsActive:           req.IsActive,
	}
	if req.Name != nil {
		params.Name = *req.Name
	}
	if req.ReadOnly != nil {
		params.ReadOnlySet = true
	}
	if req.Config != nil {
		params.ConfigJSON = configBytes
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.connection_test", false); err != nil {
		return err
	}

	// Call service
	ds, err := s.datasourcesService.Update(
		c.Context(),
		orgSlug,
		dsSlug,
		s.eventActor(c),
		params,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(dataSourceToHTTP(ds))
}

// UpdateDataSourceCredentials updates credentials for an existing data source.
func (s *server) UpdateDataSourceCredentials(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}

	var req api.UpdateDataSourceCredentialsRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	current, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, dsSlug)
	if err != nil {
		return mapServiceError(err)
	}

	if err := validateCredentialsByType(current.Type, req.Credentials); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid credentials: %v", err))
	}

	credentials, err := credentialsToMap(req.Credentials)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Failed to process credentials: %v", err))
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "credential.mutation", true); err != nil {
		return err
	}

	updated, err := s.datasourcesService.UpdateCredentials(
		c.Context(),
		orgSlug,
		dsSlug,
		s.eventActor(c),
		&datasourcessvc.UpdateCredentialsParams{Credentials: credentials},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(dataSourceToHTTP(updated))
}

// DeleteDataSource deletes a data source.
func (s *server) DeleteDataSource(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	// Parse organization slug
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	// Parse data source slug
	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	// Check authorization
	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceDataSources); err != nil {
		return err
	}

	if err := s.datasourcesService.Delete(c.Context(), orgSlug, dsSlug, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// RestoreDataSource restores a soft-deleted data source.
func (s *server) RestoreDataSource(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Restore, model.ResourceDataSources); err != nil {
		return err
	}
	if err := s.admissionService.CheckDataSourceAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}

	if err := s.datasourcesService.Restore(c.Context(), orgSlug, dsSlug, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// IntrospectDataSourceSchema introspects database schema (not yet implemented).
func (s *server) IntrospectDataSourceSchema(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	schema, err := s.datasourcesService.IntrospectSchema(c.Context(), orgSlug, dsSlug)
	if err != nil {
		return mapServiceError(err)
	}

	resp, err := schemaResultToHTTP(schema)
	if err != nil {
		slog.Error("failed to map schema result", "error", err)
		return apierror.ValidationFailed("Invalid data source type")
	}

	return c.JSON(resp)
}

// RefreshDataSourceSchema queues cached schema refresh/import work.
func (s *server) RefreshDataSourceSchema(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "connector.schema_import", false); err != nil {
		return err
	}

	result, err := s.datasourcesService.RefreshSchema(
		c.Context(),
		orgSlug,
		dsSlug,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusAccepted).JSON(schemaImportResultToHTTP(result))
}

func createDataSourcePayloadFromRequest(
	req api.CreateDataSourceRequest,
) (createDataSourcePayload, error) {
	value, err := req.ValueByDiscriminator()
	if err != nil {
		return createDataSourcePayload{}, err
	}

	switch v := value.(type) {
	case api.CreatePostgresDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypePostgres,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateMySQLDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeMySQL,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateBigQueryDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeBigQuery,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateSnowflakeDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeSnowflake,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateHTTPDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeHTTP,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateMongoDBDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeMongoDB,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateHubSpotDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeHubSpot,
			Config:      v.Config,
			Credentials: v.Credentials,
			// HubSpot defaults to read-only because its saved operations can dispatch mutating upserts.
			ReadOnly: v.ReadOnly == nil || *v.ReadOnly,
		}, nil
	case api.CreateFTPDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeFTP,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateGitDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeGit,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateGitHubDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeGithub,
			Config:      v.Config,
			Credentials: map[string]any{},
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateGitLabDataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeGitlab,
			Config:      v.Config,
			Credentials: map[string]any{},
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	case api.CreateS3DataSourceRequest:
		return createDataSourcePayload{
			Name:        v.Name,
			Slug:        apiSlugPtrToStringPtr(v.Slug),
			Type:        model.DataSourceTypeS3,
			Config:      v.Config,
			Credentials: v.Credentials,
			ReadOnly:    v.ReadOnly != nil && *v.ReadOnly,
		}, nil
	default:
		return createDataSourcePayload{}, xerror.InvalidInput(
			xerror.DataSource,
		).Detail(
			"unknown data source request variant",
		)
	}
}

func testDataSourceConnectionPayloadFromRequest(
	req api.TestDataSourceConnectionRequest,
) (testDataSourceConnectionPayload, error) {
	value, err := req.ValueByDiscriminator()
	if err != nil {
		return testDataSourceConnectionPayload{}, err
	}

	switch v := value.(type) {
	case api.TestPostgresDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypePostgres,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestMySQLDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeMySQL,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestBigQueryDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeBigQuery,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestSnowflakeDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeSnowflake,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestHTTPDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeHTTP,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestMongoDBDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeMongoDB,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestHubSpotDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeHubSpot,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestFTPDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeFTP,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestGitDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeGit,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	case api.TestGitHubDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeGithub,
			Config:      v.Config,
			Credentials: map[string]any{},
		}, nil
	case api.TestGitLabDataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeGitlab,
			Config:      v.Config,
			Credentials: map[string]any{},
		}, nil
	case api.TestS3DataSourceConnectionRequest:
		return testDataSourceConnectionPayload{
			Type:        model.DataSourceTypeS3,
			Config:      v.Config,
			Credentials: v.Credentials,
		}, nil
	default:
		return testDataSourceConnectionPayload{}, xerror.InvalidInput(
			xerror.DataSource,
		).Detail(
			"unknown data source request variant",
		)
	}
}

func apiSlugPtrToStringPtr(value *api.Slug) *string {
	if value == nil {
		return nil
	}
	return value
}

func isJSONObjectPayload(body []byte) bool {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload != nil
}

// validateCredentialsByType performs type-specific validation on the credentials payload.
// There goes validations that couldn't be done on the API schema level, such as conditional
// required fields based on data
// source type.
func validateCredentialsByType(dsType model.DataSourceType, creds any) error {
	if dsType == model.DataSourceTypeSnowflake {
		credentialsJSON, err := json.Marshal(creds)
		if err != nil {
			return xerror.InvalidInput(xerror.DataSource).Detail("invalid Snowflake credentials")
		}
		var credentials snowflakeconnector.Credentials
		if err := json.Unmarshal(credentialsJSON, &credentials); err != nil {
			return xerror.InvalidInput(xerror.DataSource).Detail("invalid Snowflake credentials")
		}
		return snowflakeconnector.ValidateCredentials(credentials)
	}

	if dsType != model.DataSourceTypeHTTP {
		return nil
	}

	httpCreds, err := httpCredentialsFromPayload(creds)
	if err != nil {
		return err
	}

	if httpCreds.Oauth != nil && (httpCreds.SecretHeaders != nil || httpCreds.ApiKey != nil || httpCreds.ApiKeyHeader != nil || httpCreds.BearerToken != nil || httpCreds.Username != nil || httpCreds.Password != nil) {
		return xerror.InvalidInput(xerror.DataSource).Detail("HTTP OAuth settings cannot be combined with other credentials")
	}

	headers := map[string]string(nil)
	if httpCreds.SecretHeaders != nil {
		headers = *httpCreds.SecretHeaders
	}
	return httpconnector.ValidateCredentials(httpconnector.Credentials{
		SecretHeaders: headers,
		APIKey:        optionalStringValue(httpCreds.ApiKey),
		APIKeyHeader:  optionalStringValue((*string)(httpCreds.ApiKeyHeader)),
		BearerToken:   optionalStringValue(httpCreds.BearerToken),
		Username:      optionalStringValue(httpCreds.Username),
		Password:      optionalStringValue(httpCreds.Password),
	})
}

func httpCredentialsFromPayload(creds any) (api.HTTPCredentials, error) {
	switch v := creds.(type) {
	case api.HTTPCredentials:
		return v, nil
	case api.DataSourceCredentials:
		httpCreds, err := v.AsHTTPCredentials()
		if err != nil {
			return api.HTTPCredentials{}, xerror.InvalidInput(xerror.DataSource).With(err)
		}
		return httpCreds, nil
	default:
		return api.HTTPCredentials{}, xerror.InvalidInput(
			xerror.DataSource,
		).Detail(
			"invalid http credentials",
		)
	}
}

func credentialsToMap(creds any) (map[string]any, error) {
	credentialsBytes, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}

	var credentials map[string]any
	if err := json.Unmarshal(credentialsBytes, &credentials); err != nil {
		return nil, err
	}

	return credentials, nil
}

// dataSourceToHTTP converts a service-layer DataSource to an API response.
func dataSourceToHTTP(ds *datasourcessvc.DataSource) api.DataSource {
	resp := api.DataSource{
		ConnectionRevision:           &ds.ConnectionRevision,
		OrganizationGitlabConnection: ds.OrganizationGitLabConnection,
		Slug:                         ds.Slug.String(),
		Name:                         ds.Name,
		Type:                         api.DataSourceType(ds.Type.String()),
		IsActive:                     ds.IsActive,
		ReadOnly:                     ds.ReadOnly,
		LastTestedAt:                 ds.LastTestedAt,
		TestError:                    ds.TestError,
		CreatedAt:                    ds.CreatedAt,
		UpdatedAt:                    ds.UpdatedAt,
		SchemaStatus:                 dataSourceSchemaStatusToHTTP(ds.SchemaImportStatus, ds.SchemaCached),
	}
	if ds.Credential != nil {
		credential := credentialMetadataToHTTP(*ds.Credential)
		resp.Credential = &credential
	}
	resp.SchemaDiagnostic = schemaImportDiagnosticToHTTP(ds.SchemaImportDiagnostic)
	// Set the polymorphic Config field from raw message bytes
	if err := resp.Config.UnmarshalJSON(ds.Config); err != nil {
		slog.Error("failed to unmarshal data source config", "error", err)
	}
	schema := ds.Schema
	if schema == nil {
		empty, err := emptySchemaResponseForDataSourceType(ds.Type)
		if err != nil {
			slog.Error("failed to map empty data source schema", "error", err)
			if resp.SchemaStatus != api.DataSourceSchemaStatusFailed {
				resp.SchemaStatus = api.DataSourceSchemaStatusFailed
			}
			resp.SchemaDiagnostic = schemaResponseMappingDiagnosticToHTTP()
			return resp
		}
		resp.Schema = empty
		return resp
	}

	mapped, err := schemaResultToHTTP(schema)
	if err != nil {
		slog.Error("failed to map cached data source schema", "error", err)
		if resp.SchemaStatus != api.DataSourceSchemaStatusFailed {
			resp.SchemaStatus = api.DataSourceSchemaStatusFailed
		}
		resp.SchemaDiagnostic = schemaResponseMappingDiagnosticToHTTP()
		return resp
	}
	resp.Schema = mapped
	return resp
}

// schemaImportResultToHTTP maps queued import state returned by the service to the refresh API response.
func schemaImportResultToHTTP(result *datasourcessvc.SchemaImportResult) api.DataSourceSchemaImportResponse {
	if result == nil {
		return api.DataSourceSchemaImportResponse{
			SchemaStatus: api.DataSourceSchemaStatusNotImported,
			Enqueued:     false,
		}
	}

	resp := api.DataSourceSchemaImportResponse{
		DataSourceSlug:   result.DataSourceSlug.String(),
		SchemaStatus:     dataSourceSchemaStatusToHTTP(result.Status, false),
		SchemaDiagnostic: schemaImportDiagnosticToHTTP(result.Diagnostic),
		StartedAt:        result.StartedAt,
		FinishedAt:       result.FinishedAt,
		Enqueued:         result.Enqueued,
	}
	if result.Job != nil {
		resp.Job = &api.DataSourceSchemaImportJob{
			Kind:  result.Job.Kind,
			Queue: result.Job.Queue,
			State: api.DataSourceSchemaImportJobState(result.Job.State),
		}
	}
	return resp
}

// dataSourceSchemaStatusToHTTP maps persisted import status, using schemaCached only for unknown legacy states.
func dataSourceSchemaStatusToHTTP(
	status model.DataSourceSchemaImportStatus,
	schemaCached bool,
) api.DataSourceSchemaStatus {
	switch status {
	case model.DataSourceSchemaImportStatusReady:
		return api.DataSourceSchemaStatusReady
	case model.DataSourceSchemaImportStatusImporting:
		return api.DataSourceSchemaStatusImporting
	case model.DataSourceSchemaImportStatusFailed:
		return api.DataSourceSchemaStatusFailed
	case model.DataSourceSchemaImportStatusNotImported:
		return api.DataSourceSchemaStatusNotImported
	default:
		if schemaCached {
			return api.DataSourceSchemaStatusReady
		}
		return api.DataSourceSchemaStatusNotImported
	}
}

// schemaImportDiagnosticToHTTP maps a sanitized service diagnostic into the API shape.
func schemaImportDiagnosticToHTTP(
	diagnostic *datasourcessvc.SchemaImportDiagnostic,
) *api.DataSourceSchemaDiagnostic {
	if diagnostic == nil {
		return nil
	}
	return &api.DataSourceSchemaDiagnostic{
		Code:    diagnostic.Code,
		Message: diagnostic.Message,
	}
}

// schemaResponseMappingDiagnosticToHTTP reports local cached-schema rendering failures safely to clients.
func schemaResponseMappingDiagnosticToHTTP() *api.DataSourceSchemaDiagnostic {
	return &api.DataSourceSchemaDiagnostic{
		Code:    "SCHEMA_RESPONSE_MAPPING_FAILED",
		Message: "cached schema could not be rendered",
	}
}

// emptySchemaResponseForDataSourceType builds a non-provider placeholder schema for DataSource responses.
func emptySchemaResponseForDataSourceType(dsType model.DataSourceType) (api.IntrospectSchemaResponse, error) {
	var resp api.IntrospectSchemaResponse

	switch dsType {
	case model.DataSourceTypePostgres, model.DataSourceTypeMySQL, model.DataSourceTypeBigQuery:
		return resp, resp.FromSQLSchemaResponse(api.SQLSchemaResponse{
			Tables: []api.TableInfo{},
			Type:   api.SQLSchemaResponseType(dsType.String()),
		})
	case model.DataSourceTypeHTTP:
		return resp, resp.FromHTTPSchemaResponse(api.HTTPSchemaResponse{
			Operations: []api.OperationInfo{},
		})
	case model.DataSourceTypeHubSpot:
		return resp, resp.FromObjectAPISchemaResponse(api.ObjectAPISchemaResponse{
			Objects: []api.ObjectAPIObjectInfo{},
			Type:    dsType.String(),
		})
	case model.DataSourceTypeMongoDB:
		return resp, resp.FromDocumentSchemaResponse(api.DocumentSchemaResponse{
			Collections: []api.CollectionInfo{},
			Type:        api.DocumentSchemaResponseType(dsType.String()),
		})
	case model.DataSourceTypeFTP,
		model.DataSourceTypeGit,
		model.DataSourceTypeGithub,
		model.DataSourceTypeGitlab,
		model.DataSourceTypeS3:
		return resp, resp.FromFileSchemaResponse(api.FileSchemaResponse{
			Files: []api.FileInfo{},
			Type:  api.FileSchemaResponseType(dsType.String()),
		})
	default:
		return resp, xerror.Unexpected().Detailf("unsupported data source type for empty schema: %s", dsType)
	}
}

func schemaResultToHTTP(result datasourcessvc.SchemaResult) (api.IntrospectSchemaResponse, error) {
	var resp api.IntrospectSchemaResponse

	switch schema := result.(type) {
	case datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]:
		tables := make([]api.TableInfo, 0, len(schema.Payload.Tables))
		for _, table := range schema.Payload.Tables {
			columns := make([]api.ColumnInfo, 0, len(table.Columns))
			for _, column := range table.Columns {
				columns = append(columns, api.ColumnInfo{
					Name: column.Name,
					Type: column.Type,
				})
			}

			tables = append(tables, api.TableInfo{
				Name:    table.Name,
				Columns: columns,
			})
		}

		if err := resp.FromSQLSchemaResponse(api.SQLSchemaResponse{
			Tables: tables,
			Type:   api.SQLSchemaResponseType(schema.Type),
		}); err != nil {
			return resp, err
		}

	case datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]:
		operations := make([]api.OperationInfo, 0, len(schema.Payload.Operations))
		for _, operation := range schema.Payload.Operations {
			var description *string
			if operation.Description != "" {
				value := operation.Description
				description = &value
			}

			var requestBody *api.OperationRequestBodyInfo
			if len(operation.RequestBody) > 0 {
				decoded := api.OperationRequestBodyInfo{}
				if err := json.Unmarshal(operation.RequestBody, &decoded); err != nil {
					return resp, xerror.Unexpected().With(err)
				}
				requestBody = &decoded
			}

			var responses *map[string]api.OperationResponseInfo
			if len(operation.Responses) > 0 {
				decoded := make(map[string]api.OperationResponseInfo)
				if err := json.Unmarshal(operation.Responses, &decoded); err != nil {
					return resp, xerror.Unexpected().With(err)
				}
				responses = &decoded
			}

			var draft *api.HTTPOperationDraft
			if len(operation.Draft) > 0 {
				draft = &api.HTTPOperationDraft{}
				if err := json.Unmarshal(operation.Draft, draft); err != nil {
					return resp, xerror.Unexpected().With(err)
				}
			}
			var diagnostics *[]string
			if len(operation.Diagnostics) > 0 {
				diagnostics = &operation.Diagnostics
			}
			operations = append(operations, api.OperationInfo{
				Draft:       draft,
				Diagnostics: diagnostics,
				Id:          operation.ID,
				Path:        operation.Path,
				Method:      api.OperationInfoMethod(operation.Method),
				Description: description,
				RequestBody: requestBody,
				Responses:   responses,
			})
		}

		if err := resp.FromHTTPSchemaResponse(api.HTTPSchemaResponse{
			Operations: operations,
		}); err != nil {
			return resp, err
		}

	case datasourcessvc.Schema[datasourcessvc.DocumentSchemaPayload]:
		collections := make([]api.CollectionInfo, 0, len(schema.Payload.Collections))
		for _, collection := range schema.Payload.Collections {
			fields := make([]api.FieldInfo, 0, len(collection.Fields))
			for _, field := range collection.Fields {
				fields = append(fields, api.FieldInfo{
					Name: field.Name,
					Type: field.Type,
				})
			}

			collections = append(collections, api.CollectionInfo{
				Name:   collection.Name,
				Fields: fields,
			})
		}

		if err := resp.FromDocumentSchemaResponse(api.DocumentSchemaResponse{
			Collections: collections,
			Type:        api.DocumentSchemaResponseType(schema.Type),
		}); err != nil {
			return resp, err
		}

	case datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]:
		files := make([]api.FileInfo, 0, len(schema.Payload.Files))
		for _, file := range schema.Payload.Files {
			fields := make([]api.FieldInfo, 0, len(file.Fields))
			for _, field := range file.Fields {
				fields = append(fields, api.FieldInfo{
					Name: field.Name,
					Type: field.Type,
				})
			}

			files = append(files, api.FileInfo{
				SizeBytes: file.SizeBytes, Name: file.Name, ResourceId: fileResultTargetPath(file.ResourceID), MimeType: fileResultTargetPath(file.MIMEType),
				Fields: fields,
			})
		}

		var diagnostics *[]api.FileSchemaDiagnostic
		if len(schema.Payload.Diagnostics) > 0 {
			items := make([]api.FileSchemaDiagnostic, 0, len(schema.Payload.Diagnostics))
			for _, diagnostic := range schema.Payload.Diagnostics {
				items = append(items, api.FileSchemaDiagnostic{
					Name:     diagnostic.Name,
					Target:   diagnostic.Target,
					Severity: diagnostic.Severity,
					Code:     diagnostic.Code,
					Message:  diagnostic.Message,
				})
			}
			diagnostics = &items
		}

		if err := resp.FromFileSchemaResponse(api.FileSchemaResponse{
			Diagnostics: diagnostics,
			Files:       files,
			Type:        api.FileSchemaResponseType(schema.Type),
		}); err != nil {
			return resp, err
		}

	case datasourcessvc.Schema[datasourcessvc.ObjectAPISchemaPayload]:
		objects := make([]api.ObjectAPIObjectInfo, 0, len(schema.Payload.Objects))
		for _, object := range schema.Payload.Objects {
			fields := make([]api.ObjectAPIFieldInfo, 0, len(object.Fields))
			for _, field := range object.Fields {
				label := field.Label
				fields = append(fields, api.ObjectAPIFieldInfo{
					Name:             field.Name,
					Label:            optionalString(label),
					Type:             field.Type,
					ProviderMetadata: optionalAnyMap(field.ProviderMetadata),
				})
			}

			operations := make([]api.ObjectAPIOperationInfo, 0, len(object.Operations))
			for _, operation := range object.Operations {
				operations = append(operations, api.ObjectAPIOperationInfo{
					Name:             operation.Name,
					Label:            optionalString(operation.Label),
					ProviderMetadata: optionalAnyMap(operation.ProviderMetadata),
				})
			}

			objects = append(objects, api.ObjectAPIObjectInfo{
				Name:             object.Name,
				Label:            optionalString(object.Label),
				Fields:           fields,
				Operations:       &operations,
				ProviderMetadata: optionalAnyMap(object.ProviderMetadata),
			})
		}

		if err := resp.FromObjectAPISchemaResponse(api.ObjectAPISchemaResponse{
			Objects: objects,
			Type:    schema.Type,
		}); err != nil {
			return resp, err
		}

	default:
		return resp, xerror.Unexpected().With(
			xerror.Unexpected().Detailf("unsupported schema result type: %T", result),
		)
	}

	return resp, nil
}

func optionalAnyMap(value map[string]any) *map[string]any {
	if len(value) == 0 {
		return nil
	}

	copied := make(map[string]any, len(value))
	for key, item := range value {
		copied[key] = item
	}
	return &copied
}

func (s *server) GetSnowflakeOnboarding(c fiber.Ctx, slugParam api.SlugParam, dsSlugParam api.DataSourceSlugParam) error {
	org, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	ds, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid data source slug")
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	onboarding, err := s.datasourcesService.GetSnowflakeOnboarding(c.Context(), org, ds)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(api.SnowflakeOnboarding{IdentityState: api.SnowflakeOnboardingIdentityState(onboarding.IdentityState), Active: onboarding.Active, Issuer: onboarding.Issuer, Subject: onboarding.Subject, Audience: onboarding.Audience, Sql: onboarding.SQL, Diagnostic: onboarding.Diagnostic})
}
