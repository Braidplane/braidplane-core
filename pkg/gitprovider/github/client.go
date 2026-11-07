package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	gogithub "github.com/google/go-github/v85/github"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultTimeout       = 30 * time.Second
	defaultBaseURL       = "https://api.github.com"
	bootstrapDescription = "Git-first Braidplane pipeline definitions"
	proofCheckName       = "Braidplane Proof"
)

// PublishProofCheck idempotently projects one sanitized receipt onto its exact commit.
func (c *Client) PublishProofCheck(ctx context.Context, check gitprovider.ProofCheck) error {
	filter := "all"
	opts := &gogithub.ListCheckRunsOptions{
		CheckName: gogithub.Ptr(proofCheckName), Filter: &filter,
	}
	status := "completed"
	output := &gogithub.CheckRunOutput{Title: gogithub.Ptr(proofCheckName), Summary: &check.Summary}
	actions := []*gogithub.CheckRunAction{{Label: "Re-run Proof", Description: "Recompile Proof for this commit", Identifier: "rerequest"}}
	for {
		runs, response, err := c.client.Checks.ListCheckRunsForRef(ctx, c.repo.Owner, c.repo.Name, check.HeadSHA, opts)
		if err != nil {
			return mapProviderError(err, c.token)
		}
		for _, run := range runs.CheckRuns {
			if run.GetExternalID() != check.ExternalID {
				continue
			}
			if run.GetStatus() == status && run.GetConclusion() == check.Conclusion && run.GetDetailsURL() == check.DetailsURL && run.GetOutput().GetSummary() == check.Summary {
				return nil
			}
			_, _, err = c.client.Checks.UpdateCheckRun(ctx, c.repo.Owner, c.repo.Name, run.GetID(), gogithub.UpdateCheckRunOptions{
				Name: proofCheckName, DetailsURL: &check.DetailsURL, ExternalID: &check.ExternalID,
				Status: &status, Conclusion: &check.Conclusion, Output: output, Actions: actions,
			})
			return mapProviderError(err, c.token)
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		opts.Page = response.NextPage
	}
	if check.ExistingOnly {
		return nil
	}
	var err error
	_, _, err = c.client.Checks.CreateCheckRun(ctx, c.repo.Owner, c.repo.Name, gogithub.CreateCheckRunOptions{
		Name: proofCheckName, HeadSHA: check.HeadSHA, DetailsURL: &check.DetailsURL,
		ExternalID: &check.ExternalID, Status: &status, Conclusion: &check.Conclusion, Output: output, Actions: actions,
	})
	return mapProviderError(err, c.token)
}

// Client wraps GitHub pull request APIs behind Braidplane's provider proposal contract.
type Client struct {
	repo                     gitprovider.RepositoryRef
	token                    string
	githubContentsPermission *string
	client                   *gogithub.Client
}

// New creates a GitHub provider proposal client.
func New(
	ctx context.Context,
	config gitprovider.Config,
	creds gitprovider.Credentials,
	repo gitprovider.RepositoryRef,
) (*Client, error) {
	if config.AllowPlaintext {
		ctx = transportsecurity.WithPlaintext(ctx)
	}
	token := strings.TrimSpace(creds.Token)
	if token == "" {
		return nil, xerror.InvalidCredentials().Detail("github token is required")
	}
	if repo.Provider != repostore.ProviderGitHub {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("github repository ref is required")
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if err := validateBaseURL(ctx, baseURL, config.AllowPrivateHosts); err != nil {
		return nil, err
	}

	timeout := defaultTimeout
	if config.TimeoutSeconds > 0 {
		timeout = time.Duration(config.TimeoutSeconds) * time.Second
	}
	transport, err := guardedTransport(config.AllowPrivateHosts)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{Timeout: timeout, Transport: transportsecurity.HTTPTransport(transport, transportsecurity.PlaintextAllowed(ctx)), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client := gogithub.NewClient(httpClient).WithAuthToken(token)
	if baseURL != defaultBaseURL {
		parsed, err := url.Parse(baseURL)
		if err != nil {
			return nil, xerror.InvalidInput(xerror.DataSource).With(err)
		}
		if !strings.HasSuffix(parsed.Path, "/") {
			parsed.Path += "/"
		}
		client.BaseURL = parsed
	}

	return &Client{repo: repo, token: token, githubContentsPermission: creds.GitHubContentsPermission, client: client}, nil
}

// ListOpenProposals returns open pull requests for the requested source/target branches.
func (c *Client) ListOpenProposals(
	ctx context.Context,
	req gitprovider.ProposalListRequest,
) ([]gitprovider.ProposalRef, error) {
	base := gitprovider.NormalizeBranchName(req.TargetBranch)
	head := c.repo.Owner + ":" + gitprovider.NormalizeBranchName(req.SourceBranch)
	pulls, _, err := c.client.PullRequests.List(ctx, c.repo.Owner, c.repo.Name, &gogithub.PullRequestListOptions{
		State: "open",
		Head:  head,
		Base:  base,
	})
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}

	results := make([]gitprovider.ProposalRef, 0, len(pulls))
	for _, pull := range pulls {
		results = append(results, c.proposalRefFromPull(pull))
	}
	return results, nil
}

// GetProposal returns one pull request, including closed and merged states.
func (c *Client) GetProposal(
	ctx context.Context,
	req gitprovider.ProposalGetRequest,
) (*gitprovider.ProposalRef, error) {
	number, err := strconv.Atoi(strings.TrimSpace(req.ProposalID))
	if err != nil || number <= 0 {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("github pull request number is invalid")
	}
	pull, _, err := c.client.PullRequests.Get(ctx, c.repo.Owner, c.repo.Name, number)
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}
	ref := c.proposalRefFromPull(pull)
	return &ref, nil
}

// CreateProposal creates a GitHub pull request.
func (c *Client) CreateProposal(
	ctx context.Context,
	req gitprovider.ProposalRequest,
) (*gitprovider.ProposalRef, error) {
	pull, _, err := c.client.PullRequests.Create(ctx, c.repo.Owner, c.repo.Name, &gogithub.NewPullRequest{
		Title: gogithub.Ptr(req.Title),
		Head:  gogithub.Ptr(gitprovider.NormalizeBranchName(req.SourceBranch)),
		Base:  gogithub.Ptr(gitprovider.NormalizeBranchName(req.TargetBranch)),
		Body:  gogithub.Ptr(req.Body),
	})
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}

	ref := c.proposalRefFromPull(pull)
	return &ref, nil
}

