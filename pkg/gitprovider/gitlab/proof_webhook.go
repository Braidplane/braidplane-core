package gitlab

import (
	"context"
	"net/url"

	gogitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type ProofWebhook struct {
	ProjectID int64
	HookID    int64
}

func (c *Client) ProjectID(ctx context.Context) (int64, error) {
	project, _, err := c.client.Projects.GetProject(c.repo.ProjectPath, nil, gogitlab.WithContext(ctx))
	if err != nil {
		return 0, mapProviderError(err, c.token)
	}
	if project == nil || project.ID <= 0 {
		return 0, gitprovider.ErrChangeUnavailable.Detail("GitLab project identity is unavailable")
	}
	return project.ID, nil
}

// EnsureProofWebhook creates or rotates the Braidplane-managed merge-request hook.
func (c *Client) EnsureProofWebhook(ctx context.Context, projectID int64, callbackURL, signingToken string, existingHookID int64) (ProofWebhook, error) {
	if projectID <= 0 {
		return ProofWebhook{}, gitprovider.ErrChangeUnavailable.Detail("GitLab project identity is unavailable")
	}
	callback, err := url.Parse(callbackURL)
	if err != nil || callback.Hostname() == "" || (callback.Scheme != "http" && callback.Scheme != "https") {
		return ProofWebhook{}, xerror.InvalidInput(xerror.DataSource).Detail("GitLab webhook callback URL is invalid")
	}
	if _, ok := standardWebhookSigningKey(signingToken); !ok {
		return ProofWebhook{}, xerror.InvalidCredentials().Detail("GitLab webhook signing token is invalid")
	}
	mergeRequests := true
	ssl := true
	name := "Braidplane Proof"
	if existingHookID > 0 {
		hook, _, err := c.client.Projects.EditProjectHook(projectID, existingHookID, &gogitlab.EditProjectHookOptions{
			Name: &name, URL: &callbackURL, SigningToken: &signingToken,
			MergeRequestsEvents: &mergeRequests, EnableSSLVerification: &ssl,
		}, gogitlab.WithContext(ctx))
		if err != nil {
			return ProofWebhook{}, mapProviderError(err, c.token)
		}
		return proofWebhook(projectID, hook)
	}
	hook, _, err := c.client.Projects.AddProjectHook(projectID, &gogitlab.AddProjectHookOptions{
		Name: &name, URL: &callbackURL, SigningToken: &signingToken,
		MergeRequestsEvents: &mergeRequests, EnableSSLVerification: &ssl,
	}, gogitlab.WithContext(ctx))
	if err != nil {
		return ProofWebhook{}, mapProviderError(err, c.token)
	}
	return proofWebhook(projectID, hook)
}

func proofWebhook(projectID int64, hook *gogitlab.ProjectHook) (ProofWebhook, error) {
	if hook == nil || hook.ID <= 0 || hook.ProjectID != projectID {
		return ProofWebhook{}, xerror.Unavailable(xerror.DataSource).Detail("GitLab project webhook response is invalid")
	}
	return ProofWebhook{ProjectID: projectID, HookID: hook.ID}, nil
}
