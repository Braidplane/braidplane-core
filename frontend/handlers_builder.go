package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	builderExampleSingleQuery = "single-query"

	builderStatePayloadMalformedDetail     = "builder state payload is malformed"
	builderImportLoadFailedDetail          = "pipeline source could not be loaded into the builder"
	builderCanonicalLoadFailedDetail       = "canonical pipeline source could not be loaded into the builder"
	builderCanonicalStateUnavailableDetail = "canonical pipeline state could not be loaded right now"
	builderExportFailedDetail              = "builder state could not be exported"
)

type builderImportRequest struct {
	Format string `json:"format"`
	Source string `json:"source"`
}

type builderPublishDraftRequest struct {
	ExpectedVersionID *string `json:"expected_spec_hash,omitempty"`
	Force             bool    `json:"force,omitempty"`
}

type builderSaveResponse struct {
	api.PipelineDraftResponse
	ExpectedVersionID     *string `json:"expected_spec_hash,omitempty"`
	CurrentVersionID      *string `json:"current_version_spec_hash,omitempty"`
	DraftBasedOnVersionID *string `json:"draft_based_on_spec_hash,omitempty"`
}

type builderConnectorDescriptor struct {
	SupportedSinkReplayStrategies    []string `json:"supported_sink_replay_strategies"`
	Type                             string   `json:"type"`
	SchemaFamily                     string   `json:"schema_family"`
	QueryOperationType               string   `json:"query_operation_type"`
	SinkBackend                      string   `json:"sink_backend"`
	SupportsPipelineSource           bool     `json:"supports_pipeline_source"`
	SupportsPipelineSink             bool     `json:"supports_pipeline_sink"`
	IncrementalSourceCheckpointKinds []string `json:"incremental_source_checkpoint_kinds"`
	ForcedReadOnly                   bool     `json:"forced_read_only"`
}

func writeBuilderAPIError(c fiber.Ctx, apiErr *apierror.Error) error {
	return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
}

func builderStateInvalidAPIError(detail string) *apierror.Error {
	return apierror.ValidationFailed(errBuilderStateInvalid.Error()).WithDetailedError(detail)
}

func builderPipelineSourceInvalidAPIError(detail string) *apierror.Error {
	return apierror.ValidationFailed("pipeline source is invalid").WithDetailedError(detail)
}

func builderPaletteEntries() []views.BuilderPaletteEntry {
	return []views.BuilderPaletteEntry{
		{
			Group:       "Inputs",
			Kind:        "input",
			NodeType:    "query",
			Label:       "Event Input",
			Description: "Accept the run's JSON payload with an inline or pinned ODCS agreement.",
		},
		{
			Group:       "Query Steps",
			Kind:        "query",
			NodeType:    "query",
			Label:       "Query Step (Execute)",
			Description: "Run the referenced query with step parameters.",
		},
		{
			Group:       "Transform Steps",
			Kind:        "transform",
			NodeType:    "query",
			Label:       "Transform Step",
			Description: "Apply SQL transform logic to upstream rows.",
		},
		{
			Group:       "Sinks",
			Kind:        "sink",
			NodeType:    "query",
			Label:       "Sink Step",
			Description: "Write ordered upstream output to a target operation.",
		},
	}
}

