package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/model"
	transformfixture "github.com/Braidplane/braidplane-core/pkg/pipeline/fixture"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type builderMappingRequest struct {
	Action         string                                           `json:"action"`
	SQL            string                                           `json:"sql,omitempty"`
	Mapping        *transformsql.FieldMapping                       `json:"mapping,omitempty"`
	Source         *payload.Contract                                `json:"source,omitempty"`
	Destination    *payload.Contract                                `json:"destination,omitempty"`
	FixtureJSON    string                                           `json:"fixture_json,omitempty"`
	InputContracts map[string]*pipelinespec.InputContractAttachment `json:"input_contracts,omitempty"`
	Step           string                                           `json:"step,omitempty"`
	OutputKind     model.TransformOutputKind                        `json:"output_kind,omitempty"`
	Org            string                                           `json:"org,omitempty"`
	Project        string                                           `json:"project,omitempty"`
	SourceRef      *pipelinespec.ODCSContractRef                    `json:"source_ref,omitempty"`
	DestinationRef *pipelinespec.ODCSContractRef                    `json:"destination_ref,omitempty"`
}

// This SSR adapter edits submitted SQL only. Schema and ODCS reads use their
// existing authorized API endpoints; editing performs no tenant reads or writes.
func (r *Router) handleBuilderMapping(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	var request builderMappingRequest
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if len(c.Body()) > 4<<20 || decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Enter a mapping request of at most 4 MiB with only documented fields."})
	}
	failure := func(err error) error {
		code := "PIPELINE_MAPPING_INVALID"
		var typed *xerror.Error
		if xerror.As(err, &typed) {
			code = typed.Code()
		}
		if request.Action == "fixture" {
			return builderFixtureResponse(c, http.StatusUnprocessableEntity, transformfixture.Failure(request.Step, code,
				"Supply valid fixture inputs and select one inline shape or exact reference per port."))
		}
		return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{
			"valid": false, "diagnostics": []fiber.Map{{"code": code, "message": err.Error()}},
		})
	}
	var fixture transformfixture.Fixture
	ctx := c.Context()
	if request.Action == "fixture" {
		var err error
		fixture, err = transformfixture.Decode([]byte(request.FixtureJSON))
		if err != nil {
			return builderFixtureResponse(c, http.StatusUnprocessableEntity, transformfixture.Failure(request.Step, "TRANSFORM_FIXTURE_INVALID", err.Error()))
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if request.InputContracts != nil {
			if len(request.InputContracts) == 0 || len(request.InputContracts) > 64 || request.Source != nil || request.SourceRef != nil {
				return failure(xerror.ValidationFailed().Detail("use either a combined input contract or up to 64 named input contracts"))
			}
		}
	}
	type contractPort struct {
		ref   *pipelinespec.ODCSContractRef
		shape **payload.Contract
	}
	ports := []contractPort{{request.SourceRef, &request.Source}, {request.DestinationRef, &request.Destination}}
	if request.Action == "fixture" {
		for name, input := range request.InputContracts {
			if _, exists := fixture.Inputs[name]; !exists || input == nil || (input.Contract == nil && input.ContractRef == nil) {
				return failure(xerror.ValidationFailed().Detail("supply a fixture input for every named contract"))
			}
			ports = append(ports, contractPort{input.ContractRef, &input.Contract})
		}
	}
	for _, port := range ports {
		if port.ref != nil && *port.shape != nil {
			return failure(xerror.ValidationFailed().Detail("choose one inline shape or exact reference per mapping port"))
		}
	}
	for _, port := range ports {
		if port.ref == nil {
			continue
		}
		shape, status, err := r.resolveBuilderMappingContract(ctx, c.Get(fiber.HeaderCookie), request.Org, request.Project, port.ref)
		if err != nil {
			if request.Action == "fixture" {
				return builderFixtureResponse(c, status, transformfixture.Failure(request.Step, "MAPPING_CONTRACT_UNAVAILABLE", err.Error()))
			}
			return c.Status(status).JSON(fiber.Map{"valid": false, "diagnostics": []fiber.Map{{"code": "MAPPING_CONTRACT_UNAVAILABLE", "message": err.Error()}}})
		}
		*port.shape = shape
	}
	switch request.Action {
	case "inspect":
		mapping, err := transformsql.ReadFieldMapping(request.SQL)
		if err != nil {
			return failure(err)
		}
		return c.JSON(fiber.Map{"mapping": mapping})
	case "render":
		if request.Mapping == nil {
			return failure(transformsql.ErrFieldMappingUnsupported.Detail("supply fields to edit"))
		}
		query, err := transformsql.WriteFieldMapping(*request.Mapping)
		if err != nil {
			return failure(err)
		}
		return c.JSON(fiber.Map{"sql": query})
	case "suggest":
		for _, contract := range []*payload.Contract{request.Source, request.Destination} {
			if err := transformsql.ValidateFieldMappingSchema(contract); err != nil {
				return failure(err)
			}
		}
		if request.Mapping == nil {
			return failure(transformsql.ErrFieldMappingUnsupported.Detail("select a transform input"))
		}
		if err := transformsql.ValidateTransformInputRelationName(request.Mapping.Input); err != nil {
			return failure(err)
		}
		return c.JSON(fiber.Map{"mapping": transformsql.SuggestFieldMapping(request.Mapping.Input, request.Source, request.Destination)})
	case "check":
		if err := transformsql.ValidateFieldMappingContracts(request.SQL, request.OutputKind, request.Source, request.Destination); err != nil {
			return failure(err)
		}
		return c.JSON(fiber.Map{"valid": true, "scope": "explicit_contract_shapes"})
	case "fixture":
		transform := transformsql.Transform{SQL: request.SQL, OutputKind: request.OutputKind, InputContract: request.Source, OutputContract: request.Destination}
		if request.InputContracts != nil {
			transform.InputContracts = make(map[string]*payload.Contract, len(request.InputContracts))
			for name, input := range request.InputContracts {
				transform.InputContracts[name] = input.Contract
			}
		}
		result := transformfixture.Run(ctx, request.Step, transform, fixture)
		if !result.Passed {
			return builderFixtureResponse(c, http.StatusUnprocessableEntity, result)
		}
		return builderFixtureResponse(c, http.StatusOK, result)
	default:
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Choose inspect, render, suggest, check or fixture."})
	}
}

