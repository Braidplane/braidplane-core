package gitlab

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	gogitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const bootstrapDescription = "Git-first Braidplane pipeline definitions"

// ValidateRepository verifies that the connection can write the selected project branch.
func (c *Client) ValidateRepository(ctx context.Context, branch string) error {
	project, _, err := c.client.Projects.GetProject(c.repo.ProjectPath, nil, gogitlab.WithContext(ctx))
	if err != nil {
		return mapProviderError(err, c.token)
	}
	if project == nil || project.Permissions == nil || !hasWriteAccess(project.Permissions) {
		return xerror.Forbidden(xerror.DataSource).Detail("GitLab connection cannot write to the repository")
	}
	if _, _, err := c.client.Branches.GetBranch(c.repo.ProjectPath, gitprovider.NormalizeBranchName(branch), gogitlab.WithContext(ctx)); err != nil {
		return mapProviderError(err, c.token)
	}
	return nil
}

// BootstrapRepository creates a private GitLab project and one exact initial commit.
func (c *Client) BootstrapRepository(
	ctx context.Context,
	req gitprovider.RepositoryBootstrapRequest,
) (result *gitprovider.RepositoryBootstrapResult, resultErr error) {
	if strings.TrimSpace(req.Owner) != c.repo.Owner || strings.TrimSpace(req.Name) != c.repo.Name {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project target does not match the selected connection")
	}

	marker, resumable := bootstrapMarker(req.Marker)
	project, response, err := c.client.Projects.GetProject(c.repo.ProjectPath, nil, gogitlab.WithContext(ctx))
	if err == nil {
		if !resumable {
			return nil, xerror.AlreadyExists(xerror.RepoFile).Detail("GitLab project already exists")
		}
		if !c.ownedProject(project, marker) || (req.ExpectedRepositoryID != "" && strconv.FormatInt(project.ID, 10) != req.ExpectedRepositoryID) {
			return nil, xerror.AlreadyExists(xerror.RepoFile).Detail("GitLab project already exists and is not owned by this setup")
		}
		result = c.bootstrapResult(project, "")
		if project.MarkedForDeletionOn != nil {
			return result, pendingDeletionError()
		}
		return c.resumeOwnedProject(ctx, project, req, result)
	}
	if !isNotFound(response) {
		return nil, mapProviderError(err, c.token)
	}

	if req.ExpectedRepositoryID != "" {
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("GitLab bootstrap project is missing for its expected immutable ID")
	}
	namespace, _, err := c.client.Namespaces.GetNamespace(c.repo.Owner, gogitlab.WithContext(ctx))
	if err != nil {
		return nil, mapProviderError(err, c.token)
	}
	if namespace == nil || namespace.ID <= 0 || !strings.EqualFold(strings.TrimSpace(namespace.FullPath), c.repo.Owner) || (namespace.Kind != "user" && namespace.Kind != "group") {
		return nil, xerror.Forbidden(xerror.DataSource).Detail("GitLab connection cannot create a project in the requested namespace")
	}

	project, response, err = c.client.Projects.CreateProject(&gogitlab.CreateProjectOptions{
		Name:                 gogitlab.Ptr(c.repo.Name),
		Path:                 gogitlab.Ptr(c.repo.Name),
		NamespaceID:          gogitlab.Ptr(namespace.ID),
		Description:          gogitlab.Ptr(marker),
		Visibility:           gogitlab.Ptr(gogitlab.PrivateVisibility),
		InitializeWithReadme: gogitlab.Ptr(false),
	}, gogitlab.WithContext(ctx), gogitlab.WithRequestRetry(noRetry))
	createdThisCall := err == nil
	if err != nil {
		if isConflict(response) {
			if !resumable {
				return nil, xerror.AlreadyExists(xerror.RepoFile).Detail("GitLab project already exists")
			}
			project, err = c.recoverAmbiguousProjectCreate(ctx, marker, mapProviderError(err, c.token))
			if err != nil {
				return nil, err
			}
			result = c.bootstrapResult(project, "")
			if project.MarkedForDeletionOn != nil {
				return result, pendingDeletionError()
			}
			return c.resumeOwnedProject(ctx, project, req, result)
		}
		createErr := mapProviderError(err, c.token)
		if response != nil && response.StatusCode < http.StatusInternalServerError {
			return nil, createErr
		}
		project, err = c.recoverAmbiguousProjectCreate(ctx, marker, createErr)
		if err != nil {
			return nil, err
		}
		if resumable {
			result = c.bootstrapResult(project, "")
			if project.MarkedForDeletionOn != nil {
				return result, pendingDeletionError()
			}
			return c.resumeOwnedProject(ctx, project, req, result)
		}
	}
	if !c.ownedProject(project, marker) {
		return nil, xerror.Unexpected().Detail("GitLab created project identity is invalid")
	}
	result = c.bootstrapResult(project, namespace.Kind)
	if project.MarkedForDeletionOn != nil {
		return result, pendingDeletionError()
	}
	complete := false
	defer func() {
		if complete || !createdThisCall || resumable {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTimeout)
		defer cancel()
		if _, cleanupErr := c.client.Projects.DeleteProject(project.ID, nil, gogitlab.WithContext(cleanupCtx), gogitlab.WithRequestRetry(noRetry)); cleanupErr != nil {
			resultErr = xerror.Join(resultErr, gitprovider.ErrRepositoryCleanupFailed.With(mapProviderError(cleanupErr, c.token)))
		}
	}()
	result, resultErr = c.initializeOwnedProject(ctx, project, req, result, resumable)
	complete = resultErr == nil
	return result, resultErr
}

