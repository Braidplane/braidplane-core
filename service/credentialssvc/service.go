package credentialssvc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
)

type Service interface {
	ConnectNotificationConnection(context.Context, CredentialOwner, model.EventActor) (ProviderConnectorInstall, error)
	CreateNotificationConnection(context.Context, CreateNotificationConnectionParams) (NotificationConnection, error)
	ListNotificationConnections(context.Context, slug.Slug) ([]NotificationConnection, error)
	ConnectHTTPOAuth(context.Context, StartOAuthInstallParams) (*ProviderConnectorInstall, error)
	AcquireHTTPOAuthTestCredential(context.Context, HTTPOAuthApp) (RuntimeCredential, error)
	ConfigureHTTPOAuthApp(context.Context, ConfigureHTTPOAuthAppParams) error
	ConnectHTTPOAuthClientCredentials(context.Context, ResolveRuntimeCredentialParams) (CredentialMetadata, error)
	CreateStaticSecret(context.Context, CreateStaticSecretParams) (CredentialMetadata, error)
	CreatePendingOAuthCredential(context.Context, CreatePendingOAuthCredentialParams) (CredentialMetadata, error)
	CreateOrganizationGitLabConnection(context.Context, CreateOrganizationGitLabConnectionParams) (OrganizationGitLabConnection, error)
	AttachOrganizationGitLabConnection(context.Context, AttachOrganizationGitLabConnectionParams) error
	GetOrganizationGitLabConnection(context.Context, slug.Slug) (OrganizationGitLabConnection, error)
	ListOrganizationGitLabRepositories(context.Context, slug.Slug) ([]ProviderRepository, error)
	StartOAuthInstall(context.Context, StartOAuthInstallParams) (ProviderConnectorInstall, error)
	FindOAuthInstall(context.Context, FindOAuthInstallParams) (CredentialInstall, error)
	ConsumeOAuthInstall(context.Context, ConsumeOAuthInstallParams) (CredentialInstall, error)
	CompleteOAuthCallback(context.Context, CompleteOAuthCallbackParams) (CompleteOAuthCallbackResult, error)
	ResolveRuntimeCredential(context.Context, ResolveRuntimeCredentialParams) (RuntimeCredential, error)
	Refresh(context.Context, RefreshGrantParams) (CredentialMetadata, error)
	Revoke(context.Context, RevokeParams) (CredentialMetadata, error)
	Rotate(context.Context, RotateParams) (ProviderConnectorInstall, error)
}

func (s *service) ListOrganizationGitLabRepositories(ctx context.Context, org slug.Slug) ([]ProviderRepository, error) {
	return s.ListProviderRepositories(ctx, ListProviderRepositoriesParams{Owner: CredentialOwner{Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: org}, Provider: "gitlab"})
}

func (s *service) ListProviderRepositories(
	ctx context.Context,
	params ListProviderRepositoriesParams,
) ([]ProviderRepository, error) {
	provider, accessToken, err := s.repositoryProvider(ctx, params.Owner, params.Provider)
	if err != nil {
		return nil, err
	}
	return provider.ListRepositories(ctx, accessToken)
}

func (s *service) ResolveProviderRepository(
	ctx context.Context,
	params ResolveProviderRepositoryParams,
) (ProviderRepository, error) {
	if strings.TrimSpace(params.RepositoryID) == "" {
		return ProviderRepository{}, ErrInvalidInput.Detail("provider repository ID is required")
	}
	provider, accessToken, err := s.repositoryProvider(ctx, params.Owner, params.Provider)
	if err != nil {
		return ProviderRepository{}, err
	}
	return provider.ResolveRepository(ctx, accessToken, params.RepositoryID)
}

func (s *service) ResolveProviderRepositoryWithToken(
	ctx context.Context,
	params ResolveProviderRepositoryWithTokenParams,
) (ProviderRepository, error) {
	if err := validate.Default().Struct(params); err != nil {
		return ProviderRepository{}, ErrInvalidInput.With(err)
	}
	provider, err := s.provider(params.Provider)
	if err != nil {
		return ProviderRepository{}, err
	}
	repositoryProvider, ok := provider.(RepositoryProvider)
	if !ok {
		return ProviderRepository{}, ErrInvalidInput.Detail("provider does not support repository selection")
	}
	return repositoryProvider.ResolveRepository(ctx, params.AccessToken, params.RepositoryID)
}

func (s *service) repositoryProvider(
	ctx context.Context,
	owner CredentialOwner,
	providerKey string,
) (RepositoryProvider, string, error) {
	if err := validateOAuthOwner(owner); err != nil {
		return nil, "", err
	}
	provider, err := s.provider(providerKey)
	if err != nil {
		return nil, "", err
	}
	repositoryProvider, ok := provider.(RepositoryProvider)
	if !ok {
		return nil, "", ErrInvalidInput.Detail("provider does not support repository selection")
	}
	runtimeCredential, err := s.ResolveRuntimeCredential(ctx, ResolveRuntimeCredentialParams{Owner: owner})
	if err != nil {
		return nil, "", err
	}
	if runtimeCredential.Kind != CredentialKindOAuth2Grant {
		return nil, "", ErrInvalidInput.Detail("provider repository selection requires an OAuth grant")
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(runtimeCredential.PlaintextJSON, &payload); err != nil || strings.TrimSpace(payload.AccessToken) == "" {
		return nil, "", xerror.Unexpected().Detail("provider runtime credential is invalid")
	}
	return repositoryProvider, payload.AccessToken, nil
}

type PipelineWebhookSecretLifecycleService interface {
	IssuePipelineWebhookSecret(context.Context, PipelineWebhookSecretParams) (IssuedPipelineWebhookSecret, error)
	RevokePipelineWebhookSecrets(context.Context, PipelineWebhookSecretParams) error
}

type FullService interface {
	Service
	ProofResolver
	CheckProofCredential(context.Context, proofevidence.Grant) error
	PipelineWebhookSecretLifecycleService
	ResolvePipelineWebhookSecret(context.Context, ResolvePipelineWebhookSecretParams) ([]byte, error)
}

// CreatePendingOAuthCredential persists a payload-free OAuth credential owner for a new data source.
func (s *service) CreatePendingOAuthCredential(
	ctx context.Context,
	params CreatePendingOAuthCredentialParams,
) (CredentialMetadata, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CredentialMetadata{}, ErrInvalidInput.With(err)
	}
	if err := validateOAuthOwner(params.Owner); err != nil {
		return CredentialMetadata{}, err
	}
	if params.Owner.Kind != CredentialOwnerKindDataSource {
		return CredentialMetadata{}, ErrInvalidInput.Detail("pending source credentials require a data source owner")
	}
	if _, err := s.provider(params.Provider); params.Provider != "http" && err != nil {
		return CredentialMetadata{}, err
	}

	var metadata CredentialMetadata
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		credentialSlug, err := s.nextSlug("oauth-credential", params.Owner.DataSourceSlug)
		if err != nil {
			return ErrInvalidInput.With(err)
		}
		record, err := s.repo.CreatePendingOAuthCredential(ctx, tx, repocredentials.CredentialRecord{
			Slug:                       credentialSlug,
			OrgSlug:                    params.Owner.OrgSlug,
			OwnerKind:                  repocredentials.OwnerKindDataSource,
			DataSourceSlug:             params.Owner.DataSourceSlug,
			NotificationConnectionSlug: params.Owner.NotificationConnectionSlug,
			Kind:                       CredentialKindOAuth2Grant.String(),
			Status:                     CredentialStatusPending.String(),
		})
		if err != nil {
			return xerror.Wrapf("create pending oauth credential: %w", err)
		}
		metadata = metadataFromRecord(fromRepoCredentialRecord(record))
		metadata.OAuth.Provider = params.Provider
		return nil
	})
	if err != nil {
		return CredentialMetadata{}, err
	}
	return metadata, nil
}

