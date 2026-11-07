package gitprovider

import (
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RepositoryRefFromRemoteURL parses a git remote URL into provider API repository identity.
func RepositoryRefFromRemoteURL(provider repostore.Provider, repoURL string) (RepositoryRef, error) {
	normalized := repostore.NormalizeRemoteURL(repoURL)
	parts := strings.Split(normalized, "/")
	if len(parts) < 3 {
		return RepositoryRef{}, xerror.InvalidInput(xerror.RepoFile).
			Detail("provider repo URL must include owner and repo")
	}

	pathParts := append([]string(nil), parts[1:]...)
	pathParts[len(pathParts)-1] = strings.TrimSuffix(pathParts[len(pathParts)-1], ".git")

	switch provider {
	case repostore.ProviderGitHub:
		if len(pathParts) != 2 {
			return RepositoryRef{}, xerror.InvalidInput(xerror.RepoFile).
				Detail("GitHub repo URL must include owner and repo")
		}
	case repostore.ProviderGitLab:
		if len(pathParts) < 2 {
			return RepositoryRef{}, xerror.InvalidInput(xerror.RepoFile).
				Detail("GitLab repo URL must include namespace and repo")
		}
	default:
		return RepositoryRef{}, xerror.InvalidInput(xerror.RepoFile).
			Detail("provider proposals require github or gitlab")
	}

	projectPath := strings.Join(pathParts, "/")
	owner := strings.Join(pathParts[:len(pathParts)-1], "/")
	name := pathParts[len(pathParts)-1]
	return RepositoryRef{
		Provider:    provider,
		Owner:       owner,
		Name:        name,
		ProjectPath: projectPath,
		FullName:    projectPath,
	}, nil
}
