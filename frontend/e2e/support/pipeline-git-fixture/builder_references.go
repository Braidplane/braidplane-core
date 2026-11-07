package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/gofiber/fiber/v3"
)

var (
	builderReferencesEnabled   bool
	builderCompositionDocument string
	builderDraft               map[string]any
	builderReferenceRequests   []string
	builderValidationRejected  bool
)

func registerBuilderReferenceFixture(app *fiber.App) {
	app.Post("/_test/builder-validation-rejected", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		builderValidationRejected = true
		return c.SendStatus(http.StatusOK)
	})
	app.Post("/_test/builder-composition", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		var document map[string]any
		if err := json.Unmarshal([]byte(workspacePipeline), &document); err != nil {
			return err
		}
		var steps []any
		stepsJSON := []byte(builderCompositionSteps)
		if len(c.Body()) > 0 {
			stepsJSON = c.Body()
		}
		if err := json.Unmarshal(stepsJSON, &steps); err != nil {
			return err
		}
		document["spec"].(map[string]any)["steps"] = steps
		encoded, err := json.Marshal(document)
		if err != nil {
			return err
		}
		builderCompositionDocument = string(encoded)
		builderReferencesEnabled, imported, bound = true, true, true
		return c.SendStatus(http.StatusOK)
	})
	app.Post("/_test/builder-references", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		builderReferencesEnabled, imported, bound = true, true, true
		return c.SendStatus(http.StatusOK)
	})
	app.Get("/_test/builder-requests", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		return c.JSON(builderReferenceRequests)
	})
	app.Get("/api/*", func(c fiber.Ctx) error {
		response := httptest.NewRecorder()
		handleAPI(response, httptest.NewRequestWithContext(c.Context(), http.MethodGet, c.OriginalURL(), nil))
		c.Set("Content-Type", "application/json")
		return c.Status(response.Code).Send(response.Body.Bytes())
	})
}

func handleBuilderReferenceAPI(w http.ResponseWriter, r *http.Request) bool {
	encode := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	}
	builderReferenceRequests = append(builderReferenceRequests, r.Method+" "+r.URL.Path)
	switch r.URL.Path {
	case prefix + "/contracts/products/revisions/v1/inspection":
		_, err := w.Write([]byte(`{"revision":{"slug":"v1","digest":"sha256:products-v1","document":{}},"compiler_id":"braidplane/odcs-flat-port-decimal/v1","schema_object_id":"product","document_valid":true,"projection_available":true,"coverage_complete":true,"coverage":[],"diagnostics":[],"shape":{"name":"product","fields":[{"name":"sku","type":"string","required":true},{"name":"name","type":"string","required":true},{"name":"unit_price","type":"decimal","precision":18,"scale":2,"required":true},{"name":"currency","type":"string","required":true}]}}`))
		if err != nil {
			panic(err)
		}
	case prefix + "/environments/dev/pipeline-validation", prefix + "/environments/prod/pipeline-validation":
		if builderValidationRejected {
			encode(map[string]any{"valid": false, "diagnostics": []any{map[string]string{
				"field": "steps", "code": "orphan_step", "message": "pipeline has 2 disconnected data-flow groups: [fetch, store], [other, archive]; split independent flows into separate pipelines or connect them with an actual data dependency",
			}}})
			return true
		}
		encode(map[string]any{"valid": true, "diagnostics": []any{}})
	case prefix + "/environments":
		encode(map[string]any{"project_environments": []any{
			map[string]any{"environment_slug": "dev", "environment_name": "Development", "is_default": true},
			map[string]any{"environment_slug": "prod", "environment_name": "Production", "is_default": false},
		}})
	case prefix + "/pipelines/daily-sync":
		var document map[string]any
		definition := workspacePipeline
		if builderCompositionDocument != "" {
			definition = builderCompositionDocument
		}
		if err := json.Unmarshal([]byte(definition), &document); err != nil {
			panic(err)
		}
		if builderDraft != nil {
			document["draft"] = builderDraft
		}
		encode(document)
	case prefix + "/pipelines/daily-sync/draft":
		var request struct {
			Spec map[string]any `json:"spec"`
		}
		if r.Method != http.MethodPut || json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		metadata, ok := request.Spec["metadata"].(map[string]any)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		metadata["slug"] = "daily-sync"
		builderDraft = map[string]any{
			"pipeline_slug": "daily-sync", "initiating_member": "test-user", "based_on_spec_hash": version,
			"draft_state": "editing", "draft_spec_hash": version, "spec": request.Spec,
			"proposal_supported": true, "repository": "https://example.com/pipelines.git",
		}
		encode(map[string]any{"draft": builderDraft})
	case "/api/organizations/acme/data-sources/warehouse/queries", "/api/organizations/acme/data-sources/warehouse-prod/queries":
		encode(map[string]any{"queries": []any{
			map[string]string{"slug": "customers", "name": "Customers"},
			map[string]string{"slug": "upsert-customers", "name": "Upsert customers"},
		}})
	default:
		return false
	}
	return true
}

const builderCompositionSteps = `[
 {"kind":"input","slug":"event","name":"Incoming event","input":{"output_contract_ref":{"contract_slug":"events","revision_slug":"rev-event-one","schema_object_id":"event","representation":"json","root_shape":"object"}}},
 {"kind":"query","slug":"fetch","name":"Fetch customers","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"customers"}},
 {"kind":"transform","slug":"normalize","name":"Normalize customers","transform":{"inputs":{"events":"event","customers":"fetch"},"sql":"SELECT email FROM customers","output_kind":"rows","input_contracts":{"events":{"contract_ref":{"contract_slug":"events","revision_slug":"rev-event-one","schema_object_id":"event","representation":"json","root_shape":"object"}},"customers":{"contract_ref":{"contract_slug":"customers","revision_slug":"rev-customers-two","schema_object_id":"customer","representation":"rows"}}}}},
 {"kind":"sink","slug":"store","name":"Store customers","sink":{"input":"normalize","data_source_ref":"source","operation_type":"sql","operation_ref":"upsert-customers","idempotency_class":"deduplicated_by_ledger","input_contract":{"name":"stored-customers","representation":"rows","fields":[{"name":"email","type":"string","required":true}]}}}
]`
