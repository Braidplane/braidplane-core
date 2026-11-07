package http

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"testing"

	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// Keep the reviewed anonymous entry points separate from inherited protection.
func TestOpenAPISecurityMatchesRuntimeRoutes(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	public := map[string]bool{
		"createUser": true, "createSession": true, "verifySessionMFA": true,
		"getAuthenticationMethods": true, "registerGoogleAccount": true,
		"initiateGoogleAuth": true, "handleGoogleCallback": true,
		"verifyEmail": true, "requestPasswordReset": true, "resetPassword": true,
		"resendVerificationEmail": true,
	}
	require.Equal(t, openapi3.SecurityRequirements{{"BearerAuth": {}}, {"SessionAuth": {}}}, document.Security)
	sessionOnly := map[string]bool{
		"requestAccountDeletion":           true,
		"updateUserPassword":               true,
		"deleteRevokedPersonalAccessToken": true,

		"getOrganizationGitHubAppConnection":        true,
		"disconnectOrganizationGitHubAppConnection": true,
		"startOrganizationGitHubAppInstall":         true,
		"authorizeOrganizationGitHubAppConnection":  true,
		"deleteRevokedAPIKey":                       true,
	}
	require.Equal(t, "session_id", document.Components.SecuritySchemes["SessionAuth"].Value.Name)
	require.Equal(t, "cookie", document.Components.SecuritySchemes["SessionAuth"].Value.In)
	require.Equal(t, "bearer", document.Components.SecuritySchemes["BearerAuth"].Value.Scheme)
	require.Equal(t, "Braidplane-Webhook-Signature", document.Components.SecuritySchemes["WebhookSignature"].Value.Name)
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			t.Run(operation.OperationID, func(t *testing.T) {
				if public[operation.OperationID] {
					require.NotNil(t, operation.Security)
					require.Empty(t, *operation.Security)
					delete(public, operation.OperationID)
					return
				}
				if operation.OperationID == "receivePipelineWebhook" {
					require.NotNil(t, operation.Security)
					require.Equal(t, openapi3.SecurityRequirements{{"WebhookSignature": {}}}, *operation.Security)
					return // Signature enforcement is exercised by pipeline_webhooks_test.go.
				}
				if operation.OperationID == "completeOrganizationGitHubAppInstall" {
					require.Equal(t, &openapi3.SecurityRequirements{{"SessionAuth": {}}}, operation.Security)
					return // State lookup precedes the session guard; covered by github_app_connections_test.go.
				}
				if operation.OperationID == "handleProviderConnectorCallback" {
					require.Nil(t, operation.Security)
					return // Valid state resolves the permission target; covered by provider_connectors_test.go.
				}
				requiresSession := sessionOnly[operation.OperationID]
				if requiresSession {
					require.Equal(t, &openapi3.SecurityRequirements{{"SessionAuth": {}}}, operation.Security)
					delete(sessionOnly, operation.OperationID)
				} else {
					require.Nil(t, operation.Security, "protected operation should inherit the default")
				}
				cfg := testServerConfig{
					githubAppService:        &githubappsvc.Service{},
					proofObservationService: &proofreceiptsvc.LiveCompiler{},
					proofReceiptService:     &fakeOperationalReceiptService{},
				}
				app, _ := newTestApp(t, cfg)
				query := url.Values{}
				for _, param := range append(item.Parameters, operation.Parameters...) {
					if param.Value.In == "query" && param.Value.Required {
						query.Set(param.Value.Name, "synthetic")
					}
				}
				var body any
				if operation.OperationID == "startProviderConnectorInstall" {
					body = map[string]string{"intent": "create_grant"}
				}
				request := jsonRequest(t, method, parameter.ReplaceAllString(path, "synthetic")+"?"+query.Encode(), body)
				response, err := app.Test(request)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusUnauthorized, response.StatusCode, "%s %s", method, path)
				if requiresSession {
					cfg.middleware = []fiber.Handler{patPrincipalMiddleware(true)}
					app, _ = newTestApp(t, cfg)
					request = jsonRequest(t, method, request.URL.String(), body)
					response, err = app.Test(request)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
					require.Equal(t, http.StatusForbidden, response.StatusCode, "session-only route accepted a PAT: %s", operation.OperationID)
				}
			})
		}
	}
	require.Empty(t, public, "reviewed public operation disappeared")
	require.Empty(t, sessionOnly, "reviewed session-only operation disappeared")
	// Both generated projections must retain effective security, including exceptions.
	raw, err := PublicOpenAPIDocument(document)
	require.NoError(t, err)
	cli, err := os.ReadFile("../../cli/api/schema.json")
	require.NoError(t, err)
	for _, data := range [][]byte{raw, cli} {
		projected, err := openapi3.NewLoader().LoadFromData(data)
		require.NoError(t, err)
		require.NoError(t, projected.Validate(t.Context()))
		require.Equal(t, document.Security, projected.Security)
		for path, item := range projected.Paths.Map() {
			for method, operation := range item.Operations() {
				original := document.Paths.Value(path).GetOperation(method)
				require.Equal(t, original.Security, operation.Security, operation.OperationID)
			}
		}
	}
}
