package frontend

import (
	"net/http"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
)

func definitionUnavailable(status int) string {
	switch status {
	case http.StatusForbidden, http.StatusUnauthorized:
		return "Definition content is restricted."
	case http.StatusNotFound:
		return "The requested definition is missing."
	case http.StatusGone:
		return "The requested definition has expired or was deleted."
	case http.StatusRequestEntityTooLarge:
		return "The requested definition exceeds the inspection limit."
	case http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return "The requested definition format is unsupported."
	default:
		return "The requested definition could not be loaded."
	}
}

func (r *Router) loadDefinitionInspection(c fiber.Ctx, org, project string, pipeline *api.Pipeline) {
	d := views.DefinitionInspection{Authority: "Current materialization"}
	defer func() {
		c.Set(fiber.HeaderCacheControl, "no-store")
		c.SetContext(views.WithDefinitionInspection(c.Context(), d))
	}()
	if pipeline == nil {
		d.Message = "Pipeline definition is unavailable."
		return
	}
	expected := strings.TrimSpace(c.Query("spec_hash"))
	d.Hash = expected
	switch c.Query("definition", "current") {
	case "current":
		d.Hash = ""
		if pipeline.CurrentVersionSpecHash != nil {
			d.Hash = *pipeline.CurrentVersionSpecHash
		}
		if expected != "" && expected != d.Hash {
			d.Hash = expected
			d.Message = "Current materialization changed. Reopen Inspect definition to inspect the new version."
			return
		}
		d.Spec = &pipeline.Spec
	case "draft":
		d.Authority = "Member UI draft — not deployed"
		if pipeline.Draft == nil {
			d.Hash = expected
			d.Message = "Member draft is unavailable."
			return
		}
		d.Hash = pipeline.Draft.DraftSpecHash
		if expected != "" && expected != d.Hash {
			d.Hash = expected
			d.Message = "Member draft changed. Reopen the draft to inspect its new content."
			return
		}
		d.Spec = &pipeline.Draft.Spec
	case "deployed":
		d.Authority, d.Hash = "Deployed version", expected

		environment := requestRuntimeEnvironmentSlug(c)
		if environment == "" {
			d.Message = "Select a runtime target to inspect its deployment."
			return
		}
		deployment, status, _, err := r.getPipelineDeploymentViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, environment, pipeline.Slug)
		if err != nil {
			d.Message = definitionUnavailable(status)
			return
		}
		if d.Hash == "" {
			d.Hash = deployment.CurrentVersionSpecHash
		} else if d.Hash != deployment.CurrentVersionSpecHash {
			d.Notice = "The current deployment pointer differs. Inspecting the requested snapshot; this view does not establish deployment history."
		}

	case "version":
		d.Authority, d.Hash = "Selected immutable version", expected
	default:
		d.Message = "Unsupported definition authority."
		return
	}
	if d.Spec != nil {
		return
	}
	if d.Hash == "" {
		d.Message = "No exact definition hash was recorded."
		return
	}
	version, status, _, err := r.getPipelineVersionViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, pipeline.Slug, d.Hash)
	if err != nil {
		d.Message = definitionUnavailable(status)
		return
	}
	if !strings.HasPrefix(version.SpecHash, d.Hash) {
		d.Message = "The returned definition does not match the requested version."
		return
	}
	d.Hash, d.Spec = version.SpecHash, &version.Spec
	if !version.CreatedAt.IsZero() {
		d.Created = version.CreatedAt.String()
	}
	if version.GitCommitSha != nil {
		d.Commit = *version.GitCommitSha
	}
}