func (s *service) CreateOrganizationGitLabConnection(ctx context.Context, params CreateOrganizationGitLabConnectionParams) (OrganizationGitLabConnection, error) {
	if err := validate.Default().Struct(params); err != nil {
		return OrganizationGitLabConnection{}, ErrInvalidInput.With(err)
	}
	if _, err := s.provider(params.Provider); err != nil {
		return OrganizationGitLabConnection{}, err
	}
	if params.Provider != "gitlab" {
		return OrganizationGitLabConnection{}, ErrInvalidInput
	}
	owner := CredentialOwner{Kind: CredentialOwnerKindOrganizationGitLab, OrgSlug: params.OrganizationSlug}
	var result OrganizationGitLabConnection
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		existing, err := s.GetOrganizationGitLabConnection(ctx, owner.OrgSlug)
		if err == nil {
			result = existing
			return nil
		}
		if !xerror.Is(err, ErrCredentialNotFound) {
			return err
		}
		credentialSlug, err := s.nextSlug("oauth-credential", owner.OrgSlug)
		if err != nil {
			return ErrInvalidInput.With(err)
		}
		record, err := s.repo.CreateOrganizationGitLabConnection(ctx, tx, repocredentials.CredentialRecord{Slug: credentialSlug, OrgSlug: owner.OrgSlug, OwnerKind: owner.Kind.String(), Kind: CredentialKindOAuth2Grant.String(), Status: CredentialStatusPending.String()})
		if err != nil {
			return xerror.Wrapf("create organization gitlab connection: %w", err)
		}
		metadata := metadataFromRecord(fromRepoCredentialRecord(record))
		metadata.OAuth.Provider = params.Provider
		result = OrganizationGitLabConnection{OrganizationSlug: owner.OrgSlug, Credential: metadata}
		return nil
	})
	return result, err
}

func (s *service) AttachOrganizationGitLabConnection(ctx context.Context, params AttachOrganizationGitLabConnectionParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Serialize attachment with organization rotation before locking the source.
		current, err := s.repo.LockOAuthGrantIntent(ctx, tx, repocredentials.ResolveRuntimeCredentialParams{OwnerKind: repocredentials.OwnerKindOrganizationGitLab, OrgSlug: params.OrganizationSlug})
		if err != nil {
			return err
		}
		if current.CredentialSlug != params.ExpectedCredentialSlug || current.Status != CredentialStatusActive.String() {
			return ErrOAuthIntentConflict
		}
		return s.repo.AttachOrganizationGitLabConnection(ctx, tx, params.OrganizationSlug, params.DataSourceSlug, params.ExpectedCredentialSlug, params.ExpectedRepositoryID)
	})
}

func (s *service) GetOrganizationGitLabConnection(ctx context.Context, orgSlug slug.Slug) (OrganizationGitLabConnection, error) {
	if orgSlug == "" {
		return OrganizationGitLabConnection{}, ErrInvalidInput.Detail("organization slug is required")
	}
	record, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (repocredentials.CredentialRecord, error) {
		return s.repo.FindOrganizationGitLabConnection(ctx, db, orgSlug)
	})
	if err != nil {
		return OrganizationGitLabConnection{}, err
	}
	metadata := metadataFromRecord(fromRepoCredentialRecord(record))
	metadata.OAuth.Provider = "gitlab"
	if metadata.Status != CredentialStatusPending {
		grant, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (repocredentials.CredentialGrant, error) {
			return s.repo.FindOAuthGrant(ctx, db, repocredentials.ResolveRuntimeCredentialParams{OwnerKind: repocredentials.OwnerKindOrganizationGitLab, OrgSlug: orgSlug})
		})
		if err != nil {
			return OrganizationGitLabConnection{}, err
		}
		metadata = metadataFromGrant(fromRepoCredentialGrant(grant))
	}
	return OrganizationGitLabConnection{OrganizationSlug: orgSlug, Credential: metadata}, nil
}

type service struct {
	txm                         postgres.TransactionManager
	repo                        repocredentials.Repository
	enc                         crypto.CredentialCipher
	providers                   ProviderRegistry
	clock                       clock.Clock
	allowPrivateDataSourceHosts bool
}

// New creates the shared credential lifecycle service.
func New(
	txm postgres.TransactionManager,
	repo repocredentials.Repository,
	enc crypto.CredentialCipher,
	providers ProviderRegistry,
	clock clock.Clock,
	allowPrivateDataSourceHosts bool,
) (FullService, error) {
	if txm == nil {
		return nil, xerror.Unexpected().Detail("transaction manager is required")
	}
	if repo == nil {
		return nil, xerror.Unexpected().Detail("credential repository is required")
	}
	if enc == nil {
		return nil, xerror.Unexpected().Detail("credential encryptor is required")
	}
	if clock == nil {
		return nil, xerror.Unexpected().Detail("clock is required")
	}
	return &service{
		txm:                         txm,
		repo:                        repo,
		enc:                         enc,
		providers:                   providers,
		clock:                       clock,
		allowPrivateDataSourceHosts: allowPrivateDataSourceHosts,
	}, nil
}

// CreateStaticSecret encrypts a non-OAuth credential payload and returns only safe metadata.
func (s *service) CreateStaticSecret(
	ctx context.Context,
	params CreateStaticSecretParams,
) (CredentialMetadata, error) {
	var metadata CredentialMetadata
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		metadata, err = s.createStaticSecret(ctx, tx, params)
		return err
	})
	if err != nil {
		return CredentialMetadata{}, err
	}
	return metadata, nil
}

func (s *service) IssuePipelineWebhookSecret(
	ctx context.Context,
	params PipelineWebhookSecretParams,
) (IssuedPipelineWebhookSecret, error) {
	if err := validate.Default().Struct(params); err != nil {
		return IssuedPipelineWebhookSecret{}, ErrInvalidInput.With(err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return IssuedPipelineWebhookSecret{}, xerror.Wrapf("generate pipeline webhook secret: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	credentialSlug, err := s.nextSlug("webhook", params.TriggerSlug)
	if err != nil {
		return IssuedPipelineWebhookSecret{}, ErrInvalidInput.With(err)
	}
	encrypted, err := s.enc.Seal(credentialScope(params.OrgSlug, credentialSlug, "pipeline-webhook-secret"), []byte(value))
	if err != nil {
		return IssuedPipelineWebhookSecret{}, xerror.Wrapf("encrypt pipeline webhook secret: %w", err)
	}
	var version int
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, version, err = s.repo.CreatePipelineWebhookCredential(ctx, tx, repocredentials.PipelineWebhookOwner{
			OrgSlug: params.OrgSlug, ProjectSlug: params.ProjectSlug, EnvironmentSlug: params.EnvironmentSlug, TriggerSlug: params.TriggerSlug,
		}, repocredentials.CredentialRecord{
			Slug: credentialSlug, OrgSlug: params.OrgSlug, Kind: CredentialKindStaticSecret.String(),
			CredentialType: "pipeline_webhook", Status: CredentialStatusActive.String(), EncryptedPayload: encrypted,
		}, s.now())
		return err
	})
	if err != nil {
		return IssuedPipelineWebhookSecret{}, err
	}
	return IssuedPipelineWebhookSecret{KeyVersion: version, Value: value}, nil
}

func (s *service) RevokePipelineWebhookSecrets(ctx context.Context, params PipelineWebhookSecretParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		owner := repocredentials.PipelineWebhookOwner{
			OrgSlug: params.OrgSlug, ProjectSlug: params.ProjectSlug, EnvironmentSlug: params.EnvironmentSlug, TriggerSlug: params.TriggerSlug,
		}
		if err := s.repo.LockPipelineWebhookCredentialOwner(ctx, tx, owner); err != nil {
			return err
		}
		return s.repo.RevokePipelineWebhookCredentials(ctx, tx, owner, s.now())
	})
}

// ResolvePipelineWebhookSecret returns one enabled revision for in-memory signature verification.
func (s *service) ResolvePipelineWebhookSecret(
	ctx context.Context,
	params ResolvePipelineWebhookSecretParams,
) ([]byte, error) {
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	record, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (repocredentials.CredentialRecord, error) {
		return s.repo.ResolvePipelineWebhookCredential(ctx, db, repocredentials.PipelineWebhookOwner{
			OrgSlug: params.OrgSlug, ProjectSlug: params.ProjectSlug, EnvironmentSlug: params.EnvironmentSlug,
			TriggerSlug: params.TriggerSlug,
		}, params.KeyVersion)
	})
	if err != nil {
		return nil, err
	}
	secret, err := s.enc.Open(credentialScope(params.OrgSlug, record.Slug, "pipeline-webhook-secret"), record.EncryptedPayload)
	if err != nil {
		return nil, xerror.Wrapf("decrypt pipeline webhook secret: %w", err)
	}
	return secret, nil
}