// builderConnectorDescriptorsJSON serializes catalog-backed connector facts for builder JavaScript.
// Public API oneOf request/response shapes stay explicit; this payload only drives UI capability wiring.
func builderConnectorDescriptorsJSON() string {
	capabilities := pipelinespec.AllConnectorCapabilities()
	descriptors := make([]builderConnectorDescriptor, 0, len(capabilities))
	for _, capability := range capabilities {
		checkpointKinds := make([]string, 0, len(capability.IncrementalSourceCheckpointKinds))
		for _, kind := range capability.IncrementalSourceCheckpointKinds {
			checkpointKinds = append(checkpointKinds, kind.String())
		}
		replayStrategies := make([]string, 0, len(capability.SupportedSinkReplayStrategies))
		for _, strategy := range capability.SupportedSinkReplayStrategies {
			replayStrategies = append(replayStrategies, strategy.String())
		}
		descriptors = append(descriptors, builderConnectorDescriptor{
			Type:                             capability.DataSourceType.String(),
			SupportedSinkReplayStrategies:    replayStrategies,
			SchemaFamily:                     capability.SchemaFamily,
			QueryOperationType:               capability.QueryOperationType,
			SinkBackend:                      capability.SinkBackend.String(),
			SupportsPipelineSource:           capability.SupportsPipelineSource,
			SupportsPipelineSink:             capability.SupportsPipelineSink,
			IncrementalSourceCheckpointKinds: checkpointKinds,
			ForcedReadOnly:                   capability.ForcedReadOnly,
		})
	}

	raw, err := json.Marshal(descriptors)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// builderRuntimeTargetName formats the environment slug as explicit runtime-target copy.
func builderRuntimeTargetName(environmentSlug string) string {
	trimmed := strings.TrimSpace(environmentSlug)
	if trimmed == "" {
		return "the selected project runtime target"
	}
	return fmt.Sprintf("runtime target %q", trimmed)
}

func builderRunAvailability(state views.BuilderBlueprintState) (bool, string) {
	if strings.TrimSpace(state.DeepLinkProjectSlug) == "" {
		return false, "Run is unavailable until an active project is selected."
	}
	if strings.TrimSpace(state.TargetEnvironmentSlug) == "" {
		return false, "Run is unavailable until a project runtime target is selected."
	}

	pipelineSlug := strings.TrimSpace(state.PipelineSlug)
	if state.HasDraft {
		return false,
			"A saved draft is active. Review Git settings to merge, sync or discard it before running the deployed version."
	}
	if !state.IsValid {
		return false, "Run is unavailable until blocking validation issues are resolved."
	}
	if !state.HasImportedGraph || pipelineSlug == "" || pipelineSlug == views.BuilderDraftPipelineSlug {
		return false,
			"Run is unavailable for draft builder state. Load a saved pipeline with a real slug first."
	}

	return true, fmt.Sprintf("Run will execute pipeline %q in %s.", pipelineSlug, builderRuntimeTargetName(state.TargetEnvironmentSlug))
}

func builderSaveAvailability(state views.BuilderBlueprintState) (bool, string) {
	pipelineSlug := strings.TrimSpace(state.PipelineSlug)
	if pipelineSlug == "" || pipelineSlug == views.BuilderDraftPipelineSlug {
		return false,
			"Save Draft is available after a pipeline has been materialized from the project Git repository."
	}
	if !state.HasImportedGraph || len(state.ImportedNodes) == 0 {
		return false,
			"Save Draft is available once the canvas has at least one node."
	}

	return true, fmt.Sprintf(
		"Save will write pipeline %q to the active organization.",
		pipelineSlug,
	)
}

func (r *Router) handleBuilderBlueprintPage(c fiber.Ctx) error {
	organizations := []api.OrganizationWithRole{}
	pageError := ""

	params := &api.ListOrganizationsParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	orgList, status, errorResponse, err := r.listOrganizationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		params,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Failed to load organizations (status %d).", status)
		}
		pageError = message
	} else {
		organizations = orgList.Organizations
	}

	builderState := views.BuilderBlueprintState{
		MemberSlug:               reqctx.GetUserSlug(c, r.sessionStore).String(),
		IsValid:                  false,
		HasDraft:                 false,
		CanRun:                   false,
		ErrorCount:               0,
		PipelineExample:          strings.TrimSpace(c.Query("pipeline_example")),
		PipelineSlug:             views.BuilderDraftPipelineSlug,
		PipelineName:             "",
		SourceFormat:             model.PipelineFormatYAML.String(),
		ExportFormat:             model.PipelineFormatYAML.String(),
		ConnectorDescriptorsJSON: builderConnectorDescriptorsJSON(),
		PaletteEntries:           builderPaletteEntries(),
	}

	deepLinkOrgSlugRaw := strings.TrimSpace(c.Query("org_slug"))
	projectSlugRaw := strings.TrimSpace(c.Query("project_slug"))
	targetEnvironmentSlugRaw := strings.TrimSpace(c.Query("target_environment_slug"))
	if targetEnvironmentSlugRaw == "" {
		targetEnvironmentSlugRaw = strings.TrimSpace(c.Query("environment_slug"))
	}
	deepLinkPipelineSlugRaw := strings.TrimSpace(c.Query("pipeline_slug"))
	targetOrgSlugRaw := strings.TrimSpace(c.Query("target_org_slug"))
	draftName := strings.TrimSpace(c.Query("draft_name"))
	draftSlugRaw := strings.TrimSpace(c.Query("draft_slug"))
	targetOrgSlug := ""

	if deepLinkOrgSlugRaw != "" {
		orgSlugValue, slugErr := slug.New(deepLinkOrgSlugRaw)
		if slugErr != nil {
			pageError = "Invalid org_slug query parameter."
		} else {
			builderState.DeepLinkOrgSlug = orgSlugValue.String()
		}
	}
	if projectSlugRaw != "" {
		projectSlugValue, slugErr := slug.New(projectSlugRaw)
		if slugErr != nil {
			pageError = "Invalid project_slug query parameter."
		} else {
			builderState.DeepLinkProjectSlug = projectSlugValue.String()
		}
	}
	if targetEnvironmentSlugRaw != "" {
		environmentSlugValue, slugErr := slug.New(targetEnvironmentSlugRaw)
		if slugErr != nil {
			pageError = "Invalid target_environment_slug query parameter."
		} else {
			builderState.TargetEnvironmentSlug = environmentSlugValue.String()
		}
	}
	if deepLinkPipelineSlugRaw != "" {
		pipelineSlugValue, slugErr := slug.New(deepLinkPipelineSlugRaw)
		if slugErr != nil {
			pageError = "Invalid pipeline_slug query parameter."
		} else {
			builderState.DeepLinkPipelineSlug = pipelineSlugValue.String()
		}
	}
	if targetOrgSlugRaw != "" {
		orgSlugValue, slugErr := slug.New(targetOrgSlugRaw)
		if slugErr != nil {
			pageError = "Invalid target_org_slug query parameter."
		} else {
			targetOrgSlug = orgSlugValue.String()
		}
	}
	if deepLinkPipelineSlugRaw == "" && draftName != "" {
		builderState.PipelineName = draftName
	}
	if deepLinkPipelineSlugRaw == "" && draftSlugRaw != "" {
		draftSlugValue, slugErr := slug.New(draftSlugRaw)
		if slugErr != nil {
			pageError = "Invalid draft_slug query parameter."
		} else {
			builderState.PipelineSlug = draftSlugValue.String()
		}
	}

	if deepLinkOrgSlugRaw != "" && deepLinkPipelineSlugRaw != "" {
		if builderState.DeepLinkOrgSlug == "" ||
			builderState.DeepLinkProjectSlug == "" ||
			builderState.DeepLinkPipelineSlug == "" {
			builderState.IsValid = false
			builderState.ErrorCount = 1
			builderState.ValidationIssues = []string{
				"Resolve invalid deep-link query parameters to continue.",
			}
		} else {
			loadedPipeline, versionState, dlStatus, dlErrResp, dlErr := r.getBuilderPipelineViaAPI(
				c.Context(),
				c.Get(fiber.HeaderCookie),
				builderState.DeepLinkOrgSlug,
				builderState.DeepLinkProjectSlug,
				builderState.DeepLinkPipelineSlug,
			)
			if dlErr != nil {
				if dlStatus == fiber.StatusUnauthorized {
					return redirectToLogin(c, c.OriginalURL())
				}
				if dlStatus >= http.StatusInternalServerError {
					return c.Status(
						http.StatusInternalServerError,
					).SendString(
						"unable to load pipeline",
					)
				}
				message := messageFromErrorResponse(dlErrResp)
				if message == "" {
					message = fmt.Sprintf("Failed to load pipeline (status %d).", dlStatus)
				}
				pageError = message
			} else {
				builderState.HasDraft = loadedPipeline.Draft != nil
				if loadedPipeline.Draft != nil {
					review, reviewErr := buildBuilderProposalReview(loadedPipeline)
					if reviewErr != nil {
						pageError = fmt.Sprintf("Failed to prepare Git proposal review: %v", reviewErr)
					} else {
						builderState.ProposalReview = proposalReviewView(review)
					}
				}
				loadedState, stateErr := loadBuilderStateFromAPIPipeline(loadedPipeline)
				if stateErr != nil {
					pageError = fmt.Sprintf("Failed to parse pipeline spec: %v", stateErr)
					builderState.IsValid = false
					builderState.ErrorCount = 1
					builderState.ValidationIssues = []string{pageError}
				} else {
					loadedState.Context.ExpectedVersionID = versionState.ExpectedVersionID
					loadedState.Context.CurrentVersionID = versionState.CurrentVersionID
					loadedState.Context.DraftBasedOnVersionID = versionState.DraftBasedOnVersionID
					if builderState.TargetEnvironmentSlug != "" {
						loadedState.Context.TargetEnvironmentSlug = builderState.TargetEnvironmentSlug
					}
					applyBuilderLoadedState(&builderState, loadedState)
				}
			}
		}
	}

	example := builderState.PipelineExample
	if example != "" && deepLinkOrgSlugRaw == "" {
		loadedState, source, err := loadBuilderStateFromExample(example)
		if err != nil {
			pageError = fmt.Sprintf("Failed to load pipeline example: %v", err)
			builderState.IsValid = false
			builderState.ErrorCount = 1
			builderState.ValidationIssues = []string{pageError}
		} else {
			builderState.InitialPipelineSource = source
			applyBuilderLoadedState(&builderState, loadedState)
		}
	}

	if !builderState.HasImportedGraph {
		builderState.IsEmpty = true
		builderState.IsValid = true
		builderState.ErrorCount = 0
		builderState.ValidationIssues = []string{}
	}

	shellData := r.buildShellDataForDashboard(c, "Builder", organizations, pageError, "")
	saveOrganizationSlug := strings.TrimSpace(shellData.ActiveOrganizationSlug)
	saveProjectSlug := strings.TrimSpace(shellData.ActiveProjectSlug)
	if strings.TrimSpace(builderState.DeepLinkOrgSlug) != "" {
		saveOrganizationSlug = strings.TrimSpace(builderState.DeepLinkOrgSlug)
		saveProjectSlug = strings.TrimSpace(builderState.DeepLinkProjectSlug)
	} else if strings.TrimSpace(targetOrgSlug) != "" {
		saveOrganizationSlug = strings.TrimSpace(targetOrgSlug)
		saveProjectSlug = strings.TrimSpace(builderState.DeepLinkProjectSlug)
	}
	if saveOrganizationSlug != "" && saveProjectSlug != "" {
		shellData = r.buildShellDataForProjectPage(
			c,
			"Builder",
			saveOrganizationSlug,
			saveProjectSlug,
			pageError,
			"",
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Edit pipeline graphs and Git proposal drafts for the selected project runtime target."
	}
	if saveProjectSlug != "" {
		builderState.DeepLinkProjectSlug = saveProjectSlug
	}
	builderState.TargetEnvironmentSlug = strings.TrimSpace(shellData.ActiveEnvironmentSlug)
	builderState.TargetEnvironmentColor = strings.TrimSpace(shellData.ActiveEnvironmentColor)
	builderState.CanRun, builderState.RunReason = builderRunAvailability(builderState)
	if saveOrganizationSlug != "" {
		permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), saveOrganizationSlug)
		builderState.CanCreatePipeline = permissions.Can("pipelines", "create")
		builderState.CanRefreshSchemas = permissions.Can("datasources", "update")
		builderState.CanExecutePipeline = permissions.Can("pipelines", "execute")
		if !builderState.CanExecutePipeline {
			builderState.CanRun = false
			builderState.RunReason = "You don't have permission to execute pipelines."
		}
	}
	canSave, saveReason := builderSaveAvailability(builderState)
	builderState.SaveReason = saveReason

	if saveOrganizationSlug != "" && saveProjectSlug != "" {
		builderState.SaveURL = fmt.Sprintf(
			"/orgs/%s/projects/%s/pipelines/save",
			saveOrganizationSlug,
			saveProjectSlug,
		)
	} else if canSave {
		builderState.SaveReason = "Save to Braidplane is unavailable until an active organization and project are selected."
	}
	return renderTempl(c, views.BuilderBlueprintPage(shellData, builderState))
}

