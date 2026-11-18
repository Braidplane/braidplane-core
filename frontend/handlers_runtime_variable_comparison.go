package frontend

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (r *Router) handleRuntimeVariableComparison(c fiber.Ctx) error {
	return r.renderRuntimeVariableComparison(c, http.StatusOK, "", nil)
}

func (r *Router) renderRuntimeVariableComparison(c fiber.Ctx, status int, message string, submitted *api.RuntimeVariableDefinition) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	ctx, cookie := c.Context(), c.Get(fiber.HeaderCookie)
	data := views.RuntimeVariableComparisonView{
		Organization: org.String(),
		Permissions:  r.resolveOrganizationPermissions(ctx, cookie, org.String()),
		Form:         submitted,
		Unsaved:      submitted != nil && c.FormValue("action") != "delete",
	}
	params := &api.CompareRuntimeVariablesParams{}
	project, pipeline, hash := c.Query("project"), c.Query("pipeline"), c.Query("spec_hash")
	if project != "" || pipeline != "" || hash != "" {
		if _, err := slug.New(project); err != nil {
			return c.SendStatus(http.StatusBadRequest)
		}
		if _, err := slug.New(pipeline); err != nil || hash == "" {
			return c.SendStatus(http.StatusBadRequest)
		}
		data.Project, data.Pipeline, data.SpecHash = project, pipeline, hash
		version, code, _, err := r.getPipelineVersionViaAPI(ctx, cookie, org.String(), project, pipeline, hash)
		if code == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if err != nil {
			return c.Status(code).SendString("The selected pipeline version could not be loaded.")
		}
		var spec pipelinespec.Pipeline
		encoded, err := json.Marshal(version.Spec)
		if err != nil || json.Unmarshal(encoded, &spec) != nil {
			return c.Status(http.StatusBadGateway).SendString("The selected pipeline version could not be read.")
		}
		keys := pipelineruntime.EnvironmentKeys(&spec)
		params.UsedKey = &keys
	}
	resp, err := r.apiClient.CompareRuntimeVariablesWithResponse(ctx, org.String(), params, frontendRequestEditor(ctx, cookie))
	switch {
	case err != nil:
		if message == "" {
			message, status = "Runtime variables could not be loaded.", http.StatusBadGateway
		}
	case resp.StatusCode() == http.StatusUnauthorized:
		return redirectToLogin(c, c.OriginalURL())
	case resp.JSON200 == nil:
		if message == "" {
			message, status = "Runtime variables could not be loaded.", resp.StatusCode()
		}
	default:
		data.Comparison = resp.JSON200
	}
	if (c.Query("define") == "1" || submitted != nil) && data.Permissions.Can("environments", "update") {
		data.Editing = c.Query("key") != ""
		if data.Form == nil {
			data.Form = &api.RuntimeVariableDefinition{ValueType: api.RuntimeVariableValueTypeText}
			if data.Editing && data.Comparison != nil {
				for _, row := range data.Comparison.Rows {
					if row.Definition != nil && row.Key == c.Query("key") {
						data.Form = row.Definition
						break
					}
				}
			}
			if data.Editing && data.Form.Key == "" {
				data.Form = nil
				if data.Comparison != nil && message == "" {
					message, status = "The runtime variable definition could not be found.", http.StatusNotFound
				}
			}
		}
		environments, code, _, err := r.listEnvironmentsViaAPI(ctx, cookie, org.String())
		if err != nil {
			data.Form = nil
			if message == "" {
				message, status = "Environment protection could not be loaded.", code
			}
		} else {
			for _, environment := range catalogEnvironments(environments) {
				if environment.Protected {
					data.Protected = append(data.Protected, environment.Slug)
				}
			}
		}
	}
	shell := r.buildShellDataForOrganizationPage(c, "Runtime variables", org.String(), message, "", cookie)
	setHTMLResponseStatus(c, status)
	return renderTempl(c, views.RuntimeVariableComparisonPage(shell, data))
}

func (r *Router) handleRuntimeVariableDefinitionSubmit(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	ctx, cookie := c.Context(), c.Get(fiber.HeaderCookie)
	key := c.Query("key")
	if key == "" {
		key = strings.TrimSpace(c.FormValue("key"))
	}
	if key == "" {
		return c.SendStatus(http.StatusBadRequest)
	}
	form := &api.RuntimeVariableDefinition{
		Key:       key,
		ValueType: api.RuntimeVariableValueType(c.FormValue("value_type")),
		Required:  c.FormValue("required") == "1",
	}
	if description := normalizeDefinitionFormText(c.FormValue("description")); description != "" {
		form.Description = &description
	}
	if c.FormValue("has_default") == "1" {
		value := normalizeDefinitionFormText(c.FormValue("default_value"))
		form.DefaultValue = &value
	}
	confirmations := strings.Fields(c.FormValue("protected_environment_confirmations"))

	status := http.StatusBadGateway
	var body []byte
	if c.FormValue("action") == "delete" {
		if c.Query("key") == "" {
			return c.SendStatus(http.StatusBadRequest)
		}
		resp, err := r.apiClient.DeleteRuntimeVariableDefinitionWithResponse(ctx, org.String(), key,
			&api.DeleteRuntimeVariableDefinitionParams{ProtectedEnvironmentConfirmations: &confirmations}, frontendRequestEditor(ctx, cookie))
		if err == nil {
			status, body = resp.StatusCode(), resp.Body
		}
	} else {
		resp, err := r.apiClient.UpsertRuntimeVariableDefinitionWithResponse(ctx, org.String(), key,
			api.UpsertRuntimeVariableDefinition{
				CreateOnly: new(c.Query("key") == ""), ValueType: form.ValueType, Required: form.Required, DefaultValue: form.DefaultValue,
				Description: form.Description, ProtectedEnvironmentConfirmations: &confirmations,
			}, frontendRequestEditor(ctx, cookie))
		if err == nil {
			status, body = resp.StatusCode(), resp.Body
		}
	}
	if status == http.StatusOK || status == http.StatusNoContent {
		return c.Redirect().Status(http.StatusSeeOther).To(views.RuntimeVariableComparisonHref(
			org.String(), c.Query("project"), c.Query("pipeline"), c.Query("spec_hash"),
		))
	}
	if status == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	message := formFeedbackFromErrorResponse(parseAPIErrorResponse(body), "The runtime variable definition could not be saved.")
	if c.FormValue("action") == "delete" {
		form = nil
	}
	return r.renderRuntimeVariableComparison(c, status, message, form)
}

func normalizeDefinitionFormText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}