// StartOAuthInstall stores hashed state and returns the raw state only for the provider redirect.
func (s *service) StartOAuthInstall(
	ctx context.Context,
	params StartOAuthInstallParams,
) (ProviderConnectorInstall, error) {
	if err := validate.Default().Struct(params); err != nil {
		return ProviderConnectorInstall{}, ErrInvalidInput.With(err)
	}
	if err := validateOAuthOwner(params.Owner); err != nil {
		return ProviderConnectorInstall{}, err
	}
	var install ProviderConnectorInstall
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		install, err = s.startOAuthInstall(ctx, tx, params)
		return err
	})
	if err != nil {
		return ProviderConnectorInstall{}, err
	}
	return install, nil
}

// FindOAuthInstall resolves install metadata for callback authorization without consuming state.
func (s *service) FindOAuthInstall(
	ctx context.Context,
	params FindOAuthInstallParams,
) (CredentialInstall, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CredentialInstall{}, ErrInvalidInput.With(err)
	}

	now := s.now()
	var install CredentialInstall
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoInstall, err := s.repo.FindOAuthInstallByState(
			ctx,
			tx,
			params.Provider,
			oauthflow.HashState(params.State),
			now,
		)
		install = fromRepoCredentialInstall(repoInstall)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return install, err
	}
	return install, nil
}

// ConsumeOAuthInstall terminally consumes a callback state without exchanging or storing provider credentials.
func (s *service) ConsumeOAuthInstall(
	ctx context.Context,
	params ConsumeOAuthInstallParams,
) (CredentialInstall, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CredentialInstall{}, ErrInvalidInput.With(err)
	}

	now := s.now()
	var install CredentialInstall
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoInstall, err := s.repo.ConsumeOAuthInstall(
			ctx,
			tx,
			params.Provider,
			oauthflow.HashState(params.State),
			now,
		)
		install = fromRepoCredentialInstall(repoInstall)
		return err
	})
	if err != nil {
		return install, err
	}
	return install, nil
}

// CompleteOAuthCallback validates the pending install, exchanges the provider code without
// holding a database transaction, then atomically consumes the install and stores the grant.
func (s *service) CompleteOAuthCallback(
	ctx context.Context,
	params CompleteOAuthCallbackParams,
) (CompleteOAuthCallbackResult, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CompleteOAuthCallbackResult{}, ErrInvalidInput.With(err)
	}
	var provider ProviderAdapter
	var err error

	now := s.now()
	var prepared CredentialInstall
	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		repoInstall, err := s.repo.FindOAuthInstallByState(
			ctx,
			tx,
			params.Provider,
			oauthflow.HashState(params.State),
			now,
		)
		if err != nil {
			return err
		}
		prepared = fromRepoCredentialInstall(repoInstall)
		if prepared.RepositorySetupSlug != "" && (params.Actor.UserSlugValue() == "" || params.Actor.UserSlugValue() != prepared.Actor.UserSlugValue()) {
			return ErrOAuthStateInvalid
		}
		intentState, err := s.repo.LockOAuthGrantIntent(ctx, tx, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: prepared.Owner}))
		if err != nil {
			return err
		}
		return validateOAuthGrantIntentState(prepared, intentState)
	})
	if err != nil {
		return CompleteOAuthCallbackResult{}, err
	}

	verifier := ""
	if params.Provider == "http" {
		provider, verifier, err = s.httpOAuthInstallProvider(prepared)
	} else {
		provider, err = s.provider(params.Provider)
	}
	if err != nil {
		return CompleteOAuthCallbackResult{}, err
	}
	tokenSet, err := provider.ExchangeCode(
		postgres.WithoutTransaction(ctx),
		ExchangeCodeParams{Code: params.Code, PKCEVerifier: verifier, Issuer: params.Issuer, RequestedScopes: append([]string(nil), prepared.RequestedScopes...)},
	)
	if err != nil {
		// Preserve typed provider remediation errors so the HTTP callback exposes their safe status instead of a false 503.
		var providerErr *xerror.Error
		if xerror.As(err, &providerErr) {
			return CompleteOAuthCallbackResult{}, err
		}
		return CompleteOAuthCallbackResult{}, ErrProviderUnavailable.With(err)
	}
	grant, err := s.grantFromTokenSet(prepared.Owner, params.Provider, tokenSet, now, initialGrantSlug(prepared))
	if err != nil {
		return CompleteOAuthCallbackResult{}, err
	}

	if params.Provider == "google_workspace" {
		if params.ValidateWorkspace == nil {
			return CompleteOAuthCallbackResult{}, ErrInvalidInput.Detail("Workspace callback requires source verification")
		}
		if err := params.ValidateWorkspace(postgres.WithoutTransaction(ctx), prepared, tokenSet); err != nil {
			return CompleteOAuthCallbackResult{}, err
		}
	}

	var result CompleteOAuthCallbackResult
	finalizeCtx, cancel := credentialFinalizeContext(ctx)
	defer cancel()
	finalizeNow := s.now()
	err = s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		result, err = s.finalizeOAuthCallback(ctx, tx, params, grant, tokenSet, finalizeNow)
		return err
	})
	if err != nil {
		return CompleteOAuthCallbackResult{}, err
	}
	return result, nil
}

// ResolveRuntimeCredential returns a decrypted in-memory credential payload for connector construction.
func (s *service) ResolveRuntimeCredential(
	ctx context.Context,
	params ResolveRuntimeCredentialParams,
) (RuntimeCredential, error) {
	if err := validate.Default().Struct(params); err != nil {
		return RuntimeCredential{}, ErrInvalidInput.With(err)
	}
	if err := validateCredentialOwner(params.Owner); err != nil {
		return RuntimeCredential{}, err
	}
	if params.Owner.Kind == CredentialOwnerKindDataSource || params.Owner.Kind == CredentialOwnerKindOrganizationGitLab || params.Owner.Kind == CredentialOwnerKindNotificationConnection {
		for attempt := range 2 {
			grant, err := postgres.InRead(
				postgres.WithoutTransaction(ctx),
				s.txm,
				func(ctx context.Context, db postgres.DBTX) (CredentialGrant, error) {
					repoGrant, err := s.repo.FindOAuthGrant(ctx, db, toRepoResolveParams(params))
					if err != nil {
						return CredentialGrant{}, err
					}
					return fromRepoCredentialGrant(repoGrant), nil
				},
			)
			if err == nil {
				runtime, err := s.resolveGrant(postgres.WithoutTransaction(ctx), grant)
				if xerror.Is(err, ErrOAuthGrantConflict) {
					if attempt == 1 {
						return RuntimeCredential{}, err
					}
					continue
				}
				return runtime, err
			}
			if !xerror.Is(err, ErrCredentialNotFound) {
				return RuntimeCredential{}, err
			}
			break
		}
	}

	if params.Owner.Kind == CredentialOwnerKindNotificationConnection {
		return RuntimeCredential{}, ErrCredentialNotFound
	}
	return postgres.InRead(
		postgres.WithoutTransaction(ctx),
		s.txm,
		func(ctx context.Context, db postgres.DBTX) (RuntimeCredential, error) {
			repoRecord, err := s.findStaticSecret(ctx, db, params.Owner)
			if err != nil {
				return RuntimeCredential{}, err
			}
			record := fromRepoCredentialRecord(repoRecord)
			plaintext, err := s.decryptPayload(credentialScope(record.Owner.OrgSlug, record.Slug, "static-secret"), record.EncryptedPayload)
			if err != nil {
				return RuntimeCredential{}, err
			}
			return RuntimeCredential{
				Kind:          CredentialKindStaticSecret,
				Version:       runtimeCredentialVersion(record.Slug, record.UpdatedAt),
				PlaintextJSON: plaintext,
			}, nil
		},
	)
}