func (r *Router) handleBuilderProposalReview(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	pipeline, status, errorResponse, getErr := r.getPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		pipelineSlug.String(),
	)
	if getErr != nil {
		if status < 100 {
			status = fiber.StatusBadGateway
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return c.Status(status).JSON(api.ErrorResponse{Error: api.Error{
			Code: "PROPOSAL_REVIEW_UNAVAILABLE", Message: "proposal review is unavailable",
		}})
	}
	review, err := buildBuilderProposalReview(pipeline)
	if err != nil {
		slog.ErrorContext(c.Context(), "build pipeline proposal review",
			"request_id", reqctx.FiberRequestID(c), "error", err)
		return c.Status(fiber.StatusUnprocessableEntity).JSON(api.ErrorResponse{Error: api.Error{
			Code: "PROPOSAL_REVIEW_INVALID", Message: "proposal review could not be generated",
		}})
	}
	return c.JSON(review)
}

func applyBuilderLoadedState(bs *views.BuilderBlueprintState, loaded builderState) {
	bs.IsEmpty = false
	bs.IsValid = true
	bs.ErrorCount = 0
	bs.ValidationIssues = []string{"No blocking issues."}
	bs.HasImportedGraph = true
	bs.ImportedEdgeCount = len(loaded.Graph.Edges)
	bs.ImportedBindingCount = len(loaded.Graph.Bindings)
	bs.PipelineSlug = loaded.PipelineSpec.Metadata.Slug.String()
	bs.PipelineName = loaded.PipelineSpec.Metadata.Name
	bs.TargetEnvironmentSlug = strings.TrimSpace(loaded.Context.TargetEnvironmentSlug)
	bs.ExpectedVersionID = loaded.Context.ExpectedVersionID
	bs.CurrentVersionID = loaded.Context.CurrentVersionID
	bs.DraftBasedOnVersionID = loaded.Context.DraftBasedOnVersionID
	bs.SourceFormat = resolveBuilderPipelineExportFormat(
		builderStateContext{SourceFormat: loaded.Context.SourceFormat},
	).String()
	bs.ExportFormat = resolveBuilderPipelineExportFormat(loaded.Context).String()
	stepBySlug := make(map[slug.Slug]pipelinespec.Step, len(loaded.PipelineSpec.Steps))
	for _, step := range loaded.PipelineSpec.Steps {
		stepBySlug[step.Slug] = step
	}
	// Compute deterministic positions for all nodes
	positions := layoutGraph(&loaded.PipelineSpec)
	bs.ImportedNodes = make([]views.BuilderBlueprintNode, len(loaded.Graph.Nodes))
	for i, node := range loaded.Graph.Nodes {
		bs.ImportedNodes[i] = builderBlueprintNodeFromGraphNode(node, stepBySlug[node.StepSlug], positions)
	}
	bs.ImportedEdges = make([]views.BuilderBlueprintEdge, len(loaded.Graph.Edges))
	for i, edge := range loaded.Graph.Edges {
		bs.ImportedEdges[i] = views.BuilderBlueprintEdge{
			From: edge.FromStep.String(),
			To:   edge.ToStep.String(),
		}
	}
	bs.ImportedBindings = make([]views.BuilderBlueprintBinding, len(loaded.Graph.Bindings))
	for i, binding := range loaded.Graph.Bindings {
		bs.ImportedBindings[i] = views.BuilderBlueprintBinding{
			FromNode: binding.FromStep.String(),
			FromPort: binding.FromPort,
			ToNode:   binding.ToStep.String(),
			ToPort:   binding.ToPort,
		}
	}
}

