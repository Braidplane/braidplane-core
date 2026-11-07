package hubspot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	hubspotclient "github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const (
	defaultResultObjectType = "contacts"
	defaultPageLimit        = 100
	maxResponseBodyBytes    = 10 * 1024 * 1024
)

type providerClient interface {
	AccountDetails(context.Context) (*hubspotclient.AccountDetails, error)
	ListProperties(context.Context, string) ([]hubspotclient.Property, error)
	ListCustomObjectSchemas(context.Context) ([]hubspotclient.CustomObjectSchema, error)
	List(context.Context, hubspotclient.ListRequest) (*hubspotclient.Page, error)
	Search(context.Context, hubspotclient.SearchRequest) (*hubspotclient.Page, error)
	Upsert(context.Context, hubspotclient.UpsertRequest) (*hubspotclient.UpsertResult, error)
	Close() error
}

type boundedPropertiesClient interface {
	ListPropertiesBounded(context.Context, string, int64) ([]hubspotclient.Property, error)
}

type boundedCustomObjectSchemasClient interface {
	ListCustomObjectSchemasBounded(context.Context, int64) ([]hubspotclient.CustomObjectSchema, error)
}

type Connector struct {
	client        providerClient
	grantedScopes map[string]struct{}
	enforceScopes bool
}

// New constructs a HubSpot connector, preferring an OAuth access token over a static private-app token.
func New(
	ctx context.Context,
	config hubspotclient.Config,
	creds Credentials,
) (*Connector, error) {
	accessToken := strings.TrimSpace(creds.AccessToken)
	if accessToken == "" {
		accessToken = strings.TrimSpace(creds.PrivateAppToken)
	}

	client, err := hubspotclient.New(ctx, config, accessToken)
	if err != nil {
		return nil, err
	}

	return newWithClientAndScopePolicy(
		client,
		creds.GrantedScopes,
		strings.TrimSpace(creds.AccessToken) != "",
	), nil
}

// newWithClient constructs a connector for tests and static credentials without OAuth scope restrictions.
func newWithClient(client providerClient) *Connector {
	return newWithClientAndScopes(client, nil)
}

// newWithClientAndScopes normalizes granted scopes into connector authorization state.
func newWithClientAndScopes(client providerClient, scopes []string) *Connector {
	return newWithClientAndScopePolicy(client, scopes, scopes != nil)
}

func newWithClientAndScopePolicy(
	client providerClient,
	scopes []string,
	enforceScopes bool,
) *Connector {
	grantedScopes := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		grantedScopes[strings.TrimSpace(scope)] = struct{}{}
	}
	return &Connector{
		client:        client,
		grantedScopes: grantedScopes,
		enforceScopes: enforceScopes,
	}
}

func (c *Connector) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func (c *Connector) TestConnection(ctx context.Context) error {
	_, err := c.client.AccountDetails(ctx)
	return err
}