// UpdateProposal updates a GitHub pull request title and body.
func (c *Client) UpdateProposal(
	ctx context.Context,
	req gitprovider.ProposalUpdateRequest,
) (*gitprovider.ProposalRef, error) {
	number, err := strconv.Atoi(strings.TrimSpace(req.ProposalID))
	if err != nil || number <= 0 {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("github pull request number is invalid")
	}

	pull, _, err := c.client.PullRequests.Edit(ctx, c.repo.Owner, c.repo.Name, number, &gogithub.PullRequest{
		Title: gogithub.Ptr(req.Title),
		Body:  gogithub.Ptr(req.Body),
	})
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}

	ref := c.proposalRefFromPull(pull)
	return &ref, nil
}

// ValidateRepository verifies repository access and the exact branch selected for onboarding.
func (c *Client) ValidateRepository(ctx context.Context, branch string) error {
	repo, _, err := c.client.Repositories.Get(ctx, c.repo.Owner, c.repo.Name)
	if err != nil {
		return mapProviderError(err, c.token)
	}
	if c.githubContentsPermission != nil {
		if *c.githubContentsPermission != "write" {
			return xerror.Forbidden(xerror.DataSource).Detail("GitHub App installation cannot write to the repository")
		}
	} else if repo.GetPermissions() == nil || !repo.GetPermissions().GetPush() {
		return xerror.Forbidden(xerror.DataSource).Detail("GitHub connection cannot write to the repository")
	}
	if _, _, err := c.client.Repositories.GetBranch(
		ctx,
		c.repo.Owner,
		c.repo.Name,
		gitprovider.NormalizeBranchName(branch),
		0,
	); err != nil {
		return mapProviderError(err, c.token)
	}
	return nil
}