// Refresh forces an OAuth grant refresh and returns safe updated credential metadata.
func (s *service) Refresh(ctx context.Context, params RefreshGrantParams) (CredentialMetadata, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CredentialMetadata{}, ErrInvalidInput.With(err)
	}
	if err := validateOAuthOwner(params.Owner); err != nil {
		return CredentialMetadata{}, err
	}
	grant, err := postgres.InRead(
		postgres.WithoutTransaction(ctx),
		s.txm,
		func(ctx context.Context, db postgres.DBTX) (CredentialGrant, error) {
			repoGrant, err := s.repo.FindOAuthGrant(
				ctx,
				db,
				toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner}),
			)
			if err != nil {
				return CredentialGrant{}, err
			}
			return fromRepoCredentialGrant(repoGrant), nil
		},
	)
	if err != nil {
		return CredentialMetadata{}, err
	}
	if grant.Owner.Kind != params.Owner.Kind {
		return CredentialMetadata{}, ErrInvalidInput.Detail("Manage the shared GitLab connection in organization settings.")
	}
	refreshed, err := s.refreshGrant(postgres.WithoutTransaction(ctx), grant, s.now())
	if err != nil {
		return CredentialMetadata{}, err
	}
	return metadataFromGrant(refreshed), nil
}

// Revoke invalidates the active credential for a supported owner without exposing secret material.
func (s *service) Revoke(ctx context.Context, params RevokeParams) (CredentialMetadata, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CredentialMetadata{}, ErrInvalidInput.With(err)
	}
	if err := validateCredentialOwner(params.Owner); err != nil {
		return CredentialMetadata{}, err
	}
	switch params.Owner.Kind {
	case CredentialOwnerKindDataSource:
		return s.revokeDataSourceOAuthGrant(ctx, params)
	case CredentialOwnerKindOrganizationGitLab, CredentialOwnerKindNotificationConnection:
		return s.revokeDataSourceOAuthGrant(ctx, params)
	case CredentialOwnerKindEnvironmentSecret:
		return s.revokeStaticEnvironmentSecret(ctx, params)
	default:
		return CredentialMetadata{}, ErrInvalidInput.Detailf("unsupported credential owner kind %q", params.Owner.Kind)
	}
}

// Rotate starts a replacement OAuth install while the current grant remains active.
func (s *service) Rotate(ctx context.Context, params RotateParams) (ProviderConnectorInstall, error) {
	if err := validate.Default().Struct(params); err != nil {
		return ProviderConnectorInstall{}, ErrInvalidInput.With(err)
	}
	if err := validateOAuthOwner(params.Owner); err != nil {
		return ProviderConnectorInstall{}, err
	}
	var install ProviderConnectorInstall
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoFound, err := s.repo.FindOAuthGrant(ctx, tx, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner}))
		if err != nil {
			return err
		}
		grant := fromRepoCredentialGrant(repoFound)
		if grant.Owner.Kind != params.Owner.Kind {
			return ErrInvalidInput.Detail("Manage the shared GitLab connection in organization settings.")
		}
		install, err = s.startOAuthInstall(ctx, tx, StartOAuthInstallParams{
			Owner:              params.Owner,
			Provider:           grant.Provider,
			RequestedScopes:    append([]string{}, grant.Scopes...),
			IncludeWriteScopes: params.IncludeWriteScopes,
			AccessMode:         params.AccessMode,
			Intent:             OAuthInstallIntentRotate,
			Actor:              params.Actor,
		})
		return err
	})
	if err != nil {
		return ProviderConnectorInstall{}, err
	}
	return install, nil
}

// findStaticSecret dispatches active static-secret lookup by validated owner kind.
func (s *service) findStaticSecret(
	ctx context.Context,
	tx postgres.DBTX,
	owner CredentialOwner,
) (repocredentials.CredentialRecord, error) {
	repoParams := toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: owner})
	switch owner.Kind {
	case CredentialOwnerKindDataSource:
		return s.repo.FindStaticSecretByDataSource(ctx, tx, repoParams)
	case CredentialOwnerKindEnvironmentSecret:
		return s.repo.FindStaticSecretByEnvironmentSecret(ctx, tx, repoParams)
	default:
		return repocredentials.CredentialRecord{}, ErrInvalidInput.Detailf("unsupported credential owner kind %q", owner.Kind)
	}
}

// revokeDataSourceOAuthGrant revokes a data-source OAuth grant locally and at the provider when available.
func (s *service) revokeDataSourceOAuthGrant(ctx context.Context, params RevokeParams) (CredentialMetadata, error) {
	now := s.now()
	grant, err := postgres.InRead(
		postgres.WithoutTransaction(ctx),
		s.txm,
		func(ctx context.Context, db postgres.DBTX) (CredentialGrant, error) {
			repoGrant, err := s.repo.FindOAuthGrant(
				ctx,
				db,
				toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner}),
			)
			if err != nil {
				return CredentialGrant{}, err
			}
			return fromRepoCredentialGrant(repoGrant), nil
		},
	)
	if err != nil {
		return CredentialMetadata{}, err
	}
	if grant.Owner.Kind != params.Owner.Kind {
		return CredentialMetadata{}, ErrInvalidInput.Detail("Manage the shared GitLab connection in organization settings.")
	}
	if grant.Status == CredentialStatusRevoked {
		return metadataFromGrant(grant), nil
	}
	if grant.Status != CredentialStatusActive {
		return CredentialMetadata{}, ErrCredentialNotFound
	}
	tokenSet, err := s.decryptTokenSet(grant)
	if err != nil {
		return CredentialMetadata{}, err
	}
	grant.Status = CredentialStatusRevoked
	grant.RevokedAt = &now
	grant.UpdatedAt = now
	finalizeCtx, cancel := credentialFinalizeContext(ctx)
	defer cancel()
	var stored CredentialGrant
	err = s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
		repoStored, err := s.repo.MarkOAuthGrantRevoked(ctx, tx, toRepoCredentialGrant(grant))
		if err != nil {
			return err
		}
		stored = fromRepoCredentialGrant(repoStored)
		return nil
	})
	if err != nil {
		return CredentialMetadata{}, err
	}
	if provider, providerErr := s.providerForGrant(grant, tokenSet); providerErr == nil {
		_ = provider.Revoke(postgres.WithoutTransaction(ctx), RevokeProviderGrantParams{
			AccessToken:  tokenSet.AccessToken,
			RefreshToken: tokenSet.RefreshToken,
		})
	}
	return metadataFromGrant(stored), nil
}

// revokeStaticEnvironmentSecret tombstones the encrypted payload for an environment-scoped static secret.
func (s *service) revokeStaticEnvironmentSecret(ctx context.Context, params RevokeParams) (CredentialMetadata, error) {
	now := s.now()
	var metadata CredentialMetadata
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoRecord, err := s.repo.MarkStaticEnvironmentSecretRevoked(
			ctx,
			tx,
			toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner}),
			now,
		)
		if err != nil {
			return err
		}
		record := fromRepoCredentialRecord(repoRecord)
		metadata = metadataFromRecord(record)
		return nil
	})
	if err != nil {
		return CredentialMetadata{}, err
	}
	return metadata, nil
}

// createStaticSecret encrypts and stores a static credential using the supplied DB handle.
func (s *service) createStaticSecret(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateStaticSecretParams,
) (CredentialMetadata, error) {
	if err := validate.Default().Struct(params); err != nil {
		return CredentialMetadata{}, ErrInvalidInput.With(err)
	}
	if err := validateCredentialOwner(params.Owner); err != nil {
		return CredentialMetadata{}, err
	}

	now := s.now()
	slugBase, err := credentialSlugBase(params.Owner)
	if err != nil {
		return CredentialMetadata{}, err
	}
	credentialSlug, err := s.nextSlug("cred", slugBase)
	if err != nil {
		return CredentialMetadata{}, ErrInvalidInput.With(err)
	}

	encrypted, err := s.encryptPayload(credentialScope(params.Owner.OrgSlug, credentialSlug, "static-secret"), params.PlaintextJSON)
	if err != nil {
		return CredentialMetadata{}, err
	}

	record := CredentialRecord{
		Slug:             credentialSlug,
		Owner:            params.Owner,
		Kind:             CredentialKindStaticSecret,
		CredentialType:   params.CredentialType,
		Status:           CredentialStatusActive,
		EncryptedPayload: encrypted,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	repoStored, err := s.repo.CreateStaticSecret(ctx, tx, toRepoCredentialRecord(record))
	if err != nil {
		return CredentialMetadata{}, xerror.Wrapf("create static credential: %w", err)
	}
	stored := fromRepoCredentialRecord(repoStored)
	if params.Owner.Kind == CredentialOwnerKindEnvironmentSecret {
		if _, err := s.repo.MarkStaticEnvironmentSecretRotated(
			ctx,
			tx,
			toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner}),
			stored.Slug,
			now,
		); err != nil {
			return CredentialMetadata{}, xerror.Wrapf("rotate previous environment secret credential: %w", err)
		}
	}
	return metadataFromRecord(stored), nil
}