func (c *Connector) IntrospectSchema(ctx context.Context) (*Schema, error) {
	objects := hubspotclient.SupportedObjects()
	schema := &Schema{
		Objects: make([]ObjectSchema, 0, len(objects)),
	}

	for _, object := range objects {
		// Introspection omits unauthorized objects so schema suggestions do not promise inaccessible CRM data.
		if err := c.requireObjectScope(object.Name); err != nil {
			continue
		}
		if err := schemaquota.Charge(ctx, 1, len(object.Name)+len(object.Label)); err != nil {
			return nil, err
		}
		var properties []hubspotclient.Property
		var err error
		if bounded, ok := c.client.(boundedPropertiesClient); ok {
			remainingBytes := schemaquota.RemainingBytes(ctx, maxResponseBodyBytes)
			if remainingBytes <= 0 {
				return nil, schemaquota.Charge(ctx, 0, 1)
			}
			properties, err = bounded.ListPropertiesBounded(
				ctx,
				object.Name,
				int64(remainingBytes),
			)
		} else {
			properties, err = c.client.ListProperties(ctx, object.Name)
		}
		if err != nil {
			return nil, err
		}

		schemaObject := ObjectSchema{
			Name:       object.Name,
			Label:      object.Label,
			Properties: make([]PropertySchema, 0, len(properties)),
			Operations: c.supportedOperationsSchema(object.Name),
		}

		for _, property := range properties {
			if err := schemaquota.Charge(ctx, 0, len(property.Name)+len(property.Label)+len(property.Type)); err != nil {
				return nil, err
			}
			schemaObject.Properties = append(schemaObject.Properties, propertySchema(property))
		}

		schema.Objects = append(schema.Objects, schemaObject)
	}

	if c.hasObjectScope(customObjectReadScope) {
		customObjects, err := c.listCustomObjectSchemas(ctx, maxResponseBodyBytes)
		if err != nil {
			return nil, err
		}
		for _, object := range customObjects {
			if err := schemaquota.Charge(
				ctx,
				1,
				len(object.ObjectTypeID)+len(object.Name)+len(object.Label),
			); err != nil {
				return nil, err
			}
			schemaObject := ObjectSchema{
				Name:       object.ObjectTypeID,
				Label:      object.Label,
				Properties: make([]PropertySchema, 0, len(object.Properties)),
				Operations: c.supportedOperationsSchema(object.ObjectTypeID),
				ProviderMetadata: map[string]any{
					"custom":         true,
					"object_type_id": object.ObjectTypeID,
					"provider_name":  object.Name,
				},
			}
			for _, property := range object.Properties {
				if err := schemaquota.Charge(
					ctx,
					0,
					len(property.Name)+len(property.Label)+len(property.Type),
				); err != nil {
					return nil, err
				}
				schemaObject.Properties = append(
					schemaObject.Properties,
					propertySchema(property),
				)
			}
			schema.Objects = append(schema.Objects, schemaObject)
		}
	}

	return schema, nil
}

func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	execLimits := types.ResolveExecutionLimits(limits...)
	maxResponseBytes := effectiveHubSpotMaxResponseBytes(execLimits)
	start := time.Now()

	normalizedParams, err := types.NormalizeParams(params)
	if err != nil {
		return hubSpotFailureResult(
			start,
			"",
			"",
			nil,
			xerror.Wrapf("parameter normalization failed: %w", err),
		), nil
	}

	var statement statement
	if err := json.Unmarshal(queryData, &statement); err != nil {
		return hubSpotFailureResult(
			start,
			"",
			"",
			nil,
			xerror.Unexpected().Detailf("invalid hubspot query data: %w", err),
		), nil
	}

	objectType := strings.TrimSpace(statement.ObjectType)
	customObjects, err := c.validateObjectType(ctx, objectType, maxResponseBytes)
	if err != nil {
		return hubSpotFailureResult(start, objectType, "", nil, err), nil
	}
	// Execution returns a remediation error for an unauthorized object instead of silently changing the request.
	if err := c.requireObjectScope(objectType); err != nil {
		return hubSpotFailureResult(start, objectType, "", nil, err), nil
	}

	operation := normalizeOperation(statement)
	if operation == "" {
		return hubSpotFailureResult(
			start,
			objectType,
			"",
			nil,
			xerror.InvalidInput(xerror.Query).Detail("hubspot operation must be list, search, or upsert"),
		), nil
	}
	if operation == OperationUpsert {
		if err := c.requireObjectWriteScope(objectType); err != nil {
			return hubSpotFailureResult(start, objectType, operation, nil, err), nil
		}
		return c.executeUpsert(
			ctx,
			start,
			objectType,
			statement,
			normalizedParams,
			execLimits,
			maxResponseBytes,
			customObjects,
		), nil
	}

	pageLimit := defaultPageLimit
	if statement.Limit != nil && *statement.Limit > 0 {
		pageLimit = *statement.Limit
	}
	if execLimits.MaxResultRows > 0 {
		limit := execLimits.MaxResultRows + 1
		if pageLimit <= 0 || pageLimit > limit {
			pageLimit = limit
		}
	}

	properties, err := substituteProperties(statement.Properties, normalizedParams)
	if err != nil {
		return hubSpotFailureResult(
			start,
			objectType,
			operation,
			nil,
			xerror.Wrapf("resolve hubspot properties: %w", err),
		), nil
	}

	var page *hubspotclient.Page
	switch operation {
	case operationList:
		page, err = c.executeList(
			ctx,
			objectType,
			statement,
			normalizedParams,
			pageLimit,
			properties,
			maxResponseBytes,
		)
	case operationSearch:
		page, err = c.executeSearch(
			ctx,
			objectType,
			statement,
			normalizedParams,
			pageLimit,
			properties,
			maxResponseBytes,
		)
	default:
		err = xerror.InvalidInput(xerror.Query).Detailf(
			"unsupported HubSpot operation %q",
			operation,
		)
	}
	if err != nil {
		return hubSpotFailureResult(start, objectType, operation, properties, err), nil
	}
	if execLimits.MaxResultRows > 0 && len(page.Records) > execLimits.MaxResultRows {
		return hubSpotFailureResult(
			start,
			objectType,
			operation,
			properties,
			xerror.ExecutionRowLimitExceeded().Detailf(
				"HubSpot operation returned more than %d rows",
				execLimits.MaxResultRows,
			),
		), nil
	}

	columnNames := buildColumnNames(properties, page.Records)
	rows := make([]map[string]any, 0, len(page.Records))
	for _, item := range page.Records {
		row := make(map[string]any, len(columnNames))
		row[RecordIDFieldName] = item.ID
		for _, column := range columnNames[1:] {
			row[column] = item.Properties[column]
		}
		rows = append(rows, row)
	}

	return result.NewObjectRowsResult(
		"hubspot",
		&result.ObjectRowsResult{
			ObjectType:  objectType,
			Operation:   operation,
			Rows:        rows,
			ColumnNames: columnNames,
			RowCount:    len(rows),
			NextCursor:  page.NextAfter,
			HasMore:     page.HasMore,
		},
		time.Since(start).Milliseconds(),
		nil,
	), nil
}

