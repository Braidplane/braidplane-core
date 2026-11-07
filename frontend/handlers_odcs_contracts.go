package frontend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

var odcsPlanDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (r *Router) handleODCSCatalog(c fiber.Ctx) error {
	org, project, ok := projectActionSlugs(c)
	if !ok {
		return c.SendStatus(http.StatusBadRequest)
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	if !permissions.Can("projects", "read") {
		return c.SendStatus(http.StatusForbidden)
	}
	data := views.ODCSCatalogView{Org: org.String(), Project: project.String(), Contract: c.Query("contract"), Revision: c.Query("revision"), Selector: c.Query("selector") == "1"}
	data.DefinitionReturn = views.DefinitionReturnHref(c.Query("definition_return"), org.String(), project.String())
	data.Section = c.Query("section", "schema")
	data.Object = c.Query("object")
	switch data.Section {
	case "schema", "details", "consumers", "document", "coverage", "impact":
	default:
		return c.SendStatus(http.StatusBadRequest)
	}
	for _, value := range []string{data.Contract, data.Revision} {
		if value != "" {
			if _, err := slug.New(value); err != nil {
				return c.SendStatus(http.StatusBadRequest)
			}
		}
	}
	if data.Revision == "latest" || (data.Revision != "" && data.Contract == "") {
		return c.SendStatus(http.StatusBadRequest)
	}
	editor := frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie))
	limit := api.LimitParam(20)
	var cursor *api.CursorParam
	if value := c.Query("cursor"); value != "" {
		cursor = &value
	}
	switch {
	case data.Contract == "":
		resp, err := r.apiClient.ListODCSContractsWithResponse(c.Context(), data.Org, data.Project, &api.ListODCSContractsParams{Limit: &limit, Cursor: cursor}, editor)
		if err != nil || resp.JSON200 == nil {
			if err == nil && resp.StatusCode() >= http.StatusBadRequest {
				c.Status(resp.StatusCode())
			} else {
				c.Status(http.StatusBadGateway)
			}
			data.Error = "Contracts could not be loaded. Check project access and try again."
		} else {
			data.Contracts = resp.JSON200.Contracts
			data.NextContracts = resp.JSON200.NextCursor
		}
	case data.Revision == "":
		resp, err := r.apiClient.ListODCSRevisionsWithResponse(c.Context(), data.Org, data.Project, data.Contract, &api.ListODCSRevisionsParams{Limit: &limit, Cursor: cursor}, editor)
		if err != nil || resp.JSON200 == nil {
			if err == nil && resp.StatusCode() >= http.StatusBadRequest {
				c.Status(resp.StatusCode())
			} else {
				c.Status(http.StatusBadGateway)
			}
			data.Error = "Revisions could not be loaded. Check project access and try again."
		} else {
			data.Revisions = resp.JSON200.Revisions
			data.NextRevisions = resp.JSON200.NextCursor
		}
	default:
		resp, err := r.apiClient.GetODCSRevisionWithResponse(c.Context(), data.Org, data.Project, data.Contract, data.Revision, editor)
		if err != nil || resp.JSON200 == nil {
			if err == nil && resp.StatusCode() >= http.StatusBadRequest {
				c.Status(resp.StatusCode())
			} else {
				c.Status(http.StatusBadGateway)
			}
			data.Error = "Revision could not be loaded. Check project access and try again."
			break
		}
		revision := resp.JSON200
		data.Metadata = &api.ODCSRevisionMetadata{Slug: revision.Slug, Digest: revision.Digest, DocumentVersion: revision.DocumentVersion, DeclaredStatus: revision.DeclaredStatus, CreatedAt: revision.CreatedAt}
		if data.Section == "impact" {
			c.Set(fiber.HeaderCacheControl, "no-store")
			data.CompareRevision = c.Query("compare_revision")
			data.FromPipeline, data.FromPlan, data.ToPipeline, data.ToPlan = c.Query("from_pipeline"), c.Query("from_plan"), c.Query("to_pipeline"), c.Query("to_plan")
			if data.CompareRevision == "" {
				break
			}
			if _, err := slug.New(data.CompareRevision); err != nil || data.CompareRevision == "latest" {
				setHTMLResponseStatus(c, http.StatusBadRequest)
				data.ImpactError = "Enter an exact revision slug to compare; latest is not accepted."
				break
			}
			request := api.ODCSRevisionImpactRequest{FromRevisionSlug: data.Revision, ToRevisionSlug: data.CompareRevision}
			if data.FromPipeline != "" || data.FromPlan != "" || data.ToPipeline != "" || data.ToPlan != "" || c.Query("from_projection_input") != "" || c.Query("to_projection_input") != "" {
				_, fromErr := slug.New(data.FromPipeline)
				_, toErr := slug.New(data.ToPipeline)
				if fromErr != nil || toErr != nil || !odcsPlanDigestPattern.MatchString(data.FromPlan) || !odcsPlanDigestPattern.MatchString(data.ToPlan) {
					setHTMLResponseStatus(c, http.StatusBadRequest)
					data.ImpactError = "Provide both pipeline slugs and both exact sha256 plan digests, or leave all four empty."
					break
				}
				request.FromPlan = &api.ODCSImpactPlanSelection{PipelineSlug: data.FromPipeline, Digest: data.FromPlan}
				request.ToPlan = &api.ODCSImpactPlanSelection{PipelineSlug: data.ToPipeline, Digest: data.ToPlan}
				data.FromInput, data.ToInput = c.Query("from_projection_input"), c.Query("to_projection_input")
				for _, selected := range []struct {
					raw    string
					target **int
				}{{data.FromInput, &request.FromPlan.ProjectionInput}, {data.ToInput, &request.ToPlan.ProjectionInput}} {
					if selected.raw == "" {
						continue
					}
					index, err := strconv.Atoi(selected.raw)
					if err != nil || index < 0 || index >= odcs.MaxProjectionInputs {
						setHTMLResponseStatus(c, http.StatusBadRequest)
						data.ImpactError = "Select a projection input index from 0 to 63."
						break
					}
					*selected.target = &index
				}
				if data.ImpactError != "" {
					break
				}
			}
			impact, err := r.apiClient.PreviewODCSRevisionImpactWithResponse(c.Context(), data.Org, data.Project, data.Contract, request, editor)
			if err != nil || impact.JSON200 == nil {
				data.ImpactError = "Comparison unavailable. Check access and select exact retained revisions and supported plans."
			} else {
				data.Impact = impact.JSON200
			}
			break
		}
		if data.Section == "document" || data.Section == "coverage" {
			c.Set(fiber.HeaderCacheControl, "no-store")
			page, err := strconv.Atoi(c.Query("page", "0"))
			if err != nil || page < 0 || page > odcs.MaxCoverageEntries/100 {
				return c.SendStatus(http.StatusBadRequest)
			}
			inspection, err := r.apiClient.InspectODCSRevisionWithResponse(c.Context(), data.Org, data.Project, data.Contract, data.Revision, &api.InspectODCSRevisionParams{SchemaObjectId: &data.Object}, editor)
			if err != nil || inspection.JSON200 == nil {
				if err == nil && inspection.StatusCode() >= http.StatusBadRequest {
					c.Status(inspection.StatusCode())
				} else {
					c.Status(http.StatusBadGateway)
				}
				data.Error = "Document inspection could not be loaded. Check project access and current admission requirements."
				break
			}
			data.Inspection = inspection.JSON200
			var pretty bytes.Buffer
			if data.Inspection.Revision.Digest != revision.Digest || json.Indent(&pretty, data.Inspection.Revision.Document, "", "  ") != nil {
				data.Inspection = nil
				data.Error = "Revision integrity could not be confirmed. Reload before inspecting."
				break
			}
			data.Document = pretty.String()
			data.CoveragePage = page
			start := min(page*100, len(data.Inspection.Coverage))
			end := min(start+100, len(data.Inspection.Coverage))
			data.Coverage = data.Inspection.Coverage[start:end]
			data.CoverageMore = end < len(data.Inspection.Coverage)
			c.Set(fiber.HeaderCacheControl, "no-store")
			break
		}
		for _, object := range revision.SchemaObjects {
			result, err := odcs.Convert(revision.Document, object.Id)
			if err != nil || result.Digest != revision.Digest {
				data.Error = "Revision integrity could not be confirmed. Reload before selecting a port."
				data.Objects = nil
				break
			}
			data.Objects = append(data.Objects, views.ODCSCatalogObject{SchemaObjectID: result.SchemaObjectID, DocumentValid: result.DocumentValid, Shape: result.Shape, Diagnostics: result.Diagnostics})
		}
		if data.Section == "consumers" {
			impact, err := r.apiClient.PreviewODCSRevisionImpactWithResponse(c.Context(), data.Org, data.Project, data.Contract, api.ODCSRevisionImpactRequest{FromRevisionSlug: data.Revision, ToRevisionSlug: data.Revision}, editor)
			if err != nil || impact.JSON200 == nil {
				data.ConsumersError = "Consumers could not be loaded. Try again."
			} else {
				data.Consumers = impact.JSON200.Consumers
			}
		}
	}
	if data.Selector {
		return renderTempl(c, views.ODCSCatalogPanel(data))
	}
	shell := r.buildPipelineShellData(c, data.Org, data.Project, "", "")
	shell.Title = "Data contracts"
	shell.Subtitle = "Browse contracts and immutable revisions."
	return renderTempl(c, views.ODCSCatalogPage(shell, data))
}