// BootstrapRepository creates a private GitHub repository with the exact initial template.
func (c *Client) BootstrapRepository(
	ctx context.Context,
	req gitprovider.RepositoryBootstrapRequest,
) (result *gitprovider.RepositoryBootstrapResult, resultErr error) {
	if strings.TrimSpace(req.Owner) != c.repo.Owner || strings.TrimSpace(req.Name) != c.repo.Name {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub repository target does not match the selected connection")
	}
	if err := validateGitHubBootstrapFiles(req.Files); err != nil {
		return nil, err
	}
	marker, resumable := githubBootstrapMarker(req.Marker)
	if existing, response, err := c.client.Repositories.Get(ctx, c.repo.Owner, c.repo.Name); err == nil {
		if !resumable || !c.ownedRepository(existing, marker) || !matchesExpectedRepositoryID(existing, req.ExpectedRepositoryID) {
			return nil, xerror.AlreadyExists(xerror.RepoFile).Detail("GitHub repository already exists and is not owned by this setup")
		}
		result = c.bootstrapResult(existing, "")
		return c.resumeOwnedRepository(ctx, existing, req, result)
	} else if response == nil || response.StatusCode != http.StatusNotFound {
		return nil, mapProviderError(err, c.token)
	}
	if strings.TrimSpace(req.ExpectedRepositoryID) != "" {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap repository is missing for its expected immutable ID")
	}

	user, _, err := c.client.Users.Get(ctx, "")
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}
	organization := c.repo.Owner
	ownerKind := "organization"
	if strings.EqualFold(user.GetLogin(), c.repo.Owner) {
		organization = ""
		ownerKind = "user"
	}
	createDescription := marker
	created, response, err := c.client.Repositories.Create(ctx, organization, &gogithub.Repository{
		Name:        gogithub.Ptr(c.repo.Name),
		Description: gogithub.Ptr(createDescription),
		Private:     gogithub.Ptr(true),
		AutoInit:    gogithub.Ptr(false),
	})
	createdThisCall := err == nil
	if err != nil {
		createErr := mapProviderError(err, c.token)
		if response != nil && response.StatusCode < http.StatusInternalServerError {
			return nil, createErr
		}
		created, err = c.recoverAmbiguousRepositoryCreate(ctx, createDescription, createErr)
		if err != nil {
			return nil, err
		}
	}
	if !c.ownedRepository(created, createDescription) {
		return nil, xerror.Unexpected().Detail("GitHub created repository identity is invalid")
	}
	result = c.bootstrapResult(created, ownerKind)
	complete := false
	defer func() {
		if complete || !createdThisCall || resumable {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTimeout)
		defer cancel()
		if cleanupErr := c.deleteOwnedRepository(cleanupCtx, created.GetID()); cleanupErr != nil {
			resultErr = xerror.Join(
				resultErr,
				gitprovider.ErrRepositoryCleanupFailed.With(mapProviderError(cleanupErr, c.token)),
			)
		}
	}()

	result, resultErr = c.initializeOwnedRepository(ctx, created, req, result, resumable, 0)
	complete = resultErr == nil
	return result, resultErr
}

func (c *Client) resumeOwnedRepository(ctx context.Context, repository *gogithub.Repository, req gitprovider.RepositoryBootstrapRequest, result *gitprovider.RepositoryBootstrapResult) (*gitprovider.RepositoryBootstrapResult, error) {
	branch := gitprovider.NormalizeBranchName(req.DefaultBranch)
	if _, response, err := c.client.Repositories.GetBranch(ctx, c.repo.Owner, c.repo.Name, branch, 0); err != nil {
		if response != nil && response.StatusCode == http.StatusNotFound {
			branches, _, listErr := c.client.Repositories.ListBranches(ctx, c.repo.Owner, c.repo.Name, nil)
			if listErr != nil {
				return result, mapProviderError(listErr, c.token)
			}
			if len(branches) == 0 {
				return c.initializeOwnedRepository(ctx, repository, req, result, true, 0)
			}
			return result, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub repository contains branches outside the requested bootstrap branch")
		}
		return result, mapProviderError(err, c.token)
	}
	sha, prefix, err := c.verifyGitHubTemplatePrefix(ctx, req, branch)
	if err != nil {
		return result, err
	}
	if prefix < len(req.Files) {
		return c.initializeOwnedRepository(ctx, repository, req, result, true, prefix)
	}
	result.HeadSHA = sha
	return result, nil
}

