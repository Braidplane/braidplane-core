package gitlab_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestPublishProofCheckExactCandidateAndRetries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state, head, project                 string
		existing, existingOnly, laterPage, foreign bool
		wantWrites                                 int
		wantStale                                  bool
	}{
		{name: "create", wantWrites: 1},
		{name: "update", existing: true, wantWrites: 1},
		{name: "later page", existing: true, laterPage: true, wantWrites: 1},
		{name: "ignore another author", existing: true, foreign: true, wantWrites: 1},
		{name: "no historical creation", existingOnly: true},
		{name: "invalidate old head", existing: true, existingOnly: true, head: strings.Repeat("b", 40), wantWrites: 1},
		{name: "new head during pagination", head: strings.Repeat("b", 40), wantStale: true},
		{name: "closed", state: "closed", wantStale: true},
		{name: "merged", state: "merged", wantStale: true},
		{name: "replaced repository", project: "43", wantStale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check := gitprovider.ProofCheck{RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40), ExternalID: "proof-check-1", DetailsURL: "https://app.example.test/proof-1", Summary: "Informational Proof: unverified", Conclusion: "neutral", ExistingOnly: tc.existingOnly}
			marker := "<!-- braidplane:" + check.ExternalID + ":" + check.HeadSHA + " -->\n"
			writes := 0
			noteBody := marker + "old evidence"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer managed-oauth", r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"))
				switch r.URL.EscapedPath() {
				case "/api/v4/user":
					writeJSON(t, w, map[string]any{"id": 5})
				case "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests/7":
					state, head, project := "opened", check.HeadSHA, "42"
					if tc.state != "" {
						state = tc.state
					}
					if tc.head != "" {
						head = tc.head
					}
					if tc.project != "" {
						project = tc.project
					}
					writeJSON(t, w, json.RawMessage(`{"iid":7,"state":"`+state+`","sha":"`+head+`","target_project_id":`+project+`}`))
				case "/api/v4/projects/42/merge_requests/7/notes", "/api/v4/projects/42/merge_requests/7/notes/9":
					if r.Method == http.MethodGet {
						if tc.laterPage && r.URL.Query().Get("page") != "2" {
							w.Header().Set("X-Next-Page", "2")
							writeJSON(t, w, []any{})
							return
						}
						if tc.existing || writes > 0 {
							author := 5
							if tc.foreign && writes == 0 {
								author = 6
							}
							writeJSON(t, w, []map[string]any{{"id": 9, "body": noteBody, "author": map[string]any{"id": author}}})
						} else {
							writeJSON(t, w, []any{})
						}
						return
					}
					writes++
					if tc.existing && !tc.foreign {
						assert.Equal(t, http.MethodPut, r.Method)
					} else {
						assert.Equal(t, http.MethodPost, r.Method)
					}
					var body struct {
						Body string `json:"body"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					noteBody = body.Body
					assert.Contains(t, noteBody, "Head: `"+check.HeadSHA+"`")
					assert.Contains(t, noteBody, check.Summary)
					assert.Contains(t, noteBody, check.DetailsURL)
					writeJSON(t, w, map[string]any{"id": 9})
				default:
					t.Errorf("unexpected provider request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			client, err := gitlabprovider.NewOAuth(transportsecurity.WithPlaintext(t.Context()), gitprovider.Config{BaseURL: server.URL, AllowPrivateHosts: true}, gitprovider.Credentials{Token: "managed-oauth"}, gitlabRepoRef())
			require.NoError(t, err)
			err = client.PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), check)
			if tc.wantStale {
				require.ErrorIs(t, err, gitprovider.ErrChangeStale)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantWrites, writes)
			if writes > 0 {
				require.NoError(t, client.PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), check))
				require.Equal(t, tc.wantWrites, writes, "identical retry must not write again")
			}
		})
	}
}

func TestPublishProofCheckProviderErrorsAreTypedAndRedacted(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 403, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				writeJSON(t, w, map[string]any{"message": "glpat-secret denied"})
			}))
			t.Cleanup(server.Close)
			err := newClient(t, server.URL).PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProofCheck{RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40), ExternalID: "proof-1", Conclusion: "neutral"})
			var typed *xerror.Error
			require.ErrorAs(t, err, &typed)
			kind := xerror.KindUnavailable
			switch status {
			case http.StatusUnauthorized:
				kind = xerror.KindUnauthorized
			case http.StatusForbidden:
				kind = xerror.KindForbidden
			}
			require.Equal(t, kind, typed.Kind())
			require.NotContains(t, err.Error(), "glpat-secret")
		})
	}
}

func TestPublishProofCheckRecoversAmbiguousCreationWithoutDuplicates(t *testing.T) {
	t.Parallel()
	check := gitprovider.ProofCheck{RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40), ExternalID: "proof-1", Conclusion: "neutral"}
	var saved string
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/user":
			writeJSON(t, w, map[string]any{"id": 5})
		case "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests/7":
			writeJSON(t, w, map[string]any{"iid": 7, "state": "opened", "sha": check.HeadSHA, "target_project_id": 42})
		case "/api/v4/projects/42/merge_requests/7/notes":
			if r.Method == http.MethodGet {
				if saved == "" {
					writeJSON(t, w, []any{})
				} else {
					writeJSON(t, w, []map[string]any{{"id": 9, "body": saved, "author": map[string]any{"id": 5}}})
				}
				return
			}
			assert.Equal(t, http.MethodPost, r.Method)
			creates++
			var body struct {
				Body string `json:"body"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			saved = body.Body
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(t, w, map[string]any{"message": "response lost after creation"})
		default:
			t.Errorf("unexpected mutation or request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL)
	err := client.PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), check)
	var typed *xerror.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, xerror.KindUnavailable, typed.Kind())
	require.Equal(t, 1, creates, "SDK must not repeat an ambiguous creation")
	require.NoError(t, client.PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), check))
	require.Equal(t, 1, creates, "worker retry must find the saved result")
}
