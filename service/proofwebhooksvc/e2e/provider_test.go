//go:build provider_e2e

package e2e

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v85/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider/providerclient"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
)

const providerTimeout = 30 * time.Second

func TestProviderBoundProofIntake(t *testing.T) {
	common := loadCommonProviderConfig(t)
	for _, provider := range []repostore.Provider{repostore.ProviderGitHub, repostore.ProviderGitLab} {
		t.Run(string(provider), func(t *testing.T) {
			cfg := loadProviderConfig(t, provider)
			providerCommon := common
			if project := strings.TrimSpace(os.Getenv("BRAIDPLANE_PROOF_" + strings.ToUpper(string(provider)) + "_PROJECT")); project != "" {
				providerCommon.project = project
			}
			auth := newProviderAuth(t, cfg)
			reader, err := providerclient.NewProposalReader(t.Context(), provider, auth.RepoURL, auth.Env)
			require.NoError(t, err)
			proposal, err := reader.GetProposal(t.Context(), gitprovider.ProposalGetRequest{ProposalID: cfg.changeID})
			require.NoError(t, err)
			require.Equal(t, gitprovider.ProposalStateOpen, proposal.State, "provider fixture must be an open PR/MR")

			before, err := candidateEvents(t.Context(), providerCommon, cfg, proposal)
			require.NoError(t, err)
			var redeliver func(*testing.T) []byte
			var callback string
			if provider == repostore.ProviderGitHub {
				callback, redeliver = githubDelivery(t, common, cfg, proposal)
			} else {
				callback, redeliver = gitlabDelivery(t, cfg, proposal)
			}

			payload := redeliver(t)
			after := waitForCandidateEvents(t, providerCommon, cfg, proposal)
			require.NotEmpty(t, after, "provider delivery must reach the exact tenant-bound candidate")
			if len(before) > 0 {
				require.Len(t, after, len(before), "replaying an existing provider delivery must not emit another candidate event")
			}

			redeliver(t)
			assertStableCandidateEvents(t, providerCommon, cfg, proposal, len(after))
			rejectInvalidSignature(t, provider, callback, payload)
		})
	}
}

type commonProviderConfig struct {
	serverURL    string
	apiKey       string
	organization string
	project      string
	environment  string
}

type providerConfig struct {
	provider     repostore.Provider
	repoURL      string
	token        string
	connectionID string
	changeID     string
	pipelinePath string
	githubAppID  int64
	githubKey    string
}

func loadCommonProviderConfig(t *testing.T) commonProviderConfig {
	t.Helper()
	value := requiredEnv(t)
	serverURL := strings.TrimSuffix(strings.TrimRight(value("BRAIDPLANE_PROOF_SERVER_URL"), "/"), "/api")
	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)
	require.NotEmpty(t, parsed.Scheme)
	require.NotEmpty(t, parsed.Host)
	return commonProviderConfig{
		serverURL: serverURL, apiKey: value("BRAIDPLANE_PROOF_API_KEY"),
		organization: value("BRAIDPLANE_PROOF_ORG"), project: value("BRAIDPLANE_PROOF_PROJECT"),
		environment: value("BRAIDPLANE_PROOF_ENVIRONMENT"),
	}
}

func loadProviderConfig(t *testing.T, provider repostore.Provider) providerConfig {
	t.Helper()
	value := requiredEnv(t)
	prefix := "BRAIDPLANE_PROOF_" + strings.ToUpper(string(provider)) + "_"
	cfg := providerConfig{
		provider: provider, repoURL: value(prefix + "REPO_URL"), token: value(prefix + "TOKEN"),
		changeID: value(prefix + "CHANGE_ID"), pipelinePath: value(prefix + "PIPELINE_PATH"),
	}
	if provider == repostore.ProviderGitHub {
		cfg.connectionID = value(prefix + "INSTALLATION_ID")
		cfg.githubAppID = requiredInt64(t, value(prefix+"APP_ID"), prefix+"APP_ID")
		cfg.githubKey = value(prefix + "PRIVATE_KEY")
	} else {
		cfg.connectionID = value(prefix + "DATA_SOURCE")
	}
	return cfg
}