func builderBlueprintNodeFromGraphNode(
	node builderGraphNode,
	step pipelinespec.Step,
	positions map[string]position,
) views.BuilderBlueprintNode {
	// Look up deterministically computed position for this node
	pos, ok := positions[node.StepSlug.String()]
	if !ok {
		// Fallback if position not found (should not happen)
		pos = position{X: 2, Y: 1}
	}

	viewNode := views.BuilderBlueprintNode{
		Slug:         node.StepSlug.String(),
		Kind:         strings.TrimSpace(step.Kind),
		X:            pos.X,
		Y:            pos.Y,
		StepSlug:     step.Slug.String(),
		StepName:     step.Name,
		OriginalStep: formatBuilderNodeJSON(step),
	}

	switch step.Kind {
	case builderKindQuery:
		if step.Query == nil {
			return viewNode
		}
		useInlineSQL := strings.EqualFold(step.Query.OperationType, builderDefaultOperationType) &&
			strings.TrimSpace(inlineSQLFrom(step.Query.OperationInline)) != ""
		viewNode.DataSourceSlug = step.Query.DataSourceSlug.String()
		viewNode.OperationType = step.Query.OperationType
		viewNode.OperationInline = formatBuilderNodeOperationInline(step.Query.OperationInline)
		viewNode.QueryOutputContract = formatBuilderNodeJSON(step.Query.OutputContract)
		viewNode.QueryIncremental = formatBuilderNodeJSON(step.Query.Incremental)
		viewNode.QuerySlug = step.Query.OperationRef.String()
		viewNode.UseInlineSQL = useInlineSQL
		viewNode.UseInlineOperation = len(step.Query.OperationInline) > 0 && !useInlineSQL
		viewNode.InlineSQL = inlineSQLFrom(step.Query.OperationInline)
		viewNode.Parameters = formatBuilderNodeParameters(step.Query.Parameters)
	case builderKindTransform:
		if step.Transform == nil {
			return viewNode
		}
		viewNode.TransformOutputKind = step.Transform.OutputKind.String()
		viewNode.TransformSQL = step.Transform.SQL
		viewNode.TransformInputs = formatBuilderNodeJSON(step.Transform.Inputs)
		viewNode.TransformInputContract = formatBuilderNodeJSON(step.Transform.InputContract)
		viewNode.TransformOutputContract = formatBuilderNodeJSON(step.Transform.OutputContract)
	case builderKindSink:
		if step.Sink == nil {
			return viewNode
		}
		viewNode.SinkBackend = resolveBuilderSinkBackend(step.Sink.OperationType).String()
		viewNode.SinkDataSourceSlug = step.Sink.DataSourceSlug.String()
		viewNode.SinkOperationType = step.Sink.OperationType
		viewNode.SinkOperationInline = formatBuilderNodeOperationInline(step.Sink.OperationInline)
		viewNode.SinkIdempotencyClass = step.Sink.IdempotencyClass.String()
		viewNode.SinkInputContract = formatBuilderNodeJSON(step.Sink.InputContract)
		viewNode.SinkUseInlineOperation = len(step.Sink.OperationInline) > 0
		viewNode.SinkQuerySlug = step.Sink.OperationRef.String()
	}

	return viewNode
}