func (c *Client) initializeOwnedRepository(ctx context.Context, repository *gogithub.Repository, req gitprovider.RepositoryBootstrapRequest, result *gitprovider.RepositoryBootstrapResult, preserveMarker bool, start int) (*gitprovider.RepositoryBootstrapResult, error) {
	branch := gitprovider.NormalizeBranchName(req.DefaultBranch)
	var headSHA string
	for _, file := range req.Files[start:] {
		response, _, err := c.client.Repositories.CreateFile(ctx, c.repo.Owner, c.repo.Name, file.Path, &gogithub.RepositoryContentFileOptions{
			Message: gogithub.Ptr("Bootstrap Braidplane pipeline repository"), Content: []byte(file.Content), Branch: gogithub.Ptr(branch),
		})
		if err != nil {
			return result, mapProviderError(err, c.token)
		}
		if response == nil || response.GetSHA() == "" {
			return result, xerror.Unexpected().Detail("GitHub bootstrap file commit is missing its SHA")
		}
		headSHA = response.GetSHA()
	}
	if headSHA == "" {
		return result, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap template requires files")
	}
	edit := &gogithub.Repository{DefaultBranch: gogithub.Ptr(branch)}
	if !preserveMarker {
		edit.Description = gogithub.Ptr(bootstrapDescription)
	}
	if _, _, err := c.client.Repositories.Edit(ctx, c.repo.Owner, c.repo.Name, edit); err != nil {
		return result, mapProviderError(err, c.token)
	}
	verifiedSHA, prefix, err := c.verifyGitHubTemplatePrefix(ctx, req, branch)
	if err != nil {
		return result, err
	}
	if prefix != len(req.Files) || verifiedSHA != headSHA {
		return result, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap branch changed while creating the template")
	}
	result.HeadSHA = headSHA
	return result, nil
}

// verifyGitHubTemplatePrefix accepts only the exact sequence of bootstrap commits.
// It returns the validated head and how many requested files that sequence contains.
func (c *Client) verifyGitHubTemplatePrefix(ctx context.Context, req gitprovider.RepositoryBootstrapRequest, branch string) (string, int, error) {
	if len(req.Files) == 0 {
		return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap template requires files")
	}
	ref, _, err := c.client.Repositories.GetBranch(ctx, c.repo.Owner, c.repo.Name, branch, 0)
	if err != nil {
		return "", 0, mapProviderError(err, c.token)
	}
	commit, _, err := c.client.Git.GetCommit(ctx, c.repo.Owner, c.repo.Name, ref.GetCommit().GetSHA())
	if err != nil {
		return "", 0, mapProviderError(err, c.token)
	}
	if commit == nil || commit.GetSHA() == "" {
		return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
	}
	head := commit
	chain := make([]*gogithub.Commit, 0, len(req.Files))
	for {
		if len(chain) >= len(req.Files) || commit.GetMessage() != "Bootstrap Braidplane pipeline repository" || len(commit.Parents) > 1 {
			return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
		}
		chain = append(chain, commit)
		if len(commit.Parents) == 0 {
			break
		}
		commit, _, err = c.client.Git.GetCommit(ctx, c.repo.Owner, c.repo.Name, commit.Parents[0].GetSHA())
		if err != nil {
			return "", 0, mapProviderError(err, c.token)
		}
		if commit == nil {
			return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
		}
	}
	for index := range chain {
		commit := chain[len(chain)-1-index]
		tree, _, err := c.client.Git.GetTree(ctx, c.repo.Owner, c.repo.Name, commit.GetTree().GetSHA(), true)
		if err != nil {
			return "", 0, mapProviderError(err, c.token)
		}
		if tree == nil || tree.GetTruncated() {
			return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
		}
		entries := make(map[string]*gogithub.TreeEntry, len(tree.Entries))
		for _, entry := range tree.Entries {
			if entry.GetType() == "tree" {
				continue
			}
			if _, exists := entries[entry.GetPath()]; exists {
				return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap tree contains duplicate file paths")
			}
			entries[entry.GetPath()] = entry
		}
		if len(entries) != index+1 {
			return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
		}
		for _, file := range req.Files[:index+1] {
			entry := entries[file.Path]
			if entry == nil || entry.GetType() != "blob" || entry.GetMode() != "100644" {
				return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
			}
			blob, _, err := c.client.Git.GetBlob(ctx, c.repo.Owner, c.repo.Name, entry.GetSHA())
			if err != nil || blob == nil {
				if err != nil {
					return "", 0, mapProviderError(err, c.token)
				}
				return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
			}
			raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.GetContent(), "\n", ""))
			if err != nil || !bytes.Equal(raw, []byte(file.Content)) {
				return "", 0, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap commit does not match the requested template")
			}
		}
	}
	return head.GetSHA(), len(chain), nil
}