// finalizeOAuthCallback revalidates the intent before storing the exchanged grant.
func (s *service) finalizeOAuthCallback(
	ctx context.Context,
	tx postgres.DBTX,
	params CompleteOAuthCallbackParams,
	grant CredentialGrant,
	tokenSet OAuthTokenSet,
	now time.Time,
) (CompleteOAuthCallbackResult, error) {
	repoInstall, err := s.repo.ConsumeOAuthInstall(
		ctx,
		tx,
		params.Provider,
		oauthflow.HashState(params.State),
		now,
	)
	if err != nil {
		return CompleteOAuthCallbackResult{}, err
	}
	install := fromRepoCredentialInstall(repoInstall)
	if install.RepositorySetupSlug != "" && (params.Actor.UserSlugValue() == "" || params.Actor.UserSlugValue() != install.Actor.UserSlugValue()) {
		return CompleteOAuthCallbackResult{}, ErrOAuthStateInvalid
	}
	intentState, err := s.repo.LockOAuthGrantIntent(ctx, tx, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: install.Owner}))
	if err != nil {
		return CompleteOAuthCallbackResult{}, err
	}
	if err := validateOAuthGrantIntentState(install, intentState); err != nil {
		return CompleteOAuthCallbackResult{}, err
	}
	if params.Provider == "google_workspace" {
		if params.FinalizeWorkspace == nil {
			return CompleteOAuthCallbackResult{}, ErrInvalidInput.Detail("Workspace callback requires source verification")
		}
		if err := params.FinalizeWorkspace(ctx, install, tokenSet); err != nil {
			return CompleteOAuthCallbackResult{}, err
		}
	}
	repoStored, err := s.repo.CreateOAuthGrant(ctx, tx, toRepoCredentialGrant(grant), install.Intent.String())
	if err != nil {
		return CompleteOAuthCallbackResult{}, xerror.Wrapf("create oauth grant: %w", err)
	}
	stored := fromRepoCredentialGrant(repoStored)

	return CompleteOAuthCallbackResult{
		InstallSlug: install.Slug,
		Owner:       install.Owner,
		Intent:      install.Intent,
		Actor:       install.Actor,
		Credential:  metadataFromGrant(stored),
	}, nil
}

// validateOAuthGrantIntentState rejects callbacks whose locked credential state no longer matches their intent.
func validateOAuthGrantIntentState(
	install CredentialInstall,
	state repocredentials.OAuthGrantIntentState,
) error {
	if install.Provider == "http" && (install.ExpectedSourceRevision == nil || *install.ExpectedSourceRevision != state.SourceRevision || install.ExpectedCredentialSlug != state.CredentialSlug) {
		return ErrOAuthIntentConflict
	}
	if install.Intent == OAuthInstallIntentRotate {
		// Rotation can either replace an OAuth grant or switch a static credential to OAuth.
		validSourceState := state.Kind == CredentialKindStaticSecret.String() && !state.HasGrant ||
			state.Kind == CredentialKindOAuth2Grant.String() && state.HasGrant
		if state.CredentialSlug == install.ExpectedCredentialSlug &&
			state.Status == install.ExpectedCredentialStatus.String() &&
			validSourceState {
			return nil
		}
		return ErrOAuthIntentConflict
	}
	if install.Intent == OAuthInstallIntentCreate &&
		state.Kind == CredentialKindOAuth2Grant.String() &&
		state.Status == CredentialStatusPending.String() &&
		!state.HasGrant {
		return nil
	}
	return ErrOAuthIntentConflict
}

func (s *service) startOAuthInstall(
	ctx context.Context,
	tx postgres.DBTX,
	params StartOAuthInstallParams,
) (ProviderConnectorInstall, error) {
	if (params.RepositorySetupSlug != "" || params.RepositorySetupProjectSlug != "") && (params.RepositorySetupSlug == "" || params.RepositorySetupProjectSlug == "" || params.Provider != "gitlab" || params.Owner.Kind != CredentialOwnerKindOrganizationGitLab || params.Actor.UserSlugValue() == "" || !params.IncludeWriteScopes) {
		return ProviderConnectorInstall{}, ErrInvalidInput.Detail("Repository setup consent requires a GitLab organization connection, user actor and API write access.")
	}
	if params.Owner.Kind != CredentialOwnerKindNotificationConnection && params.Provider == "slack" {
		return ProviderConnectorInstall{}, ErrInvalidInput.Detail("Slack OAuth requires a notification connection")
	}
	if params.AccessMode != "" && params.Provider != "google_workspace" {
		return ProviderConnectorInstall{}, ErrInvalidInput.Detail("OAuth file access mode is only supported by Google Workspace")
	}
	if err := validateOAuthOwner(params.Owner); err != nil {
		return ProviderConnectorInstall{}, err
	}
	var provider ProviderAdapter
	var app *httpOAuthProvider
	var err error
	if params.Provider == "http" {
		// Hold the source revision stable while reading the app and persisting its install snapshot.
		if _, err := s.repo.LockOAuthGrantIntent(ctx, tx, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner})); err != nil {
			return ProviderConnectorInstall{}, err
		}
		app, err = s.httpOAuthProvider(ctx, params.Owner)
		provider = app
	} else {
		provider, err = s.provider(params.Provider)
	}
	if err != nil {
		return ProviderConnectorInstall{}, err
	}

	rawState, err := oauthflow.RandomState()
	if err != nil {
		return ProviderConnectorInstall{}, err
	}
	now := s.now()
	slugBase, err := credentialSlugBase(params.Owner)
	if err != nil {
		return ProviderConnectorInstall{}, err
	}
	installSlug, err := s.nextSlug("oauth-install", slugBase)
	if err != nil {
		return ProviderConnectorInstall{}, ErrInvalidInput.With(err)
	}
	expiresAt := now.Add(DefaultInstallTTL)
	scopePlan := OAuthScopePlan{Required: append([]string(nil), params.RequestedScopes...)}
	if contextualProvider, ok := provider.(ContextualScopePlanningProvider); ok {
		scopePlan = contextualProvider.OAuthScopePlanForInstall(OAuthScopePlanParams{
			IncludeWriteScopes: params.IncludeWriteScopes,
			AccessMode:         params.AccessMode,
		})
	} else if plannedProvider, ok := provider.(ScopePlanningProvider); ok {
		scopePlan = plannedProvider.OAuthScopePlan()
	}
	requestedScopes := append([]string{}, scopePlan.Required...)
	requestedScopes = append(requestedScopes, scopePlan.Optional...)
	install := CredentialInstall{
		RepositorySetupSlug:        params.RepositorySetupSlug,
		RepositorySetupProjectSlug: params.RepositorySetupProjectSlug,
		Slug:                       installSlug,
		Owner:                      params.Owner,
		Provider:                   params.Provider,
		StateHash:                  oauthflow.HashState(rawState),
		Intent:                     params.Intent,
		Actor:                      normalizeEventActor(params.Actor),
		RequestedScopes:            requestedScopes,
		ExpiresAt:                  expiresAt,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}
	verifier := ""
	if app != nil {
		if app.app.Config.GrantType != "authorization_code" {
			return ProviderConnectorInstall{}, ErrInvalidInput.Detail("HTTP OAuth app does not use authorization_code")
		}
		verifier, err = httpoauth.NewVerifier()
		if err != nil {
			return ProviderConnectorInstall{}, err
		}
		payload, marshalErr := json.Marshal(httpOAuthInstallContext{Verifier: verifier, App: app.app})
		if marshalErr != nil {
			return ProviderConnectorInstall{}, xerror.Unexpected().With(marshalErr)
		}
		install.EncryptedPKCEVerifier, err = s.encryptPayload(credentialScope(install.Owner.OrgSlug, install.Slug, "oauth-install-snapshot"), payload)
		if err != nil {
			return ProviderConnectorInstall{}, err
		}
	}
	authURL, err := provider.AuthorizationURL(ctx, AuthorizationURLParams{
		State:          rawState,
		PKCEVerifier:   verifier,
		Scopes:         append([]string(nil), scopePlan.Required...),
		OptionalScopes: append([]string(nil), scopePlan.Optional...),
	})
	if err != nil {
		return ProviderConnectorInstall{}, ErrProviderUnavailable.With(err)
	}

	repoStored, err := s.repo.CreateOAuthInstall(ctx, tx, toRepoCredentialInstall(install))
	if err != nil {
		return ProviderConnectorInstall{}, xerror.Wrapf("create oauth install: %w", err)
	}
	stored := fromRepoCredentialInstall(repoStored)
	return ProviderConnectorInstall{
		Slug:             stored.Slug,
		Provider:         stored.Provider,
		AuthorizationURL: authURL,
		State:            rawState,
		ExpiresAt:        stored.ExpiresAt,
		Intent:           stored.Intent,
		RequestedScopes:  append([]string(nil), stored.RequestedScopes...),
	}, nil
}