func (r *Router) handleBuilderSave(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	body := c.Body()
	state, issues, err := deserializeBuilderStateWithIssues(c.Body())
	if err != nil {
		if len(issues) > 0 {
			state, err = deserializeBuilderStatePermissive(body)
			if err != nil {
				return writeBuilderAPIError(
					c,
					builderStateInvalidAPIError(builderStatePayloadMalformedDetail),
				)
			}
		} else {
			return writeBuilderAPIError(
				c,
				builderStateInvalidAPIError(builderStatePayloadMalformedDetail),
			)
		}
	}

	pipelineSlug := strings.TrimSpace(state.PipelineSpec.Metadata.Slug.String())
	if pipelineSlug == "" || pipelineSlug == views.BuilderDraftPipelineSlug {
		_, saveReason := builderSaveAvailability(
			views.BuilderBlueprintState{PipelineSlug: pipelineSlug},
		)
		apiErr := apierror.ValidationFailed(errBuilderStateInvalid.Error()).WithDetailedError(saveReason)
		return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
	}

	pipeline, err := builderStateToDraftPipeline(state)
	if err != nil {
		fallbackName := strings.TrimSpace(state.PipelineSpec.Metadata.Name)
		if fallbackName == "" {
			fallbackName = "Draft Pipeline"
		}

		pipeline = &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: state.PipelineSpec.Metadata.Slug,
				Name: fallbackName,
			},
			Steps: []pipelinespec.Step{},
		}
	}

	savedDraft, conflictResponse, status, errorResponse, saveErr := r.savePipelineDraftViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		pipeline,
		state.Context.ExpectedVersionID,
	)
	if saveErr != nil {
		if status == fiber.StatusUnauthorized {
			apiErr := apierror.Unauthorized().WithDetailedError(
				"Your session has expired. Sign in again and retry the save.",
			)
			return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
		}
		if conflictResponse != nil {
			enriched := r.enrichBuilderConflictResponseWithComparableDiff(
				c.Context(),
				c.Get(fiber.HeaderCookie),
				orgSlug.String(),
				projectSlug,
				state.PipelineSpec.Metadata.Slug.String(),
				conflictResponse,
				state,
			)
			if enrichedConflict, ok := enriched.(builderDraftConflictResponse); ok &&
				enrichedConflict.hasNoComparableDiff() &&
				conflictResponse.CurrentSpecHash != nil {
				retrySavedDraft, retryConflictResponse, retryStatus, retryErrorResponse, retryErr := r.savePipelineDraftViaAPI(
					c.Context(),
					c.Get(fiber.HeaderCookie),
					orgSlug.String(),
					projectSlug,
					pipeline,
					conflictResponse.CurrentSpecHash,
				)
				if retryErr != nil {
					switch {
					case retryStatus == fiber.StatusUnauthorized:
						apiErr := apierror.Unauthorized().WithDetailedError(
							"Your session has expired. Sign in again and retry the save.",
						)
						return c.Status(apiErr.HTTPStatus).JSON(
							apierror.ErrorResponse{Error: apiErr},
						)
					case retryConflictResponse != nil:
						enriched = r.enrichBuilderConflictResponseWithComparableDiff(
							c.Context(),
							c.Get(fiber.HeaderCookie),
							orgSlug.String(),
							projectSlug,
							state.PipelineSpec.Metadata.Slug.String(),
							retryConflictResponse,
							state,
						)
						return c.Status(retryStatus).JSON(enriched)
					case retryErrorResponse != nil:
						return c.Status(retryStatus).JSON(retryErrorResponse)
					case retryStatus == http.StatusOK:
						currentPipeline, versionState, _, _, versionErr := r.getBuilderPipelineViaAPI(
							c.Context(),
							c.Get(fiber.HeaderCookie),
							orgSlug.String(),
							projectSlug,
							pipeline.Metadata.Slug.String(),
						)
						if versionErr == nil &&
							currentPipeline != nil &&
							currentPipeline.Draft != nil {
							responsePayload := builderSaveResponse{
								PipelineDraftResponse: api.PipelineDraftResponse{
									Draft: *currentPipeline.Draft,
								},
								ExpectedVersionID:     versionState.ExpectedVersionID,
								CurrentVersionID:      versionState.CurrentVersionID,
								DraftBasedOnVersionID: versionState.DraftBasedOnVersionID,
							}
							return c.Status(http.StatusOK).JSON(responsePayload)
						}
						fallthrough
					default:
						saveFailed := api.Error{
							Code:    "SAVE_FAILED",
							Message: fmt.Sprintf("pipeline save failed (status %d)", retryStatus),
						}
						return c.Status(retryStatus).JSON(api.ErrorResponse{
							Error: saveFailed,
						})
					}
				}
				responsePayload := builderSaveResponse{
					PipelineDraftResponse: *retrySavedDraft,
					ExpectedVersionID:     state.Context.ExpectedVersionID,
					CurrentVersionID:      state.Context.CurrentVersionID,
					DraftBasedOnVersionID: state.Context.DraftBasedOnVersionID,
				}
				savedPipelineSlug := strings.TrimSpace(retrySavedDraft.Draft.PipelineSlug)
				if savedPipelineSlug != "" {
					_, versionState, _, _, versionErr := r.getBuilderPipelineViaAPI(
						c.Context(),
						c.Get(fiber.HeaderCookie),
						orgSlug.String(),
						projectSlug,
						savedPipelineSlug,
					)
					if versionErr == nil {
						responsePayload.ExpectedVersionID = versionState.ExpectedVersionID
						responsePayload.CurrentVersionID = versionState.CurrentVersionID
						responsePayload.DraftBasedOnVersionID = versionState.DraftBasedOnVersionID
					}
				}
				return c.Status(retryStatus).JSON(responsePayload)
			}
			return c.Status(status).JSON(enriched)
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return c.Status(status).JSON(api.ErrorResponse{
			Error: api.Error{
				Code:    "SAVE_FAILED",
				Message: fmt.Sprintf("pipeline save failed (status %d)", status),
			},
		})
	}

	responsePayload := builderSaveResponse{
		PipelineDraftResponse: *savedDraft,
		ExpectedVersionID:     state.Context.ExpectedVersionID,
		CurrentVersionID:      state.Context.CurrentVersionID,
		DraftBasedOnVersionID: state.Context.DraftBasedOnVersionID,
	}

	savedPipelineSlug := strings.TrimSpace(savedDraft.Draft.PipelineSlug)
	if savedPipelineSlug != "" {
		_, versionState, _, _, versionErr := r.getBuilderPipelineViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			projectSlug,
			savedPipelineSlug,
		)
		if versionErr == nil {
			responsePayload.ExpectedVersionID = versionState.ExpectedVersionID
			responsePayload.CurrentVersionID = versionState.CurrentVersionID
			responsePayload.DraftBasedOnVersionID = versionState.DraftBasedOnVersionID
		}
	}

	return c.Status(status).JSON(responsePayload)
}

func (r *Router) handleBuilderPublishPipelineDraft(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	var req builderPublishDraftRequest
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&req); err != nil {
			apiErr := apierror.InvalidRequestBody()
			return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
		}
	}

	published, conflictResponse, status, errorResponse, publishErr := r.publishPipelineDraftViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		pipelineSlug.String(),
		req.ExpectedVersionID,
		req.Force,
	)
	if publishErr != nil {
		if status == fiber.StatusUnauthorized {
			apiErr := apierror.Unauthorized().WithDetailedError(
				"Your session has expired. Sign in again and retry publish.",
			)
			return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
		}
		if conflictResponse != nil {
			return c.Status(status).JSON(
				r.enrichBuilderPublishConflictResponseWithComparableDiff(
					c.Context(),
					c.Get(fiber.HeaderCookie),
					orgSlug.String(),
					projectSlug,
					pipelineSlug.String(),
					conflictResponse,
				),
			)
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return c.Status(status).JSON(api.ErrorResponse{
			Error: api.Error{
				Code:    "PUBLISH_FAILED",
				Message: fmt.Sprintf("pipeline publish failed (status %d)", status),
			},
		})
	}

	return c.Status(status).JSON(published)
}

func (r *Router) handleBuilderRefreshPipelineProposal(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}
	refreshed, status, errorResponse, refreshErr := r.refreshPipelineDraftProposalViaAPI(
		c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), projectSlug, pipelineSlug.String(),
	)
	if refreshErr != nil {
		if status < 100 {
			status = fiber.StatusBadGateway
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return c.Status(status).JSON(api.ErrorResponse{Error: api.Error{
			Code: "PROPOSAL_REFRESH_FAILED", Message: fmt.Sprintf("proposal refresh failed (status %d)", status),
		}})
	}
	return c.Status(status).JSON(refreshed)
}

func (r *Router) handleBuilderDiscardPipelineDraft(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	status, errorResponse, deleteErr := r.deletePipelineDraftViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		pipelineSlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			apiErr := apierror.Unauthorized().WithDetailedError(
				"Your session has expired. Sign in again and retry discard.",
			)
			return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return c.Status(status).JSON(api.ErrorResponse{
			Error: api.Error{
				Code:    "DISCARD_DRAFT_FAILED",
				Message: fmt.Sprintf("pipeline draft discard failed (status %d)", status),
			},
		})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (r *Router) handleGetCanonicalPipelineState(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	pipeline, versionState, status, errorResponse, getErr := r.getBuilderPipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		pipelineSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			apiErr := apierror.Unauthorized().WithDetailedError(
				"Your session has expired. Sign in again and retry.",
			)
			return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return writeBuilderAPIError(
			c,
			apierror.InternalServerError().WithDetailedError(builderCanonicalStateUnavailableDetail),
		)
	}

	state, err := loadCanonicalBuilderStateFromAPIPipeline(pipeline)
	if err != nil {
		return writeBuilderAPIError(c, builderPipelineSourceInvalidAPIError(builderCanonicalLoadFailedDetail))
	}

	state.Context.CurrentVersionID = versionState.CurrentVersionID
	state.Context.ExpectedVersionID = versionState.CurrentVersionID
	state.Context.DraftBasedOnVersionID = versionState.CurrentVersionID

	raw, err := serializeBuilderStateCanonical(state)
	if err != nil {
		return writeBuilderAPIError(c, builderPipelineSourceInvalidAPIError(builderCanonicalLoadFailedDetail))
	}

	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSONCharsetUTF8)
	return c.Send(raw)
}