func hubSpotFailureResult(
	start time.Time,
	objectType string,
	operation string,
	properties []string,
	err error,
) *result.ExecutionResult {
	return result.NewObjectRowsResult(
		"hubspot",
		&result.ObjectRowsResult{
			ObjectType:  hubSpotResultObjectType(objectType),
			Operation:   operation,
			Rows:        []map[string]any{},
			ColumnNames: buildColumnNames(properties, nil),
			RowCount:    0,
			NextCursor:  nil,
			HasMore:     false,
		},
		time.Since(start).Milliseconds(),
		err,
	)
}

func hubSpotResultObjectType(objectType string) string {
	trimmed := strings.TrimSpace(objectType)
	if trimmed != "" {
		return trimmed
	}

	return defaultResultObjectType
}

// requireObjectScope rejects OAuth operations that the granted scope set does not authorize.
func (c *Connector) requireObjectScope(objectType string) error {
	if c.hasObjectScope(requiredReadScope(objectType)) {
		return nil
	}
	requiredScope := requiredReadScope(objectType)
	return xerror.InvalidInput(xerror.Query).Detailf(
		"HubSpot %s requires OAuth scope %q; reinstall the data source to request it",
		objectType,
		requiredScope,
	)
}

func (c *Connector) requireObjectWriteScope(objectType string) error {
	requiredScope := requiredWriteScope(objectType)
	if c.hasObjectScope(requiredScope) {
		return nil
	}
	return xerror.InvalidInput(xerror.Query).Detailf(
		"HubSpot %s upsert requires OAuth scope %q; reinstall the data source to request it",
		objectType,
		requiredScope,
	)
}

const (
	customObjectReadScope  = "crm.objects.custom.read"
	customObjectWriteScope = "crm.objects.custom.write"
)

func requiredReadScope(objectType string) string {
	if hubspotclient.IsCustomObjectTypeID(objectType) {
		return customObjectReadScope
	}
	return "crm.objects." + strings.TrimSpace(objectType) + ".read"
}

func requiredWriteScope(objectType string) string {
	if hubspotclient.IsCustomObjectTypeID(objectType) {
		return customObjectWriteScope
	}
	return "crm.objects." + strings.TrimSpace(objectType) + ".write"
}

func (c *Connector) hasObjectScope(requiredScope string) bool {
	if !c.enforceScopes {
		return true
	}
	_, ok := c.grantedScopes[requiredScope]
	return ok
}