func (c *Client) resumeOwnedProject(ctx context.Context, project *gogitlab.Project, req gitprovider.RepositoryBootstrapRequest, result *gitprovider.RepositoryBootstrapResult) (*gitprovider.RepositoryBootstrapResult, error) {
	if !project.EmptyRepo {
		sha, err := c.verifyInitialTemplate(ctx, project, req)
		if err != nil {
			return result, err
		}
		result.HeadSHA = sha
		return result, nil
	}
	return c.initializeOwnedProject(ctx, project, req, result, true)
}

func (c *Client) initializeOwnedProject(ctx context.Context, project *gogitlab.Project, req gitprovider.RepositoryBootstrapRequest, result *gitprovider.RepositoryBootstrapResult, preserveMarker bool) (*gitprovider.RepositoryBootstrapResult, error) {
	branch := gitprovider.NormalizeBranchName(req.DefaultBranch)
	actions := make([]*gogitlab.CommitActionOptions, 0, len(req.Files))
	for _, file := range req.Files {
		actions = append(actions, &gogitlab.CommitActionOptions{Action: gogitlab.Ptr(gogitlab.FileCreate), FilePath: gogitlab.Ptr(file.Path), Content: gogitlab.Ptr(file.Content)})
	}
	commit, _, err := c.client.Commits.CreateCommit(project.ID, &gogitlab.CreateCommitOptions{
		Branch: gogitlab.Ptr(branch), CommitMessage: gogitlab.Ptr("Bootstrap Braidplane pipeline repository"), Actions: actions,
	}, gogitlab.WithContext(ctx), gogitlab.WithRequestRetry(noRetry))
	if err != nil {
		return result, mapProviderError(err, c.token)
	}
	if commit == nil || strings.TrimSpace(commit.ID) == "" {
		return result, xerror.Unexpected().Detail("GitLab bootstrap commit is missing its SHA")
	}
	edit := &gogitlab.EditProjectOptions{DefaultBranch: gogitlab.Ptr(branch)}
	if !preserveMarker {
		edit.Description = gogitlab.Ptr(bootstrapDescription)
	}
	if _, _, err := c.client.Projects.EditProject(project.ID, edit, gogitlab.WithContext(ctx), gogitlab.WithRequestRetry(noRetry)); err != nil {
		return result, mapProviderError(err, c.token)
	}
	result.HeadSHA = commit.ID
	return result, nil
}