func (r *Router) handleBuilderExport(c fiber.Ctx) error {
	return r.exportBuilderDefinition(c, false)
}

func (r *Router) handleBuilderValidate(c fiber.Ctx) error {
	return r.exportBuilderDefinition(c, true)
}

func (r *Router) exportBuilderDefinition(c fiber.Ctx, validateTarget bool) error {
	state, issues, err := deserializeBuilderStateWithIssues(c.Body())
	if err != nil {
		if len(issues) > 0 {
			validationErrors := make([]apierror.ValidationError, 0, len(issues))
			for _, issue := range issues {
				validationErrors = append(validationErrors, apierror.ValidationError{
					Field:   issue.Field,
					Code:    issue.Code,
					Message: issue.Message,
				})
			}

			apiErr := apierror.ValidationFailed(
				errBuilderStateInvalid.Error(),
			).WithDetailedError(
				err.Error(),
			)
			apiErr.ValidationErrors = validationErrors
			return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
		}
		return writeBuilderAPIError(
			c,
			builderStateInvalidAPIError(builderStatePayloadMalformedDetail),
		)
	}

	exportFormat := resolveBuilderPipelineExportFormat(state.Context)
	output, err := serializeBuilderPipelineSpec(state, exportFormat)
	if err != nil {
		return writeBuilderAPIError(c, builderStateInvalidAPIError(
			fmt.Sprintf("%s: %v", builderExportFailedDetail, err),
		))
	}

	if validateTarget {
		apiErr, status, errResp, validationErr := r.validateBuilderPipelineTarget(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			state,
		)
		if validationErr != nil {
			if errResp != nil {
				return c.Status(status).JSON(errResp)
			}
			if apiErr != nil {
				return c.Status(status).JSON(apierror.ErrorResponse{Error: apiErr})
			}
			return writeBuilderAPIError(c, builderStateInvalidAPIError(builderExportFailedDetail))
		}
	} else if err := validateBuilderPipelineSpec(exportFormat, output); err != nil {
		return writeBuilderAPIError(c, builderStateInvalidAPIError(
			fmt.Sprintf("%s: %v", builderExportFailedDetail, err),
		))
	}

	filename := strings.TrimSpace(state.PipelineSpec.Metadata.Slug.String())
	if filename == "" {
		filename = views.BuilderDraftPipelineSlug
	}

	ext := ".pipeline.yaml"
	contentType := "application/yaml; charset=utf-8"
	if exportFormat == model.PipelineFormatJSON {
		ext = ".pipeline.json"
		contentType = fiber.MIMEApplicationJSONCharsetUTF8
	}

	c.Set(fiber.HeaderContentType, contentType)
	c.Set(
		fiber.HeaderContentDisposition,
		fmt.Sprintf("attachment; filename=%q", filename+ext),
	)
	return c.Send(output)
}

func (r *Router) validateBuilderPipelineTarget(
	ctx context.Context,
	cookieHeader string,
	state builderState,
) (*apierror.Error, int, *api.ErrorResponse, error) {
	targetOrgSlug := strings.TrimSpace(state.Context.TargetOrgSlug)
	targetProjectSlug := strings.TrimSpace(state.Context.TargetProjectSlug)
	targetEnvironmentSlug := strings.TrimSpace(state.Context.TargetEnvironmentSlug)
	if targetOrgSlug == "" || targetProjectSlug == "" || targetEnvironmentSlug == "" {
		detail := "Select an organization, project and runtime target before validation."
		return builderStateInvalidAPIError(detail), http.StatusBadRequest, nil, errBuilderStateInvalid.Detail(detail)
	}

	pipeline, err := builderStateToPipeline(state)
	if err != nil {
		return builderStateInvalidAPIError(builderExportFailedDetail), http.StatusBadRequest, nil, err
	}
	spec, err := pipelineToAPIPipelineSpec(pipeline)
	if err != nil {
		return builderStateInvalidAPIError(builderExportFailedDetail), http.StatusBadRequest, nil, err
	}

	result, status, errResp, err := r.validatePipelineViaAPI(
		ctx,
		cookieHeader,
		targetOrgSlug,
		targetProjectSlug,
		targetEnvironmentSlug,
		api.PipelineValidationRequest{Spec: spec},
	)
	if err != nil {
		if errResp != nil {
			return nil, status, errResp, err
		}
		if status >= http.StatusInternalServerError {
			return apierror.InternalServerError().WithDetailedError(builderExportFailedDetail), status, nil, err
		}
		return builderStateInvalidAPIError(builderExportFailedDetail), status, nil, err
	}
	if result.Valid {
		return nil, http.StatusOK, nil, nil
	}

	apiErr := apierror.ValidationFailed(errBuilderStateInvalid.Error()).
		WithDetailedError(builderExportFailedDetail)
	apiErr.ValidationErrors = make([]apierror.ValidationError, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		apiErr.ValidationErrors = append(apiErr.ValidationErrors, apierror.ValidationError{
			Field:   diagnostic.Field,
			Code:    diagnostic.Code,
			Message: diagnostic.Message,
		})
	}

	return apiErr, http.StatusBadRequest, nil, xerror.InvalidInput(xerror.Pipeline).Detail(
		"pipeline validation failed",
	)
}