func (c *Connector) validateObjectType(
	ctx context.Context,
	objectType string,
	maxResponseBytes int64,
) ([]hubspotclient.CustomObjectSchema, error) {
	if hubspotclient.IsStandardObject(objectType) {
		return nil, nil
	}
	if !hubspotclient.IsCustomObjectTypeID(objectType) {
		return nil, xerror.InvalidInput(xerror.Query).Detailf(
			"unsupported HubSpot object type %q",
			objectType,
		)
	}
	if err := c.requireObjectScope(objectType); err != nil {
		return nil, err
	}
	customObjects, err := c.listCustomObjectSchemas(ctx, maxResponseBytes)
	if err != nil {
		return nil, xerror.Wrapf("discover HubSpot custom object schemas: %w", err)
	}
	for _, object := range customObjects {
		if object.ObjectTypeID == objectType {
			return customObjects, nil
		}
	}
	return nil, xerror.InvalidInput(xerror.Query).Detailf(
		"unsupported HubSpot custom object type %q; refresh the data source schema",
		objectType,
	)
}

func (c *Connector) listCustomObjectSchemas(
	ctx context.Context,
	maxResponseBytes int64,
) ([]hubspotclient.CustomObjectSchema, error) {
	if bounded, ok := c.client.(boundedCustomObjectSchemasClient); ok {
		remainingBytes := schemaquota.RemainingBytes(ctx, int(maxResponseBytes))
		if remainingBytes <= 0 {
			return nil, schemaquota.Charge(ctx, 0, 1)
		}
		return bounded.ListCustomObjectSchemasBounded(ctx, int64(remainingBytes))
	}
	return c.client.ListCustomObjectSchemas(ctx)
}

