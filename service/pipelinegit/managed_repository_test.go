package pipelinegit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repositoryGrantFake struct {
	connection credentialssvc.OrganizationGitLabConnection
	runtime    credentialssvc.RuntimeCredential
	resolved   bool
}

func (f *repositoryGrantFake) GetOrganizationGitLabConnection(_ context.Context, org slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	if org != "acme" {
		panic("wrong organization")
	}
	return f.connection, nil
}

func (f *repositoryGrantFake) ResolveRuntimeCredential(_ context.Context, p credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error) {
	if p.Owner.OrgSlug != "acme" || p.Owner.Kind != credentialssvc.CredentialOwnerKindOrganizationGitLab {
		panic("wrong credential owner")
	}
	f.resolved = true
	return f.runtime, nil
}

func TestManagedRepositoryRequiresActiveAPIGrant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status credentialssvc.CredentialStatus
		scopes []string
	}{
		{"read only", credentialssvc.CredentialStatusActive, []string{"read_api"}},
		{"repository write only", credentialssvc.CredentialStatusActive, []string{"write_repository"}},
		{"revoked", credentialssvc.CredentialStatusRevoked, []string{"api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &repositoryGrantFake{connection: credentialssvc.OrganizationGitLabConnection{Credential: credentialssvc.CredentialMetadata{Status: tc.status, OAuth: credentialssvc.CredentialOAuthMetadata{Scopes: tc.scopes}}}}
			b, err := NewManagedRepositoryBootstrapper(f, gitprovider.Config{})
			require.NoError(t, err)
			_, err = b.BootstrapGitLabRepository(t.Context(), "acme", gitprovider.RepositoryBootstrapRequest{})
			require.ErrorContains(t, err, "API write permission")
			require.False(t, f.resolved)
		})
	}
}

func TestManagedRepositoryUsesFreshOrganizationOAuthGrant(t *testing.T) {
	var marker string
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "Bearer isolated-test-grant", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/alice%2Fpipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /api/v4/namespaces/alice":
			_, _ = w.Write([]byte(`{"id":3,"kind":"user","full_path":"alice"}`))
		case "POST /api/v4/projects":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			marker = body["description"].(string)
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"id": 42, "visibility": "private", "description": marker, "path_with_namespace": "alice/pipelines", "http_url_to_repo": "https://gitlab.com/alice/pipelines.git"}))
		case "POST /api/v4/projects/42/repository/commits":
			_, _ = w.Write([]byte(`{"id":"initial-sha"}`))
		case "PUT /api/v4/projects/42":
			_, _ = w.Write([]byte(`{"id":42}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	f := &repositoryGrantFake{connection: credentialssvc.OrganizationGitLabConnection{Credential: credentialssvc.CredentialMetadata{Status: credentialssvc.CredentialStatusActive, OAuth: credentialssvc.CredentialOAuthMetadata{Scopes: []string{"api"}}}}, runtime: credentialssvc.RuntimeCredential{Kind: credentialssvc.CredentialKindOAuth2Grant, PlaintextJSON: []byte(`{"access_token":"isolated-test-grant"}`)}}
	b, err := NewManagedRepositoryBootstrapper(f, gitprovider.Config{BaseURL: srv.URL + "/api/v4", AllowPrivateHosts: true, AllowPlaintext: true})
	require.NoError(t, err)
	got, err := b.BootstrapGitLabRepository(t.Context(), "acme", gitprovider.RepositoryBootstrapRequest{Owner: "alice", Name: "pipelines", DefaultBranch: "main", Marker: "repo-isolated", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "safe"}}})
	require.NoError(t, err)
	require.Equal(t, "42", got.RepositoryID)
	require.True(t, f.resolved)
	require.NotEmpty(t, marker)
	require.Equal(t, 5, requests)
}