func (r *Router) handleBuilderImport(c fiber.Ctx) error {
	if len(strings.TrimSpace(string(c.Body()))) == 0 {
		apiErr := apierror.ValidationFailed("pipeline source is invalid").WithDetailedError(
			"empty payload",
		)
		return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
	}

	var request builderImportRequest
	if err := json.Unmarshal(c.Body(), &request); err != nil {
		apiErr := apierror.ValidationFailed("pipeline source is invalid").WithDetailedError(
			fmt.Sprintf("invalid JSON payload: %v", err),
		)
		return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
	}

	rawSource := strings.TrimSpace(request.Source)
	if rawSource == "" {
		apiErr := apierror.ValidationFailed("pipeline source is invalid").WithDetailedError(
			"source is required",
		)
		return c.Status(apiErr.HTTPStatus).JSON(apierror.ErrorResponse{Error: apiErr})
	}

	format := model.PipelineFormatYAML
	switch strings.ToLower(strings.TrimSpace(request.Format)) {
	case model.PipelineFormatYAML.String():
	case model.PipelineFormatJSON.String():
		format = model.PipelineFormatJSON
	default:
		if strings.HasPrefix(rawSource, "{") || strings.HasPrefix(rawSource, "[") {
			format = model.PipelineFormatJSON
		}
	}
	filename := "builder-import.pipeline.yaml"
	if format == model.PipelineFormatJSON {
		filename = "builder-import.pipeline.json"
	}
	state, err := builderStateFromPipelineSpec(filename, []byte(rawSource))
	if err != nil {
		return writeBuilderAPIError(c, builderPipelineSourceInvalidAPIError(builderImportLoadFailedDetail))
	}

	rawState, err := serializeBuilderStateCanonical(state)
	if err != nil {
		return writeBuilderAPIError(c, builderPipelineSourceInvalidAPIError(builderImportLoadFailedDetail))
	}

	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSONCharsetUTF8)
	return c.Send(rawState)
}

func (r *Router) handleExecutePipelineSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	pipelineSlug, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid pipeline slug")
	}

	environmentSlug, err := requiredRuntimeEnvironmentSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(api.ErrorResponse{
			Error: api.Error{Code: "BAD_REQUEST", Message: err.Error()},
		})
	}

	payload := api.ExecutePipelineRequest{}
	if body := strings.TrimSpace(string(c.Body())); body != "" {
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(api.ErrorResponse{
				Error: api.Error{Code: "BAD_REQUEST", Message: "invalid request payload"},
			})
		}
	}

	runRecord, status, errorResponse, execErr := r.executePipelineViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		environmentSlug.String(),
		pipelineSlug.String(),
		payload,
	)
	if execErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if errorResponse != nil {
			return c.Status(status).JSON(errorResponse)
		}
		return c.Status(status).JSON(api.ErrorResponse{
			Error: api.Error{
				Code:    "EXECUTION_FAILED",
				Message: fmt.Sprintf("pipeline execution failed (status %d)", status),
			},
		})
	}

	return c.Status(status).JSON(runRecord)
}

func formatBuilderNodeParameters(parameters map[string]any) string {
	if len(parameters) == 0 {
		return ""
	}

	raw, err := json.MarshalIndent(parameters, "", "  ")
	if err != nil {
		return ""
	}

	return string(raw)
}

func formatBuilderNodeOperationInline(operationInline map[string]any) string {
	return formatBuilderNodeJSON(operationInline)
}

