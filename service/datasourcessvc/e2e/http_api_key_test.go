package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/testutil/datasourcee2e"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/require"
)

func TestHTTPAPIKeyCredentialLifecycleAndTenantIsolation(t *testing.T) {
	for _, headerSet := range []bool{false, true} {
		t.Run(fmt.Sprint("secret_headers=", headerSet), func(t *testing.T) {
			first := map[string]any{"api_key": "first-synthetic-key", "api_key_header": "X-Api-Key"}
			second := map[string]any{"api_key": "second-synthetic-key", "api_key_header": "Authorization"}
			credentialField := `"api_key"`
			if headerSet {
				first = map[string]any{"secret_headers": map[string]string{"accessKey": "first-synthetic-key", "secretKey": "first-secret", "appKey": "first-app"}}
				second = map[string]any{"secret_headers": map[string]string{"Authorization": "ApiKey second-synthetic-key", "X-Custom-Key": "second-secret"}}
				credentialField = `"secret_headers"`
			}
			firstJSON, err := json.Marshal(first)
			require.NoError(t, err)
			secondJSON, err := json.Marshal(second)
			require.NoError(t, err)

			if testing.Short() {
				t.Skip("requires PostgreSQL credential storage")
			}
			pool := e2edb.OpenPostgresPool(t, "HTTP API-key credential lifecycle")
			stack := datasourcee2e.NewStack(t, pool)
			svc := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(_ context.Context, request datasourcessvc.ConnectionTestRequest) error {
				require.Contains(t, string(request.CredentialsJSON), credentialField)
				return nil
			}))
			dsSlug := datasourcee2e.UniqueSlug(t, "http-key")
			org := datasourcee2e.UniqueSlug(t, "http-key-org")
			other := datasourcee2e.UniqueSlug(t, "http-key-other")
			datasourcee2e.SeedOrganization(t, pool, org)
			datasourcee2e.SeedOrganization(t, pool, other)
			created, err := svc.Create(t.Context(), org, model.ExternalEventActor(), &datasourcessvc.CreateParams{
				Slug: dsSlug, Name: "Synthetic API key", Type: model.DataSourceTypeHTTP,
				ConfigJSON: json.RawMessage(`{"base_url":"https://api.example.test"}`), ReadOnly: true,
				Credentials: first,
			})
			require.NoError(t, err)
			require.True(t, created.ReadOnly)
			assertEncrypted := func(key string) {
				stored := stack.FindDataSource(t, org, dsSlug)
				var encrypted []byte
				err := pool.QueryRow(t.Context(), `SELECT c.encrypted_payload FROM credentials c JOIN organizations o ON o.id = c.organization_id WHERE o.slug = $1 AND c.slug = $2`, org, stored.Credential.Slug).Scan(&encrypted)
				require.NoError(t, err)
				require.NotEmpty(t, encrypted)
				require.NotContains(t, string(encrypted), key)
			}
			assertEncrypted("first-synthetic-key")
			owner := credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindDataSource, OrgSlug: org, DataSourceSlug: dsSlug}
			runtime, err := stack.Credentials.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
			require.NoError(t, err)
			require.JSONEq(t, string(firstJSON), string(runtime.PlaintextJSON))
			foreignOwner := owner
			foreignOwner.OrgSlug = other
			_, err = stack.Credentials.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: foreignOwner})
			require.Error(t, err)
			updated, err := svc.UpdateCredentials(t.Context(), org, dsSlug, model.ExternalEventActor(), &datasourcessvc.UpdateCredentialsParams{Credentials: second})
			require.NoError(t, err)
			require.True(t, updated.IsActive)
			runtime, err = stack.Credentials.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
			require.NoError(t, err)
			require.JSONEq(t, string(secondJSON), string(runtime.PlaintextJSON))
			assertEncrypted("second-synthetic-key")
			stored := stack.FindDataSource(t, org, dsSlug)
			metadata, err := json.Marshal(stored.Credential)
			require.NoError(t, err)
			require.NotContains(t, string(metadata), "synthetic-key")
		})
	}
}
