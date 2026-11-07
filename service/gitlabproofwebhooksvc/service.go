package gitlabproofwebhooksvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/gitlabproofwebhooks"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
)

const routePrefix = "/api/webhooks/gitlab/merge-requests/"

type ResolvedWebhook struct {
	SigningToken string
	Organization slug.Slug
	Authority    gitlabprovider.WebhookAuthority
}

type Service struct {
	txm            postgres.TransactionManager
	repo           gitlabproofwebhooks.Repository
	cipher         crypto.CredentialCipher
	baseURL        *url.URL
	providerConfig gitprovider.Config
}

type secretPayload struct {
	RouteKey     string `json:"route_key"`
	SigningToken string `json:"signing_token"`
}

func New(txm postgres.TransactionManager, repo gitlabproofwebhooks.Repository, cipher crypto.CredentialCipher, publicURL string, providerConfig gitprovider.Config) (*Service, error) {
	baseURL, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if txm == nil || repo == nil || cipher == nil || err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, xerror.Unexpected().With(err).Detail("GitLab Proof webhook dependencies are invalid")
	}
	apiURL := strings.TrimRight(providerConfig.BaseURL, "/")
	if apiURL != "" {
		apiURL += "/api/v4"
	}
	providerConfig.BaseURL = apiURL
	return &Service{txm: txm, repo: repo, cipher: cipher, baseURL: baseURL, providerConfig: providerConfig}, nil
}

func (s *Service) Ensure(ctx context.Context, org, dataSource slug.Slug, configJSON, credentialJSON []byte) error {
	var config gitconnector.Config
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return xerror.InvalidInput(xerror.DataSource).With(err)
	}
	var tokens credentialssvc.OAuthTokenSet
	if err := json.Unmarshal(credentialJSON, &tokens); err != nil {
		return xerror.InvalidCredentials().With(err)
	}
	accessToken := strings.TrimSpace(tokens.AccessToken)
	newClient := gitlabprovider.NewOAuth
	if accessToken == "" {
		var credentials gitconnector.Credentials
		if err := json.Unmarshal(credentialJSON, &credentials); err != nil || credentials.Username != "oauth2" {
			return xerror.InvalidCredentials().Detail("GitLab OAuth access token is required")
		}
		accessToken = strings.TrimSpace(credentials.Token)
		if accessToken == "" {
			return xerror.InvalidCredentials().Detail("GitLab OAuth access token is required")
		}
		newClient = gitlabprovider.New
	}
	repository, err := gitprovider.RepositoryRefFromRemoteURL("gitlab", config.RepoURL)
	if err != nil {
		return err
	}
	client, err := newClient(ctx, s.providerConfig, gitprovider.Credentials{Token: accessToken}, repository)
	if err != nil {
		return err
	}
	binding, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (gitlabproofwebhooks.Binding, error) {
		return s.repo.GetByDataSource(ctx, db, org, dataSource)
	})
	if err != nil && !xerror.Is(err, gitlabproofwebhooks.ErrNotFound) {
		return err
	}
	projectID, err := client.ProjectID(ctx)
	if err != nil {
		return err
	}
	if binding.Status == "active" && binding.ProjectID == projectID {
		return nil
	}
	// Hook IDs belong to a project; replacing the repository must create a new hook.
	if binding.ProjectID != projectID {
		binding.HookID = 0
	}
	secrets, err := s.newSecrets()
	if err != nil {
		return err
	}
	callback := *s.baseURL
	callback.Path = strings.TrimRight(callback.Path, "/") + routePrefix + secrets.RouteKey
	encrypted, err := s.encrypt(org, dataSource, secrets)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(secrets.RouteKey))
	pending := gitlabproofwebhooks.Binding{
		Organization: org, DataSource: dataSource, ProjectID: projectID, HookID: binding.HookID,
		RouteKeyHash: hex.EncodeToString(hash[:]), EncryptedSigningToken: encrypted,
	}
	if err := s.claim(ctx, pending); err != nil {
		return err
	}
	hook, err := client.EnsureProofWebhook(ctx, projectID, callback.String(), secrets.SigningToken, binding.HookID)
	if err != nil {
		if failErr := s.fail(ctx, pending); failErr != nil {
			return xerror.Join(err, xerror.Wrapf("retain failed GitLab Proof webhook setup: %w", failErr))
		}
		return err
	}
	pending.HookID = hook.HookID
	if err := s.activate(ctx, pending); err != nil {
		if failErr := s.fail(ctx, pending); failErr != nil {
			return xerror.Join(err, xerror.Wrapf("retain failed GitLab Proof webhook activation: %w", failErr))
		}
		return err
	}
	return nil
}

