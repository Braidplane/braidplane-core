package frontend

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleProofReceiptPage(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid project slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid environment slug")
	}
	receiptSlug, err := slug.New(c.Params("receiptSlug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid proof receipt slug")
	}

	receipt, status, errorResponse, getErr := r.getProofReceiptViaAPI(c, orgSlug.String(), projectSlug.String(), environmentSlug.String(), receiptSlug.String())
	if status == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	message, details := "", ""
	if getErr != nil {
		message = messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Proof receipt is unavailable."
		}
		details = backendErrorDetails("get proof receipt", status, errorResponse, getErr)
	}
	shell := r.buildShellDataForProjectPage(c, "Proof receipt", orgSlug.String(), projectSlug.String(), message, details, c.Get(fiber.HeaderCookie))
	shell.ActiveEnvironmentSlug = environmentSlug.String()
	c.Status(status)
	return renderTempl(c, views.ProofReceiptPage(shell, receipt, r.proofChangeURL(c, receipt)))
}

func (r *Router) handleProofReceiptHistoryPage(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	project, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid project slug")
	}
	environment, status, err := r.resolveRuntimeEnvironmentSlug(c)
	if status == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if err != nil {
		message := "Unable to load the selected project environment."
		if status == http.StatusBadRequest {
			message = "Select an enabled project environment to view Proof receipt history."
		}
		shell := r.buildShellDataForProjectPage(c, "Proof", org.String(), project.String(), message, "", c.Get(fiber.HeaderCookie))
		c.Status(status)
		return renderTempl(c, views.ProofHistoryPage(shell, nil))
	}
	limit := api.LimitParam(20)
	params := &api.ListProofReceiptsParams{Limit: &limit}
	if cursor := c.Query("cursor"); cursor != "" {
		params.Cursor = &cursor
	}
	if direction := c.Query("direction"); direction != "" {
		value := api.ListProofReceiptsParamsDirection(direction)
		params.Direction = &value
	}
	resp, listErr := r.apiClient.ListProofReceiptsWithResponse(c.Context(), org.String(), project.String(), environment.String(), params, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	var history *api.ProofReceiptListResponse
	message := ""
	if listErr != nil {
		status = http.StatusBadGateway
		message = "Proof receipt history is unavailable. Try again."
	} else {
		status = resp.StatusCode()
		if status == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if status == http.StatusOK && resp.JSON200 != nil {
			history = resp.JSON200
		} else {
			if status == http.StatusOK {
				status = http.StatusBadGateway
			}
			message = messageFromErrorResponse(parseAPIErrorResponse(resp.Body))
			if message == "" {
				message = "Proof receipt history is unavailable."
			}
		}
	}
	shell := r.buildShellDataForProjectPage(c, "Proof", org.String(), project.String(), message, "", c.Get(fiber.HeaderCookie))
	shell.ActiveEnvironmentSlug = environment.String()
	c.Status(status)
	return renderTempl(c, views.ProofHistoryPage(shell, history))
}

func (r *Router) getProofReceiptViaAPI(c fiber.Ctx, orgSlug, projectSlug, environmentSlug, receiptSlug string) (*api.ProofReceipt, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetProofReceiptWithResponse(c.Context(), orgSlug, projectSlug, environmentSlug, receiptSlug, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf("get proof receipt api returned status %d", resp.StatusCode())
}

// Resolve the stable repository ID against the tenant's current repository metadata.
// Missing access or a disconnected repository must not hide the historical receipt.
func (r *Router) proofChangeURL(c fiber.Ctx, receipt *api.ProofReceipt) string {
	if receipt == nil {
		return ""
	}
	candidate := receipt.Candidate
	repositoryID, err := parsePositiveInt64(candidate.RepositoryId)
	if err != nil {
		return ""
	}
	number, err := parsePositiveInt64(candidate.ChangeRequestId)
	if err != nil {
		return ""
	}
	resp, err := r.apiClient.GetOrganizationGitHubAppConnectionWithResponse(c.Context(), candidate.OrganizationSlug, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil || resp.JSON200 == nil {
		return ""
	}
	for _, repo := range resp.JSON200.Repositories {
		if repo.Id != repositoryID {
			continue
		}
		parts := strings.Split(repo.FullName, "/")
		if len(parts) != 2 {
			return ""
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return ""
			}
		}
		return "https://github.com/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pull/" + strconv.FormatInt(number, 10)
	}
	return ""
}