func requiredEnv(t *testing.T) func(string) string {
	t.Helper()
	return func(name string) string {
		got := strings.TrimSpace(os.Getenv(name))
		require.NotEmpty(t, got, name+" is required")
		return got
	}
}

func requiredInt64(t *testing.T, value, name string) int64 {
	t.Helper()
	got, err := strconv.ParseInt(value, 10, 64)
	require.NoError(t, err, name+" must be an integer")
	require.Positive(t, got, name+" must be positive")
	return got
}

func newProviderAuth(t *testing.T, cfg providerConfig) *datasourcessvc.GitDataSourceAuth {
	t.Helper()
	username, tokenKey := "x-access-token", "GITHUB_TOKEN"
	if cfg.provider == repostore.ProviderGitLab {
		username, tokenKey = "oauth2", "GITLAB_TOKEN"
	}
	env, modifyURL, cleanup, err := gitconnector.AuthEnv(gitconnector.Credentials{
		AuthType: gitconnector.AuthTypeHTTPS, Username: username, Token: cfg.token,
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	repoURL, err := modifyURL(cfg.repoURL)
	require.NoError(t, err)
	return &datasourcessvc.GitDataSourceAuth{
		Provider: string(cfg.provider), RepoURL: repoURL,
		Env: append(env, tokenKey+"="+cfg.token), Cleanup: func() {},
	}
}

func githubDelivery(t *testing.T, common commonProviderConfig, cfg providerConfig, proposal *gitprovider.ProposalRef) (string, func(*testing.T) []byte) {
	t.Helper()
	client := gogithub.NewClient(&http.Client{Timeout: providerTimeout}).WithAuthToken(githubAppJWT(t, cfg.githubAppID, cfg.githubKey))
	hook, _, err := client.Apps.GetHookConfig(t.Context())
	require.NoError(t, err)
	callback := hook.GetURL()
	callbackURL, err := url.Parse(callback)
	require.NoError(t, err)
	serverURL, err := url.Parse(common.serverURL)
	require.NoError(t, err)
	require.Equal(t, "https", serverURL.Scheme)
	require.True(t, strings.HasPrefix(serverURL.Hostname(), "app."), "Braidplane server host must use the public app hostname")
	require.Empty(t, serverURL.Port(), "Braidplane server URL must use the default HTTPS port")
	expectedCallback := (&url.URL{
		Scheme: "https",
		Host:   "api." + strings.TrimPrefix(serverURL.Hostname(), "app."),
		Path:   "/webhooks/github/pull-requests",
	}).String()
	require.Equal(t, "https", callbackURL.Scheme)
	require.Nil(t, callbackURL.User)
	require.Equal(t, "/webhooks/github/pull-requests", callbackURL.Path)
	require.Empty(t, callbackURL.RawQuery)
	require.Empty(t, callbackURL.Fragment)
	require.Equal(t, expectedCallback, callback)
	require.Equal(t, "json", hook.GetContentType())
	require.Equal(t, "0", hook.GetInsecureSSL())

	deliveries, _, err := client.Apps.ListHookDeliveries(t.Context(), &gogithub.ListCursorOptions{PerPage: 100})
	require.NoError(t, err)
	var selected *gogithub.HookDelivery
	for _, delivery := range deliveries {
		if delivery.GetEvent() != "pull_request" || delivery.GetRepositoryID() != requiredInt64(t, proposal.RepositoryID, "GitHub repository ID") {
			continue
		}
		detail, _, getErr := client.Apps.GetHookDelivery(t.Context(), delivery.GetID())
		require.NoError(t, getErr)
		parsed, parseErr := detail.ParseRequestPayload()
		if parseErr != nil {
			continue
		}
		event, ok := parsed.(*gogithub.PullRequestEvent)
		if ok && detail.GetStatusCode() == http.StatusAccepted &&
			(event.GetAction() == "opened" || event.GetAction() == "reopened" || event.GetAction() == "synchronize" || event.GetAction() == "edited") &&
			strconv.Itoa(event.GetNumber()) == proposal.ID && event.GetPullRequest().GetState() == "open" &&
			strings.EqualFold(event.GetPullRequest().GetHead().GetSHA(), proposal.HeadSHA) &&
			event.GetPullRequest().GetBase().GetRef() == proposal.TargetBranch &&
			event.GetInstallation().GetID() == requiredInt64(t, cfg.connectionID, "GitHub installation ID") {
			selected = detail
			break
		}
	}
	require.NotNil(t, selected, "a recent exact GitHub pull_request delivery is required")
	require.Equal(t, http.StatusAccepted, selected.GetStatusCode(), "the original GitHub delivery must have reached staging")
	payload := append([]byte(nil), (*selected.Request.RawPayload)...)
	return callback, func(t *testing.T) []byte {
		t.Helper()
		replayGitHubDelivery(t, callback, selected.Request)
		return payload
	}
}

func replayGitHubDelivery(t *testing.T, callback string, delivery *gogithub.HookRequest) {
	t.Helper()
	require.NotNil(t, delivery)
	require.NotNil(t, delivery.RawPayload)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, callback, bytes.NewReader(*delivery.RawPayload))
	require.NoError(t, err)
	for _, header := range []string{"Content-Type", "X-GitHub-Event", "X-Hub-Signature-256"} {
		value := delivery.GetHeader(header)
		require.NotEmpty(t, value, "retained GitHub delivery must include %s", header)
		req.Header.Set(header, value)
	}
	requestID := uuid.NewString()
	req.Header.Set("X-GitHub-Delivery", requestID)
	req.Header.Set("X-Request-ID", requestID)
	t.Logf("GitHub replay request_id=%s", requestID)
	response, err := (&http.Client{
		Timeout: providerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	require.Equal(t, http.StatusAccepted, response.StatusCode)
}

func githubAppJWT(t *testing.T, appID int64, keyPEM string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(keyPEM))
	require.NotNil(t, block, "GitHub App private key must be PEM")
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		require.NoError(t, parseErr)
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		require.True(t, ok, "GitHub App private key must be RSA")
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]int64{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

type gitlabHook struct {
	ID                    int64  `json:"id"`
	ProjectID             int64  `json:"project_id"`
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	MergeRequestsEvents   bool   `json:"merge_requests_events"`
	EnableSSLVerification bool   `json:"enable_ssl_verification"`
	SigningTokenPresent   bool   `json:"signing_token_present"`
}

type gitlabHookEvent struct {
	ID             int64           `json:"id"`
	Trigger        string          `json:"trigger"`
	URL            string          `json:"url"`
	RequestData    json.RawMessage `json:"request_data"`
	ResponseStatus json.RawMessage `json:"response_status"`
}

type gitlabMergeRequestEvent struct {
	Project struct {
		ID int64 `json:"id"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int64  `json:"iid"`
		State        string `json:"state"`
		Action       string `json:"action"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

func gitlabDelivery(t *testing.T, cfg providerConfig, proposal *gitprovider.ProposalRef) (string, func(*testing.T) []byte) {
	t.Helper()
	repository, err := gitprovider.RepositoryRefFromRemoteURL(repostore.ProviderGitLab, cfg.repoURL)
	require.NoError(t, err)
	repoURL, err := url.Parse(cfg.repoURL)
	require.NoError(t, err)
	hooksAPI := repoURL.Scheme + "://" + repoURL.Host + "/api/v4/projects/" + url.PathEscape(repository.ProjectPath) + "/hooks"
	client := &http.Client{Timeout: providerTimeout}
	var hooks []gitlabHook
	doJSON(t, client, http.MethodGet, hooksAPI, cfg.token, nil, http.StatusOK, &hooks)
	managed := make([]gitlabHook, 0, 1)
	for _, hook := range hooks {
		if hook.Name == "Braidplane Proof" {
			managed = append(managed, hook)
		}
	}
	require.Len(t, managed, 1, "exactly one managed Braidplane Proof hook is required")
	hook := managed[0]
	require.Equal(t, "Braidplane Proof", hook.Name)
	require.True(t, hook.MergeRequestsEvents)
	require.True(t, hook.EnableSSLVerification)
	require.True(t, hook.SigningTokenPresent)
	require.Equal(t, requiredInt64(t, proposal.RepositoryID, "GitLab project ID"), hook.ProjectID)
	api := hooksAPI + "/" + strconv.FormatInt(hook.ID, 10)

	var events []gitlabHookEvent
	doJSON(t, client, http.MethodGet, api+"/events", cfg.token, nil, http.StatusOK, &events)
	var selected *gitlabHookEvent
	for i := range events {
		if events[i].Trigger != "merge_request_hooks" || gitlabStatus(events[i].ResponseStatus) != http.StatusAccepted {
			continue
		}
		var event gitlabMergeRequestEvent
		if json.Unmarshal(events[i].RequestData, &event) == nil && gitlabUpsertAction(event.ObjectAttributes.Action) && event.ObjectAttributes.State == "opened" &&
			strconv.FormatInt(event.ObjectAttributes.IID, 10) == proposal.ID &&
			strings.EqualFold(event.ObjectAttributes.LastCommit.ID, proposal.HeadSHA) &&
			event.ObjectAttributes.SourceBranch == proposal.SourceBranch && event.ObjectAttributes.TargetBranch == proposal.TargetBranch &&
			strconv.FormatInt(event.Project.ID, 10) == proposal.RepositoryID {
			selected = &events[i]
			break
		}
	}
	require.NotNil(t, selected, "a recent exact GitLab merge_request_hooks delivery is required")
	require.Equal(t, hook.URL, selected.URL)
	return hook.URL, func(t *testing.T) []byte {
		t.Helper()
		var result gitlabHookEvent
		doJSON(t, client, http.MethodPost, api+"/events/"+strconv.FormatInt(selected.ID, 10)+"/resend", cfg.token, nil, http.StatusCreated, &result)
		require.Equal(t, http.StatusAccepted, gitlabStatus(result.ResponseStatus))
		return append([]byte(nil), selected.RequestData...)
	}
}

func gitlabUpsertAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "open", "reopen", "update", "approved", "unapproved", "approval", "unapproval":
		return true
	default:
		return false
	}
}

func gitlabStatus(raw json.RawMessage) int {
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		number, _ = strconv.Atoi(text)
	}
	return number
}

type eventList struct {
	Events []struct {
		Metadata map[string]any `json:"metadata"`
	} `json:"events"`
}

func candidateEvents(ctx context.Context, common commonProviderConfig, cfg providerConfig, proposal *gitprovider.ProposalRef) ([]map[string]any, error) {
	list, err := listCandidateEvents(ctx, common)
	if err != nil {
		return nil, err
	}
	want := map[string]string{
		"project_slug": common.project, "environment_slug": common.environment,
		"provider": string(cfg.provider), "provider_connection_id": cfg.connectionID,
		"repository_id": proposal.RepositoryID, "change_request_id": proposal.ID,
		"source_ref": "refs/heads/" + gitprovider.NormalizeBranchName(proposal.SourceBranch),
		"target_ref": "refs/heads/" + gitprovider.NormalizeBranchName(proposal.TargetBranch),
		"head_sha":   strings.ToLower(proposal.HeadSHA), "pipeline_path": cfg.pipelinePath,
	}
	var matches []map[string]any
	for _, event := range list.Events {
		match := true
		for key, value := range want {
			if fmt.Sprint(event.Metadata[key]) != value {
				match = false
				break
			}
		}
		if match {
			matches = append(matches, event.Metadata)
		}
	}
	return matches, nil
}

func listCandidateEvents(ctx context.Context, common commonProviderConfig) (eventList, error) {
	endpoint := fmt.Sprintf("%s/api/organizations/%s/events?limit=100&order=desc", common.serverURL, url.PathEscape(common.organization))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return eventList{}, err
	}
	req.Header.Set("Authorization", "Bearer "+common.apiKey)
	response, err := (&http.Client{Timeout: providerTimeout}).Do(req)
	if err != nil {
		return eventList{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return eventList{}, err
	}
	if response.StatusCode != http.StatusOK {
		return eventList{}, fmt.Errorf("list Braidplane candidate events: status %d: %s", response.StatusCode, body)
	}
	var list eventList
	if err := json.Unmarshal(body, &list); err != nil {
		return eventList{}, err
	}
	return list, nil
}

func waitForCandidateEvents(t *testing.T, common commonProviderConfig, cfg providerConfig, proposal *gitprovider.ProposalRef) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(providerTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		events, err := candidateEvents(t.Context(), common, cfg, proposal)
		if err == nil && len(events) > 0 {
			return events
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	require.NoError(t, lastErr)
	logCandidateEventNearMatches(t, common, cfg, proposal)
	require.FailNow(t, "provider delivery did not produce the exact candidate event")
	return nil
}

func logCandidateEventNearMatches(t *testing.T, common commonProviderConfig, cfg providerConfig, proposal *gitprovider.ProposalRef) {
	t.Helper()
	list, err := listCandidateEvents(t.Context(), common)
	if err != nil {
		t.Logf("candidate near-match readback failed: %v", err)
		return
	}
	for _, event := range list.Events {
		metadata := event.Metadata
		if fmt.Sprint(metadata["provider"]) != string(cfg.provider) ||
			fmt.Sprint(metadata["repository_id"]) != proposal.RepositoryID ||
			fmt.Sprint(metadata["change_request_id"]) != proposal.ID ||
			!strings.EqualFold(fmt.Sprint(metadata["head_sha"]), proposal.HeadSHA) {
			continue
		}
		t.Logf(
			"candidate near match: provider_connection_id=%q project_slug=%q environment_slug=%q source_ref=%q target_ref=%q pipeline_path=%q",
			fmt.Sprint(metadata["provider_connection_id"]), fmt.Sprint(metadata["project_slug"]), fmt.Sprint(metadata["environment_slug"]),
			fmt.Sprint(metadata["source_ref"]), fmt.Sprint(metadata["target_ref"]), fmt.Sprint(metadata["pipeline_path"]),
		)
	}
}

func assertStableCandidateEvents(t *testing.T, common commonProviderConfig, cfg providerConfig, proposal *gitprovider.ProposalRef, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := candidateEvents(t.Context(), common, cfg, proposal)
		require.NoError(t, err)
		require.Len(t, events, want, "replaying the same provider delivery must stay idempotent")
		time.Sleep(time.Second)
	}
}

func rejectInvalidSignature(t *testing.T, provider repostore.Provider, callback string, payload []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, callback, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if provider == repostore.ProviderGitHub {
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "webhook-fixture-rejected")
		req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	} else {
		req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
		req.Header.Set("webhook-id", "webhook-fixture-rejected")
		req.Header.Set("webhook-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
		req.Header.Set("webhook-signature", "v1,invalid")
	}
	response, err := (&http.Client{Timeout: providerTimeout}).Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func doJSON(t *testing.T, client *http.Client, method, endpoint, token string, body io.Reader, wantStatus int, target any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint, body)
	require.NoError(t, err)
	req.Header.Set("PRIVATE-TOKEN", token)
	response, err := client.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	require.Equal(t, wantStatus, response.StatusCode, string(data))
	if target != nil && len(data) > 0 {
		require.NoError(t, json.Unmarshal(data, target))
	}
}