func (s *service) resolveGrant(
	ctx context.Context,
	grant CredentialGrant,
) (RuntimeCredential, error) {
	if grant.Status == CredentialStatusRevoked {
		return RuntimeCredential{}, ErrProviderGrantRevoked
	}
	if grant.Status != CredentialStatusActive {
		return RuntimeCredential{}, ErrCredentialNotFound
	}

	tokenSet, err := s.decryptTokenSet(grant)
	if err != nil {
		return RuntimeCredential{}, err
	}

	now := s.now()
	refreshBefore := now
	if grant.Provider == "http" {
		refreshBefore = now.Add(30 * time.Second)
	}
	if grant.AccessTokenExpires.IsZero() || grant.AccessTokenExpires.After(refreshBefore) {
		runtime, err := grantRuntimeCredential(grant, tokenSet)
		if err != nil {
			return RuntimeCredential{}, err
		}
		runtime.Version = runtimeCredentialVersion(grant.Slug, grant.UpdatedAt)
		return runtime, nil
	}

	updated, err := s.refreshGrant(ctx, grant, now)
	if err != nil {
		return RuntimeCredential{}, err
	}
	refreshed, err := s.decryptTokenSet(updated)
	if err != nil {
		return RuntimeCredential{}, err
	}
	runtime, err := grantRuntimeCredential(updated, refreshed)
	if err != nil {
		return RuntimeCredential{}, err
	}
	runtime.Version = runtimeCredentialVersion(updated.Slug, updated.UpdatedAt)
	return runtime, nil
}

func (s *service) refreshGrantUnlocked(
	ctx context.Context,
	grant CredentialGrant,
	now time.Time,
) (CredentialGrant, error) {
	if grant.Status == CredentialStatusRevoked {
		return CredentialGrant{}, ErrProviderGrantRevoked
	}
	if grant.Status != CredentialStatusActive {
		return CredentialGrant{}, ErrCredentialNotFound
	}
	tokenSet, err := s.decryptTokenSet(grant)
	if err != nil {
		return CredentialGrant{}, err
	}
	provider, err := s.providerForGrant(grant, tokenSet)
	if err != nil {
		return CredentialGrant{}, err
	}
	refreshed, err := provider.Refresh(
		postgres.WithoutTransaction(ctx),
		RefreshParams{RefreshToken: tokenSet.RefreshToken, Scopes: append([]string(nil), grant.Scopes...)},
	)
	if err != nil {
		if xerror.Is(err, ErrProviderInvalidGrant) {
			revoked := grant
			revoked.Status = CredentialStatusRevoked
			revoked.RevokedAt = &now
			revoked.FailureCode = "invalid_grant"
			revoked.FailureMessage = "Provider grant was revoked"
			revoked.UpdatedAt = now
			finalizeCtx, cancel := credentialFinalizeContext(ctx)
			defer cancel()
			updateErr := s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
				_, updateErr := s.repo.MarkOAuthGrantRevoked(ctx, tx, toRepoCredentialGrant(revoked))
				return updateErr
			})
			if updateErr != nil {
				return CredentialGrant{}, updateErr
			}
			return CredentialGrant{}, ErrProviderGrantRevoked
		}
		return CredentialGrant{}, ErrProviderUnavailable.With(err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = tokenSet.RefreshToken
	}

	updated, err := s.grantFromTokenSet(grant.Owner, grant.Provider, refreshed, now, grant.Slug)
	if err != nil {
		return CredentialGrant{}, err
	}
	updated.Slug = grant.Slug
	updated.CreatedAt = grant.CreatedAt
	updated.GrantUpdatedAt = grant.GrantUpdatedAt
	updated.LastRefreshedAt = &now
	finalizeCtx, cancel := credentialFinalizeContext(ctx)
	defer cancel()
	var repoUpdated repocredentials.CredentialGrant
	err = s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
		var updateErr error
		repoUpdated, updateErr = s.repo.UpsertOAuthGrant(ctx, tx, toRepoCredentialGrant(updated))
		return updateErr
	})
	if err != nil {
		return CredentialGrant{}, err
	}
	return fromRepoCredentialGrant(repoUpdated), nil
}

func credentialFinalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := postgres.WithoutTransaction(context.WithoutCancel(ctx))
	return context.WithTimeout(base, 5*time.Second)
}