func propertySchema(property hubspotclient.Property) PropertySchema {
	metadata := map[string]any{}
	if property.HasUniqueValue {
		metadata["has_unique_value"] = true
	}
	if property.ReadOnlyValue {
		metadata["read_only_value"] = true
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return PropertySchema{
		Name:             property.Name,
		Label:            property.Label,
		Type:             property.Type,
		ProviderMetadata: metadata,
	}
}

func (c *Connector) supportedOperationsSchema(objectType string) []OperationSchema {
	operations := []OperationSchema{
		{Name: operationList, Label: "List"},
		{Name: operationSearch, Label: "Search"},
	}
	if c.hasObjectScope(requiredWriteScope(objectType)) {
		operations = append(operations, OperationSchema{Name: OperationUpsert, Label: "Upsert"})
	}
	return operations
}

func (c *Connector) executeList(
	ctx context.Context,
	objectType string,
	statement statement,
	params map[string]any,
	pageLimit int,
	properties []string,
	maxResponseBytes int64,
) (*hubspotclient.Page, error) {
	request := hubspotclient.ListRequest{
		ObjectType:       objectType,
		Properties:       append([]string(nil), properties...),
		Limit:            pageLimit,
		MaxResponseBytes: maxResponseBytes,
	}

	if statement.After != nil {
		after, err := substituteOptionalString(statement.After, params)
		if err != nil {
			return nil, xerror.Wrapf("resolve hubspot pagination cursor: %w", err)
		}
		request.After = after
	}

	return c.client.List(ctx, request)
}

func (c *Connector) executeSearch(
	ctx context.Context,
	objectType string,
	statement statement,
	params map[string]any,
	pageLimit int,
	properties []string,
	maxResponseBytes int64,
) (*hubspotclient.Page, error) {
	request := hubspotclient.SearchRequest{
		ObjectType:       objectType,
		Properties:       append([]string(nil), properties...),
		Limit:            pageLimit,
		MaxResponseBytes: maxResponseBytes,
	}

	if statement.Query != nil {
		value, err := param.SubstituteValue(*statement.Query, params)
		if err != nil {
			return nil, xerror.Wrapf("resolve hubspot query: %w", err)
		}
		request.Query = toString(value)
	}

	if statement.After != nil {
		after, err := substituteOptionalString(statement.After, params)
		if err != nil {
			return nil, xerror.Wrapf("resolve hubspot pagination cursor: %w", err)
		}
		request.After = after
	}

	if len(statement.Sorts) > 0 {
		sorts := make([]string, 0, len(statement.Sorts))
		for _, rawSort := range statement.Sorts {
			resolvedSort, err := param.SubstituteString(rawSort, params)
			if err != nil {
				return nil, xerror.Wrapf("resolve hubspot sorts: %w", err)
			}
			resolvedSort = strings.TrimSpace(resolvedSort)
			if resolvedSort == "" {
				continue
			}
			sorts = append(sorts, resolvedSort)
		}
		request.Sorts = sorts
	}

	if len(statement.FilterGroups) > 0 {
		filterGroups, err := resolveFilterGroups(statement.FilterGroups, params)
		if err != nil {
			return nil, err
		}
		request.FilterGroups = filterGroups
	}

	return c.client.Search(ctx, request)
}

func (c *Connector) executeUpsert(
	ctx context.Context,
	start time.Time,
	objectType string,
	statement statement,
	params map[string]any,
	limits types.ExecutionLimits,
	maxResponseBytes int64,
	customObjects []hubspotclient.CustomObjectSchema,
) *result.ExecutionResult {
	rows, err := resolveUpsertRows(statement.Rows, params)
	if err != nil {
		return hubSpotFailureResult(start, objectType, OperationUpsert, nil, err)
	}
	if limits.MaxResultRows > 0 && len(rows) > limits.MaxResultRows {
		return hubSpotFailureResult(
			start,
			objectType,
			OperationUpsert,
			nil,
			xerror.ExecutionRowLimitExceeded().Detailf(
				"HubSpot upsert received more than %d rows",
				limits.MaxResultRows,
			),
		)
	}

	properties, err := c.objectProperties(ctx, objectType, maxResponseBytes, customObjects)
	if err != nil {
		return hubSpotFailureResult(start, objectType, OperationUpsert, nil, err)
	}
	inputs, err := buildUpsertInputs(objectType, statement, rows, properties)
	if err != nil {
		return hubSpotFailureResult(start, objectType, OperationUpsert, nil, err)
	}

	outputRows := make([]map[string]any, 0, len(inputs))
	for offset := 0; offset < len(inputs); offset += hubspotclient.MaxBatchUpsertInputs {
		end := min(offset+hubspotclient.MaxBatchUpsertInputs, len(inputs))
		upsertResult, upsertErr := c.client.Upsert(ctx, hubspotclient.UpsertRequest{
			ObjectType:       objectType,
			Inputs:           inputs[offset:end],
			MaxResponseBytes: maxResponseBytes,
		})
		if upsertErr != nil {
			if offset > 0 {
				upsertErr = xerror.Wrapf(
					"HubSpot upsert stopped after a prior batch completed; verify provider state before replay: %w",
					upsertErr,
				)
			}
			return hubSpotFailureResult(start, objectType, OperationUpsert, nil, upsertErr)
		}
		for _, record := range upsertResult.Records {
			outputRows = append(outputRows, map[string]any{
				RecordIDFieldName: record.ID,
				"created":         record.Created,
			})
		}
	}

	return result.NewObjectRowsResult(
		"hubspot",
		&result.ObjectRowsResult{
			ObjectType:  objectType,
			Operation:   OperationUpsert,
			Rows:        outputRows,
			ColumnNames: []string{RecordIDFieldName, "created"},
			RowCount:    len(outputRows),
			HasMore:     false,
		},
		time.Since(start).Milliseconds(),
		nil,
	)
}

func (c *Connector) objectProperties(
	ctx context.Context,
	objectType string,
	maxResponseBytes int64,
	customObjects []hubspotclient.CustomObjectSchema,
) ([]hubspotclient.Property, error) {
	if hubspotclient.IsStandardObject(objectType) {
		if bounded, ok := c.client.(boundedPropertiesClient); ok {
			return bounded.ListPropertiesBounded(ctx, objectType, maxResponseBytes)
		}
		return c.client.ListProperties(ctx, objectType)
	}
	for _, object := range customObjects {
		if object.ObjectTypeID == objectType {
			return object.Properties, nil
		}
	}
	return nil, xerror.InvalidInput(xerror.Query).Detailf(
		"unsupported HubSpot custom object type %q; refresh the data source schema",
		objectType,
	)
}

func resolveUpsertRows(raw json.RawMessage, params map[string]any) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot upsert rows are required")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, xerror.InvalidInput(xerror.Query).Detailf("invalid hubspot rows payload: %w", err)
	}
	resolved, err := param.SubstituteValue(value, params)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.Query).Detailf("resolve hubspot rows: %w", err)
	}
	switch typed := resolved.(type) {
	case []map[string]any:
		return append([]map[string]any(nil), typed...), nil
	case []any:
		rows := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot upsert rows must resolve to row objects")
			}
			rows = append(rows, row)
		}
		return rows, nil
	case map[string]any:
		return []map[string]any{typed}, nil
	default:
		return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot upsert rows must resolve to row objects")
	}
}