func (c *Client) verifyInitialTemplate(ctx context.Context, project *gogitlab.Project, req gitprovider.RepositoryBootstrapRequest) (string, error) {
	branch := gitprovider.NormalizeBranchName(req.DefaultBranch)
	commit, _, err := c.client.Commits.GetCommit(project.ID, branch, nil, gogitlab.WithContext(ctx))
	if err != nil {
		return "", mapProviderError(err, c.token)
	}
	if commit == nil || strings.TrimSpace(commit.ID) == "" || len(commit.ParentIDs) != 0 {
		return "", xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project bootstrap commit does not match the requested template")
	}
	expected := make(map[string]string, len(req.Files))
	for _, file := range req.Files {
		if _, exists := expected[file.Path]; exists {
			return "", xerror.InvalidInput(xerror.RepoFile).Detail("GitLab bootstrap template contains duplicate file paths")
		}
		expected[file.Path] = file.Content
	}
	actual := make(map[string]struct{}, len(expected))
	page := int64(1)
	for {
		nodes, response, err := c.client.Repositories.ListTree(project.ID, &gogitlab.ListTreeOptions{ListOptions: gogitlab.ListOptions{Page: page, PerPage: 100}, Ref: gogitlab.Ptr(commit.ID), Recursive: gogitlab.Ptr(true)}, gogitlab.WithContext(ctx))
		if err != nil {
			return "", mapProviderError(err, c.token)
		}
		for _, node := range nodes {
			if node != nil && node.Type == "tree" {
				continue
			}
			if node == nil || node.Type != "blob" || node.Mode != "100644" {
				return "", xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project bootstrap commit does not match the requested template")
			}
			if _, exists := expected[node.Path]; !exists {
				return "", xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project bootstrap commit does not match the requested template")
			}
			actual[node.Path] = struct{}{}
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		page = response.NextPage
	}
	if len(actual) != len(expected) {
		return "", xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project bootstrap commit does not match the requested template")
	}
	for path, content := range expected {
		raw, _, err := c.client.RepositoryFiles.GetRawFile(project.ID, path, &gogitlab.GetRawFileOptions{Ref: gogitlab.Ptr(commit.ID)}, gogitlab.WithContext(ctx))
		if err != nil {
			return "", mapProviderError(err, c.token)
		}
		if !bytes.Equal(raw, []byte(content)) {
			return "", xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project bootstrap commit does not match the requested template")
		}
	}
	return commit.ID, nil
}

func (c *Client) bootstrapResult(project *gogitlab.Project, ownerKind string) *gitprovider.RepositoryBootstrapResult {
	if ownerKind == "" && project.Namespace != nil {
		ownerKind = project.Namespace.Kind
	}
	return &gitprovider.RepositoryBootstrapResult{Repository: c.repo, RepositoryID: strconv.FormatInt(project.ID, 10), RemoteURL: project.HTTPURLToRepo, OwnerKind: ownerKind}
}

func bootstrapMarker(marker string) (string, bool) {
	if marker = strings.TrimSpace(marker); marker != "" {
		return bootstrapDescription + " [" + marker + "]", true
	}
	return bootstrapDescription + " [" + rand.Text() + "]", false
}

func pendingDeletionError() error {
	return xerror.Unavailable(xerror.RepoFile).Detail("GitLab project is pending deletion and cannot be initialized or reused")
}

// DeleteRepository uses the immutable identity returned by bootstrap, never a namespace lookup.
func (c *Client) DeleteRepository(ctx context.Context, repositoryID string) error {
	id, err := strconv.ParseInt(repositoryID, 10, 64)
	if err != nil || id <= 0 {
		return xerror.InvalidInput(xerror.RepoFile).Detail("GitLab project deletion requires a bootstrap-owned project ID")
	}
	project, _, err := c.client.Projects.GetProject(id, nil, gogitlab.WithContext(ctx))
	if err != nil {
		return mapProviderError(err, c.token)
	}
	if project == nil || project.ID != id || !strings.EqualFold(project.PathWithNamespace, c.repo.ProjectPath) {
		return xerror.InvalidInput(xerror.RepoFile).Detail("GitLab cleanup project does not match the bootstrapped repository")
	}
	if _, err := c.client.Projects.DeleteProject(id, nil, gogitlab.WithContext(ctx), gogitlab.WithRequestRetry(noRetry)); err != nil {
		return mapProviderError(err, c.token)
	}
	return nil
}

func (c *Client) recoverAmbiguousProjectCreate(ctx context.Context, marker string, createErr error) (*gogitlab.Project, error) {
	for attempt := 0; attempt < 3; attempt++ {
		project, response, err := c.client.Projects.GetProject(c.repo.ProjectPath, nil, gogitlab.WithContext(ctx))
		if err == nil {
			if c.ownedProject(project, marker) {
				return project, nil
			}
			return nil, createErr
		}
		if !isNotFound(response) {
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

func (c *Client) ownedProject(project *gogitlab.Project, marker string) bool {
	return project != nil && project.ID > 0 && project.Description == marker && project.Visibility == gogitlab.PrivateVisibility && strings.EqualFold(project.PathWithNamespace, c.repo.ProjectPath)
}

func hasWriteAccess(permissions *gogitlab.Permissions) bool {
	return (permissions.ProjectAccess != nil && permissions.ProjectAccess.AccessLevel >= gogitlab.DeveloperPermissions) ||
		(permissions.GroupAccess != nil && permissions.GroupAccess.AccessLevel >= gogitlab.DeveloperPermissions)
}

func isNotFound(response *gogitlab.Response) bool {
	return response != nil && response.StatusCode == http.StatusNotFound
}

func isConflict(response *gogitlab.Response) bool {
	return response != nil && response.StatusCode == http.StatusConflict
}

func noRetry(context.Context, *http.Response, error) (bool, error) { return false, nil }
