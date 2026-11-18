package http

import (
	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (s *server) CompareRuntimeVariables(c fiber.Ctx, org api.SlugParam, params api.CompareRuntimeVariablesParams) error {
	orgSlug, err := slug.New(org)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEnvironments); err != nil {
		return err
	}
	var keys []string
	if params.UsedKey != nil {
		keys = *params.UsedKey
	}
	comparison, err := s.environmentsService.CompareRuntimeVariables(c.Context(), orgSlug, keys)
	if err != nil {
		return mapServiceError(err)
	}
	response := api.RuntimeVariableComparison{
		Environments: make([]api.Slug, len(comparison.Environments)),
		Rows:         make([]api.RuntimeVariableComparisonRow, len(comparison.Rows)),
	}
	for i, environment := range comparison.Environments {
		response.Environments[i] = environment.String()
	}
	for i, row := range comparison.Rows {
		mapped := api.RuntimeVariableComparisonRow{
			Key: row.Key, Used: row.Used, Cells: make([]api.RuntimeVariableComparisonCell, len(row.Cells)),
		}
		if row.Definition != nil {
			definition := runtimeVariableDefinitionToHTTP(*row.Definition)
			mapped.Definition = &definition
		}
		for j, cell := range row.Cells {
			mapped.Cells[j] = api.RuntimeVariableComparisonCell{
				Source: api.RuntimeVariableComparisonCellSource(cell.Source), Required: cell.Required,
				TypeMismatch: cell.TypeMismatch, Blocked: cell.Blocked(),
			}
			if cell.Variable != nil {
				variable := runtimeVariableToHTTP(*cell.Variable)
				mapped.Cells[j].Variable = &variable
			}
		}
		response.Rows[i] = mapped
	}
	return c.JSON(response)
}

func (s *server) UpsertRuntimeVariableDefinition(c fiber.Ctx, org api.SlugParam, key api.RuntimeVariableKeyParam) error {
	orgSlug, err := slug.New(org)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceEnvironments); err != nil {
		return err
	}
	var req api.UpsertRuntimeVariableDefinition
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	valueType, err := model.RuntimeVariableValueTypeString(string(req.ValueType))
	if err != nil {
		return apierror.ValidationFailed("Invalid runtime variable value type")
	}
	confirmations, err := runtimeVariableDefinitionConfirmations(req.ProtectedEnvironmentConfirmations)
	if err != nil {
		return err
	}
	definition, err := s.environmentsService.UpsertRuntimeVariableDefinition(c.Context(), orgSlug, model.RuntimeVariableDefinition{
		Key: key, ValueType: valueType, Description: req.Description, Required: req.Required, DefaultValue: req.DefaultValue,
	}, confirmations, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(runtimeVariableDefinitionToHTTP(*definition))
}

func (s *server) DeleteRuntimeVariableDefinition(c fiber.Ctx, org api.SlugParam, key api.RuntimeVariableKeyParam, params api.DeleteRuntimeVariableDefinitionParams) error {
	orgSlug, err := slug.New(org)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceEnvironments); err != nil {
		return err
	}
	confirmations, err := runtimeVariableDefinitionConfirmations(params.ProtectedEnvironmentConfirmations)
	if err != nil {
		return err
	}
	if err := s.environmentsService.DeleteRuntimeVariableDefinition(c.Context(), orgSlug, key, confirmations, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func runtimeVariableDefinitionConfirmations(values *[]api.Slug) ([]slug.Slug, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]slug.Slug, len(*values))
	for i, value := range *values {
		parsed, err := slug.New(value)
		if err != nil {
			return nil, apierror.ValidationFailed("Invalid protected environment confirmation")
		}
		result[i] = parsed
	}
	return result, nil
}

func runtimeVariableDefinitionToHTTP(definition model.RuntimeVariableDefinition) api.RuntimeVariableDefinition {
	return api.RuntimeVariableDefinition{
		Key: definition.Key, ValueType: api.RuntimeVariableValueType(definition.ValueType.String()),
		Description: definition.Description, Required: definition.Required, DefaultValue: definition.DefaultValue,
	}
}