func buildUpsertInputs(
	objectType string,
	statement statement,
	rows []map[string]any,
	properties []hubspotclient.Property,
) ([]hubspotclient.UpsertInput, error) {
	idProperty := strings.TrimSpace(statement.IDProperty)
	if idProperty == "" {
		return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot upsert id_property is required")
	}
	if len(statement.PropertyMapping) == 0 {
		return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot upsert property_mapping is required")
	}
	if len(rows) == 0 {
		return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot upsert requires at least one row")
	}

	propertyByName := make(map[string]hubspotclient.Property, len(properties))
	for _, property := range properties {
		propertyByName[property.Name] = property
	}
	idSchema, ok := propertyByName[idProperty]
	if !ok {
		return nil, xerror.InvalidInput(xerror.Query).Detailf("unknown HubSpot id property %q", idProperty)
	}
	if !idSchema.HasUniqueValue && (objectType != "contacts" || idProperty != "email") {
		return nil, xerror.InvalidInput(xerror.Query).Detailf(
			"HubSpot id property %q must have a unique-value constraint",
			idProperty,
		)
	}

	targets := make(map[string]string, len(statement.PropertyMapping))
	idInputField := ""
	for rawInputField, rawTarget := range statement.PropertyMapping {
		inputField := strings.TrimSpace(rawInputField)
		target := strings.TrimSpace(rawTarget)
		if inputField == "" || target == "" {
			return nil, xerror.InvalidInput(xerror.Query).Detail("hubspot property_mapping fields must not be empty")
		}
		if inputField != rawInputField || target != rawTarget {
			return nil, xerror.InvalidInput(xerror.Query).Detail(
				"hubspot property_mapping fields must not contain surrounding whitespace",
			)
		}
		if prior, exists := targets[target]; exists {
			return nil, xerror.InvalidInput(xerror.Query).Detailf(
				"HubSpot property %q is mapped from both %q and %q",
				target,
				prior,
				inputField,
			)
		}
		property, exists := propertyByName[target]
		if !exists {
			return nil, xerror.InvalidInput(xerror.Query).Detailf("unknown HubSpot property %q", target)
		}
		if property.ReadOnlyValue {
			return nil, xerror.InvalidInput(xerror.Query).Detailf("HubSpot property %q is read-only", target)
		}
		targets[target] = inputField
		if target == idProperty {
			idInputField = inputField
		}
	}
	if idInputField == "" {
		return nil, xerror.InvalidInput(xerror.Query).Detailf(
			"hubspot property_mapping must map one input field to id_property %q",
			idProperty,
		)
	}

	seenIDs := make(map[string]struct{}, len(rows))
	inputs := make([]hubspotclient.UpsertInput, 0, len(rows))
	for rowIndex, row := range rows {
		for inputField := range row {
			if _, mapped := statement.PropertyMapping[inputField]; !mapped {
				return nil, xerror.InvalidInput(xerror.Query).Detailf(
					"hubspot upsert row %d contains unmapped input field %q",
					rowIndex+1,
					inputField,
				)
			}
		}
		rawID, exists := row[idInputField]
		idValue, scalar := scalarUpsertID(rawID)
		if !exists || !scalar || idValue == "" {
			return nil, xerror.InvalidInput(xerror.Query).Detailf(
				"hubspot upsert row %d is missing id field %q",
				rowIndex+1,
				idInputField,
			)
		}
		if _, duplicate := seenIDs[idValue]; duplicate {
			return nil, xerror.InvalidInput(xerror.Query).Detailf(
				"hubspot upsert input contains duplicate id_property value in row %d",
				rowIndex+1,
			)
		}
		seenIDs[idValue] = struct{}{}
		values := make(map[string]any, len(statement.PropertyMapping)-1)
		for inputField, target := range statement.PropertyMapping {
			if target == idProperty {
				continue
			}
			if value, exists := row[inputField]; exists {
				values[target] = value
			}
		}
		inputs = append(inputs, hubspotclient.UpsertInput{
			ID:         idValue,
			IDProperty: idProperty,
			Properties: values,
		})
	}
	return inputs, nil
}