func (s *service) grantFromTokenSet(
	owner CredentialOwner,
	provider string,
	tokenSet OAuthTokenSet,
	now time.Time,
	grantSlug slug.Slug,
) (CredentialGrant, error) {
	if err := validateOAuthOwner(owner); err != nil {
		return CredentialGrant{}, err
	}
	payload, err := marshalOAuthTokenSet(tokenSet)
	if err != nil {
		return CredentialGrant{}, xerror.Unexpected().With(err)
	}
	slugBase, err := credentialSlugBase(owner)
	if err != nil {
		return CredentialGrant{}, err
	}
	if grantSlug == "" {
		grantSlug, err = s.nextSlug("grant", slugBase)
		if err != nil {
			return CredentialGrant{}, ErrInvalidInput.With(err)
		}
	}
	encrypted, err := s.encryptPayload(credentialScope(owner.OrgSlug, grantSlug, "oauth-token-set"), payload)
	if err != nil {
		return CredentialGrant{}, err
	}
	return CredentialGrant{
		Slug:               grantSlug,
		Owner:              owner,
		Kind:               CredentialKindOAuth2Grant,
		Provider:           provider,
		Status:             CredentialStatusActive,
		EncryptedTokenSet:  encrypted,
		AccessTokenExpires: tokenSet.ExpiresAt,
		Scopes:             append([]string(nil), tokenSet.Scopes...),
		Account:            tokenSet.Account,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

func marshalOAuthTokenSet(tokenSet OAuthTokenSet) ([]byte, error) {
	payload := map[string]any{
		"access_token":  tokenSet.AccessToken,
		"refresh_token": tokenSet.RefreshToken,
		"expires_at":    tokenSet.ExpiresAt,
		"scopes":        append([]string(nil), tokenSet.Scopes...),
		"account":       tokenSet.Account,
	}
	if tokenSet.SlackWebhookURL != "" {
		payload["slack_webhook_url"] = tokenSet.SlackWebhookURL
	}
	if tokenSet.HTTPApp != nil {
		payload["http_app"] = tokenSet.HTTPApp
	}
	return json.Marshal(payload)
}

func (s *service) provider(provider string) (ProviderAdapter, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, ErrProviderNotConfigured
	}
	adapter, ok := s.providers[provider]
	if !ok || adapter == nil {
		return nil, ErrProviderNotConfigured
	}
	return adapter, nil
}

func (s *service) encryptPayload(scope crypto.CredentialScope, plaintext []byte) ([]byte, error) {
	encoded := []byte(base64.RawStdEncoding.EncodeToString(plaintext))
	encrypted, err := s.enc.Seal(scope, encoded)
	if err != nil {
		return nil, xerror.Wrapf("encrypt credential payload: %w", err)
	}
	return encrypted, nil
}

func (s *service) decryptPayload(scope crypto.CredentialScope, ciphertext []byte) ([]byte, error) {
	decrypted, err := s.enc.Open(scope, ciphertext)
	if err != nil {
		return nil, xerror.Wrapf("decrypt credential payload: %w", err)
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(string(decrypted)); err == nil {
		return decoded, nil
	}
	raw := string(decrypted)
	if strings.HasPrefix(raw, "encrypted:") {
		return []byte(strings.TrimPrefix(raw, "encrypted:")), nil
	}
	return decrypted, nil
}

func (s *service) decryptTokenSet(grant CredentialGrant) (OAuthTokenSet, error) {
	payload, err := s.decryptPayload(credentialScope(grant.Owner.OrgSlug, grant.Slug, "oauth-token-set"), grant.EncryptedTokenSet)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	var tokenSet OAuthTokenSet
	if err := json.Unmarshal(payload, &tokenSet); err != nil {
		return OAuthTokenSet{}, xerror.Unexpected().With(err)
	}
	return tokenSet, nil
}

// accessTokenRuntime serializes an OAuth access token and granted scopes for runtime connector use.
func accessTokenRuntime(accessToken string, scopes []string) (RuntimeCredential, error) {
	payload, err := json.Marshal(map[string]any{
		"access_token":   accessToken,
		"bearer_token":   accessToken,
		"granted_scopes": append([]string(nil), scopes...),
	})
	if err != nil {
		return RuntimeCredential{}, xerror.Unexpected().With(err)
	}
	return RuntimeCredential{Kind: CredentialKindOAuth2Grant, PlaintextJSON: payload}, nil
}

func runtimeCredentialVersion(credentialSlug slug.Slug, updatedAt time.Time) string {
	return credentialSlug.String() + "/" + updatedAt.UTC().Format(time.RFC3339Nano)
}

func metadataFromRecord(record CredentialRecord) CredentialMetadata {
	return CredentialMetadata{
		Slug:      record.Slug,
		Kind:      record.Kind,
		Status:    record.Status,
		UpdatedAt: record.UpdatedAt,
	}
}

func metadataFromGrant(grant CredentialGrant) CredentialMetadata {
	return CredentialMetadata{
		Slug:      grant.Slug,
		Kind:      CredentialKindOAuth2Grant,
		Status:    grant.Status,
		UpdatedAt: grant.UpdatedAt,
		OAuth: CredentialOAuthMetadata{
			Provider:             grant.Provider,
			Scopes:               append([]string(nil), grant.Scopes...),
			Account:              grant.Account,
			AccessTokenExpiresAt: grant.AccessTokenExpires,
			LastRefreshedAt:      grant.LastRefreshedAt,
			RevokedAt:            grant.RevokedAt,
			FailureCode:          grant.FailureCode,
			FailureMessage:       grant.FailureMessage,
		},
	}
}

func (s *service) now() time.Time {
	return s.clock.Now().UTC()
}

func (s *service) nextSlug(prefix string, ownerSlug slug.Slug) (slug.Slug, error) {
	return slug.WithRandomSuffix(slug.Slug(prefix+"-"+ownerSlug.String()), 6)
}

// validateCredentialOwner enforces exactly one supported owner shape before persistence.
func validateCredentialOwner(owner CredentialOwner) error {
	if owner.OrgSlug == "" {
		return ErrInvalidInput.Detail("credential owner org slug is required")
	}
	if owner.Kind != CredentialOwnerKindNotificationConnection && owner.NotificationConnectionSlug != "" {
		return ErrInvalidInput.Detail("notification connection slug requires notification connection ownership")
	}
	switch owner.Kind {
	case CredentialOwnerKindDataSource:
		if owner.DataSourceSlug == "" {
			return ErrInvalidInput.Detail("data source credential owner requires data_source_slug")
		}
		if owner.EnvironmentSlug != "" || owner.EnvironmentSecretKey != "" {
			return ErrInvalidInput.Detail("data source credential owner cannot include environment secret fields")
		}
	case CredentialOwnerKindEnvironmentSecret:
		if owner.EnvironmentSlug == "" {
			return ErrInvalidInput.Detail("environment secret credential owner requires environment_slug")
		}
		if strings.TrimSpace(owner.EnvironmentSecretKey) == "" {
			return ErrInvalidInput.Detail("environment secret credential owner requires environment_secret_key")
		}
		if owner.DataSourceSlug != "" {
			return ErrInvalidInput.Detail("environment secret credential owner cannot include data_source_slug")
		}
	case CredentialOwnerKindNotificationConnection:
		if owner.NotificationConnectionSlug == "" || owner.DataSourceSlug != "" || owner.EnvironmentSlug != "" || owner.EnvironmentSecretKey != "" {
			return ErrInvalidInput.Detail("notification connection owner requires only organization and connection slugs")
		}
	case CredentialOwnerKindOrganizationGitLab:
		if owner.DataSourceSlug != "" || owner.EnvironmentSlug != "" || owner.EnvironmentSecretKey != "" {
			return ErrInvalidInput.Detail("organization GitLab credential owner only permits org_slug")
		}
	default:
		return ErrInvalidInput.Detailf("unsupported credential owner kind %q", owner.Kind)
	}
	return nil
}

func validateOAuthOwner(owner CredentialOwner) error {
	if err := validateCredentialOwner(owner); err != nil {
		return err
	}
	if owner.Kind != CredentialOwnerKindDataSource && owner.Kind != CredentialOwnerKindOrganizationGitLab && owner.Kind != CredentialOwnerKindNotificationConnection {
		return ErrInvalidInput.Detail("oauth credential lifecycle requires a data source, organization GitLab, or notification connection owner")
	}
	return nil
}

// credentialSlugBase derives a URL-safe base for generated credential slugs.
func credentialSlugBase(owner CredentialOwner) (slug.Slug, error) {
	if err := validateCredentialOwner(owner); err != nil {
		return "", err
	}
	switch owner.Kind {
	case CredentialOwnerKindDataSource:
		return owner.DataSourceSlug, nil
	case CredentialOwnerKindEnvironmentSecret:
		keySlug, err := slug.FromName(owner.EnvironmentSecretKey)
		if err != nil {
			return "", ErrInvalidInput.With(err)
		}
		return slug.New(owner.EnvironmentSlug.String() + "-" + keySlug.String())
	case CredentialOwnerKindNotificationConnection:
		return owner.NotificationConnectionSlug, nil
	case CredentialOwnerKindOrganizationGitLab:
		return owner.OrgSlug, nil
	default:
		return "", ErrInvalidInput.Detailf("unsupported credential owner kind %q", owner.Kind)
	}
}

func toRepoResolveParams(params ResolveRuntimeCredentialParams) repocredentials.ResolveRuntimeCredentialParams {
	return repocredentials.ResolveRuntimeCredentialParams{
		OwnerKind:                  params.Owner.Kind.String(),
		OrgSlug:                    params.Owner.OrgSlug,
		DataSourceSlug:             params.Owner.DataSourceSlug,
		NotificationConnectionSlug: params.Owner.NotificationConnectionSlug,
		EnvironmentSlug:            params.Owner.EnvironmentSlug,
		EnvironmentSecretKey:       params.Owner.EnvironmentSecretKey,
	}
}

func toRepoCredentialRecord(record CredentialRecord) repocredentials.CredentialRecord {
	return repocredentials.CredentialRecord{
		Slug:                       record.Slug,
		OrgSlug:                    record.Owner.OrgSlug,
		OwnerKind:                  record.Owner.Kind.String(),
		DataSourceSlug:             record.Owner.DataSourceSlug,
		NotificationConnectionSlug: record.Owner.NotificationConnectionSlug,
		EnvironmentSlug:            record.Owner.EnvironmentSlug,
		EnvironmentSecretKey:       record.Owner.EnvironmentSecretKey,
		Kind:                       record.Kind.String(),
		CredentialType:             record.CredentialType.String(),
		Status:                     record.Status.String(),
		EncryptedPayload:           append([]byte(nil), record.EncryptedPayload...),
		CreatedAt:                  record.CreatedAt,
		UpdatedAt:                  record.UpdatedAt,
		RotatedAt:                  record.RotatedAt,
		RevokedAt:                  record.RevokedAt,
		FailureCode:                record.FailureCode,
		FailureMessage:             record.FailureMessage,
	}
}

func fromRepoCredentialRecord(record repocredentials.CredentialRecord) CredentialRecord {
	return CredentialRecord{
		Slug: record.Slug,
		Owner: CredentialOwner{
			Kind:                       mustCredentialOwnerKind(record.OwnerKind),
			OrgSlug:                    record.OrgSlug,
			DataSourceSlug:             record.DataSourceSlug,
			NotificationConnectionSlug: record.NotificationConnectionSlug,
			EnvironmentSlug:            record.EnvironmentSlug,
			EnvironmentSecretKey:       record.EnvironmentSecretKey,
		},
		Kind:             mustCredentialKind(record.Kind),
		CredentialType:   mustStaticCredentialType(record.CredentialType),
		Status:           mustCredentialStatus(record.Status),
		EncryptedPayload: append([]byte(nil), record.EncryptedPayload...),
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
		RotatedAt:        record.RotatedAt,
		RevokedAt:        record.RevokedAt,
		FailureCode:      record.FailureCode,
		FailureMessage:   record.FailureMessage,
	}
}

func toRepoCredentialInstall(install CredentialInstall) repocredentials.CredentialInstall {
	return repocredentials.CredentialInstall{
		RepositorySetupSlug:        install.RepositorySetupSlug,
		RepositorySetupProjectSlug: install.RepositorySetupProjectSlug,
		Slug:                       install.Slug,
		OrgSlug:                    install.Owner.OrgSlug,
		OwnerKind:                  install.Owner.Kind.String(),
		DataSourceSlug:             install.Owner.DataSourceSlug,
		NotificationConnectionSlug: install.Owner.NotificationConnectionSlug,
		Provider:                   install.Provider,
		StateHash:                  install.StateHash,
		EncryptedPKCEVerifier:      append([]byte(nil), install.EncryptedPKCEVerifier...),
		Intent:                     install.Intent.String(),
		ExpectedCredentialSlug:     install.ExpectedCredentialSlug,
		ExpectedCredentialStatus:   install.ExpectedCredentialStatus.String(),
		ActorType:                  install.Actor.Type.String(),
		ActorSlug:                  install.Actor.Slug,
		RequestedScopes:            append([]string(nil), install.RequestedScopes...),
		ExpectedSourceRevision:     install.ExpectedSourceRevision,
		ExpiresAt:                  install.ExpiresAt,
		ConsumedAt:                 install.ConsumedAt,
		CreatedAt:                  install.CreatedAt,
		UpdatedAt:                  install.UpdatedAt,
	}
}

func fromRepoCredentialInstall(install repocredentials.CredentialInstall) CredentialInstall {
	return CredentialInstall{
		RepositorySetupSlug:        install.RepositorySetupSlug,
		RepositorySetupProjectSlug: install.RepositorySetupProjectSlug,
		Slug:                       install.Slug,
		Owner: CredentialOwner{
			Kind:                       mustCredentialOwnerKind(install.OwnerKind),
			OrgSlug:                    install.OrgSlug,
			DataSourceSlug:             install.DataSourceSlug,
			NotificationConnectionSlug: install.NotificationConnectionSlug,
		},
		Provider:                 install.Provider,
		StateHash:                install.StateHash,
		EncryptedPKCEVerifier:    append([]byte(nil), install.EncryptedPKCEVerifier...),
		Intent:                   mustOAuthInstallIntent(install.Intent),
		ExpectedCredentialSlug:   install.ExpectedCredentialSlug,
		ExpectedCredentialStatus: mustCredentialStatus(install.ExpectedCredentialStatus),
		Actor:                    repoEventActor(install.ActorType, install.ActorSlug),
		RequestedScopes:          append([]string(nil), install.RequestedScopes...),
		ExpectedSourceRevision:   install.ExpectedSourceRevision,
		ExpiresAt:                install.ExpiresAt,
		ConsumedAt:               install.ConsumedAt,
		CreatedAt:                install.CreatedAt,
		UpdatedAt:                install.UpdatedAt,
	}
}

func toRepoCredentialGrant(grant CredentialGrant) repocredentials.CredentialGrant {
	account, err := json.Marshal(grant.Account)
	if err != nil {
		account = []byte("{}")
	}
	return repocredentials.CredentialGrant{
		Slug:                       grant.Slug,
		OrgSlug:                    grant.Owner.OrgSlug,
		OwnerKind:                  grant.Owner.Kind.String(),
		DataSourceSlug:             grant.Owner.DataSourceSlug,
		NotificationConnectionSlug: grant.Owner.NotificationConnectionSlug,
		Kind:                       grant.Kind.String(),
		Provider:                   grant.Provider,
		Status:                     grant.Status.String(),
		EncryptedTokenSet:          append([]byte(nil), grant.EncryptedTokenSet...),
		AccessTokenExpires:         grant.AccessTokenExpires,
		Scopes:                     append([]string(nil), grant.Scopes...),
		Account:                    account,
		LastRefreshedAt:            grant.LastRefreshedAt,
		RevokedAt:                  grant.RevokedAt,
		RotatedAt:                  grant.RotatedAt,
		FailureCode:                grant.FailureCode,
		FailureMessage:             grant.FailureMessage,
		CreatedAt:                  grant.CreatedAt,
		UpdatedAt:                  grant.UpdatedAt,
		GrantUpdatedAt:             grant.GrantUpdatedAt,
	}
}

func fromRepoCredentialGrant(grant repocredentials.CredentialGrant) CredentialGrant {
	var account ProviderAccount
	if len(grant.Account) > 0 {
		_ = json.Unmarshal(grant.Account, &account)
	}
	return CredentialGrant{
		Slug: grant.Slug,
		Owner: CredentialOwner{
			Kind:                       mustCredentialOwnerKind(grant.OwnerKind),
			OrgSlug:                    grant.OrgSlug,
			DataSourceSlug:             grant.DataSourceSlug,
			NotificationConnectionSlug: grant.NotificationConnectionSlug,
		},
		Kind:               mustCredentialKind(grant.Kind),
		Provider:           grant.Provider,
		Status:             mustCredentialStatus(grant.Status),
		EncryptedTokenSet:  append([]byte(nil), grant.EncryptedTokenSet...),
		AccessTokenExpires: grant.AccessTokenExpires,
		Scopes:             append([]string(nil), grant.Scopes...),
		Account:            account,
		LastRefreshedAt:    grant.LastRefreshedAt,
		RevokedAt:          grant.RevokedAt,
		RotatedAt:          grant.RotatedAt,
		FailureCode:        grant.FailureCode,
		FailureMessage:     grant.FailureMessage,
		CreatedAt:          grant.CreatedAt,
		UpdatedAt:          grant.UpdatedAt,
		GrantUpdatedAt:     grant.GrantUpdatedAt,
	}
}

func mustCredentialKind(value string) CredentialKind {
	parsed, err := CredentialKindString(value)
	if err != nil {
		return CredentialKindStaticSecret
	}
	return parsed
}

func mustCredentialOwnerKind(value string) CredentialOwnerKind {
	parsed, err := CredentialOwnerKindString(value)
	if err != nil {
		return CredentialOwnerKindDataSource
	}
	return parsed
}

func mustCredentialStatus(value string) CredentialStatus {
	parsed, err := CredentialStatusString(value)
	if err != nil {
		return CredentialStatusPending
	}
	return parsed
}

func mustStaticCredentialType(value string) StaticCredentialType {
	parsed, err := StaticCredentialTypeString(value)
	if err != nil {
		return StaticCredentialTypePostgres
	}
	return parsed
}

func mustOAuthInstallIntent(value string) OAuthInstallIntent {
	parsed, err := OAuthInstallIntentString(value)
	if err != nil {
		return OAuthInstallIntentCreate
	}
	return parsed
}

func repoEventActor(actorType string, actorSlug *slug.Slug) model.EventActor {
	parsed, err := model.EventActorTypeString(actorType)
	if err != nil || parsed == model.EventActorTypeUnknown {
		return model.SystemEventActor()
	}
	return model.EventActor{Type: parsed, Slug: actorSlug}
}

func normalizeEventActor(actor model.EventActor) model.EventActor {
	if actor.Type == model.EventActorTypeUnknown {
		return model.SystemEventActor()
	}
	return actor
}
