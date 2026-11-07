package main

import (
	"encoding/json"
	"net/http"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
)

var (
	firstPipelineEnabled, firstPipelineRestricted, firstPipelineImported bool
	firstPipelineStatus                                                  = "publish_queued"
	firstPipelineSpec                                                    api.PipelineSpec
	firstPipelineRequests                                                []string
)

func registerFirstPipelineFixture(app *fiber.App) {
	app.Post("/_test/first-pipeline/:state", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		firstPipelineEnabled = true
		switch c.Params("state") {
		case "restricted":
			firstPipelineRestricted = true
		case "failed":
			firstPipelineStatus = "publish_failed"
		case "branch":
			firstPipelineStatus = "branch_pushed"
		case "open":
			firstPipelineStatus = "proposal_open"
		case "imported":
			firstPipelineStatus, firstPipelineImported = "proposal_open", true
		}
		return c.SendStatus(http.StatusOK)
	})
	app.Get("/_test/first-pipeline-requests", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		return c.JSON(firstPipelineRequests)
	})
}

func handleFirstPipelineAPI(w http.ResponseWriter, r *http.Request) bool {
	firstPipelineRequests = append(firstPipelineRequests, r.Method+" "+r.URL.Path)
	switch r.URL.Path {
	case "/api/organizations/acme/roles/admin":
		permissions := []api.Permission{{Resource: "pipelines", Action: "read"}, {Resource: "projects", Action: "read"}, {Resource: "projects", Action: "update"}, {Resource: "datasources", Action: "read"}}
		if !firstPipelineRestricted {
			permissions = append(permissions, api.Permission{Resource: "pipelines", Action: "create"})
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"slug": "admin", "permissions": permissions}); err != nil {
			http.Error(w, "encode role", http.StatusInternalServerError)
		}
	case "/api/organizations/acme/data-sources/warehouse/queries":
		_, _ = w.Write([]byte(`{"queries":[{"slug":"orders","name":"Recent orders"}]}`))
	case "/api/organizations/acme/data-sources/warehouse/queries/orders":
		_, _ = w.Write([]byte(`{"slug":"orders","name":"Recent orders","data_source_slug":"warehouse","updated_at":"2026-10-21T00:00:00Z","parameters":[{"name":"count","data_type":"integer","is_required":true,"description":"Maximum orders to read"}],"query_statement":{"type":"postgres","sql":"SELECT :count"},"row_limit":100,"timeout_seconds":30}`))
	case prefix + "/pipeline-creations":
		if r.Method == http.MethodGet {
			page := api.PipelineCreationProposalListResponse{PipelineCreations: []api.PipelineCreationProposal{}}
			if firstPipelineSpec.Metadata.Slug != "" {
				page.PipelineCreations = append(page.PipelineCreations, firstPipelineCreation())
			}
			if err := json.NewEncoder(w).Encode(page); err != nil {
				http.Error(w, "encode proposals", http.StatusInternalServerError)
			}
			return true
		}
		var body api.PipelineSpec
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || firstPipelineRestricted {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		if firstPipelineSpec.Metadata.Slug == "" {
			firstPipelineSpec = body
		} else {
			original, originalErr := json.Marshal(firstPipelineSpec)
			actual, actualErr := json.Marshal(body)
			if originalErr != nil || actualErr != nil {
				http.Error(w, "encode proposal", http.StatusInternalServerError)
				return true
			}
			if string(original) != string(actual) {
				w.WriteHeader(http.StatusConflict)
				return true
			}
		}
		if firstPipelineStatus == "publish_failed" {
			firstPipelineStatus = "publish_queued"
		}
		writeFirstPipelineCreation(w)
	case prefix + "/pipeline-creations/creation-one":
		writeFirstPipelineCreation(w)
	case prefix + "/pipelines/" + firstPipelineSpec.Metadata.Slug + "/versions/" + version:
		if !firstPipelineImported {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Version not imported"}}`))
		} else {
			if err := json.NewEncoder(w).Encode(api.PipelineVersion{PipelineSlug: firstPipelineSpec.Metadata.Slug, SpecHash: version, Spec: firstPipelineSpec}); err != nil {
				http.Error(w, "encode version", http.StatusInternalServerError)
			}
		}
	default:
		return false
	}
	return true
}

func writeFirstPipelineCreation(w http.ResponseWriter) {
	if err := json.NewEncoder(w).Encode(firstPipelineCreation()); err != nil {
		http.Error(w, "encode proposal", http.StatusInternalServerError)
	}
}

func firstPipelineCreation() api.PipelineCreationProposal {
	record := api.PipelineCreationProposal{Slug: "creation-one", ProjectSlug: "ops", PipelineSlug: firstPipelineSpec.Metadata.Slug, SpecHash: version, Spec: firstPipelineSpec, Status: api.PipelineCreationProposalStatus(firstPipelineStatus), SourcePath: "pipelines/" + firstPipelineSpec.Metadata.Slug + ".pipeline.yaml"}
	if firstPipelineStatus == "proposal_open" {
		proposalURL := "https://example.test/customer/pipelines/pull/1"
		record.ProposalUrl = &proposalURL
	}
	if firstPipelineStatus == "branch_pushed" {
		provider, branch, head := "git", "braidplane/create/creation-one", commit
		record.ProposalProvider, record.BranchName, record.BranchHeadSha = &provider, &branch, &head
	}
	return record
}
