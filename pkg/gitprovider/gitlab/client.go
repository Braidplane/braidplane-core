package gitlab

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	gogitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"golang.org/x/oauth2"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultTimeout = 30 * time.Second
	defaultBaseURL = "https://gitlab.com/api/v4"
)

// Client wraps GitLab merge request APIs behind Braidplane's provider proposal contract.
type Client struct {
	repo   gitprovider.RepositoryRef
	token  string
	client *gogitlab.Client
}

// New creates a GitLab provider proposal client.
func New(
	ctx context.Context,
	config gitprovider.Config,
	creds gitprovider.Credentials,
	repo gitprovider.RepositoryRef,
) (*Client, error) {
	return newClient(ctx, config, creds, repo, false)
}

// NewOAuth creates a GitLab provider proposal client using OAuth bearer authentication.
func NewOAuth(
	ctx context.Context,
	config gitprovider.Config,
	creds gitprovider.Credentials,
	repo gitprovider.RepositoryRef,
) (*Client, error) {
	return newClient(ctx, config, creds, repo, true)
}

func newClient(
	ctx context.Context,
	config gitprovider.Config,
	creds gitprovider.Credentials,
	repo gitprovider.RepositoryRef,
	oauth bool,
) (*Client, error) {
	if config.AllowPlaintext {
		ctx = transportsecurity.WithPlaintext(ctx)
	}
	token := strings.TrimSpace(creds.Token)
	if token == "" {
		return nil, xerror.InvalidCredentials().Detail("gitlab token is required")
	}
	if repo.Provider != repostore.ProviderGitLab {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("gitlab repository ref is required")
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

	options := []gogitlab.ClientOptionFunc{
		gogitlab.WithBaseURL(baseURL),
		gogitlab.WithHTTPClient(&http.Client{Timeout: timeout, Transport: transportsecurity.HTTPTransport(transport, transportsecurity.PlaintextAllowed(ctx)), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}),
	}
	var client *gogitlab.Client
	if oauth {
		client, err = gogitlab.NewAuthSourceClient(gogitlab.OAuthTokenSource{
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		}, options...)
	} else {
		client, err = gogitlab.NewClient(token, options...)
	}
	if err != nil {
		return nil, xerror.InvalidInput(xerror.DataSource).With(err)
	}

	return &Client{repo: repo, token: token, client: client}, nil
}

// ListOpenProposals returns open merge requests for the requested source/target branches.
func (c *Client) ListOpenProposals(
	ctx context.Context,
	req gitprovider.ProposalListRequest,
) ([]gitprovider.ProposalRef, error) {
	state := "opened"
	sourceBranch := gitprovider.NormalizeBranchName(req.SourceBranch)
	targetBranch := gitprovider.NormalizeBranchName(req.TargetBranch)
	mrs, _, err := c.client.MergeRequests.ListProjectMergeRequests(
		c.repo.ProjectPath,
		&gogitlab.ListProjectMergeRequestsOptions{
			State:        &state,
			SourceBranch: &sourceBranch,
			TargetBranch: &targetBranch,
		},
		gogitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}

	results := make([]gitprovider.ProposalRef, 0, len(mrs))
	for _, mr := range mrs {
		results = append(results, c.proposalRefFromBasicMergeRequest(mr))
	}
	return results, nil
}

// GetProposal returns one merge request, including closed and merged states.
func (c *Client) GetProposal(
	ctx context.Context,
	req gitprovider.ProposalGetRequest,
) (*gitprovider.ProposalRef, error) {
	iid, err := strconv.ParseInt(strings.TrimSpace(req.ProposalID), 10, 64)
	if err != nil || iid <= 0 {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("gitlab merge request IID is invalid")
	}
	mr, _, err := c.client.MergeRequests.GetMergeRequest(
		c.repo.ProjectPath,
		iid,
		nil,
		gogitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}
	ref := c.proposalRefFromMergeRequest(mr)
	return &ref, nil
}

// CreateProposal creates a GitLab merge request.
func (c *Client) CreateProposal(
	ctx context.Context,
	req gitprovider.ProposalRequest,
) (*gitprovider.ProposalRef, error) {
	sourceBranch := gitprovider.NormalizeBranchName(req.SourceBranch)
	targetBranch := gitprovider.NormalizeBranchName(req.TargetBranch)
	mr, _, err := c.client.MergeRequests.CreateMergeRequest(
		c.repo.ProjectPath,
		&gogitlab.CreateMergeRequestOptions{
			Title:        &req.Title,
			Description:  &req.Body,
			SourceBranch: &sourceBranch,
			TargetBranch: &targetBranch,
		},
		gogitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}

	ref := c.proposalRefFromMergeRequest(mr)
	return &ref, nil
}

// UpdateProposal updates a GitLab merge request title and description.
func (c *Client) UpdateProposal(
	ctx context.Context,
	req gitprovider.ProposalUpdateRequest,
) (*gitprovider.ProposalRef, error) {
	iid, err := strconv.ParseInt(strings.TrimSpace(req.ProposalID), 10, 64)
	if err != nil || iid <= 0 {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("gitlab merge request IID is invalid")
	}

	mr, _, err := c.client.MergeRequests.UpdateMergeRequest(
		c.repo.ProjectPath,
		iid,
		&gogitlab.UpdateMergeRequestOptions{
			Title:       &req.Title,
			Description: &req.Body,
		},
		gogitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}

	ref := c.proposalRefFromMergeRequest(mr)
	return &ref, nil
}

func (c *Client) proposalRefFromBasicMergeRequest(mr *gogitlab.BasicMergeRequest) gitprovider.ProposalRef {
	state := normalizeState(mr.State, mr.MergedAt != nil)
	var repositoryID string
	if mr.TargetProjectID > 0 {
		repositoryID = strconv.FormatInt(mr.TargetProjectID, 10)
	}
	return gitprovider.ProposalRef{
		Provider:     repostore.ProviderGitLab,
		Repository:   c.repo,
		RepositoryID: repositoryID,
		ID:           strconv.FormatInt(mr.IID, 10),
		URL:          mr.WebURL,
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
		State:        state,
		HeadSHA:      mr.SHA,
		ProviderKind: gitprovider.ProposalKindMergeRequest,
		Closed:       mr.ClosedAt != nil || state == gitprovider.ProposalStateClosed,
		Merged:       mr.MergedAt != nil || state == gitprovider.ProposalStateMerged,
	}
}

func (c *Client) proposalRefFromMergeRequest(mr *gogitlab.MergeRequest) gitprovider.ProposalRef {
	return c.proposalRefFromBasicMergeRequest(&mr.BasicMergeRequest)
}

func normalizeState(state string, merged bool) gitprovider.ProposalState {
	if merged {
		return gitprovider.ProposalStateMerged
	}
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "closed", "closed_at":
		return gitprovider.ProposalStateClosed
	case "merged":
		return gitprovider.ProposalStateMerged
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
		return xerror.InvalidInput(xerror.DataSource).Detail("gitlab base_url must use http or https")
	}
	if err := transportsecurity.ValidateURL(parsed, transportsecurity.PlaintextAllowed(ctx)); err != nil {
		return err
	}
	if allowPrivateHosts {
		return nil
	}
	if err := hostguard.ValidateResolvedPublicHost(ctx, parsed.Hostname()); err != nil {
		return xerror.Wrapf("gitlab base_url blocked by ssrf protection: %w", err)
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
	if deadlineExceeded(err) {
		return gitprovider.RedactProviderError(xerror.Timeout(xerror.DataSource).With(err), token)
	}

	var providerErr *gogitlab.ErrorResponse
	if xerror.As(err, &providerErr) {
		switch providerErr.StatusCode {
		case http.StatusUnauthorized:
			return gitprovider.RedactProviderError(xerror.InvalidCredentials().Detail(providerErr.Message), token)
		case http.StatusForbidden:
			return gitprovider.RedactProviderError(xerror.Forbidden(xerror.DataSource).Detail(providerErr.Message), token)
		case http.StatusTooManyRequests:
			return gitprovider.RedactProviderError(xerror.Unavailable(xerror.DataSource).Detail(providerErr.Message), token)
		default:
			if providerErr.StatusCode >= http.StatusBadRequest &&
				providerErr.StatusCode < http.StatusInternalServerError {
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