func formatBuilderNodeJSON(value any) string {
	if value == nil {
		return ""
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	if string(raw) == "null" || string(raw) == "{}" {
		return ""
	}

	return string(raw)
}

func loadBuilderStateFromExample(name string) (builderState, string, error) {
	relativePath, err := resolveBuilderExampleRelativePath(name)
	if err != nil {
		return builderState{}, "", err
	}
	pipelinePath, err := resolveExamplePath(relativePath)
	if err != nil {
		return builderState{}, "", err
	}
	payload, err := os.ReadFile(pipelinePath)
	if err != nil {
		return builderState{}, "", xerror.Unexpected().With(err)
	}
	state, err := builderStateFromPipelineSpec(pipelinePath, payload)
	if err != nil {
		return builderState{}, "", xerror.Wrapf("convert example pipeline: %w", err)
	}
	return state, string(payload), nil
}

func resolveBuilderExampleRelativePath(name string) (string, error) {
	switch name {
	case builderExampleSingleQuery:
		return filepath.Join("examples", "pipelines", "yaml", "single-query.pipeline.yaml"), nil
	default:
		return "", xerror.Unexpected().Detailf("unknown pipeline example %q", name)
	}
}

func resolveExamplePath(relativePath string) (string, error) {
	candidates := []string{relativePath, filepath.Join("..", relativePath)}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", xerror.Unexpected().Detailf("example pipeline not found at %q", relativePath)
}

func (r *Router) executePipelineViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
	payload api.ExecutePipelineJSONRequestBody,
) (*api.PipelineRunRecord, int, *api.ErrorResponse, error) {
	requestCtx, cancel := frontendAPIExecutionContext(ctx, 0)
	defer cancel()

	resp, err := r.apiClient.ExecutePipelineWithResponse(
		requestCtx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		pipelineSlug,
		payload,
		frontendRequestEditor(requestCtx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusAccepted && resp.JSON202 != nil {
		return resp.JSON202, http.StatusAccepted, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	if errResp == nil {
		fallbackMessage := strings.TrimSpace(string(resp.Body))
		if fallbackMessage != "" {
			errResp = &api.ErrorResponse{
				Error: api.Error{Code: "EXECUTION_FAILED", Message: fallbackMessage},
			}
		}
	}

	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"execute pipeline api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) validatePipelineViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	payload api.PipelineValidationRequest,
) (*api.PipelineValidationResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ValidatePipelineWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"validate pipeline api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getPipelineViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
) (*api.Pipeline, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetPipelineWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"get pipeline api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getBuilderPipelineViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
) (*api.Pipeline, builderStateContext, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetPipelineWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, builderStateContext{}, http.StatusInternalServerError, nil, xerror.Unexpected().With(
			err,
		)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		versionState := builderStateContext{}
		versionState.CurrentVersionID = resp.JSON200.CurrentVersionSpecHash
		if resp.JSON200.Draft != nil {
			versionState.DraftBasedOnVersionID = resp.JSON200.Draft.BasedOnSpecHash
		}
		if versionState.DraftBasedOnVersionID != nil {
			versionState.ExpectedVersionID = versionState.DraftBasedOnVersionID
		} else {
			versionState.ExpectedVersionID = versionState.CurrentVersionID
		}
		if versionState.CurrentVersionID == nil {
			return nil, builderStateContext{}, http.StatusInternalServerError, nil, xerror.Unexpected().
				Detailf(
					"get pipeline api returned 200 without current_version_id for %s/%s",
					organizationSlug,
					pipelineSlug,
				)
		}
		return resp.JSON200, versionState, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, builderStateContext{}, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"get pipeline api returned status %d",
		resp.StatusCode(),
	)
}

func loadBuilderStateFromAPIPipelineSpec(
	pipelineSlug string,
	sourceSpec any,
) (builderState, error) {
	specBytes, err := json.Marshal(sourceSpec)
	if err != nil {
		return builderState{}, errBuilderStateInvalid.Detailf("marshal pipeline spec: %w", err)
	}

	// Use a .json extension so builderStateFromPipelineSpec detects JSON format.
	virtualPath := pipelineSlug + ".pipeline.json"
	state, err := builderStateFromPipelineSpec(virtualPath, specBytes)
	if err == nil {
		return state, nil
	}

	// Fallback: allow opening builder from non-publishable specs so users can repair.
	var fallbackPipeline pipelinespec.Pipeline
	if decodeErr := json.Unmarshal(specBytes, &fallbackPipeline); decodeErr != nil {
		return builderState{}, err
	}

	fallbackState, fallbackErr := builderStateFromPipeline(&fallbackPipeline)
	if fallbackErr != nil {
		return builderState{}, err
	}

	sourceFormat, formatErr := builderPipelineFormatFromPath(virtualPath)
	if formatErr != nil {
		return builderState{}, err
	}
	fallbackState.Context.SourceFormat = &sourceFormat
	fallbackState.Context.ExportFormat = &sourceFormat

	return canonicalizeBuilderState(fallbackState), nil
}

func loadBuilderStateFromAPIPipeline(pipeline *api.Pipeline) (builderState, error) {
	if pipeline == nil {
		return builderState{}, errBuilderStateInvalid.Detail("pipeline is nil")
	}

	sourceSpec := pipeline.Spec
	if pipeline.Draft != nil {
		sourceSpec = pipeline.Draft.Spec
	}

	return loadBuilderStateFromAPIPipelineSpec(pipeline.Slug, sourceSpec)
}

func loadCanonicalBuilderStateFromAPIPipeline(pipeline *api.Pipeline) (builderState, error) {
	if pipeline == nil {
		return builderState{}, errBuilderStateInvalid.Detail("pipeline is nil")
	}

	return loadBuilderStateFromAPIPipelineSpec(pipeline.Slug, pipeline.Spec)
}

func loadDraftBuilderStateFromAPIPipeline(pipeline *api.Pipeline) (builderState, error) {
	if pipeline == nil {
		return builderState{}, errBuilderStateInvalid.Detail("pipeline is nil")
	}
	if pipeline.Draft == nil {
		return builderState{}, errBuilderStateInvalid.Detail("pipeline draft is nil")
	}

	return loadBuilderStateFromAPIPipelineSpec(pipeline.Slug, pipeline.Draft.Spec)
}

func (r *Router) savePipelineDraftViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipeline *pipelinespec.Pipeline,
	expectedVersionID *string,
) (*api.PipelineDraftResponse, *api.PipelineDraftConflictResponse, int, *api.ErrorResponse, error) {
	if pipeline == nil {
		return nil, nil, http.StatusBadRequest, nil, xerror.Unexpected().Detail(
			"pipeline is required",
		)
	}

	specUpdate, err := pipelineSaveRequestBody(pipeline)
	if err != nil {
		return nil, nil, http.StatusBadRequest, nil, xerror.Wrapf(
			"build pipeline save request: %w",
			err,
		)
	}

	resp, err := r.apiClient.SavePipelineDraftWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipeline.Metadata.Slug.String(),
		api.SavePipelineDraftJSONRequestBody{
			ExpectedSpecHash: expectedVersionID,
			Spec:             specUpdate,
		},
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, nil, http.StatusOK, nil, nil
	}
	if resp.StatusCode() == http.StatusOK {
		currentPipeline, _, _, getErr := r.getPipelineViaAPI(
			ctx,
			cookieHeader,
			organizationSlug,
			projectSlug,
			pipeline.Metadata.Slug.String(),
		)
		if getErr == nil && currentPipeline != nil && currentPipeline.Draft != nil {
			return &api.PipelineDraftResponse{
				Draft: *currentPipeline.Draft,
			}, nil, http.StatusOK, nil, nil
		}
	}

	if resp.StatusCode() == http.StatusConflict {
		conflictResp := parsePipelineDraftConflictResponse(resp.Body)
		if conflictResp != nil {
			return nil, conflictResp, http.StatusConflict, nil, xerror.Unexpected().Detail(
				"save pipeline draft conflict",
			)
		}
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"save pipeline draft api returned status %d",
		resp.StatusCode(),
	)
}

func pipelineSaveRequestBody(
	pipeline *pipelinespec.Pipeline,
) (api.PipelineSpecUpdate, error) {
	spec, err := pipelineToAPIPipelineSpec(pipeline)
	if err != nil {
		return api.PipelineSpecUpdate{}, err
	}

	return api.PipelineSpecUpdate{
		Metadata: api.PipelineMetadataUpdate{Name: spec.Metadata.Name},
		Steps:    spec.Steps,
	}, nil
}

func pipelineToAPIPipelineSpec(pipeline *pipelinespec.Pipeline) (api.PipelineSpec, error) {
	data, err := json.Marshal(pipeline)
	if err != nil {
		return api.PipelineSpec{}, xerror.Unexpected().With(err)
	}

	var spec api.PipelineSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return api.PipelineSpec{}, xerror.Unexpected().With(err)
	}

	return spec, nil
}

func (r *Router) handleBootstrapPipelineCheckpointsSubmit(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	project, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	pipeline, err := slug.New(c.Params("pipelineSlug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	environment, err := requiredRuntimeEnvironmentSlug(c)
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	var payload api.BootstrapPipelineCheckpointsRequest
	if err := json.Unmarshal(c.Body(), &payload); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	ctx, cancel := frontendAPIExecutionContext(c.Context(), 0)
	defer cancel()
	resp, err := r.apiClient.BootstrapPipelineCheckpointsWithResponse(ctx, org.String(), project.String(), environment.String(), pipeline.String(), payload, frontendRequestEditor(ctx, c.Get(fiber.HeaderCookie)))
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(api.ErrorResponse{Error: api.Error{Code: "BOOTSTRAP_FAILED", Message: "Unable to bootstrap checkpoints"}})
	}
	if resp.StatusCode() == http.StatusNoContent {
		return c.SendStatus(fiber.StatusNoContent)
	}
	if apiErr := parseAPIErrorResponse(resp.Body); apiErr != nil {
		return c.Status(resp.StatusCode()).JSON(apiErr)
	}
	return c.Status(fiber.StatusBadGateway).JSON(api.ErrorResponse{Error: api.Error{Code: "BOOTSTRAP_FAILED", Message: "Unable to bootstrap checkpoints"}})
}