func (s *Service) claim(ctx context.Context, binding gitlabproofwebhooks.Binding) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.ClaimSetup(ctx, tx, binding)
	})
}

func (s *Service) activate(ctx context.Context, binding gitlabproofwebhooks.Binding) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.Activate(ctx, tx, binding)
	})
}

func (s *Service) fail(ctx context.Context, binding gitlabproofwebhooks.Binding) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.FailSetup(ctx, tx, binding)
	})
}

func (s *Service) Resolve(ctx context.Context, routeKey string) (ResolvedWebhook, error) {
	routeKey = strings.TrimSpace(routeKey)
	hash := sha256.Sum256([]byte(routeKey))
	binding, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (gitlabproofwebhooks.Binding, error) {
		return s.repo.GetByRoute(ctx, db, hex.EncodeToString(hash[:]))
	})
	if err != nil {
		if xerror.Is(err, gitlabproofwebhooks.ErrNotFound) {
			return ResolvedWebhook{}, gitprovider.ErrChangeAuthentication.With(err)
		}
		return ResolvedWebhook{}, err
	}
	plaintext, err := s.cipher.Open(bindingScope(binding.Organization, binding.DataSource), binding.EncryptedSigningToken)
	if err != nil {
		return ResolvedWebhook{}, xerror.Wrapf("decrypt GitLab Proof webhook binding: %w", err)
	}
	var secrets secretPayload
	if err := json.Unmarshal(plaintext, &secrets); err != nil || secrets.RouteKey != routeKey || secrets.SigningToken == "" {
		return ResolvedWebhook{}, gitprovider.ErrChangeAuthentication.Detail("GitLab Proof webhook binding is invalid")
	}
	return ResolvedWebhook{SigningToken: secrets.SigningToken, Organization: binding.Organization, Authority: gitlabprovider.WebhookAuthority{
		ProviderConnectionID: binding.DataSource.String(),
		RepositoryID:         strconv.FormatInt(binding.ProjectID, 10),
	}}, nil
}

func (s *Service) newSecrets() (secretPayload, error) {
	route := make([]byte, 24)
	key := make([]byte, 32)
	if _, err := rand.Read(route); err != nil {
		return secretPayload{}, xerror.Unexpected().With(err)
	}
	if _, err := rand.Read(key); err != nil {
		return secretPayload{}, xerror.Unexpected().With(err)
	}
	return secretPayload{RouteKey: base64.RawURLEncoding.EncodeToString(route), SigningToken: "whsec_" + base64.StdEncoding.EncodeToString(key)}, nil
}

func (s *Service) encrypt(org, source slug.Slug, payload secretPayload) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	encrypted, err := s.cipher.Seal(bindingScope(org, source), encoded)
	if err != nil {
		return nil, xerror.Wrapf("encrypt GitLab Proof webhook binding: %w", err)
	}
	return encrypted, nil
}

func bindingScope(org, source slug.Slug) crypto.CredentialScope {
	return crypto.CredentialScope{Tenant: org.String(), Resource: source.String(), Purpose: "gitlab-webhook-secret", SchemaVersion: 1}
}
