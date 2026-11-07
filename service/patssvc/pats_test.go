package patssvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pats"
)

func TestService_CreatePersonalAccessTokenGeneratesSlugAndRetriesGeneratedCollision(t *testing.T) {
	t.Parallel()
	repo := &fakePATsRepository{}
	repo.createFn = func(_ context.Context, _ postgres.DBTX, params pats.CreateParams) (*pats.PersonalAccessToken, error) {
		if repo.createCalls == 1 {
			return nil, pats.ErrSlugAlreadyExists
		}
		return &pats.PersonalAccessToken{Slug: params.Slug, Name: params.Name, UserSlug: params.UserSlug}, nil
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.CreatePersonalAccessToken(context.Background(), "user-1", model.NewPersonalAccessToken{Name: "Developer CLI"})

	require.NoError(t, err)
	require.NotNil(t, token)
	require.Equal(t, 2, repo.createCalls)
	require.True(t, strings.HasPrefix(repo.createParams.Slug.String(), "developer-cli-"))
}

func TestService_CreatePersonalAccessTokenExplicitSlugCollisionDoesNotRetry(t *testing.T) {
	t.Parallel()
	repo := &fakePATsRepository{createFn: func(context.Context, postgres.DBTX, pats.CreateParams) (*pats.PersonalAccessToken, error) {
		return nil, pats.ErrSlugAlreadyExists
	}}
	svc := New(testutil.FakeTransactionManager{}, repo)

	_, err := svc.CreatePersonalAccessToken(context.Background(), "user-1", model.NewPersonalAccessToken{Name: "Developer CLI", Slug: "developer-cli"})

	require.ErrorIs(t, err, pats.ErrSlugAlreadyExists)
	require.Equal(t, 1, repo.createCalls)
}

func TestService_CreatePersonalAccessTokenRejectsInvalidExplicitSlug(t *testing.T) {
	t.Parallel()
	repo := &fakePATsRepository{}
	svc := New(testutil.FakeTransactionManager{}, repo)

	_, err := svc.CreatePersonalAccessToken(context.Background(), "user-1", model.NewPersonalAccessToken{Name: "Developer CLI", Slug: "Bad Slug"})

	require.Error(t, err)
	require.False(t, repo.createCalled)
}

func TestService_CreatePersonalAccessTokenReturnsRawTokenOnceAndStoresHash(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	repo := &fakePATsRepository{
		createFn: func(_ context.Context, _ postgres.DBTX, params pats.CreateParams) (*pats.PersonalAccessToken, error) {
			return &pats.PersonalAccessToken{
				Slug:                          params.Slug,
				Name:                          params.Name,
				Description:                   params.Description,
				TokenPrefix:                   params.TokenPrefix,
				UserSlug:                      params.UserSlug,
				ExpiresAt:                     params.ExpiresAt,
				CanManagePersonalAccessTokens: params.CanManagePersonalAccessTokens,
			}, nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.CreatePersonalAccessToken(
		context.Background(),
		"user-1",
		model.NewPersonalAccessToken{
			Name:                          "Developer CLI",
			Slug:                          "developer-cli",
			Description:                   ptrTo("Draft workflow token"),
			ExpiresAt:                     &expiresAt,
			CanManagePersonalAccessTokens: true,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, token)
	require.True(t, repo.createCalled)
	require.True(t, strings.HasPrefix(token.Token, TokenPrefix))
	require.Equal(t, token.Token[:tokenPrefixLen], repo.createParams.TokenPrefix)
	require.NotEqual(t, token.Token, repo.createParams.TokenHash)
	require.Len(t, repo.createParams.TokenHash, 64)
	require.Equal(t, slug.Slug("user-1"), repo.createParams.UserSlug)
	require.Equal(t, slug.Slug("developer-cli"), repo.createParams.Slug)
	require.Equal(t, "Developer CLI", repo.createParams.Name)
	require.Equal(t, "Draft workflow token", *repo.createParams.Description)
	require.NotNil(t, repo.createParams.ExpiresAt)
	require.True(t, repo.createParams.ExpiresAt.Equal(expiresAt))
	require.True(t, repo.createParams.CanManagePersonalAccessTokens)
	require.True(t, token.CanManagePersonalAccessTokens)
	require.Equal(t, token.Token[:tokenPrefixLen], token.TokenPrefix)
}

func TestService_RotatePersonalAccessTokenReplacesStoredTokenSecret(t *testing.T) {
	t.Parallel()

	repo := &fakePATsRepository{
		rotateFn: func(_ context.Context, _ postgres.DBTX, params pats.RotateParams) (*pats.PersonalAccessToken, error) {
			return &pats.PersonalAccessToken{
				Slug:        params.Slug,
				Name:        "Developer CLI",
				TokenPrefix: params.TokenPrefix,
				UserSlug:    params.UserSlug,
			}, nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.RotatePersonalAccessToken(context.Background(), "user-1", "developer-cli")

	require.NoError(t, err)
	require.NotNil(t, token)
	require.True(t, repo.rotateCalled)
	require.True(t, strings.HasPrefix(token.Token, TokenPrefix))
	require.Equal(t, token.Token[:tokenPrefixLen], repo.rotateParams.TokenPrefix)
	require.NotEqual(t, token.Token, repo.rotateParams.TokenHash)
	require.Len(t, repo.rotateParams.TokenHash, 64)
	require.Equal(t, slug.Slug("user-1"), repo.rotateParams.UserSlug)
	require.Equal(t, slug.Slug("developer-cli"), repo.rotateParams.Slug)
}

func TestService_UpdatePersonalAccessTokenPersistsSafeMetadata(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	var repo *fakePATsRepository
	repo = &fakePATsRepository{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) (*pats.PersonalAccessToken, error) {
			if repo.updateCalled {
				return &pats.PersonalAccessToken{
					Slug:        patSlug,
					Name:        repo.updateParams.Name,
					Description: repo.updateParams.Description,
					UserSlug:    userSlug,
					ExpiresAt:   repo.updateParams.ExpiresAt,
				}, nil
			}
			return &pats.PersonalAccessToken{
				Slug:     patSlug,
				Name:     "Developer CLI",
				UserSlug: userSlug,
			}, nil
		},
		updateFn: func(_ context.Context, _ postgres.DBTX, params pats.UpdateParams) error {
			require.Equal(t, slug.Slug("user-1"), params.UserSlug)
			require.Equal(t, slug.Slug("developer-cli"), params.Slug)
			require.Equal(t, "Developer CLI renamed", params.Name)
			require.True(t, params.DescriptionSet)
			require.Equal(t, "Updated local CLI token", *params.Description)
			require.True(t, params.ExpiresAtSet)
			require.NotNil(t, params.ExpiresAt)
			require.True(t, params.ExpiresAt.Equal(expiresAt))
			return nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.UpdatePersonalAccessToken(
		context.Background(),
		"user-1",
		"developer-cli",
		model.UpdatePersonalAccessToken{
			Name: "Developer CLI renamed",
			Description: model.NullableStringUpdate{
				Present: true,
				Value:   ptrTo("Updated local CLI token"),
			},
			ExpiresAt: model.NullableTimeUpdate{
				Present: true,
				Value:   &expiresAt,
			},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, token)
	require.True(t, repo.updateCalled)
	require.Equal(t, "Developer CLI renamed", token.Name)
	require.Equal(t, "Updated local CLI token", *token.Description)
	require.NotNil(t, token.ExpiresAt)
	require.True(t, token.ExpiresAt.Equal(expiresAt))
}

func TestService_UpdatePersonalAccessTokenPreservesOmittedNullableMetadata(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	description := "Existing local CLI token"
	var repo *fakePATsRepository
	repo = &fakePATsRepository{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) (*pats.PersonalAccessToken, error) {
			name := "Developer CLI"
			if repo.updateCalled {
				name = repo.updateParams.Name
			}
			return &pats.PersonalAccessToken{
				Slug:        patSlug,
				Name:        name,
				Description: &description,
				UserSlug:    userSlug,
				ExpiresAt:   &expiresAt,
			}, nil
		},
		updateFn: func(_ context.Context, _ postgres.DBTX, params pats.UpdateParams) error {
			require.Equal(t, "Developer CLI renamed", params.Name)
			require.False(t, params.DescriptionSet)
			require.Nil(t, params.Description)
			require.False(t, params.ExpiresAtSet)
			require.Nil(t, params.ExpiresAt)
			return nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.UpdatePersonalAccessToken(
		context.Background(),
		"user-1",
		"developer-cli",
		model.UpdatePersonalAccessToken{Name: "Developer CLI renamed"},
	)

	require.NoError(t, err)
	require.NotNil(t, token)
	require.True(t, repo.updateCalled)
	require.Equal(t, "Developer CLI renamed", token.Name)
	require.Equal(t, "Existing local CLI token", *token.Description)
	require.NotNil(t, token.ExpiresAt)
	require.True(t, token.ExpiresAt.Equal(expiresAt))
}

func TestService_UpdatePersonalAccessTokenClearsNullableMetadata(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	description := "Existing local CLI token"
	var repo *fakePATsRepository
	repo = &fakePATsRepository{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) (*pats.PersonalAccessToken, error) {
			if repo.updateCalled {
				return &pats.PersonalAccessToken{
					Slug:        patSlug,
					Name:        repo.updateParams.Name,
					Description: repo.updateParams.Description,
					UserSlug:    userSlug,
					ExpiresAt:   repo.updateParams.ExpiresAt,
				}, nil
			}
			return &pats.PersonalAccessToken{
				Slug:        patSlug,
				Name:        "Developer CLI",
				Description: &description,
				UserSlug:    userSlug,
				ExpiresAt:   &expiresAt,
			}, nil
		},
		updateFn: func(_ context.Context, _ postgres.DBTX, params pats.UpdateParams) error {
			require.Equal(t, "Developer CLI renamed", params.Name)
			require.True(t, params.DescriptionSet)
			require.Nil(t, params.Description)
			require.True(t, params.ExpiresAtSet)
			require.Nil(t, params.ExpiresAt)
			return nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.UpdatePersonalAccessToken(
		context.Background(),
		"user-1",
		"developer-cli",
		model.UpdatePersonalAccessToken{
			Name: "Developer CLI renamed",
			Description: model.NullableStringUpdate{
				Present: true,
				Value:   nil,
			},
			ExpiresAt: model.NullableTimeUpdate{
				Present: true,
				Value:   nil,
			},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, token)
	require.True(t, repo.updateCalled)
	require.Equal(t, "Developer CLI renamed", token.Name)
	require.Nil(t, token.Description)
	require.Nil(t, token.ExpiresAt)
}

func TestService_UpdatePersonalAccessTokenRejectsRevokedToken(t *testing.T) {
	t.Parallel()

	revokedAt := time.Now().UTC()
	repo := &fakePATsRepository{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) (*pats.PersonalAccessToken, error) {
			return &pats.PersonalAccessToken{
				Slug:      patSlug,
				Name:      "Developer CLI",
				UserSlug:  userSlug,
				RevokedAt: &revokedAt,
			}, nil
		},
		updateFn: func(context.Context, postgres.DBTX, pats.UpdateParams) error {
			t.Fatal("revoked personal access tokens must not be updated")
			return nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	token, err := svc.UpdatePersonalAccessToken(
		context.Background(),
		"user-1",
		"developer-cli",
		model.UpdatePersonalAccessToken{Name: "Developer CLI renamed"},
	)

	require.Nil(t, token)
	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrPersonalAccessTokenRevoked))
	require.False(t, repo.updateCalled)
}

func TestService_DeleteRevokedPersonalAccessTokenDeletesMetadata(t *testing.T) {
	t.Parallel()

	revokedAt := time.Now().UTC()
	repo := &fakePATsRepository{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) (*pats.PersonalAccessToken, error) {
			return &pats.PersonalAccessToken{
				Slug:      patSlug,
				Name:      "Developer CLI",
				UserSlug:  userSlug,
				RevokedAt: &revokedAt,
			}, nil
		},
		deleteRevokedFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) error {
			require.Equal(t, slug.Slug("user-1"), userSlug)
			require.Equal(t, slug.Slug("developer-cli"), patSlug)
			return nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	err := svc.DeleteRevokedPersonalAccessToken(context.Background(), "user-1", "developer-cli")

	require.NoError(t, err)
	require.True(t, repo.deleteRevokedCalled)
}

func TestService_DeleteRevokedPersonalAccessTokenRejectsActiveToken(t *testing.T) {
	t.Parallel()

	repo := &fakePATsRepository{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, userSlug, patSlug slug.Slug) (*pats.PersonalAccessToken, error) {
			return &pats.PersonalAccessToken{
				Slug:     patSlug,
				Name:     "Developer CLI",
				UserSlug: userSlug,
			}, nil
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	err := svc.DeleteRevokedPersonalAccessToken(context.Background(), "user-1", "developer-cli")

	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrPersonalAccessTokenNotRevoked))
	require.False(t, repo.deleteRevokedCalled)
}

func TestService_AuthenticateResolvesUserPrincipalAndMarksUsed(t *testing.T) {
	t.Parallel()

	rawToken, tokenHash, _, err := generateToken()
	require.NoError(t, err)

	repo := &fakePATsRepository{
		principal: &pats.Principal{
			TokenSlug:                     "developer-cli",
			UserSlug:                      "user-1",
			CanManagePersonalAccessTokens: true,
		},
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	principal, err := svc.Authenticate(context.Background(), rawToken)

	require.NoError(t, err)
	require.NotNil(t, principal)
	require.Equal(t, slug.Slug("user-1"), principal.UserSlug)
	require.Equal(t, slug.Slug("developer-cli"), principal.PersonalAccessTokenSlug)
	require.True(t, principal.CanManagePersonalAccessTokens)
	require.Equal(t, tokenHash, repo.findTokenHash)
	require.True(t, repo.markUsedCalled)
	require.Equal(t, slug.Slug("user-1"), repo.markUsedUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), repo.markUsedTokenSlug)
}

func TestService_AuthenticateRejectsWrongTokenPrefix(t *testing.T) {
	t.Parallel()

	repo := &fakePATsRepository{}
	svc := New(testutil.FakeTransactionManager{}, repo)

	principal, err := svc.Authenticate(context.Background(), "xto_live_abc")

	require.Nil(t, principal)
	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrInvalidToken))
	require.Empty(t, repo.findTokenHash)
	require.False(t, repo.markUsedCalled)
}

func TestService_AuthenticateTreatsZeroRowMarkUsedAsInvalidToken(t *testing.T) {
	t.Parallel()

	rawToken, _, _, err := generateToken()
	require.NoError(t, err)

	repo := &fakePATsRepository{
		principal: &pats.Principal{
			TokenSlug: "developer-cli",
			UserSlug:  "user-1",
		},
		markUsedErr: pats.ErrNotFound,
	}
	svc := New(testutil.FakeTransactionManager{}, repo)

	principal, err := svc.Authenticate(context.Background(), rawToken)

	require.Nil(t, principal)
	require.Error(t, err)
	require.True(t, xerror.Is(err, ErrInvalidToken))
	require.True(t, repo.markUsedCalled)
}

type fakePATsRepository struct {
	createFn            func(context.Context, postgres.DBTX, pats.CreateParams) (*pats.PersonalAccessToken, error)
	createCalled        bool
	createCalls         int
	createParams        pats.CreateParams
	rotateFn            func(context.Context, postgres.DBTX, pats.RotateParams) (*pats.PersonalAccessToken, error)
	rotateCalled        bool
	rotateParams        pats.RotateParams
	findBySlugFn        func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*pats.PersonalAccessToken, error)
	updateFn            func(context.Context, postgres.DBTX, pats.UpdateParams) error
	updateCalled        bool
	updateParams        pats.UpdateParams
	deleteRevokedFn     func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error
	deleteRevokedCalled bool
	principal           *pats.Principal
	findErr             error
	findTokenHash       string
	markUsedErr         error
	markUsedCalled      bool
	markUsedUserSlug    slug.Slug
	markUsedTokenSlug   slug.Slug
}

func (f *fakePATsRepository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	params pats.CreateParams,
) (*pats.PersonalAccessToken, error) {
	f.createCalled = true
	f.createCalls++
	f.createParams = params
	if f.createFn != nil {
		return f.createFn(ctx, tx, params)
	}
	return nil, pats.ErrNotFound
}

func (f *fakePATsRepository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	patSlug slug.Slug,
) (*pats.PersonalAccessToken, error) {
	if f.findBySlugFn != nil {
		return f.findBySlugFn(ctx, tx, userSlug, patSlug)
	}
	return nil, pats.ErrNotFound
}

func (f *fakePATsRepository) ListByUser(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	model.ListRequest,
) ([]*pats.PersonalAccessToken, error) {
	return nil, pats.ErrNotFound
}

func (f *fakePATsRepository) Update(
	ctx context.Context,
	tx postgres.DBTX,
	params pats.UpdateParams,
) error {
	f.updateCalled = true
	f.updateParams = params
	if f.updateFn != nil {
		return f.updateFn(ctx, tx, params)
	}
	return pats.ErrNotFound
}

func (f *fakePATsRepository) Revoke(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
) error {
	return pats.ErrNotFound
}

func (f *fakePATsRepository) DeleteRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	userSlug slug.Slug,
	patSlug slug.Slug,
) error {
	f.deleteRevokedCalled = true
	if f.deleteRevokedFn != nil {
		return f.deleteRevokedFn(ctx, tx, userSlug, patSlug)
	}
	return pats.ErrNotFound
}

func (f *fakePATsRepository) Rotate(
	ctx context.Context,
	tx postgres.DBTX,
	params pats.RotateParams,
) (*pats.PersonalAccessToken, error) {
	f.rotateCalled = true
	f.rotateParams = params
	if f.rotateFn != nil {
		return f.rotateFn(ctx, tx, params)
	}
	return nil, pats.ErrNotFound
}

func (f *fakePATsRepository) FindActivePrincipalByTokenHash(
	_ context.Context,
	_ postgres.DBTX,
	tokenHash string,
) (*pats.Principal, error) {
	f.findTokenHash = tokenHash
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.principal, nil
}

func (f *fakePATsRepository) MarkUsed(
	_ context.Context,
	_ postgres.DBTX,
	userSlug slug.Slug,
	tokenSlug slug.Slug,
) error {
	f.markUsedCalled = true
	f.markUsedUserSlug = userSlug
	f.markUsedTokenSlug = tokenSlug
	return f.markUsedErr
}

func ptrTo[T any](value T) *T {
	return &value
}