func builderFixtureResponse(c fiber.Ctx, status int, result transformfixture.Result) error {
	return c.Status(status).JSON(fiber.Map{
		"passed": result.Passed, "asserted": result.Asserted, "step": result.Step, "scope": result.Scope,
		"output_json": string(result.Output), "columns": result.Columns, "diagnostics": result.Diagnostics,
	})
}

func (r *Router) resolveBuilderMappingContract(ctx context.Context, cookie, org, project string, ref *pipelinespec.ODCSContractRef) (*payload.Contract, int, error) {
	for _, value := range []string{org, project, ref.ContractSlug.String(), ref.RevisionSlug.String()} {
		if _, err := slug.New(value); err != nil {
			return nil, http.StatusBadRequest, xerror.ValidationFailed().Detail("select valid organization, project and exact contract revision identifiers")
		}
	}
	if ref.RevisionSlug.String() == "latest" || ref.SchemaObjectID == "" {
		return nil, http.StatusBadRequest, xerror.ValidationFailed().Detail("select an exact revision and schema object")
	}
	response, err := r.apiClient.InspectODCSRevisionWithResponse(ctx, org, project, ref.ContractSlug.String(), ref.RevisionSlug.String(),
		&api.InspectODCSRevisionParams{SchemaObjectId: &ref.SchemaObjectID}, frontendRequestEditor(ctx, cookie))
	if err != nil {
		return nil, http.StatusBadGateway, xerror.ValidationFailed().Detail("contract inspection is unavailable; retry after checking the API connection")
	}
	if response.JSON200 == nil {
		status := response.StatusCode()
		if status < http.StatusBadRequest || status > 599 {
			status = http.StatusBadGateway
		}
		return nil, status, xerror.ValidationFailed().Detail("contract inspection is unavailable; check project access and the exact revision")
	}
	inspection := response.JSON200
	if inspection.Revision.Slug != ref.RevisionSlug.String() || inspection.SchemaObjectId != ref.SchemaObjectID || inspection.Revision.Digest == "" ||
		!inspection.DocumentValid || !inspection.ProjectionAvailable || inspection.Shape == nil {
		return nil, http.StatusUnprocessableEntity, xerror.ValidationFailed().Detail("the selected revision has no supported flat shape; inspect contract coverage and retain advanced editing")
	}
	raw, err := json.Marshal(inspection.Shape)
	if err != nil {
		return nil, http.StatusBadGateway, xerror.Unexpected().Detail("contract shape could not be decoded")
	}
	var shape payload.Contract
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, http.StatusBadGateway, xerror.Unexpected().Detail("contract shape could not be decoded")
	}
	shape.Representation, shape.RootShape = ref.Representation, ref.RootShape
	return &shape, http.StatusOK, nil
}
