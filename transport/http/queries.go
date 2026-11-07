package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

const (
	defaultQueryRowLimit       = 1000
	defaultQueryTimeoutSeconds = 30
)

// queryDataJSON is the JSON structure stored in a query's Data field.
type queryDataJSON struct {
	Statement      string `json:"statement"`
	RowLimit       int    `json:"row_limit,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// ListQueries lists all queries for a data source.
func (s *server) ListQueries(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	params api.ListQueriesParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceQueries); err != nil {
		return err
	}

	listReq := toQueriesListRequest(params)
	includeDeleted := params.IncludeDeleted != nil && *params.IncludeDeleted
	if includeDeleted {
		if err := s.canOrg(c, orgSlug, model.Restore, model.ResourceQueries); err != nil {
			return err
		}
	}

	response, err := s.queriesService.ListByDataSource(c.Context(), orgSlug, dsSlug, listReq, includeDeleted)
	if err != nil {
		return mapServiceError(err)
	}

	apiQueries := make([]api.Query, 0, len(response.Items))

	for _, q := range response.Items {
		apiQ, mapErr := queryToHTTP(q)
		if mapErr != nil {
			slog.Error("failed to map query", "error", mapErr)
			return apierror.InternalServerError()
		}

		apiQueries = append(apiQueries, apiQ)
	}

	return c.JSON(api.QueryListResponse{
		Queries:    apiQueries,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

// CreateQuery creates a new query for a data source.
func (s *server) CreateQuery(
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

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceQueries); err != nil {
		return err
	}

	var req api.NewQuery
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if err := validateQueryContractValidationKeys(c.Body()); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid contract validations: %v", err))
	}

	var qSlug slug.Slug
	if req.Slug != nil {
		qSlug, err = slug.New(*req.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
		}
	}

	data, err := buildQueryData(req.QueryStatement, req.RowLimit, req.TimeoutSeconds)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query data: %v", err))
	}

	expectedInputContract, _, err := apiSchemaContractToPipeline(req.ExpectedInputContract)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid expected input contract: %v", err))
	}

	outputContract, _, err := apiSchemaContractToPipeline(req.OutputContract)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid output contract: %v", err))
	}
	inputRef, _, err := apiOperationContractRef(req.ExpectedInputContractRef)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid input reference: %v", err))
	}
	outputRef, _, err := apiOperationContractRef(req.OutputContractRef)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid output reference: %v", err))
	}

	svcParams := &queriessvc.CreateParams{
		Slug:                     qSlug,
		Name:                     req.Name,
		Description:              req.Description,
		Data:                     data,
		ExpectedInputContract:    expectedInputContract,
		ExpectedInputContractRef: inputRef,
		OutputContract:           outputContract,
		OutputContractRef:        outputRef,
		Parameters:               apiParamsToService(req.Parameters),
	}

	q, err := s.queriesService.Create(c.Context(), orgSlug, dsSlug, s.eventActor(c), svcParams)
	if err != nil {
		return mapServiceError(err)
	}

	apiQ, err := queryToHTTP(q)
	if err != nil {
		slog.Error("failed to map created query", "error", err)
		return apierror.InternalServerError()
	}

	return c.Status(fiber.StatusCreated).JSON(apiQ)
}

// GetQuery retrieves a single query by slug.
func (s *server) GetQuery(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceQueries); err != nil {
		return err
	}

	q, err := s.queriesService.FindBySlug(c.Context(), orgSlug, dsSlug, querySlug)
	if err != nil {
		return mapServiceError(err)
	}

	apiQ, err := queryToHTTP(q)
	if err != nil {
		slog.Error("failed to map query", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(apiQ)
}

// UpdateQuery updates an existing query (full replacement).
func (s *server) UpdateQuery(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceQueries); err != nil {
		return err
	}

	var req api.UpdateQuery
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if err := validateQueryContractValidationKeys(c.Body()); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid contract validations: %v", err))
	}

	data, err := buildQueryData(req.QueryStatement, req.RowLimit, req.TimeoutSeconds)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query data: %v", err))
	}

	expectedInputContract, _, err := apiSchemaContractToPipeline(req.ExpectedInputContract)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid expected input contract: %v", err))
	}

	outputContract, _, err := apiSchemaContractToPipeline(req.OutputContract)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid output contract: %v", err))
	}
	inputRef, _, err := apiOperationContractRef(req.ExpectedInputContractRef)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid input reference: %v", err))
	}
	outputRef, _, err := apiOperationContractRef(req.OutputContractRef)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid output reference: %v", err))
	}

	svcParams := &queriessvc.UpdateParams{
		Name:                     req.Name,
		Description:              req.Description,
		Data:                     data,
		ExpectedInputContract:    expectedInputContract,
		ExpectedInputContractRef: inputRef,
		OutputContract:           outputContract,
		OutputContractRef:        outputRef,
		Parameters:               apiParamsToService(req.Parameters),
	}

	q, err := s.queriesService.Update(
		c.Context(),
		orgSlug,
		dsSlug,
		querySlug,
		s.eventActor(c),
		svcParams,
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiQ, err := queryToHTTP(q)
	if err != nil {
		slog.Error("failed to map updated query", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(apiQ)
}

// DeleteQuery soft-deletes a query by slug.
func (s *server) DeleteQuery(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceQueries); err != nil {
		return err
	}

	if err := s.queriesService.Delete(c.Context(), orgSlug, dsSlug, querySlug, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// RestoreQuery restores a soft-deleted query by slug.
func (s *server) RestoreQuery(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Restore, model.ResourceQueries); err != nil {
		return err
	}

	if err := s.queriesService.Restore(
		c.Context(),
		orgSlug,
		dsSlug,
		querySlug,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// PurgeQuery permanently deletes a soft-deleted query by slug.
func (s *server) PurgeQuery(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Purge, model.ResourceQueries); err != nil {
		return err
	}

	if err := s.queriesService.Purge(c.Context(), orgSlug, dsSlug, querySlug, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ExecuteQuery executes a query with provided parameters.
func (s *server) ExecuteQuery(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Execute, model.ResourceQueries); err != nil {
		return err
	}

	var req api.ExecuteQueryRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	paramValues := req.Parameters
	if paramValues == nil {
		paramValues = make(map[string]any)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "query.execute", false); err != nil {
		return err
	}

	result, err := s.queriesService.Execute(
		c.Context(),
		orgSlug,
		dsSlug,
		querySlug,
		s.eventActor(c),
		paramValues,
	)
	if err != nil {
		if xerror.Is(err, queriessvc.ErrQueryExecutionFailed) {
			return apierror.New(
				fiber.StatusUnprocessableEntity,
				"QUERY_EXECUTION_FAILED",
				queryExecutionFailureMessage(err),
			)
		}
		return mapServiceError(err)
	}

	httpResult, err := executionResultToHTTP(result)
	if err != nil {
		slog.Error("failed to convert execution result", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(httpResult)
}

func queryExecutionFailureMessage(err error) string {
	if err == nil {
		return "Query execution failed."
	}

	if detail := xerror.PublicDetail(err); detail != "" {
		return detail
	}

	return "Query execution failed."
}

// ListQueryExecutions lists query execution history for a query.
func (s *server) ListQueryExecutions(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
	params api.ListQueryExecutionsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceQueries); err != nil {
		return err
	}

	listReq := toQueryExecutionsListRequest(params)

	response, err := s.queriesService.ListExecutions(
		c.Context(),
		orgSlug,
		dsSlug,
		querySlug,
		listReq,
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiExecutions, err := queryExecutionsToHTTP(response.Items)
	if err != nil {
		slog.Error("failed to map query executions", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(api.QueryExecutionListResponse{
		Executions: apiExecutions,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

// GetQueryExecution retrieves a single query execution record by slug.
func (s *server) GetQueryExecution(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	querySlugParam api.QuerySlugParam,
	executionSlugParam api.QueryExecutionSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}

	querySlug, err := slug.New(querySlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query slug: %v", err))
	}

	executionSlug, err := slug.New(executionSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid query execution slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceQueries); err != nil {
		return err
	}

	execution, err := s.queriesService.GetExecution(
		c.Context(),
		orgSlug,
		dsSlug,
		querySlug,
		executionSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiExecution, err := queryExecutionToHTTP(execution)
	if err != nil {
		slog.Error("failed to map query execution", "error", err)
		return apierror.InternalServerError()
	}

	return c.JSON(apiExecution)
}

// GetQueryExecutionSummary returns the latest recorded successful saved-query execution.
func (s *server) GetQueryExecutionSummary(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceQueries); err != nil {
		return err
	}

	summary, err := s.queriesService.GetExecutionSummary(c.Context(), orgSlug)
	if err != nil {
		return mapServiceError(err)
	}

	response := api.QueryExecutionSummary{}
	if summary != nil {
		response.SuccessfulExecution = &api.QueryExecutionIdentity{
			ExecutionSlug:  string(summary.ExecutionSlug),
			DataSourceSlug: string(summary.DataSourceSlug),
			QuerySlug:      string(summary.QuerySlug),
			ExecutedAt:     summary.ExecutedAt,
		}
	}

	return c.JSON(response)
}

// buildQueryData constructs the Data JSON from API request fields.
func buildQueryData(
	statement api.QueryStatement,
	rowLimit, timeoutSeconds *int,
) (json.RawMessage, error) {
	// Marshal the polymorphic statement to JSON
	stmtJSON, err := json.Marshal(statement)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}

	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(stmtJSON, &discriminator); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	if discriminator.Type == "google_workspace" {
		if _, err := workspace.DecodeStatement(stmtJSON); err != nil {
			return nil, xerror.Wrapf("validate Google Workspace statement: %w", err)
		}
	}

	qd := queryDataJSON{
		Statement:      string(stmtJSON),
		RowLimit:       defaultQueryRowLimit,
		TimeoutSeconds: defaultQueryTimeoutSeconds,
	}

	if rowLimit != nil {
		qd.RowLimit = *rowLimit
	}

	if timeoutSeconds != nil {
		qd.TimeoutSeconds = *timeoutSeconds
	}

	data, err := json.Marshal(qd)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.JSON).With(err)
	}

	return data, nil
}

// queryToHTTP converts a service Query to the API Query type.
func queryToHTTP(q *queriessvc.Query) (api.Query, error) {
	if q == nil {
		return api.Query{}, xerror.Unexpected().Detail("query is nil")
	}
	var qd queryDataJSON
	if err := json.Unmarshal(q.Data, &qd); err != nil {
		return api.Query{}, xerror.InvalidInput(xerror.JSON).With(err)
	}

	// Unmarshal the polymorphic query statement from stored JSON bytes
	var stmt api.QueryStatement
	if err := json.Unmarshal([]byte(qd.Statement), &stmt); err != nil {
		return api.Query{}, xerror.InvalidInput(xerror.JSON).With(err)
	}

	rowLimit := qd.RowLimit
	if rowLimit == 0 {
		rowLimit = defaultQueryRowLimit
	}

	timeoutSeconds := qd.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = defaultQueryTimeoutSeconds
	}

	apiParams := make([]api.QueryParameter, 0, len(q.Parameters))

	for _, p := range q.Parameters {
		apiParams = append(apiParams, queryParamToHTTP(p))
	}

	return api.Query{
		Slug:                     string(q.Slug),
		Name:                     q.Name,
		Description:              q.Description,
		DataSourceSlug:           string(q.DataSourceSlug),
		QueryStatement:           stmt,
		RowLimit:                 rowLimit,
		TimeoutSeconds:           timeoutSeconds,
		ExpectedInputContract:    pipelineSchemaContractToAPI(q.ExpectedInputContract),
		ExpectedInputContractRef: operationContractRefToAPI(q.ExpectedInputContractRef),
		OutputContract:           pipelineSchemaContractToAPI(q.OutputContract),
		OutputContractRef:        operationContractRefToAPI(q.OutputContractRef),
		Parameters:               apiParams,
		CreatedAt:                q.CreatedAt,
		UpdatedAt:                q.UpdatedAt,
		DeletedAt:                q.DeletedAt,
	}, nil
}

func apiSchemaContractToPipeline(
	contract *api.PipelineSchemaContract,
) (*payload.Contract, bool, error) {
	if contract == nil {
		return nil, false, nil
	}

	fields := make([]payload.ContractField, 0)
	if contract.Fields != nil {
		fields = make([]payload.ContractField, 0, len(*contract.Fields))
		for _, field := range *contract.Fields {
			fieldType, err := model.ContractFieldTypeString(string(field.Type))
			if err != nil {
				return nil, false, err
			}

			fields = append(fields, payload.ContractField{
				Name:        field.Name,
				Type:        fieldType,
				Required:    field.Required,
				Nullable:    ptr.Value(field.Nullable),
				Precision:   field.Precision,
				Scale:       field.Scale,
				Description: ptr.Value(field.Description),
				Validations: apiContractFieldValidationsToPipeline(field.Validations),
			})
		}
	}

	representation := model.ContractRepresentationUnknown
	if contract.Representation != nil {
		value, err := model.ContractRepresentationString(string(*contract.Representation))
		if err != nil {
			return nil, false, err
		}
		representation = value
	}

	rootShape := model.ContractRootShapeUnknown
	if contract.RootShape != nil {
		value, err := model.ContractRootShapeString(string(*contract.RootShape))
		if err != nil {
			return nil, false, err
		}
		rootShape = value
	}

	if representation != model.ContractRepresentationFile && len(fields) == 0 {
		return nil, false, xerror.InvalidInput(xerror.Query).Detail(
			"schema contract fields are required unless representation is file",
		)
	}

	out := &payload.Contract{
		Name:           contract.Name,
		Representation: representation,
		RootShape:      rootShape,
		Fields:         fields,
	}
	if err := payload.ValidateContractFieldDeclarations(out); err != nil {
		return nil, false, xerror.InvalidInput(xerror.Query).
			Detailf("invalid schema contract: %w", err)
	}

	return out, true, nil
}

// validateQueryContractValidationKeys rejects validation keys that the
// generated API struct would otherwise drop before service validation runs.
func validateQueryContractValidationKeys(data []byte) error {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	for _, name := range []string{"expected_input_contract", "output_contract"} {
		var contract struct {
			Fields []struct {
				Validations json.RawMessage `json:"validations"`
			} `json:"fields"`
		}
		if len(body[name]) == 0 {
			continue
		}
		if err := json.Unmarshal(body[name], &contract); err != nil {
			return err
		}
		for i, field := range contract.Fields {
			if len(field.Validations) == 0 {
				continue
			}
			if err := validateRawContractValidationObject(field.Validations, fmt.Sprintf("%s.fields[%d].validations", name, i), xerror.Query); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateRawContractValidationObject(raw json.RawMessage, path string, entity xerror.Entity) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var validations map[string]json.RawMessage
	if err := json.Unmarshal(raw, &validations); err != nil {
		return xerror.InvalidInput(entity).Detailf("%s must be an object", path)
	}

	allowedKeys := map[string]struct{}{
		"format":     {},
		"enum":       {},
		"min":        {},
		"max":        {},
		"min_length": {},
		"max_length": {},
		"unique":     {},
	}
	for key, value := range validations {
		if _, ok := allowedKeys[key]; !ok {
			return xerror.InvalidInput(entity).Detailf("%s contains unknown key %q", path, key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return xerror.InvalidInput(entity).Detailf("%s.%s must not be null", path, key)
		}
	}
	return nil
}

func pipelineSchemaContractToAPI(contract *payload.Contract) *api.PipelineSchemaContract {
	if contract == nil {
		return nil
	}

	fields := make([]api.PipelineSchemaContractField, 0, len(contract.Fields))
	for _, field := range contract.Fields {
		apiField := api.PipelineSchemaContractField{
			Name:      field.Name,
			Type:      api.PipelineSchemaContractFieldType(field.Type.String()),
			Required:  field.Required,
			Precision: field.Precision,
			Scale:     field.Scale,
		}
		if field.Nullable {
			apiField.Nullable = ptr.Ptr(field.Nullable)
		}
		if field.Description != "" {
			apiField.Description = ptr.Ptr(field.Description)
		}
		apiField.Validations = pipelineContractFieldValidationsToAPI(field.Validations)
		fields = append(fields, apiField)
	}

	apiContract := &api.PipelineSchemaContract{Name: contract.Name}
	if len(fields) > 0 {
		apiContract.Fields = &fields
	}

	if contract.Representation != model.ContractRepresentationUnknown {
		representation := api.PipelineSchemaContractRepresentation(contract.Representation.String())
		apiContract.Representation = &representation
	}

	if contract.RootShape != model.ContractRootShapeUnknown {
		rootShape := api.PipelineSchemaContractRootShape(contract.RootShape.String())
		apiContract.RootShape = &rootShape
	}

	return apiContract
}

func apiContractFieldValidationsToPipeline(
	validations *api.PipelineSchemaContractFieldValidations,
) *payload.ContractFieldValidations {
	if validations == nil {
		return nil
	}

	out := &payload.ContractFieldValidations{}
	if validations.Format != nil {
		out.Format = string(*validations.Format)
	}
	if validations.Enum != nil {
		out.Enum = make([]any, 0, len(*validations.Enum))
		for _, literal := range *validations.Enum {
			out.Enum = append(out.Enum, apiUnionValue(literal))
		}
	}
	if validations.Min != nil {
		out.Min = apiUnionValue(validations.Min)
	}
	if validations.Max != nil {
		out.Max = apiUnionValue(validations.Max)
	}
	out.MinLength = cloneAPIIntPtr(validations.MinLength)
	out.MaxLength = cloneAPIIntPtr(validations.MaxLength)
	out.Unique = cloneAPIBoolPtr(validations.Unique)
	if out.Format == "" &&
		out.Enum == nil &&
		out.Min == nil &&
		out.Max == nil &&
		out.MinLength == nil &&
		out.MaxLength == nil &&
		out.Unique == nil {
		return nil
	}
	return out
}

func pipelineContractFieldValidationsToAPI(
	validations *payload.ContractFieldValidations,
) *api.PipelineSchemaContractFieldValidations {
	if validations == nil {
		return nil
	}

	out := &api.PipelineSchemaContractFieldValidations{}
	if validations.Format != "" {
		format := api.PipelineSchemaContractFieldValidationsFormat(validations.Format)
		out.Format = &format
	}
	if validations.Enum != nil {
		literals := make([]api.PipelineSchemaContractValidationLiteral, 0, len(validations.Enum))
		for _, literal := range validations.Enum {
			literals = append(literals, pipelineValidationLiteralToAPI(literal))
		}
		out.Enum = &literals
	}
	out.Min = pipelineValidationBoundToAPI(validations.Min)
	out.Max = pipelineValidationBoundToAPI(validations.Max)
	out.MinLength = cloneAPIIntPtr(validations.MinLength)
	out.MaxLength = cloneAPIIntPtr(validations.MaxLength)
	out.Unique = cloneAPIBoolPtr(validations.Unique)
	return out
}

func apiUnionValue(value interface{ MarshalJSON() ([]byte, error) }) any {
	if value == nil {
		return nil
	}
	data, err := value.MarshalJSON()
	if err != nil {
		return nil
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil
	}
	return decoded
}

func pipelineValidationBoundToAPI(value any) *api.PipelineSchemaContractValidationBound {
	if value == nil {
		return nil
	}
	var union api.PipelineSchemaContractValidationBound
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	if err := union.UnmarshalJSON(data); err != nil {
		return nil
	}
	return &union
}

func pipelineValidationLiteralToAPI(value any) api.PipelineSchemaContractValidationLiteral {
	var union api.PipelineSchemaContractValidationLiteral
	data, err := json.Marshal(value)
	if err != nil {
		return union
	}
	_ = union.UnmarshalJSON(data)
	return union
}

func cloneAPIIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneAPIBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// queryExecutionToHTTP converts a service QueryExecution to the API QueryExecutionRecord type.
func queryExecutionToHTTP(qe *queriessvc.QueryExecution) (api.QueryExecutionRecord, error) {
	if qe == nil {
		return api.QueryExecutionRecord{}, xerror.Unexpected().Detail("query execution is nil")
	}

	var executionResult api.QueryExecutionResult
	if isStoredExecutionResultEnvelope(qe.Result) {
		storedResult, err := storedExecutionResultToHTTP(qe.Result)
		if err != nil {
			return api.QueryExecutionRecord{}, xerror.Unexpected().With(err)
		}

		executionResult = storedResult
	} else if err := json.Unmarshal(qe.Result, &executionResult); err != nil {
		return api.QueryExecutionRecord{}, xerror.Unexpected().With(err)
	}

	return api.QueryExecutionRecord{
		Slug:       string(qe.Slug),
		Status:     qe.Status,
		Result:     executionResult,
		ExecutedAt: qe.ExecutedAt,
	}, nil
}

func isStoredExecutionResultEnvelope(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	probe := struct {
		VendorType    *json.RawMessage `json:"vendor_type"`
		SQL           *json.RawMessage `json:"sql"`
		MongoDB       *json.RawMessage `json:"mongodb"`
		HTTP          *json.RawMessage `json:"http"`
		ObjectRows    *json.RawMessage `json:"object_rows"`
		FTP           *json.RawMessage `json:"ftp"`
		Git           *json.RawMessage `json:"git"`
		S3            *json.RawMessage `json:"s3"`
		WorkspaceFile *json.RawMessage `json:"workspace_file"`
	}{}

	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}

	return probe.VendorType != nil ||
		probe.SQL != nil ||
		probe.MongoDB != nil ||
		probe.HTTP != nil ||
		probe.ObjectRows != nil ||
		probe.FTP != nil ||
		probe.Git != nil ||
		probe.S3 != nil || probe.WorkspaceFile != nil
}

func storedExecutionResultToHTTP(raw json.RawMessage) (api.QueryExecutionResult, error) {
	var apiResult api.QueryExecutionResult

	stored := struct {
		ResultType    result.ExecutionResultType `json:"result_type"`
		VendorType    model.DataSourceType       `json:"vendor_type"`
		DurationMs    int64                      `json:"duration_ms"`
		Error         *string                    `json:"error,omitempty"`
		SQL           *result.SQLResult          `json:"sql,omitempty"`
		MongoDB       *result.MongoDBResult      `json:"mongodb,omitempty"`
		HTTP          *result.HTTPResult         `json:"http,omitempty"`
		ObjectRows    *result.ObjectRowsResult   `json:"object_rows,omitempty"`
		FTP           *result.FileResult         `json:"ftp,omitempty"`
		Git           *result.FileResult         `json:"git,omitempty"`
		S3            *result.FileResult         `json:"s3,omitempty"`
		WorkspaceFile *result.FileResult         `json:"workspace_file,omitempty"`
	}{}

	if err := json.Unmarshal(raw, &stored); err != nil {
		return apiResult, xerror.Unexpected().With(err)
	}

	vendorType := storedExecutionVendorTypeString(stored.VendorType)

	switch stored.ResultType {
	case result.ExecutionResultTypeSQL:
		if stored.SQL == nil {
			return apiResult, xerror.Unexpected().Detail("stored SQL result payload is nil")
		}

		sqlResult := api.SQLExecutionResult{
			ResultType: api.SQLExecutionResultResultTypeSql,
			Type:       vendorType,
			Payload: struct {
				ColumnNames *[]string        `json:"column_names,omitempty"`
				RowCount    int              `json:"row_count"`
				Rows        []map[string]any `json:"rows"`
			}{
				Rows:        stored.SQL.Rows,
				RowCount:    stored.SQL.RowCount,
				ColumnNames: &stored.SQL.ColumnNames,
			},
			DurationMs: int(stored.DurationMs),
			Success:    stored.Error == nil,
		}
		if stored.Error != nil {
			sqlResult.Error = stored.Error
		}

		if err := apiResult.FromSQLExecutionResult(sqlResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	case result.ExecutionResultTypeMongoDB:
		if stored.MongoDB == nil {
			return apiResult, xerror.Unexpected().Detail("stored MongoDB result payload is nil")
		}

		mongoResult := api.MongoDBExecutionResult{
			ResultType: api.MongoDBExecutionResultResultTypeMongodb,
			Type:       vendorType,
			Payload: struct {
				DeletedCount  *int64                                         `json:"deleted_count,omitempty"`
				DocumentCount *int                                           `json:"document_count,omitempty"`
				Documents     *[]map[string]any                              `json:"documents,omitempty"`
				InsertedId    any                                            `json:"inserted_id,omitempty"`
				MatchedCount  *int64                                         `json:"matched_count,omitempty"`
				ModifiedCount *int64                                         `json:"modified_count,omitempty"`
				OperationType api.MongoDBExecutionResultPayloadOperationType `json:"operation_type"`
				UpsertedCount *int64                                         `json:"upserted_count,omitempty"`
				UpsertedId    any                                            `json:"upserted_id,omitempty"`
			}{
				OperationType: api.MongoDBExecutionResultPayloadOperationType(
					stored.MongoDB.OperationType,
				),
				Documents:     &stored.MongoDB.Documents,
				DocumentCount: stored.MongoDB.DocumentCount,
				InsertedId:    stored.MongoDB.InsertedID,
				MatchedCount:  stored.MongoDB.MatchedCount,
				ModifiedCount: stored.MongoDB.ModifiedCount,
				DeletedCount:  stored.MongoDB.DeletedCount,
				UpsertedId:    stored.MongoDB.UpsertedID,
				UpsertedCount: stored.MongoDB.UpsertedCount,
			},
			DurationMs: int(stored.DurationMs),
			Success:    stored.Error == nil,
		}
		if stored.Error != nil {
			mongoResult.Error = stored.Error
		}

		if err := apiResult.FromMongoDBExecutionResult(mongoResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	case result.ExecutionResultTypeHTTP:
		if stored.HTTP == nil {
			return apiResult, xerror.Unexpected().Detail("stored HTTP result payload is nil")
		}

		var body any
		if len(stored.HTTP.Body) > 0 {
			if err := json.Unmarshal(stored.HTTP.Body, &body); err != nil {
				body = string(stored.HTTP.Body)
			}
		}

		httpResult := api.HTTPExecutionResult{
			ResultType: api.HTTPExecutionResultResultTypeHttp,
			Type:       vendorType,
			Payload: struct {
				Body       any                      `json:"body,omitempty"`
				File       *api.FilePayloadMetadata `json:"file,omitempty"`
				Headers    *map[string]string       `json:"headers,omitempty"`
				StatusCode int                      `json:"status_code"`
			}{
				StatusCode: stored.HTTP.StatusCode,
				Headers:    &stored.HTTP.Headers,
				Body:       body,
				File:       filePayloadMetadataToAPI(stored.HTTP.File),
			},
			DurationMs: int(stored.DurationMs),
			Success:    stored.Error == nil,
		}
		if stored.Error != nil {
			httpResult.Error = stored.Error
		}

		if err := apiResult.FromHTTPExecutionResult(httpResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	case result.ExecutionResultTypeObjectRows:
		if stored.ObjectRows == nil {
			if stored.Error == nil {
				return apiResult, xerror.Unexpected().Detail("stored object rows result payload is nil")
			}

			objectRowsResult := buildObjectRowsExecutionResult(
				vendorType,
				stored.DurationMs,
				nil,
				stored.Error,
			)
			if err := apiResult.FromObjectRowsExecutionResult(objectRowsResult); err != nil {
				return apiResult, xerror.Unexpected().With(err)
			}

			return apiResult, nil
		}

		objectRowsResult := buildObjectRowsExecutionResult(
			vendorType,
			stored.DurationMs,
			stored.ObjectRows,
			stored.Error,
		)

		if err := apiResult.FromObjectRowsExecutionResult(objectRowsResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	case result.ExecutionResultTypeFTP:
		if stored.VendorType == model.DataSourceTypeGit {
			filePayload := storedExecutionFilePayload(stored.FTP, stored.Git, true)
			if filePayload == nil && stored.Error == nil {
				return apiResult, xerror.Unexpected().Detail("stored Git result payload is nil")
			}

			gitResult := buildGitExecutionResult(
				vendorType,
				stored.DurationMs,
				filePayload,
				stored.Error,
			)
			if err := apiResult.FromGitExecutionResult(gitResult); err != nil {
				return apiResult, xerror.Unexpected().With(err)
			}

			return apiResult, nil
		}

		filePayload := storedExecutionFilePayload(stored.FTP, stored.Git, false)
		if filePayload == nil && stored.Error == nil {
			return apiResult, xerror.Unexpected().Detail("stored FTP result payload is nil")
		}

		ftpResult := buildFTPExecutionResult(
			vendorType,
			stored.DurationMs,
			filePayload,
			stored.Error,
		)
		if err := apiResult.FromFTPExecutionResult(ftpResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	case result.ExecutionResultTypeGit:
		filePayload := storedExecutionFilePayload(stored.FTP, stored.Git, true)
		if filePayload == nil && stored.Error == nil {
			return apiResult, xerror.Unexpected().Detail("stored Git result payload is nil")
		}

		gitResult := buildGitExecutionResult(
			vendorType,
			stored.DurationMs,
			filePayload,
			stored.Error,
		)
		if err := apiResult.FromGitExecutionResult(gitResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	case result.ExecutionResultTypeWorkspaceFile:
		if stored.WorkspaceFile == nil && stored.Error == nil {
			return apiResult, xerror.Unexpected().Detail("stored Workspace file payload is nil")
		}
		value := buildWorkspaceFileExecutionResult(vendorType, stored.DurationMs, stored.WorkspaceFile, stored.Error)
		if err := apiResult.FromWorkspaceFileExecutionResult(value); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeS3:
		if stored.S3 == nil && stored.Error == nil {
			return apiResult, xerror.Unexpected().Detail("stored S3 result payload is nil")
		}

		s3Result := buildS3ExecutionResult(
			vendorType,
			stored.DurationMs,
			stored.S3,
			stored.Error,
		)
		if err := apiResult.FromS3ExecutionResult(s3Result); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}

		return apiResult, nil

	default:
		return apiResult, xerror.Unexpected().Detailf(
			"unknown stored execution result type: %q",
			stored.ResultType.String(),
		)
	}
}

func storedExecutionVendorTypeString(vendorType model.DataSourceType) string {
	if vendorType == model.DataSourceTypeUnknown {
		return ""
	}

	return vendorType.String()
}

// queryExecutionsToHTTP converts service QueryExecution records
// to API QueryExecutionRecord records.
func queryExecutionsToHTTP(
	list []*queriessvc.QueryExecution,
) ([]api.QueryExecutionRecord, error) {
	records := make([]api.QueryExecutionRecord, 0, len(list))

	for _, qe := range list {
		record, err := queryExecutionToHTTP(qe)
		if err != nil {
			return nil, err
		}

		records = append(records, record)
	}

	return records, nil
}

func storedExecutionFilePayload(
	ftpPayload *result.FileResult,
	gitPayload *result.FileResult,
	preferGit bool,
) *result.FileResult {
	if preferGit {
		if gitPayload != nil {
			return gitPayload
		}
		return ftpPayload
	}

	if ftpPayload != nil {
		return ftpPayload
	}

	return gitPayload
}

func buildFTPExecutionResult(
	vendorType string,
	durationMs int64,
	filePayload *result.FileResult,
	errMsg *string,
) api.FTPExecutionResult {
	ftpResult := api.FTPExecutionResult{
		ResultType: api.FTPExecutionResultResultTypeFtp,
		Type:       normalizedExecutionVendorType(vendorType, "ftp"),
		Payload: struct {
			ColumnNames           *[]string                              `json:"column_names,omitempty"`
			File                  *api.FilePayloadMetadata               `json:"file,omitempty"`
			Format                api.FTPExecutionResultPayloadFormat    `json:"format"`
			IsHeadlessCsv         *bool                                  `json:"is_headless_csv,omitempty"`
			IsHeadlessSpreadsheet *bool                                  `json:"is_headless_spreadsheet,omitempty"`
			Operation             api.FTPExecutionResultPayloadOperation `json:"operation"`
			Range                 *string                                `json:"range,omitempty"`
			RowCount              *int                                   `json:"row_count,omitempty"`
			Rows                  *[]map[string]interface{}              `json:"rows,omitempty"`
			Sheet                 *string                                `json:"sheet,omitempty"`
			TargetPath            *string                                `json:"target_path,omitempty"`
			WrittenBytes          *int                                   `json:"written_bytes,omitempty"`
			WrittenRows           *int                                   `json:"written_rows,omitempty"`
		}{
			Operation: api.FTPExecutionResultPayloadOperationRead,
			Format:    api.FTPExecutionResultPayloadFormatJson,
		},
		DurationMs: int(durationMs),
		Success:    errMsg == nil,
	}
	if errMsg != nil {
		ftpResult.Error = errMsg
	}
	if filePayload == nil {
		return ftpResult
	}

	if strings.EqualFold(
		filePayload.Operation,
		string(api.FTPExecutionResultPayloadOperationWrite),
	) {
		ftpResult.Payload.Operation = api.FTPExecutionResultPayloadOperationWrite
	}

	rows := filePayload.Rows
	rowCount := filePayload.RowCount
	ftpResult.Payload.Rows = &rows
	ftpResult.Payload.RowCount = &rowCount
	ftpResult.Payload.Format = api.FTPExecutionResultPayloadFormat(filePayload.Format)
	ftpResult.Payload.File = filePayloadMetadataToAPI(filePayload.File)
	ftpResult.Payload.TargetPath = fileResultTargetPath(filePayload.TargetPath)
	ftpResult.Payload.WrittenBytes = filePayload.WrittenBytes
	ftpResult.Payload.WrittenRows = filePayload.WrittenRows
	if len(filePayload.ColumnNames) > 0 {
		ftpResult.Payload.ColumnNames = &filePayload.ColumnNames
	}
	if filePayload.IsHeadlessCSV {
		isHeadlessCSV := true
		ftpResult.Payload.IsHeadlessCsv = &isHeadlessCSV
	}
	if filePayload.IsHeadlessSpreadsheet {
		isHeadlessSpreadsheet := true
		ftpResult.Payload.IsHeadlessSpreadsheet = &isHeadlessSpreadsheet
	}
	ftpResult.Payload.Sheet = fileResultTargetPath(filePayload.Sheet)
	ftpResult.Payload.Range = fileResultTargetPath(filePayload.Range)

	return ftpResult
}

func buildGitExecutionResult(
	vendorType string,
	durationMs int64,
	filePayload *result.FileResult,
	errMsg *string,
) api.GitExecutionResult {
	gitResult := api.GitExecutionResult{
		ResultType: api.GitExecutionResultResultTypeGit,
		Type:       normalizedExecutionVendorType(vendorType, "git"),
		Payload: struct {
			ColumnNames           *[]string                              `json:"column_names,omitempty"`
			File                  *api.FilePayloadMetadata               `json:"file,omitempty"`
			Format                api.GitExecutionResultPayloadFormat    `json:"format"`
			IsHeadlessCsv         *bool                                  `json:"is_headless_csv,omitempty"`
			IsHeadlessSpreadsheet *bool                                  `json:"is_headless_spreadsheet,omitempty"`
			Operation             api.GitExecutionResultPayloadOperation `json:"operation"`
			Range                 *string                                `json:"range,omitempty"`
			RowCount              *int                                   `json:"row_count,omitempty"`
			Rows                  *[]map[string]interface{}              `json:"rows,omitempty"`
			Sheet                 *string                                `json:"sheet,omitempty"`
			TargetPath            *string                                `json:"target_path,omitempty"`
			WrittenBytes          *int                                   `json:"written_bytes,omitempty"`
			WrittenRows           *int                                   `json:"written_rows,omitempty"`
		}{
			Operation: api.GitExecutionResultPayloadOperationRead,
			Format:    api.GitExecutionResultPayloadFormatJson,
		},
		DurationMs: int(durationMs),
		Success:    errMsg == nil,
	}
	if errMsg != nil {
		gitResult.Error = errMsg
	}
	if filePayload == nil {
		return gitResult
	}

	if strings.EqualFold(
		filePayload.Operation,
		string(api.GitExecutionResultPayloadOperationWrite),
	) {
		gitResult.Payload.Operation = api.GitExecutionResultPayloadOperationWrite
	}

	rows := filePayload.Rows
	rowCount := filePayload.RowCount
	gitResult.Payload.Rows = &rows
	gitResult.Payload.RowCount = &rowCount
	gitResult.Payload.Format = api.GitExecutionResultPayloadFormat(filePayload.Format)
	gitResult.Payload.File = filePayloadMetadataToAPI(filePayload.File)
	gitResult.Payload.TargetPath = fileResultTargetPath(filePayload.TargetPath)
	gitResult.Payload.WrittenBytes = filePayload.WrittenBytes
	gitResult.Payload.WrittenRows = filePayload.WrittenRows
	if len(filePayload.ColumnNames) > 0 {
		gitResult.Payload.ColumnNames = &filePayload.ColumnNames
	}
	if filePayload.IsHeadlessCSV {
		isHeadlessCSV := true
		gitResult.Payload.IsHeadlessCsv = &isHeadlessCSV
	}
	if filePayload.IsHeadlessSpreadsheet {
		isHeadlessSpreadsheet := true
		gitResult.Payload.IsHeadlessSpreadsheet = &isHeadlessSpreadsheet
	}
	gitResult.Payload.Sheet = fileResultTargetPath(filePayload.Sheet)
	gitResult.Payload.Range = fileResultTargetPath(filePayload.Range)

	return gitResult
}

func buildS3ExecutionResult(
	vendorType string,
	durationMs int64,
	filePayload *result.FileResult,
	errMsg *string,
) api.S3ExecutionResult {
	s3Result := api.S3ExecutionResult{
		ResultType: api.S3ExecutionResultResultTypeS3,
		Type:       normalizedExecutionVendorType(vendorType, "s3"),
		Payload: struct {
			ColumnNames           *[]string                             `json:"column_names,omitempty"`
			File                  *api.FilePayloadMetadata              `json:"file,omitempty"`
			Format                api.S3ExecutionResultPayloadFormat    `json:"format"`
			IsHeadlessCsv         *bool                                 `json:"is_headless_csv,omitempty"`
			IsHeadlessSpreadsheet *bool                                 `json:"is_headless_spreadsheet,omitempty"`
			Operation             api.S3ExecutionResultPayloadOperation `json:"operation"`
			Range                 *string                               `json:"range,omitempty"`
			RowCount              *int                                  `json:"row_count,omitempty"`
			Rows                  *[]map[string]interface{}             `json:"rows,omitempty"`
			Sheet                 *string                               `json:"sheet,omitempty"`
			TargetPath            *string                               `json:"target_path,omitempty"`
			WrittenBytes          *int                                  `json:"written_bytes,omitempty"`
			WrittenRows           *int                                  `json:"written_rows,omitempty"`
		}{
			Operation: api.S3ExecutionResultPayloadOperationRead,
			Format:    api.S3ExecutionResultPayloadFormatJson,
		},
		DurationMs: int(durationMs),
		Success:    errMsg == nil,
	}
	if errMsg != nil {
		s3Result.Error = errMsg
	}
	if filePayload == nil {
		return s3Result
	}

	if strings.EqualFold(
		filePayload.Operation,
		string(api.S3ExecutionResultPayloadOperationWrite),
	) {
		s3Result.Payload.Operation = api.S3ExecutionResultPayloadOperationWrite
	}

	rows := filePayload.Rows
	rowCount := filePayload.RowCount
	s3Result.Payload.Rows = &rows
	s3Result.Payload.RowCount = &rowCount
	s3Result.Payload.Format = api.S3ExecutionResultPayloadFormat(filePayload.Format)
	s3Result.Payload.File = filePayloadMetadataToAPI(filePayload.File)
	s3Result.Payload.TargetPath = fileResultTargetPath(filePayload.TargetPath)
	s3Result.Payload.WrittenBytes = filePayload.WrittenBytes
	s3Result.Payload.WrittenRows = filePayload.WrittenRows
	if len(filePayload.ColumnNames) > 0 {
		s3Result.Payload.ColumnNames = &filePayload.ColumnNames
	}
	if filePayload.IsHeadlessCSV {
		isHeadlessCSV := true
		s3Result.Payload.IsHeadlessCsv = &isHeadlessCSV
	}
	if filePayload.IsHeadlessSpreadsheet {
		isHeadlessSpreadsheet := true
		s3Result.Payload.IsHeadlessSpreadsheet = &isHeadlessSpreadsheet
	}
	s3Result.Payload.Sheet = fileResultTargetPath(filePayload.Sheet)
	s3Result.Payload.Range = fileResultTargetPath(filePayload.Range)

	return s3Result
}

func fileResultTargetPath(path string) *string {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	return &path
}

// filePayloadMetadataToAPI converts internal file metadata without exposing raw bytes.
func filePayloadMetadataToAPI(metadata *filepayload.FilePayloadMetadata) *api.FilePayloadMetadata {
	if metadata == nil {
		return nil
	}

	apiMetadata := &api.FilePayloadMetadata{
		Digest:          metadata.Digest,
		DigestAlgorithm: metadata.DigestAlgorithm,
		MimeType:        metadata.MIMEType,
		SizeBytes:       metadata.SizeBytes,
	}
	if strings.TrimSpace(metadata.Filename) != "" {
		apiMetadata.Filename = ptr.Ptr(metadata.Filename)
	}

	return apiMetadata
}

func normalizedExecutionVendorType(vendorType, fallback string) string {
	if strings.TrimSpace(vendorType) == "" {
		return fallback
	}

	return vendorType
}

// queryParamToHTTP converts a service QueryParameter to the API QueryParameter type.
func queryParamToHTTP(p *queriessvc.QueryParameter) api.QueryParameter {
	var apiSlug *api.Slug
	if p.Slug != "" {
		s := api.Slug(p.Slug)
		apiSlug = &s
	}

	isRequired := p.IsRequired

	return api.QueryParameter{
		Slug:         apiSlug,
		Name:         p.Name,
		DataType:     api.QueryParameterDataType(p.DataType.String()),
		IsRequired:   &isRequired,
		DefaultValue: p.DefaultValue,
		Description:  p.Description,
	}
}

// apiParamsToService converts API QueryParameter slice to service QueryParameter slice.
func apiParamsToService(apiParams *[]api.QueryParameter) []*queriessvc.QueryParameter {
	if apiParams == nil {
		return nil
	}

	result := make([]*queriessvc.QueryParameter, 0, len(*apiParams))

	for _, p := range *apiParams {
		var paramSlug slug.Slug
		if p.Slug != nil && *p.Slug != "" {
			if s, err := slug.New(*p.Slug); err == nil {
				paramSlug = s
			}
		}

		isRequired := false
		if p.IsRequired != nil {
			isRequired = *p.IsRequired
		}

		dataType := queriessvc.ParameterDataTypeString(string(p.DataType))

		result = append(result, &queriessvc.QueryParameter{
			Slug:         paramSlug,
			Name:         p.Name,
			DataType:     dataType,
			IsRequired:   isRequired,
			DefaultValue: p.DefaultValue,
			Description:  p.Description,
		})
	}

	return result
}

// executionResultToHTTP converts a connector ExecutionResult to the API QueryExecutionResult type.
func executionResultToHTTP(r *result.ExecutionResult) (api.QueryExecutionResult, error) {
	var apiResult api.QueryExecutionResult
	if r == nil {
		return apiResult, xerror.Unexpected().Detail("execution result is nil")
	}

	switch r.Type() {
	case result.ExecutionResultTypeSQL:
		sqlData, ok := r.SQL()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid SQL result type")
		}
		result := api.SQLExecutionResult{
			ResultType: api.SQLExecutionResultResultTypeSql,
			Type:       r.VendorType(),
			Payload: struct {
				ColumnNames *[]string        `json:"column_names,omitempty"`
				RowCount    int              `json:"row_count"`
				Rows        []map[string]any `json:"rows"`
			}{
				Rows:        sqlData.Rows,
				RowCount:    sqlData.RowCount,
				ColumnNames: &sqlData.ColumnNames,
			},
			DurationMs: int(r.DurationMs()),
			Success:    r.IsSuccess(),
		}
		if r.Error() != nil {
			errMsg := r.Error().Error()
			result.Error = &errMsg
		}
		if err := apiResult.FromSQLExecutionResult(result); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeMongoDB:
		mongoData, ok := r.MongoDB()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid MongoDB result type")
		}
		result := api.MongoDBExecutionResult{
			ResultType: api.MongoDBExecutionResultResultTypeMongodb,
			Type:       r.VendorType(),
			Payload: struct {
				DeletedCount  *int64                                         `json:"deleted_count,omitempty"`
				DocumentCount *int                                           `json:"document_count,omitempty"`
				Documents     *[]map[string]any                              `json:"documents,omitempty"`
				InsertedId    any                                            `json:"inserted_id,omitempty"`
				MatchedCount  *int64                                         `json:"matched_count,omitempty"`
				ModifiedCount *int64                                         `json:"modified_count,omitempty"`
				OperationType api.MongoDBExecutionResultPayloadOperationType `json:"operation_type"`
				UpsertedCount *int64                                         `json:"upserted_count,omitempty"`
				UpsertedId    any                                            `json:"upserted_id,omitempty"`
			}{
				OperationType: api.MongoDBExecutionResultPayloadOperationType(
					mongoData.OperationType,
				),
				Documents:     &mongoData.Documents,
				DocumentCount: mongoData.DocumentCount,
				InsertedId:    mongoData.InsertedID,
				MatchedCount:  mongoData.MatchedCount,
				ModifiedCount: mongoData.ModifiedCount,
				DeletedCount:  mongoData.DeletedCount,
				UpsertedId:    mongoData.UpsertedID,
				UpsertedCount: mongoData.UpsertedCount,
			},
			DurationMs: int(r.DurationMs()),
			Success:    r.IsSuccess(),
		}
		if r.Error() != nil {
			errMsg := r.Error().Error()
			result.Error = &errMsg
		}
		if err := apiResult.FromMongoDBExecutionResult(result); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeHTTP:
		httpData, ok := r.HTTP()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid HTTP result type")
		}

		// Unmarshal the JSON body to interface{} for the API response
		var body any
		if len(httpData.Body) > 0 {
			if err := json.Unmarshal(httpData.Body, &body); err != nil {
				body = string(httpData.Body) // Fallback to string if not valid JSON
			}
		}

		result := api.HTTPExecutionResult{
			ResultType: api.HTTPExecutionResultResultTypeHttp,
			Type:       r.VendorType(),
			Payload: struct {
				Body       any                      `json:"body,omitempty"`
				File       *api.FilePayloadMetadata `json:"file,omitempty"`
				Headers    *map[string]string       `json:"headers,omitempty"`
				StatusCode int                      `json:"status_code"`
			}{
				StatusCode: httpData.StatusCode,
				Headers:    &httpData.Headers,
				Body:       body,
				File:       filePayloadMetadataToAPI(httpData.File),
			},
			DurationMs: int(r.DurationMs()),
			Success:    r.IsSuccess(),
		}
		if r.Error() != nil {
			errMsg := r.Error().Error()
			result.Error = &errMsg
		}
		if err := apiResult.FromHTTPExecutionResult(result); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeObjectRows:
		objectRowsData, ok := r.ObjectRows()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid object rows result type")
		}
		if objectRowsData == nil && r.Error() == nil {
			return apiResult, xerror.Unexpected().Detail("object rows result payload is nil")
		}

		objectRowsResult := buildObjectRowsExecutionResult(
			r.VendorType(),
			r.DurationMs(),
			objectRowsData,
			executionResultError(r.Error()),
		)
		if err := apiResult.FromObjectRowsExecutionResult(objectRowsResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeFTP:
		ftpData, ok := r.FTP()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid FTP result type")
		}
		if ftpData == nil && r.Error() == nil {
			return apiResult, xerror.Unexpected().Detail("FTP result payload is nil")
		}

		ftpResult := buildFTPExecutionResult(
			r.VendorType(),
			r.DurationMs(),
			ftpData,
			executionResultError(r.Error()),
		)
		if err := apiResult.FromFTPExecutionResult(ftpResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeGit:
		gitData, ok := r.Git()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid Git result type")
		}
		if gitData == nil && r.Error() == nil {
			return apiResult, xerror.Unexpected().Detail("Git result payload is nil")
		}

		gitResult := buildGitExecutionResult(
			r.VendorType(),
			r.DurationMs(),
			gitData,
			executionResultError(r.Error()),
		)
		if err := apiResult.FromGitExecutionResult(gitResult); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeWorkspaceFile:
		f, ok := r.File()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid Workspace file payload")
		}
		if f == nil && r.Error() == nil {
			return apiResult, xerror.Unexpected().Detail("Workspace file result payload is nil")
		}
		value := buildWorkspaceFileExecutionResult(r.VendorType(), r.DurationMs(), f, executionResultError(r.Error()))
		if err := apiResult.FromWorkspaceFileExecutionResult(value); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeS3:
		s3Data, ok := r.S3()
		if !ok {
			return apiResult, xerror.Unexpected().Detail("invalid S3 result type")
		}
		if s3Data == nil && r.Error() == nil {
			return apiResult, xerror.Unexpected().Detail("S3 result payload is nil")
		}

		s3Result := buildS3ExecutionResult(
			r.VendorType(),
			r.DurationMs(),
			s3Data,
			executionResultError(r.Error()),
		)
		if err := apiResult.FromS3ExecutionResult(s3Result); err != nil {
			return apiResult, xerror.Unexpected().With(err)
		}
		return apiResult, nil

	case result.ExecutionResultTypeUnknown:
		return apiResult, xerror.Unexpected().Detail("unknown result type")

	default:
		return apiResult, xerror.Unexpected().Detailf("unknown result type: %s", r.Type().String())
	}
}

func executionResultError(err error) *string {
	if err == nil {
		return nil
	}

	msg := err.Error()
	return &msg
}

func buildObjectRowsExecutionResult(
	vendorType string,
	durationMs int64,
	objectRowsPayload *result.ObjectRowsResult,
	errMsg *string,
) api.ObjectRowsExecutionResult {
	objectRowsResult := api.ObjectRowsExecutionResult{
		ResultType: api.ObjectRowsExecutionResultResultTypeObjectRows,
		Type:       normalizedExecutionVendorType(vendorType, "object_rows"),
		Payload: struct {
			ColumnNames      []string                 `json:"column_names"`
			HasMore          bool                     `json:"has_more"`
			NextCursor       *string                  `json:"next_cursor"`
			ObjectType       string                   `json:"object_type"`
			Operation        *string                  `json:"operation,omitempty"`
			ProviderMetadata *map[string]interface{}  `json:"provider_metadata,omitempty"`
			RowCount         int                      `json:"row_count"`
			Rows             []map[string]interface{} `json:"rows"`
		}{
			ObjectType:  "",
			ColumnNames: []string{},
			Rows:        []map[string]interface{}{},
		},
		DurationMs: int(durationMs),
		Success:    errMsg == nil,
	}
	if errMsg != nil {
		objectRowsResult.Error = errMsg
	}
	if objectRowsPayload == nil {
		return objectRowsResult
	}

	objectRowsResult.Payload.ColumnNames = objectRowsPayload.ColumnNames
	objectRowsResult.Payload.HasMore = objectRowsPayload.HasMore
	objectRowsResult.Payload.NextCursor = objectRowsPayload.NextCursor
	objectRowsResult.Payload.ObjectType = objectRowsPayload.ObjectType
	if strings.TrimSpace(objectRowsPayload.Operation) != "" {
		objectRowsResult.Payload.Operation = &objectRowsPayload.Operation
	}
	if len(objectRowsPayload.ProviderMetadata) > 0 {
		objectRowsResult.Payload.ProviderMetadata = &objectRowsPayload.ProviderMetadata
	}
	objectRowsResult.Payload.RowCount = objectRowsPayload.RowCount
	objectRowsResult.Payload.Rows = objectRowsPayload.Rows

	return objectRowsResult
}

func buildWorkspaceFileExecutionResult(vendor string, duration int64, f *result.FileResult, errMsg *string) api.WorkspaceFileExecutionResult {
	value := api.WorkspaceFileExecutionResult{ResultType: api.WorkspaceFileExecutionResultResultTypeWorkspaceFile, Type: normalizedExecutionVendorType(vendor, "google_workspace"), DurationMs: int(duration), Success: errMsg == nil, Error: errMsg}
	value.Payload.Operation = api.WorkspaceFileExecutionResultPayloadOperationRead
	value.Payload.Format = api.WorkspaceFileExecutionResultPayloadFormatJson
	if f == nil {
		return value
	}
	value.Payload.Operation = api.WorkspaceFileExecutionResultPayloadOperation(f.Operation)
	value.Payload.Format = api.WorkspaceFileExecutionResultPayloadFormat(f.Format)
	value.Payload.FileId = fileResultTargetPath(f.ResourceID)
	value.Payload.Name = fileResultTargetPath(f.Name)
	value.Payload.MimeType = fileResultTargetPath(f.MIMEType)
	value.Payload.TargetPath = fileResultTargetPath(f.TargetPath)
	value.Payload.Rows = &f.Rows
	value.Payload.RowCount = &f.RowCount
	value.Payload.WrittenBytes = f.WrittenBytes
	value.Payload.WrittenRows = f.WrittenRows
	value.Payload.File = filePayloadMetadataToAPI(f.File)
	if len(f.ColumnNames) > 0 {
		value.Payload.ColumnNames = &f.ColumnNames
	}
	if f.IsHeadlessCSV {
		value.Payload.IsHeadlessCsv = ptr.Ptr(true)
	}
	return value
}

func apiOperationContractRef(ref *api.PipelineODCSContractRef) (*pipelinespec.ODCSContractRef, bool, error) {
	if ref == nil {
		return nil, false, nil
	}
	contract, err := slug.New(ref.ContractSlug)
	if err != nil {
		return nil, false, err
	}
	revision, err := slug.New(ref.RevisionSlug)
	if err != nil {
		return nil, false, err
	}
	result := &pipelinespec.ODCSContractRef{ContractSlug: contract, RevisionSlug: revision, SchemaObjectID: ref.SchemaObjectId}
	if ref.Representation != nil {
		result.Representation, err = model.ContractRepresentationString(string(*ref.Representation))
		if err != nil {
			return nil, false, err
		}
	}
	if ref.RootShape != nil {
		result.RootShape, err = model.ContractRootShapeString(string(*ref.RootShape))
		if err != nil {
			return nil, false, err
		}
	}
	return result, true, nil
}

func operationContractRefToAPI(ref *pipelinespec.ODCSContractRef) *api.PipelineODCSContractRef {
	if ref == nil {
		return nil
	}
	result := &api.PipelineODCSContractRef{ContractSlug: ref.ContractSlug.String(), RevisionSlug: ref.RevisionSlug.String(), SchemaObjectId: ref.SchemaObjectID}
	if ref.Representation != model.ContractRepresentationUnknown {
		v := api.PipelineODCSContractRefRepresentation(ref.Representation.String())
		result.Representation = &v
	}
	if ref.RootShape != model.ContractRootShapeUnknown {
		v := api.PipelineODCSContractRefRootShape(ref.RootShape.String())
		result.RootShape = &v
	}
	return result
}
