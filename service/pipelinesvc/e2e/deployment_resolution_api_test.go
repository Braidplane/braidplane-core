package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	transport "github.com/Braidplane/braidplane-core/transport/http"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// Only lifecycle routes are invoked; unrelated server dependencies stay unused.
func resolutionAPI(svc pipelinesvc.Service, compiler *contractplansvc.Service) *fiber.App {
	server := transport.NewServer(
		nil,                         // usersService
		nil,                         // accountClosureService
		nil,                         // oidcService
		nil,                         // sessionStore
		allowAuthorizationService{}, // authorizationService
		nil,                         // orgService
		&fakes.AdmissionService{},   // admissionService
		nil,                         // projectsService
		nil,                         // repositorySetupService
		nil,                         // pipelineCreationsService
		nil,                         // odcsContractsService
		compiler,                    // compiledPlansService
		nil,                         // odcsArtifactImporter
		nil,                         // environmentsService
		nil,                         // membersService
		nil,                         // rolesService
		nil,                         // apiKeysService
		nil,                         // patsService
		nil,                         // credentialsService
		nil,                         // datasourcesService
		nil,                         // dataSourceRefsService
		nil,                         // queriesService
		nil,                         // eventsService
		nil,                         // githubAppService
		nil,                         // notificationsService
		nil,                         // retentionService
		svc,                         // pipelinesService
		nil,                         // proofReceiptService
		nil,                         // proofConfigurationService
		nil,                         // proofObservationService
		nil,                         // pipelineTriggersService
		nil,                         // pipelineWebhookService
		nil,                         // runRecencyService
		"http://localhost",          // frontendURL
		"",                          // googleRedirectURI
		false,                       // selfServiceRegistrationEnabled
		false,                       // externalOnboardingEnabled
		nil,
	)
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(func(c fiber.Ctx) error {
		reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: "synthetic-operator", Source: reqctx.UserPrincipalSourceSession})
		return c.Next()
	})
	api.RegisterHandlers(app, server)
	return app
}

func resolutionPost(t *testing.T, app *fiber.App, path string, body any, status int, target any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), "POST", path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	raw, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, status, response.StatusCode, string(raw))
	if target != nil {
		require.NoError(t, json.Unmarshal(raw, target))
	}
}

func resolutionProjectionRequest(selection *odcs.ProjectionRequest) map[string]any {
	inputs := []any{}
	for _, input := range selection.Inputs {
		types := []string{}
		for kind, enabled := range input.Mapping.Types {
			if enabled {
				types = append(types, kind.String())
			}
		}
		mapping := map[string]any{"id": input.Mapping.ID, "representation": input.Mapping.Representation.String(), "types": types}
		if input.Mapping.RootShape.String() != "unknown" {
			mapping["root_shape"] = input.Mapping.RootShape.String()
		}
		inputs = append(inputs, map[string]any{"contract_slug": input.Contract, "revision_slug": input.Revision, "schema_object_id": input.ObjectID, "digest": input.Digest, "mapping": mapping, "variables": input.Variables})
	}
	relationships := []any{}
	for _, relationship := range selection.Relationships {
		relationships = append(relationships, map[string]any{"input": relationship.Input, "target": relationship.Target, "path": relationship.Path})
	}
	return map[string]any{"projection": map[string]any{"inputs": inputs, "relationships": relationships}}
}
