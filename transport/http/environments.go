package http

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (s *server) ListEnvironments(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListEnvironmentsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	// This endpoint exposes the project environment catalog. Pipeline-read
	// callers must use explicit environment-scoped runtime paths instead.
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	response, err := s.environmentsService.ListEnvironments(
		c.Context(),
		orgSlug,
		toEnvironmentsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	environments := make([]api.Environment, 0, len(response.Items))
	for _, environment := range response.Items {
		environments = append(environments, environmentToHTTP(environment))
	}

	return c.JSON(api.EnvironmentListResponse{
		Environments: environments,
		HasMore:      response.HasMore,
		NextCursor:   response.NextCursor,
		PrevCursor:   response.PrevCursor,
	})
}

func (s *server) CreateEnvironment(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceEnvironments); err != nil {
		return err
	}

	var req api.NewEnvironment
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	var environmentSlug slug.Slug
	if req.Slug != nil {
		parsedSlug, err := slug.New(*req.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
		}
		environmentSlug = parsedSlug
	}
	environmentColor, err := environmentColorFromHTTP(req.Color)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid environment color: %v", err))
	}

	created, err := s.environmentsService.CreateEnvironment(
		c.Context(),
		orgSlug,
		model.NewEnvironment{
			Name:        req.Name,
			Slug:        environmentSlug,
			Description: environmentDescriptionFromHTTP(req.Description),
			Color:       environmentColor,
			Protected:   req.Protected != nil && *req.Protected,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(environmentToHTTP(*created))
}

func (s *server) GetEnvironment(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	environment, err := s.environmentsService.GetEnvironment(
		c.Context(),
		orgSlug,
		environmentSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(environmentToHTTP(*environment))
}

func (s *server) UpdateEnvironment(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceEnvironments); err != nil {
		return err
	}

	var req api.UpdateEnvironment
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if !jsonBodyHasBoolField(c.Body(), "protected") {
		return apierror.ValidationFailed("protected is required")
	}
	environmentColor, err := environmentColorFromHTTP(req.Color)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid environment color: %v", err))
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	environment, err := s.environmentsService.UpdateEnvironment(
		c.Context(),
		orgSlug,
		environmentSlug,
		model.UpdateEnvironment{
			Name:                             req.Name,
			Description:                      environmentDescriptionFromHTTP(req.Description),
			Color:                            environmentColor,
			Protected:                        req.Protected,
			ProtectedEnvironmentConfirmation: confirmation,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(environmentToHTTP(*environment))
}

func jsonBodyHasBoolField(body []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	value, ok := raw[field]
	if !ok {
		return false
	}
	var parsed bool
	return json.Unmarshal(value, &parsed) == nil
}

func (s *server) DeleteEnvironment(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.DeleteEnvironmentParams,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceEnvironments); err != nil {
		return err
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromParam(params.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	if err := s.environmentsService.DeleteEnvironment(
		c.Context(),
		orgSlug,
		environmentSlug,
		model.DeleteEnvironment{
			Reason:                           params.DeleteReason,
			ProtectedEnvironmentConfirmation: confirmation,
		},
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) ListProjectEnvironments(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	params api.ListProjectEnvironmentsParams,
) error {
	orgSlug, projectSlug, err := parseProjectPath(slugParam, projectSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	response, err := s.environmentsService.ListProjectEnvironments(
		c.Context(),
		orgSlug,
		projectSlug,
		toProjectEnvironmentsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	projectEnvironments := make([]api.ProjectEnvironment, 0, len(response.Items))
	for _, projectEnvironment := range response.Items {
		projectEnvironments = append(projectEnvironments, projectEnvironmentToHTTP(projectEnvironment))
	}

	return c.JSON(api.ProjectEnvironmentListResponse{
		ProjectEnvironments: projectEnvironments,
		HasMore:             response.HasMore,
		NextCursor:          response.NextCursor,
		PrevCursor:          response.PrevCursor,
	})
}

func (s *server) EnableProjectEnvironment(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) error {
	orgSlug, projectSlug, environmentSlug, err := parseProjectEnvironmentPath(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceEnvironments); err != nil {
		return err
	}

	var req api.EnableProjectEnvironmentRequest
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&req); err != nil {
			return apierror.InvalidRequestBody()
		}
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	projectEnvironment, err := s.environmentsService.EnableProjectEnvironment(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		model.EnableProjectEnvironment{
			IsDefault:                        req.IsDefault != nil && *req.IsDefault,
			ProtectedEnvironmentConfirmation: confirmation,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(projectEnvironmentToHTTP(*projectEnvironment))
}

func (s *server) DisableProjectEnvironment(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.DisableProjectEnvironmentParams,
) error {
	orgSlug, projectSlug, environmentSlug, err := parseProjectEnvironmentPath(
		slugParam,
		projectSlugParam,
		environmentSlugParam,
	)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceEnvironments); err != nil {
		return err
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromParam(params.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	if err := s.environmentsService.DisableProjectEnvironment(
		c.Context(),
		orgSlug,
		projectSlug,
		environmentSlug,
		confirmation,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) ListEnvironmentRuntimeVariables(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.ListEnvironmentRuntimeVariablesParams,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	response, err := s.environmentsService.ListRuntimeVariables(
		c.Context(),
		orgSlug,
		environmentSlug,
		toEnvironmentRuntimeVariablesListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	variables := make([]api.RuntimeVariable, 0, len(response.Items))
	for _, variable := range response.Items {
		variables = append(variables, runtimeVariableToHTTP(variable))
	}

	return c.JSON(api.RuntimeVariableListResponse{
		RuntimeVariables: variables,
		HasMore:          response.HasMore,
		NextCursor:       response.NextCursor,
		PrevCursor:       response.PrevCursor,
	})
}

func (s *server) GetEnvironmentRuntimeVariable(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	runtimeVariableKeyParam api.RuntimeVariableKeyParam,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	variable, err := s.environmentsService.GetRuntimeVariable(
		c.Context(),
		orgSlug,
		environmentSlug,
		runtimeVariableKeyParam,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(runtimeVariableToHTTP(*variable))
}

func (s *server) UpsertEnvironmentRuntimeVariable(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	runtimeVariableKeyParam api.RuntimeVariableKeyParam,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceEnvironments); err != nil {
		return err
	}

	var req api.UpsertRuntimeVariable
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if req.Value == "" {
		return apierror.ValidationFailed("runtime variable value is required")
	}

	valueType, err := model.RuntimeVariableValueTypeString(string(req.ValueType))
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid runtime variable value type: %v", err))
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	variable, err := s.environmentsService.UpsertRuntimeVariable(
		c.Context(),
		orgSlug,
		environmentSlug,
		model.UpsertRuntimeVariable{
			Key:                              runtimeVariableKeyParam,
			ValueType:                        valueType,
			Value:                            req.Value,
			Description:                      runtimeVariableDescriptionFromHTTP(req.Description),
			ProtectedEnvironmentConfirmation: confirmation,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(runtimeVariableToHTTP(*variable))
}

func (s *server) DeleteEnvironmentRuntimeVariable(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	runtimeVariableKeyParam api.RuntimeVariableKeyParam,
	params api.DeleteEnvironmentRuntimeVariableParams,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceEnvironments); err != nil {
		return err
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromParam(params.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	if err := s.environmentsService.DeleteRuntimeVariable(
		c.Context(),
		orgSlug,
		environmentSlug,
		runtimeVariableKeyParam,
		confirmation,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) ListEnvironmentSecrets(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	params api.ListEnvironmentSecretsParams,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	response, err := s.environmentsService.ListEnvironmentSecrets(
		c.Context(),
		orgSlug,
		environmentSlug,
		toEnvironmentSecretsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	secrets := make([]api.EnvironmentSecret, 0, len(response.Items))
	for _, secret := range response.Items {
		secrets = append(secrets, environmentSecretToHTTP(secret))
	}

	return c.JSON(api.EnvironmentSecretListResponse{
		EnvironmentSecrets: secrets,
		HasMore:            response.HasMore,
		NextCursor:         response.NextCursor,
		PrevCursor:         response.PrevCursor,
	})
}

func (s *server) GetEnvironmentSecret(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	environmentSecretKeyParam api.EnvironmentSecretKeyParam,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}

	secret, err := s.environmentsService.GetEnvironmentSecret(
		c.Context(),
		orgSlug,
		environmentSlug,
		environmentSecretKeyParam,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(environmentSecretToHTTP(*secret))
}

func (s *server) UpsertEnvironmentSecret(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	environmentSecretKeyParam api.EnvironmentSecretKeyParam,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceEnvironments); err != nil {
		return err
	}

	var req api.UpsertEnvironmentSecret
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if req.Value == nil || *req.Value == "" {
		return apierror.ValidationFailed("environment secret value is required")
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromNullableSlug(req.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	secret, err := s.environmentsService.UpsertEnvironmentSecret(
		c.Context(),
		orgSlug,
		environmentSlug,
		model.UpsertEnvironmentSecret{
			Key:                              environmentSecretKeyParam,
			Value:                            *req.Value,
			Description:                      environmentSecretDescriptionFromHTTP(req.Description),
			ProtectedEnvironmentConfirmation: confirmation,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(environmentSecretToHTTP(*secret))
}

func (s *server) DeleteEnvironmentSecret(
	c fiber.Ctx,
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
	environmentSecretKeyParam api.EnvironmentSecretKeyParam,
	params api.DeleteEnvironmentSecretParams,
) error {
	orgSlug, environmentSlug, err := parseEnvironmentPath(slugParam, environmentSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceEnvironments); err != nil {
		return err
	}
	var confirmation *slug.Slug
	if err := protectedConfirmationFromParam(params.ProtectedEnvironmentConfirmation, &confirmation); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid protected environment confirmation: %v", err))
	}

	if err := s.environmentsService.DeleteEnvironmentSecret(
		c.Context(),
		orgSlug,
		environmentSlug,
		environmentSecretKeyParam,
		confirmation,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func environmentToHTTP(environment model.Environment) api.Environment {
	return api.Environment{
		Slug:         environment.Slug.String(),
		Name:         environment.Name,
		Description:  environmentDescriptionToHTTP(environment.Description),
		Color:        environmentColorToHTTP(environment.Color),
		Protected:    environment.Protected,
		DeletedAt:    nullableTimestampToHTTP(environment.DeletedAt),
		DeleteReason: environment.DeleteReason,
		CreatedAt:    environment.CreatedAt,
		UpdatedAt:    environment.UpdatedAt,
	}
}

func projectEnvironmentToHTTP(projectEnvironment model.ProjectEnvironment) api.ProjectEnvironment {
	return api.ProjectEnvironment{
		EnvironmentSlug:        projectEnvironment.Environment.Slug.String(),
		EnvironmentName:        projectEnvironment.Environment.Name,
		EnvironmentDescription: environmentDescriptionToHTTP(projectEnvironment.Environment.Description),
		EnvironmentColor:       environmentColorToHTTP(projectEnvironment.Environment.Color),
		EnvironmentProtected:   projectEnvironment.Environment.Protected,
		IsDefault:              projectEnvironment.IsDefault,
		EnabledAt:              projectEnvironment.EnabledAt,
		UpdatedAt:              projectEnvironment.UpdatedAt,
	}
}

func runtimeVariableToHTTP(variable model.RuntimeVariable) api.RuntimeVariable {
	return api.RuntimeVariable{
		EnvironmentSlug: variable.EnvironmentSlug.String(),
		Key:             variable.Key,
		ValueType:       api.RuntimeVariableValueType(variable.ValueType.String()),
		Value:           variable.Value,
		Description:     runtimeVariableDescriptionToHTTP(variable.Description),
		CreatedAt:       variable.CreatedAt,
		UpdatedAt:       variable.UpdatedAt,
	}
}

func environmentSecretToHTTP(secret model.EnvironmentSecret) api.EnvironmentSecret {
	return api.EnvironmentSecret{
		EnvironmentSlug: secret.EnvironmentSlug.String(),
		Key:             secret.Key,
		Description:     environmentSecretDescriptionToHTTP(secret.Description),
		CreatedAt:       secret.CreatedAt,
		UpdatedAt:       secret.UpdatedAt,
	}
}

// environmentColorFromHTTP translates API palette values into the domain enum.
func environmentColorFromHTTP(value api.EnvironmentColor) (model.EnvironmentColor, error) {
	color, err := model.EnvironmentColorString(string(value))
	if err != nil || color == model.EnvironmentColorUnknown {
		return model.EnvironmentColorUnknown, xerror.InvalidInput(xerror.Environment).
			Detailf("unsupported environment color %q", value)
	}
	return color, nil
}

// environmentColorToHTTP serializes domain palette values and neutralizes invalid internal data.
func environmentColorToHTTP(value model.EnvironmentColor) api.EnvironmentColor {
	if !value.IsAEnvironmentColor() || value == model.EnvironmentColorUnknown {
		return api.EnvironmentColorGray
	}
	return api.EnvironmentColor(value.String())
}

func environmentDescriptionFromHTTP(value *api.EnvironmentDescription) *string {
	if value == nil {
		return nil
	}
	return value
}

func environmentDescriptionToHTTP(value *string) *api.EnvironmentDescription {
	if value == nil {
		return nil
	}
	return value
}

func runtimeVariableDescriptionFromHTTP(value *api.RuntimeVariableDescription) *string {
	if value == nil {
		return nil
	}
	return value
}

func runtimeVariableDescriptionToHTTP(value *string) *api.RuntimeVariableDescription {
	if value == nil {
		return nil
	}
	return value
}

func environmentSecretDescriptionFromHTTP(value *api.EnvironmentSecretDescription) *string {
	if value == nil {
		return nil
	}
	return value
}

func environmentSecretDescriptionToHTTP(value *string) *api.EnvironmentSecretDescription {
	if value == nil {
		return nil
	}
	return value
}

func protectedConfirmationFromNullableSlug(value *api.NullableSlug, target **slug.Slug) error {
	if value == nil {
		return nil
	}
	parsed, err := slug.New(*value)
	if err != nil {
		return err
	}
	*target = &parsed
	return nil
}

func protectedConfirmationFromParam(value *api.ProtectedEnvironmentConfirmationParam, target **slug.Slug) error {
	if value == nil {
		return nil
	}
	parsed, err := slug.New(*value)
	if err != nil {
		return err
	}
	*target = &parsed
	return nil
}

func nullableTimestampToHTTP(value *time.Time) *api.NullableTimestamp {
	if value == nil {
		return nil
	}
	return value
}

func parseEnvironmentPath(
	slugParam api.SlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	environmentSlug, err := slug.New(environmentSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
	}

	return orgSlug, environmentSlug, nil
}

func parseProjectEnvironmentPath(
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	environmentSlugParam api.EnvironmentSlugParam,
) (slug.Slug, slug.Slug, slug.Slug, error) {
	orgSlug, projectSlug, err := parseProjectPath(slugParam, projectSlugParam)
	if err != nil {
		return "", "", "", err
	}

	environmentSlug, err := slug.New(environmentSlugParam)
	if err != nil {
		return "", "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
	}

	return orgSlug, projectSlug, environmentSlug, nil
}