func validateGitHubBootstrapFiles(files []gitprovider.RepositoryFile) error {
	if len(files) == 0 {
		return xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap template requires files")
	}
	paths := make(map[string]struct{}, len(files))
	for _, file := range files {
		if _, exists := paths[file.Path]; exists {
			return xerror.InvalidInput(xerror.RepoFile).Detail("GitHub bootstrap template contains duplicate file paths")
		}
		paths[file.Path] = struct{}{}
	}
	return nil
}

func (c *Client) bootstrapResult(repository *gogithub.Repository, ownerKind string) *gitprovider.RepositoryBootstrapResult {
	return &gitprovider.RepositoryBootstrapResult{Repository: c.repo, RepositoryID: strconv.FormatInt(repository.GetID(), 10), RemoteURL: repository.GetCloneURL(), OwnerKind: ownerKind}
}

func githubBootstrapMarker(marker string) (string, bool) {
	if marker = strings.TrimSpace(marker); marker != "" {
		return bootstrapDescription + " [" + marker + "]", true
	}
	return bootstrapDescription + " [" + rand.Text() + "]", false
}

func matchesExpectedRepositoryID(repository *gogithub.Repository, expected string) bool {
	if strings.TrimSpace(expected) == "" {
		return true
	}
	id, err := strconv.ParseInt(expected, 10, 64)
	return err == nil && id > 0 && repository.GetID() == id
}

// DeleteRepository removes the exact bootstrap repository identified by its immutable ID.
func (c *Client) DeleteRepository(ctx context.Context, repositoryID string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(repositoryID), 10, 64)
	if err != nil || id <= 0 {
		return xerror.InvalidInput(xerror.RepoFile).Detail("GitHub repository deletion requires a bootstrap-owned repository ID")
	}
	return c.deleteOwnedRepository(ctx, id)
}

func (c *Client) recoverAmbiguousRepositoryCreate(
	ctx context.Context,
	description string,
	createErr error,
) (*gogithub.Repository, error) {
	for attempt := 0; attempt < 3; attempt++ {
		repository, response, err := c.client.Repositories.Get(ctx, c.repo.Owner, c.repo.Name)
		if err == nil {
			if c.ownedRepository(repository, description) {
				return repository, nil
			}
			return nil, createErr
		}
		if response == nil || response.StatusCode != http.StatusNotFound {
			return nil, xerror.Join(createErr, mapProviderError(err, c.token))
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, xerror.Join(createErr, xerror.Unexpected().With(ctx.Err()))
		case <-timer.C:
		}
	}
	return nil, createErr
}

func (c *Client) deleteOwnedRepository(ctx context.Context, repositoryID int64) error {
	repository, _, err := c.client.Repositories.Get(ctx, c.repo.Owner, c.repo.Name)
	if err != nil {
		return mapProviderError(err, c.token)
	}
	if repository.GetID() != repositoryID || !strings.EqualFold(repository.GetFullName(), c.repo.FullName) || !repository.GetPrivate() {
		return xerror.InvalidInput(xerror.RepoFile).Detail("GitHub repository no longer matches bootstrap ownership")
	}
	if _, err := c.client.Repositories.Delete(ctx, c.repo.Owner, c.repo.Name); err != nil {
		return mapProviderError(err, c.token)
	}
	return nil
}

func (c *Client) ownedRepository(repository *gogithub.Repository, description string) bool {
	return repository != nil && repository.GetID() > 0 && strings.EqualFold(repository.GetFullName(), c.repo.FullName) && repository.GetPrivate() && repository.GetDescription() == description
}