func scalarUpsertID(value any) (string, bool) {
	switch typed := value.(type) {
	case string, bool, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return strings.TrimSpace(fmt.Sprint(value)), true
	case float32:
		return scalarFloatUpsertID(float64(typed), 32, 1<<24-1)
	case float64:
		return scalarFloatUpsertID(typed, 64, 1<<53-1)
	default:
		return "", false
	}
}

func scalarFloatUpsertID(value float64, bitSize int, maxExactInteger float64) (string, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		math.Abs(value) > maxExactInteger {
		return "", false
	}

	return strconv.FormatFloat(value, 'f', -1, bitSize), true
}

func effectiveHubSpotMaxResponseBytes(limits types.ExecutionLimits) int64 {
	if limits.MaxResponseBytes > 0 {
		return limits.MaxResponseBytes
	}
	return maxResponseBodyBytes
}

func normalizeOperation(statement statement) string {
	if statement.Operation == "" {
		if statement.Query != nil || len(statement.FilterGroups) > 0 || len(statement.Sorts) > 0 {
			return operationSearch
		}
		return operationList
	}

	switch strings.ToLower(strings.TrimSpace(statement.Operation)) {
	case operationList:
		return operationList
	case operationSearch:
		return operationSearch
	case OperationUpsert:
		return OperationUpsert
	default:
		return ""
	}
}

func substituteProperties(raw []string, params map[string]any) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	properties := make([]string, 0, len(raw))
	for _, property := range raw {
		resolved, err := param.SubstituteString(property, params)
		if err != nil {
			return nil, err
		}
		resolved = strings.TrimSpace(resolved)
		if resolved == "" {
			continue
		}
		properties = append(properties, resolved)
	}

	return properties, nil
}

func substituteOptionalString(raw *string, params map[string]any) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, err := param.SubstituteValue(*raw, params)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(toString(value)), nil
}

func resolveFilterGroups(
	groups []filterGroup,
	params map[string]any,
) ([]hubspotclient.FilterGroup, error) {
	resolvedGroups := make([]hubspotclient.FilterGroup, 0, len(groups))
	for _, group := range groups {
		resolvedFilters := make([]hubspotclient.Filter, 0, len(group.Filters))
		for _, filter := range group.Filters {
			resolvedFilter := hubspotclient.Filter{
				PropertyName: filter.PropertyName,
				Operator:     filter.Operator,
			}

			if filter.Value != nil {
				value, err := param.SubstituteValue(filter.Value, params)
				if err != nil {
					return nil, xerror.Wrapf("resolve hubspot filter value: %w", err)
				}
				resolvedFilter.Value = value
			}

			if filter.HighValue != nil {
				value, err := param.SubstituteValue(filter.HighValue, params)
				if err != nil {
					return nil, xerror.Wrapf("resolve hubspot filter high value: %w", err)
				}
				resolvedFilter.HighValue = value
			}

			if len(filter.Values) > 0 {
				values, err := param.SubstituteInArray(slices.Clone(filter.Values), params)
				if err != nil {
					return nil, xerror.Wrapf("resolve hubspot filter values: %w", err)
				}
				resolvedFilter.Values = values
			}

			resolvedFilters = append(resolvedFilters, resolvedFilter)
		}

		resolvedGroups = append(resolvedGroups, hubspotclient.FilterGroup{
			Filters: resolvedFilters,
		})
	}

	return resolvedGroups, nil
}

func buildColumnNames(requestedProperties []string, records []hubspotclient.Record) []string {
	if len(requestedProperties) > 0 {
		columnNames := make([]string, 0, len(requestedProperties)+1)
		columnNames = append(columnNames, RecordIDFieldName)
		columnNames = append(columnNames, requestedProperties...)
		return columnNames
	}

	columnSet := make(map[string]struct{})
	for _, record := range records {
		for key := range record.Properties {
			columnSet[key] = struct{}{}
		}
	}

	columnNames := make([]string, 0, len(columnSet)+1)
	columnNames = append(columnNames, RecordIDFieldName)
	for key := range columnSet {
		columnNames = append(columnNames, key)
	}
	sort.Strings(columnNames[1:])

	return columnNames
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
