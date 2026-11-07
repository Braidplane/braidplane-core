package userssvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
)

func TestRegistrationRespectsReservedSlugs(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"Password", "OIDC"} {
		for _, scenario := range []string{"Available", "Reserved", "LookupFailure"} {
			t.Run(method+scenario, func(t *testing.T) {
				t.Parallel()
				lookupErr := xerror.Unexpected().Detail("reservation lookup unavailable")
				lookups, creates := 0, 0
				repo := &testutil.FakeUsersRepo{
					FindByOIDCProviderFn: func(context.Context, postgres.DBTX, string, string) (*users.UserPublic, error) {
						return nil, users.ErrUserNotFound
					},
					FindByEmailFn: func(context.Context, postgres.DBTX, string) (*users.UserPublic, error) {
						return nil, users.ErrUserNotFound
					},
					IsSlugReservedFn: func(_ context.Context, _ postgres.DBTX, candidate slug.Slug) (bool, error) {
						lookups++
						require.Regexp(t, `^user-[a-z0-9]{32}$`, candidate.String())
						if scenario == "LookupFailure" {
							return false, lookupErr
						}
						return scenario == "Reserved", nil
					},
					FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug) (*users.UserPublic, error) {
						t.Fatal("registration must not use active-only authentication lookup")
						return nil, users.ErrUserNotFound
					},
				}
				created := func(candidate slug.Slug) (*users.UserPublic, error) {
					creates++
					if scenario == "Reserved" {
						require.Regexp(t, `^user-[a-z0-9]{32}-[a-z0-9]{6}$`, candidate.String())
					} else {
						require.Regexp(t, `^user-[a-z0-9]{32}$`, candidate.String())
					}
					return testutil.StubUserPublic(candidate, "Alice Smith", "alice@example.com", model.RoleUser), nil
				}
				repo.CreateUserFn = func(_ context.Context, _ postgres.DBTX, p *users.CreateParams) (*users.UserPublic, error) {
					return created(p.Slug)
				}
				repo.CreateOIDCUserFn = func(_ context.Context, _ postgres.DBTX, p *users.CreateOIDCUserParams) (*users.UserPublic, error) {
					return created(p.Slug)
				}
				svc := newTestService(repo, nil, nil)
				var err error
				if method == "Password" {
					_, err = svc.CreateUser(t.Context(), model.NewUser{
						FullName: "Alice Smith", Email: "alice@example.com", Password: "securepass123",
						Role: model.RoleUser, Locale: "en",
					})
				} else {
					_, err = svc.LoginOrCreateOIDCUser(t.Context(), &model.OIDCUserInfo{
						Name: "Alice Smith", Email: "alice@example.com", Provider: "google", ProviderID: "test-subject",
						Locale: "en",
					})
				}
				require.Equal(t, 1, lookups)
				if scenario == "LookupFailure" {
					require.ErrorIs(t, err, lookupErr)
					require.Zero(t, creates)
				} else {
					require.NoError(t, err)
					require.Equal(t, 1, creates)
				}
			})
		}
	}
}
