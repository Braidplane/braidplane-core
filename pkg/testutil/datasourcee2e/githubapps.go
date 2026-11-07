package datasourcee2e

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
)

type GitHubAppsStub struct {
	Organization slug.Slug
	Connection   *githubappsvc.Connection
}

func (s GitHubAppsStub) Get(_ context.Context, orgSlug slug.Slug) (*githubappsvc.Connection, error) {
	if orgSlug != s.Organization {
		return nil, xerror.Unexpected().Detail("unexpected Organization")
	}
	return s.Connection, nil
}

func (s GitHubAppsStub) Token(_ context.Context, orgSlug slug.Slug, repositoryIDs []int64) (githubappsvc.InstallationToken, error) {
	if orgSlug != s.Organization || len(repositoryIDs) != 1 || (repositoryIDs[0] != 42 && repositoryIDs[0] != 43) {
		return githubappsvc.InstallationToken{}, xerror.Unexpected().Detail("unexpected GitHub token request")
	}
	return githubappsvc.InstallationToken{Token: "installation-token", ExpiresAt: time.Now().Add(time.Hour), ContentsPermission: "write"}, nil
}
