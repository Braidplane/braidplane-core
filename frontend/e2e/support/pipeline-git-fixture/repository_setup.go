package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
)

var (
	repositorySetupEnabled    bool
	repositorySetup           *api.ProjectRepositorySetup
	repositorySetupAttached   bool
	repositorySetupAttempts   int
	repositorySetupSequence   int
	repositorySetupHistory    map[string]*api.ProjectRepositorySetup
	repositoryGitLabConsent   bool
	repositoryExistingBackend *api.ProjectPipelineBackend
)

func registerRepositorySetupFixture(app *fiber.App) {
	app.Post("/_test/repository-setup", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		repositorySetupEnabled = true
		repositorySetup = nil
		repositorySetupAttached = false
		repositoryExistingBackend = nil
		repositorySetupAttempts = 0
		repositorySetupSequence = 0
		repositorySetupHistory = map[string]*api.ProjectRepositorySetup{}
		repositoryGitLabConsent = c.Query("gitlab_consent") == "required"
		if c.Query("source_test") == "required" {
			failure, branch, directory := "source_test_required", "main", "pipelines"
			repositorySetup = &api.ProjectRepositorySetup{Slug: "repo-fixture", ProjectSlug: "ops", DataSourceSlug: "repo-fixture", Status: api.ProjectRepositorySetupStatusSourceCreated, FailureCode: &failure, Request: api.CreateProjectRepositoryRequest{Provider: api.CreateProjectRepositoryRequestProviderGitlab, Owner: "customer-team", RepositoryName: "data-pipelines", TrackedBranch: &branch, PipelineDir: &directory, CreatePrivateRepository: true}}
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	app.Get("/_test/gitlab-consent/callback", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		if repositorySetup == nil || !repositoryGitLabConsent {
			return c.SendStatus(fiber.StatusConflict)
		}
		status := "denied"
		failure := "authorization_denied"
		repositorySetup.FailureCode = &failure
		if c.Query("result") == "success" {
			repositorySetup.Status = api.ProjectRepositorySetupStatusAttached
			repositorySetupAttached = true
			repositoryGitLabConsent = false
			repositorySetup.FailureCode = nil
			status = "success"
		}
		return c.Redirect().Status(fiber.StatusFound).To("/organizations/acme/projects/ops?section=repository&repository_path=create&gitlab_setup=" + status)
	})
}

func handleRepositorySetupAPI(w http.ResponseWriter, r *http.Request) bool {
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	}
	switch r.URL.Path {
	case "/api/organizations/acme/roles/admin":
		write(map[string]any{"slug": "admin", "permissions": []map[string]string{
			{"resource": "projects", "action": "read"}, {"resource": "projects", "action": "update"}, {"resource": "organizations", "action": "update"}, {"resource": "datasources", "action": "read"}, {"resource": "datasources", "action": "create"}, {"resource": "pipelines", "action": "read"},
		}})
	case prefix:
		project := api.Project{Slug: "ops", Name: "Ops"}
		if repositoryExistingBackend != nil {
			project.PipelineBackend = repositoryExistingBackend
		} else if repositorySetupAttached {
			project.PipelineBackend = &api.ProjectPipelineBackend{DataSourceSlug: "repo-fixture", Repo: "https://" + string(repositorySetup.Request.Provider) + ".com/customer-team/data-pipelines.git", TrackedBranch: "main", PipelineDir: "pipelines"}
		}
		write(project)
	case prefix + "/pipeline-backend":
		var request api.SetProjectPipelineBackendRequest
		if r.Method != http.MethodPut || json.NewDecoder(r.Body).Decode(&request) != nil || request.Mode != api.SetProjectPipelineBackendRequestModeAdoptExisting {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		repositoryExistingBackend = &api.ProjectPipelineBackend{DataSourceSlug: request.DataSourceSlug, Repo: "https://example.com/pipelines.git", TrackedBranch: request.TrackedBranch, PipelineDir: request.PipelineDir}
		write(api.Project{Slug: "ops", Name: "Ops", PipelineBackend: repositoryExistingBackend})
	case prefix + "/repository-setup":
		if r.Method == http.MethodGet {
			if repositorySetup == nil {
				w.WriteHeader(http.StatusNoContent)
			} else {
				write(repositorySetup)
			}
			return true
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		var request api.CreateProjectRepositoryRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || !bool(request.CreatePrivateRepository) {
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"error": map[string]string{"message": "Explicit creation consent is required."}})
			return true
		}
		repositorySetupAttempts++
		if repositorySetup == nil {
			repositorySetupSequence++
			setupSlug := "repo-fixture"
			if repositorySetupSequence > 1 {
				setupSlug += "-" + strconv.Itoa(repositorySetupSequence)
			}
			repositorySetup = &api.ProjectRepositorySetup{Slug: setupSlug, ProjectSlug: "ops", DataSourceSlug: setupSlug, Request: request, Status: api.ProjectRepositorySetupStatusPending}
		}
		if request.Provider == api.CreateProjectRepositoryRequestProviderGithub {
			repositorySetup.FailureCode = nil
			response := *repositorySetup
			link := "https://github.com/apps/braidplane-fixture/installations/new?state=" + strings.Repeat("f", 32)
			response.AuthorizationUrl = &link
			write(response)
			return true
		}
		if repositoryGitLabConsent {
			repositorySetup.FailureCode = nil
			response := *repositorySetup
			link := "https://gitlab.com/oauth/authorize?state=isolated-gitlab-state"
			response.AuthorizationUrl = &link
			write(response)
			return true
		}
		if repositorySetupAttempts == 1 {
			repositorySetup.Status = api.ProjectRepositorySetupStatusRepositoryCreated
			id, remote, sha, code := "42", "https://gitlab.com/customer-team/data-pipelines.git", "first-fixture-sha", "source_create_failed"
			repositorySetup.RepositoryId = &id
			repositorySetup.RepositoryUrl = &remote
			repositorySetup.FirstCommitSha = &sha
			repositorySetup.FailureCode = &code
			w.WriteHeader(http.StatusServiceUnavailable)
			write(map[string]any{"error": map[string]string{"message": "The connection could not be completed. Your private repository is saved; retry to resume."}})
			return true
		}
		repositorySetup.Status = api.ProjectRepositorySetupStatusAttached
		repositorySetup.FailureCode = nil
		repositorySetupAttached = true
		write(repositorySetup)
	case prefix + "/repository-setup/abandon":
		var request api.AbandonProjectRepositorySetupRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || !bool(request.PreserveResources) || repositorySetup == nil || repositorySetup.Slug != request.SetupSlug || repositorySetupAttached {
			w.WriteHeader(http.StatusConflict)
			return true
		}
		now := time.Now()
		repositorySetup.AbandonedAt = &now
		repositorySetupHistory[repositorySetup.Slug] = repositorySetup
		write(repositorySetup)
		repositorySetup = nil
	case "/api/github-app-connection/callback":
		if repositorySetup == nil {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		if providerError := r.URL.Query().Get("error"); providerError != "" {
			failure := "authorization_failed"
			if providerError == "access_denied" {
				failure = "authorization_denied"
			}
			repositorySetup.FailureCode = &failure
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"error": map[string]string{"message": "Authorization failed"}})
			return true
		}
		repositorySetup.Status = api.ProjectRepositorySetupStatusAttached
		repositorySetup.FailureCode = nil
		repositorySetupAttached = true
		write(map[string]any{"organization_slug": "acme", "installation_id": 9, "account_id": 100, "account_login": "customer-team", "account_type": "Organization", "repository_selection": "selected", "repositories": []any{}, "repository_setup": repositorySetup})
	default:
		if setup, ok := strings.CutPrefix(r.URL.Path, prefix+"/repository-setups/"); ok {
			if attempt := repositorySetupHistory[setup]; attempt != nil {
				write(attempt)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return true
		}
		return false
	}
	return true
}