func (c *Client) proposalRefFromPull(pull *gogithub.PullRequest) gitprovider.ProposalRef {
	var sourceBranch, targetBranch, headSHA, repositoryID string
	if pull.GetHead() != nil {
		sourceBranch = pull.GetHead().GetRef()
		headSHA = pull.GetHead().GetSHA()
	}
	if pull.GetBase() != nil {
		targetBranch = pull.GetBase().GetRef()
		if pull.GetBase().GetRepo() != nil && pull.GetBase().GetRepo().GetID() > 0 {
			repositoryID = strconv.FormatInt(pull.GetBase().GetRepo().GetID(), 10)
		}
	}

	state := normalizeState(pull.GetState(), pull.GetMerged())
	return gitprovider.ProposalRef{
		Provider:     repostore.ProviderGitHub,
		Repository:   c.repo,
		RepositoryID: repositoryID,
		ID:           strconv.FormatInt(int64(pull.GetNumber()), 10),
		URL:          pull.GetHTMLURL(),
		SourceBranch: sourceBranch,
		TargetBranch: targetBranch,
		State:        state,
		HeadSHA:      headSHA,
		ProviderKind: gitprovider.ProposalKindPullRequest,
		Closed:       state == gitprovider.ProposalStateClosed,
		Merged:       pull.GetMerged() || state == gitprovider.ProposalStateMerged,
	}
}

func normalizeState(state string, merged bool) gitprovider.ProposalState {
	if merged {
		return gitprovider.ProposalStateMerged
	}
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "closed":
		return gitprovider.ProposalStateClosed
	default:
		return gitprovider.ProposalStateOpen
	}
}

func validateBaseURL(ctx context.Context, baseURL string, allowPrivateHosts bool) error {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return xerror.InvalidInput(xerror.DataSource).With(err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return xerror.InvalidInput(xerror.DataSource).Detail("github base_url must use http or https")
	}
	if err := transportsecurity.ValidateURL(parsed, transportsecurity.PlaintextAllowed(ctx)); err != nil {
		return err
	}
	if allowPrivateHosts {
		return nil
	}
	if err := hostguard.ValidateResolvedPublicHost(ctx, parsed.Hostname()); err != nil {
		return xerror.Wrapf("github base_url blocked by ssrf protection: %w", err)
	}
	return nil
}

func guardedTransport(allowPrivateHosts bool) (http.RoundTripper, error) {
	if allowPrivateHosts {
		return http.DefaultTransport, nil
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, xerror.Unexpected().Detail("default transport has unexpected type")
	}

	cloned := defaultTransport.Clone()
	cloned.DialContext = gitprovider.SSRFProtectedDialContext()
	return cloned, nil
}

func mapProviderError(err error, token string) error {
	if err == nil {
		return nil
	}
	if deadlineExceeded(err) {
		return gitprovider.RedactProviderError(xerror.Timeout(xerror.DataSource).With(err), token)
	}

	var providerErr *gogithub.ErrorResponse
	if xerror.As(err, &providerErr) {
		switch providerErr.Response.StatusCode {
		case http.StatusUnauthorized:
			return gitprovider.RedactProviderError(xerror.InvalidCredentials().Detail(providerErr.Message), token)
		case http.StatusForbidden:
			return gitprovider.RedactProviderError(xerror.Forbidden(xerror.DataSource).Detail(providerErr.Message), token)
		case http.StatusTooManyRequests:
			return gitprovider.RedactProviderError(xerror.Unavailable(xerror.DataSource).Detail(providerErr.Message), token)
		default:
			if providerErr.Response.StatusCode >= http.StatusBadRequest &&
				providerErr.Response.StatusCode < http.StatusInternalServerError {
				return gitprovider.RedactProviderError(xerror.InvalidInput(xerror.RepoFile).Detail(providerErr.Message), token)
			}
			return gitprovider.RedactProviderError(xerror.Unavailable(xerror.DataSource).Detail(providerErr.Message), token)
		}
	}

	return gitprovider.RedactProviderError(xerror.ExecutionUnavailable().With(err), token)
}

func deadlineExceeded(err error) bool {
	var netErr net.Error
	if xerror.As(err, &netErr) {
		return netErr.Timeout()
	}
	var urlErr *url.Error
	if xerror.As(err, &urlErr) {
		return urlErr.Timeout()
	}
	return false
}
