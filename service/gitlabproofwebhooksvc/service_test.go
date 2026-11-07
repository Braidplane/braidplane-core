package gitlabproofwebhooksvc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/gitlabproofwebhooks"
)

func TestEnsurePersistsResolvableBindingBeforeActivatingHook(t *testing.T) {
	activateErr := xerror.Unavailable(xerror.DataSource).Detail("persist active binding")
	cleanupErr := xerror.Unexpected().Detail("persist failed binding")
	for _, tc := range []struct {
		name                 string
		credentials          string
		oauth                bool
		activateErr, failErr error
	}{
		{name: "success", credentials: `{"auth_type":"https","username":"oauth2","token":"token"}`},
		{name: "final persistence failure", oauth: true, activateErr: activateErr},
		{name: "final persistence and cleanup failure", oauth: true, activateErr: activateErr, failErr: cleanupErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hookCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.oauth {
					assert.Equal(t, "Bearer token", r.Header.Get("Authorization"))
					assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"))
				} else {
					assert.Equal(t, "token", r.Header.Get("PRIVATE-TOKEN"))
				}
				switch r.Method + " " + r.URL.EscapedPath() {
				case "GET /api/v4/projects/acme%2Frepo":
					writeGitLabJSON(t, w, map[string]any{"id": 42})
				case "POST /api/v4/projects/42/hooks":
					hookCalls++
					writeGitLabJSON(t, w, map[string]any{"id": 9, "project_id": 42})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			repo := &fakeBindingRepo{activateErr: tc.activateErr, failErr: tc.failErr}
			cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": make([]byte, 32)}, make([]byte, 32))
			require.NoError(t, err)
			svc, err := New(testutil.FakeTransactionManager{}, repo, cipher, "https://app.example", gitprovider.Config{BaseURL: server.URL, AllowPrivateHosts: true, AllowPlaintext: true})
			require.NoError(t, err)

			credentials := tc.credentials
			if credentials == "" {
				credentials = `{"access_token":"token"}`
			}
			err = svc.Ensure(t.Context(), must.Slug("acme"), must.Slug("repo"), []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`), []byte(credentials))
			if tc.activateErr != nil {
				require.ErrorIs(t, err, tc.activateErr)
				if tc.failErr != nil {
					require.ErrorIs(t, err, tc.failErr)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, hookCalls)
			require.NotEmpty(t, repo.saved)
			assert.Equal(t, int64(42), repo.saved[0].ProjectID)
			assert.Equal(t, int64(0), repo.saved[0].HookID)
			if tc.activateErr == nil {
				require.Len(t, repo.activated, 1)
				assert.Equal(t, int64(9), repo.activated[0].HookID)
			} else {
				require.Len(t, repo.failed, 1)
				assert.Equal(t, int64(9), repo.failed[0].HookID)
			}
		})
	}
}

func TestResolveAuthenticatesStoredRouteAndSecret(t *testing.T) {
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": make([]byte, 32)}, make([]byte, 32))
	require.NoError(t, err)
	validSigningToken := "whsec_" + base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	for _, tc := range []struct {
		name         string
		storedRoute  string
		storedHash   string
		signingToken string
		missing      bool
		wantErr      bool
	}{
		{name: "valid", storedRoute: "route", signingToken: validSigningToken},
		{name: "route mismatch", storedRoute: "other", signingToken: validSigningToken, wantErr: true},
		{name: "hash mismatch", storedRoute: "route", storedHash: "different", signingToken: validSigningToken, wantErr: true},
		{name: "empty signing token", storedRoute: "route", wantErr: true},
		{name: "missing binding", missing: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeBindingRepo{}
			if !tc.missing {
				plaintext, marshalErr := json.Marshal(secretPayload{RouteKey: tc.storedRoute, SigningToken: tc.signingToken})
				require.NoError(t, marshalErr)
				encrypted, encryptErr := cipher.Seal(bindingScope(must.Slug("acme"), must.Slug("repo")), plaintext)
				require.NoError(t, encryptErr)
				routeHash := sha256.Sum256([]byte("route"))
				storedHash := fmt.Sprintf("%x", routeHash)
				if tc.storedHash != "" {
					storedHash = tc.storedHash
				}
				repo.binding = gitlabproofwebhooks.Binding{Organization: must.Slug("acme"), DataSource: must.Slug("repo"), ProjectID: 42, RouteKeyHash: storedHash, EncryptedSigningToken: encrypted}
			}
			svc, newErr := New(testutil.FakeTransactionManager{}, repo, cipher, "https://app.example", gitprovider.Config{})
			require.NoError(t, newErr)

			resolved, resolveErr := svc.Resolve(t.Context(), "route")
			if tc.wantErr {
				require.Error(t, resolveErr)
				return
			}
			require.NoError(t, resolveErr)
			assert.Equal(t, validSigningToken, resolved.SigningToken)
			assert.Equal(t, must.Slug("acme"), resolved.Organization)
			assert.Equal(t, "repo", resolved.Authority.ProviderConnectionID)
			assert.Equal(t, "42", resolved.Authority.RepositoryID)
		})
	}
}

func TestEnsureRejectsConcurrentSetupBeforeWebhookActivation(t *testing.T) {
	var hookCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeGitLabJSON(t, w, map[string]any{"id": 42})
			return
		}
		hookCalls++
	}))
	t.Cleanup(server.Close)
	repo := &fakeBindingRepo{claimErr: gitlabproofwebhooks.ErrSetupInProgress}
	cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": make([]byte, 32)}, make([]byte, 32))
	require.NoError(t, err)
	svc, err := New(testutil.FakeTransactionManager{}, repo, cipher, "https://app.example", gitprovider.Config{BaseURL: server.URL, AllowPrivateHosts: true, AllowPlaintext: true})
	require.NoError(t, err)

	err = svc.Ensure(t.Context(), must.Slug("acme"), must.Slug("repo"), []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`), []byte(`{"access_token":"token"}`))
	require.ErrorIs(t, err, gitlabproofwebhooks.ErrSetupInProgress)
	assert.Zero(t, hookCalls)
}

func TestEnsureReplacesWebhookAfterRepositoryChange(t *testing.T) {
	for _, projectID := range []int64{41, 42} {
		t.Run(fmt.Sprint(projectID), func(t *testing.T) {
			var hookCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.EscapedPath() {
				case "GET /api/v4/projects/acme%2Frepo":
					writeGitLabJSON(t, w, map[string]any{"id": 42})
				case "POST /api/v4/projects/42/hooks":
					hookCalls++
					writeGitLabJSON(t, w, map[string]any{"id": 10, "project_id": 42})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			repo := &fakeBindingRepo{binding: gitlabproofwebhooks.Binding{ProjectID: projectID, HookID: 9, Status: "active"}}
			cipher, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": make([]byte, 32)}, make([]byte, 32))
			require.NoError(t, err)
			svc, err := New(testutil.FakeTransactionManager{}, repo, cipher, "https://app.example", gitprovider.Config{BaseURL: server.URL, AllowPrivateHosts: true, AllowPlaintext: true})
			require.NoError(t, err)
			require.NoError(t, svc.Ensure(t.Context(), must.Slug("acme"), must.Slug("repo"), []byte(`{"repo_url":"https://gitlab.com/acme/repo.git"}`), []byte(`{"access_token":"token"}`)))
			if projectID == 42 {
				assert.Zero(t, hookCalls)
				assert.Empty(t, repo.saved)
			} else {
				assert.Equal(t, 1, hookCalls)
				require.Len(t, repo.saved, 1)
				assert.Zero(t, repo.saved[0].HookID)
				assert.Equal(t, int64(42), repo.saved[0].ProjectID)
			}
		})
	}
}

type fakeBindingRepo struct {
	saved       []gitlabproofwebhooks.Binding
	activated   []gitlabproofwebhooks.Binding
	failed      []gitlabproofwebhooks.Binding
	binding     gitlabproofwebhooks.Binding
	activateErr error
	failErr     error
	claimErr    error
}

func (r *fakeBindingRepo) GetByDataSource(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (gitlabproofwebhooks.Binding, error) {
	if r.binding.ProjectID == 0 {
		return gitlabproofwebhooks.Binding{}, gitlabproofwebhooks.ErrNotFound
	}
	return r.binding, nil
}

func (r *fakeBindingRepo) GetByRoute(_ context.Context, _ postgres.DBTX, routeKeyHash string) (gitlabproofwebhooks.Binding, error) {
	if r.binding.RouteKeyHash == "" || r.binding.RouteKeyHash != routeKeyHash {
		return gitlabproofwebhooks.Binding{}, gitlabproofwebhooks.ErrNotFound
	}
	return r.binding, nil
}

func (r *fakeBindingRepo) ClaimSetup(_ context.Context, _ postgres.DBTX, binding gitlabproofwebhooks.Binding) error {
	if r.claimErr != nil {
		return r.claimErr
	}
	r.saved = append(r.saved, binding)
	return nil
}

func (r *fakeBindingRepo) Activate(_ context.Context, _ postgres.DBTX, binding gitlabproofwebhooks.Binding) error {
	if r.activateErr != nil {
		return r.activateErr
	}
	r.activated = append(r.activated, binding)
	return nil
}

func (r *fakeBindingRepo) FailSetup(_ context.Context, _ postgres.DBTX, binding gitlabproofwebhooks.Binding) error {
	r.failed = append(r.failed, binding)
	return r.failErr
}

func writeGitLabJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(t, json.NewEncoder(w).Encode(value))
}
