package githubappsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const (
	installTTL                = 10 * time.Minute
	bindingTransactionTimeout = 30 * time.Second
)

var ErrInvalidInstall = xerror.New(xerror.KindInvalidInput, "GITHUB_APP_INSTALL_INVALID", "GitHub App installation could not be verified")

var githubAccountLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

type Provider interface {
	AuthorizationURL(string) (string, error)
	VerifyInstallation(context.Context, string, int64) (githubapps.VerifiedInstallation, error)
	VerifyAuthorizedInstallation(context.Context, string, string, int64) (githubapps.VerifiedInstallation, error)
	VerifyRepositoryInstallation(context.Context, string, int64, gitprovider.RepositoryBootstrapRequest) (githubapps.VerifiedInstallation, *gitprovider.RepositoryBootstrapResult, error)
	InstallationToken(context.Context, int64, []int64) (InstallationToken, error)
}

// InstallationToken is the provider-issued authority for one bound installation token.
type InstallationToken struct {
	Token                  string
	ExpiresAt              time.Time
	ContentsPermission     string
	PullRequestsPermission string
	InstallationID         int64
	AccountID              int64
	PrincipalID            int64
	PrincipalLogin         string
}

type Connection struct {
	Organization        slug.Slug
	InstallationID      int64
	AccountID           int64
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Repositories        []githubapps.RepositoryBinding
	SuspendedAt         *time.Time
	DeletedAt           *time.Time
}

type Service struct {
	txm        postgres.TransactionManager
	repo       githubapps.Repository
	provider   Provider
	events     workerproducer.EventProducer
	installURL string
	now        func() time.Time
}

func New(txm postgres.TransactionManager, repo githubapps.Repository, provider Provider, events workerproducer.EventProducer, installURL string) (*Service, error) {
	parsed, err := url.Parse(installURL)
	if txm == nil || repo == nil || provider == nil || events == nil || err != nil || (installURL != "" && (parsed.Scheme != "https" || parsed.Hostname() != "github.com")) {
		return nil, xerror.Unexpected().Detail("GitHub App connection dependencies are invalid")
	}
	return &Service{txm: txm, repo: repo, provider: provider, events: events, installURL: installURL, now: time.Now}, nil
}

func (s *Service) Start(ctx context.Context, org slug.Slug, actor model.EventActor) (string, error) {
	return s.start(ctx, org, actor, "", "")
}

func (s *Service) StartRepositorySetup(ctx context.Context, org slug.Slug, actor model.EventActor, setup slug.Slug) (string, error) {
	if setup == "" {
		return "", ErrInvalidInstall
	}
	return s.start(ctx, org, actor, setup, "")
}

func (s *Service) StartExistingAuthorization(ctx context.Context, org slug.Slug, actor model.EventActor, accountLogin string) (string, error) {
	accountLogin = strings.TrimSpace(accountLogin)
	if !githubAccountLogin.MatchString(accountLogin) {
		return "", ErrInvalidInstall.Detail("GitHub account login is invalid")
	}
	return s.start(ctx, org, actor, "", strings.ToLower(accountLogin))
}

func (s *Service) start(ctx context.Context, org slug.Slug, actor model.EventActor, setup slug.Slug, requestedAccountLogin string) (string, error) {
	if s.installURL == "" {
		return "", xerror.Unavailable(xerror.Organization).Detail("GitHub App is not configured")
	}
	actorSlug := actor.UserSlugValue()
	if actorSlug == "" {
		return "", ErrInvalidInstall.Detail("GitHub App installation requires a user actor")
	}
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", xerror.Unexpected().With(err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	authorizationURL := ""
	if requestedAccountLogin != "" {
		var err error
		authorizationURL, err = s.provider.AuthorizationURL(state)
		if err != nil {
			return "", err
		}
	}
	hash := sha256.Sum256([]byte(state))
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.CreateIntent(ctx, tx, org, actorSlug.String(), hex.EncodeToString(hash[:]), s.now().Add(installTTL), setup, requestedAccountLogin)
	})
	if err != nil {
		return "", err
	}
	if authorizationURL != "" {
		return authorizationURL, nil
	}
	install, _ := url.Parse(s.installURL)
	query := install.Query()
	query.Set("state", state)
	install.RawQuery = query.Encode()
	return install.String(), nil
}

func (s *Service) Complete(ctx context.Context, actor model.EventActor, state, code string, installationID int64) (*Connection, error) {
	intent, err := s.installationIntent(ctx, actor, state, code, installationID)
	if err != nil {
		return nil, err
	}
	if intent.RepositorySetupSlug != "" {
		return nil, ErrInvalidInstall.Detail("Resume repository setup to complete this authorization.")
	}
	if intent.RequestedAccountLogin != "" {
		verified, err := s.provider.VerifyAuthorizedInstallation(ctx, code, intent.RequestedAccountLogin, installationID)
		if err != nil {
			return nil, ErrInvalidInstall.With(err)
		}
		return s.bindInstallation(ctx, actor, state, intent, verified.InstallationID, verified)
	}
	if installationID <= 0 {
		return nil, ErrInvalidInstall
	}
	verified, err := s.provider.VerifyInstallation(ctx, code, installationID)
	if err != nil {
		return nil, ErrInvalidInstall.With(err)
	}
	return s.bindInstallation(ctx, actor, state, intent, installationID, verified)
}

func (s *Service) CompleteRepositorySetup(ctx context.Context, org, setup slug.Slug, actor model.EventActor, state, code string, installationID int64, request gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error) {
	intent, err := s.installationIntent(ctx, actor, state, code, installationID)
	if err != nil {
		return nil, err
	}
	if setup == "" || intent.Organization != org || intent.RepositorySetupSlug != setup || intent.RequestedAccountLogin != "" || request.Marker != setup.String() || installationID <= 0 {
		return nil, ErrInvalidInstall
	}
	current, err := s.Get(ctx, org)
	if err != nil {
		return nil, err
	}
	if current != nil && current.InstallationID != installationID {
		return nil, ErrInvalidInstall.Detail("This organization is connected to another GitHub installation. Use its owner or deliberately reconnect GitHub before creating this repository.")
	}
	verified, result, err := s.provider.VerifyRepositoryInstallation(ctx, code, installationID, request)
	if err != nil {
		return incompleteRepositoryResult(result), err
	}
	if _, err := s.bindInstallation(ctx, actor, state, intent, installationID, verified); err != nil {
		return incompleteRepositoryResult(result), err
	}
	return result, nil
}

func (s *Service) installationIntent(ctx context.Context, actor model.EventActor, state, code string, installationID int64) (githubapps.Intent, error) {
	if actor.UserSlugValue() == "" || state == "" || code == "" || installationID < 0 {
		return githubapps.Intent{}, ErrInvalidInstall
	}
	hash := sha256.Sum256([]byte(state))
	intent, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (githubapps.Intent, error) {
		return s.repo.GetIntent(ctx, db, hex.EncodeToString(hash[:]))
	})
	if err != nil {
		if xerror.Is(err, githubapps.ErrNotFound) {
			return githubapps.Intent{}, ErrInvalidInstall
		}
		return githubapps.Intent{}, err
	}
	if err := s.validateIntent(intent, actor); err != nil {
		return githubapps.Intent{}, err
	}
	return intent, nil
}

func (s *Service) bindInstallation(ctx context.Context, actor model.EventActor, state string, intent githubapps.Intent, installationID int64, verified githubapps.VerifiedInstallation) (*Connection, error) {
	hash := sha256.Sum256([]byte(state))
	stateHash := hex.EncodeToString(hash[:])
	if verified.InstallationID != installationID || verified.AccountID <= 0 ||
		strings.TrimSpace(verified.AccountLogin) == "" || strings.TrimSpace(verified.AccountType) == "" ||
		(verified.RepositorySelection != "all" && verified.RepositorySelection != "selected") {
		return nil, ErrInvalidInstall
	}
	// The binding must finish atomically after provider verification even if the client disconnects.
	txCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bindingTransactionTimeout)
	defer cancel()
	err := s.txm.RunInTx(txCtx, func(ctx context.Context, tx postgres.DBTX) error {
		locked, err := s.repo.LockIntent(ctx, tx, stateHash)
		if err != nil {
			if xerror.Is(err, githubapps.ErrNotFound) {
				return ErrInvalidInstall
			}
			return err
		}
		if locked.Organization != intent.Organization || locked.RepositorySetupSlug != intent.RepositorySetupSlug || locked.RequestedAccountLogin != intent.RequestedAccountLogin {
			return ErrInvalidInstall
		}
		if err := s.validateIntent(locked, actor); err != nil {
			return err
		}
		internalInstallationID, err := s.repo.UpsertInstallation(ctx, tx, locked, verified)
		if err != nil {
			if postgres.IsUniqueViolation(err, "github_app_installations_installation_id_key") {
				return ErrInvalidInstall
			}
			return err
		}
		repositoryIDs := make([]int64, 0, len(verified.Repositories))
		for _, repository := range verified.Repositories {
			repositoryIDs = append(repositoryIDs, repository.ID)
			if err := s.repo.UpsertRepository(ctx, tx, locked.OrganizationID, internalInstallationID, repository); err != nil {
				return err
			}
		}
		// User authorization can return only a subset of the installation's repositories.
		if locked.RepositorySetupSlug == "" && locked.RequestedAccountLogin == "" {
			if err := s.repo.RemoveMissingRepositories(ctx, tx, locked.OrganizationID, internalInstallationID, repositoryIDs); err != nil {
				return err
			}
		}
		if err := s.repo.ConsumeIntent(ctx, tx, locked.ID); err != nil {
			return err
		}
		_, err = s.events.EnqueueRequired(ctx, tx, locked.Organization, actor, githubAppConnectionChangedEvent{organization: locked.Organization})
		return err
	})
	if err != nil {
		return nil, err
	}
	return connection(intent.Organization, verified, nil, nil), nil
}

func (s *Service) OrganizationForState(ctx context.Context, actor model.EventActor, state string) (slug.Slug, error) {
	org, _, err := s.RepositorySetupForState(ctx, actor, state)
	return org, err
}

func (s *Service) RepositorySetupForState(ctx context.Context, actor model.EventActor, state string) (slug.Slug, slug.Slug, error) {
	if actor.UserSlugValue() == "" || state == "" {
		return "", "", ErrInvalidInstall
	}
	hash := sha256.Sum256([]byte(state))
	intent, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (githubapps.Intent, error) {
		return s.repo.GetIntent(ctx, tx, hex.EncodeToString(hash[:]))
	})
	if err != nil {
		if xerror.Is(err, githubapps.ErrNotFound) {
			return "", "", ErrInvalidInstall
		}
		return "", "", err
	}
	if err := s.validateIntent(intent, actor); err != nil {
		return "", "", err
	}
	return intent.Organization, intent.RepositorySetupSlug, nil
}

func (s *Service) ConsumeRejectedInstall(ctx context.Context, org, setup slug.Slug, actor model.EventActor, state string) error {
	if actor.UserSlugValue() == "" || state == "" {
		return ErrInvalidInstall
	}
	hash := sha256.Sum256([]byte(state))
	txCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bindingTransactionTimeout)
	defer cancel()
	return s.txm.RunInTx(txCtx, func(ctx context.Context, tx postgres.DBTX) error {
		intent, err := s.repo.LockIntent(ctx, tx, hex.EncodeToString(hash[:]))
		if err != nil {
			if xerror.Is(err, githubapps.ErrNotFound) {
				return ErrInvalidInstall
			}
			return err
		}
		if intent.Organization != org || intent.RepositorySetupSlug != setup {
			return ErrInvalidInstall
		}
		if err := s.validateIntent(intent, actor); err != nil {
			return err
		}
		return s.repo.ConsumeIntent(ctx, tx, intent.ID)
	})
}

func (s *Service) Get(ctx context.Context, org slug.Slug) (*Connection, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*Connection, error) {
		installation, err := s.repo.GetInstallation(ctx, db, org)
		if err != nil || installation == nil {
			return nil, err
		}
		repos, err := s.repo.ListRepositories(ctx, db, org)
		if err != nil {
			return nil, err
		}
		return &Connection{
			Organization:   org,
			InstallationID: installation.InstallationID, AccountID: installation.AccountID,
			AccountLogin: installation.AccountLogin, AccountType: installation.AccountType,
			RepositorySelection: installation.RepositorySelection, Repositories: repos,
			SuspendedAt: installation.SuspendedAt, DeletedAt: installation.DeletedAt,
		}, nil
	})
}

func (s *Service) Disconnect(ctx context.Context, org slug.Slug, actor model.EventActor) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.Disconnect(ctx, tx, org); err != nil {
			return err
		}
		_, err := s.events.EnqueueRequired(ctx, tx, org, actor, githubAppConnectionChangedEvent{organization: org})
		return err
	})
}

func (s *Service) Resolve(ctx context.Context, installationID, repositoryID int64) (slug.Slug, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (slug.Slug, error) {
		return s.repo.Resolve(ctx, db, installationID, repositoryID)
	})
}

func (s *Service) ApplyLifecycle(ctx context.Context, event githubprovider.InstallationLifecycleEvent) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		org, err := s.repo.OrganizationForInstallation(ctx, tx, event.InstallationID)
		if xerror.Is(err, githubapps.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		changed := false
		if event.Action == "suspend" || event.Action == "unsuspend" || event.Action == "deleted" {
			if err := s.repo.UpdateLifecycle(ctx, tx, event.Action, event.InstallationID); err != nil {
				if xerror.Is(err, githubapps.ErrNotFound) {
					return nil
				}
				return err
			}
			changed = true
		} else {
			for _, repository := range event.Added {
				binding := githubapps.RepositoryBinding{ID: repository.ID, FullName: repository.FullName, OwnerLogin: repository.OwnerLogin, Name: repository.Name}
				if err := s.repo.AddRepository(ctx, tx, event.InstallationID, binding); err != nil {
					if xerror.Is(err, githubapps.ErrNotFound) {
						continue
					}
					return err
				}
				changed = true
			}
			for _, repositoryID := range event.Removed {
				if err := s.repo.RemoveRepository(ctx, tx, event.InstallationID, repositoryID); err != nil {
					if xerror.Is(err, githubapps.ErrNotFound) {
						continue
					}
					return err
				}
				changed = true
			}
		}
		if !changed {
			return nil
		}
		_, err = s.events.EnqueueRequired(ctx, tx, org, model.SystemEventActor(), githubAppConnectionChangedEvent{organization: org})
		return err
	})
}

func (s *Service) Token(ctx context.Context, org slug.Slug, repositoryIDs []int64) (InstallationToken, error) {
	connection, err := s.Get(ctx, org)
	if err != nil {
		return InstallationToken{}, err
	}
	if connection == nil || connection.DeletedAt != nil || connection.SuspendedAt != nil {
		return InstallationToken{}, ErrInvalidInstall
	}
	if len(repositoryIDs) == 0 {
		return InstallationToken{}, ErrInvalidInstall.Detail("at least one bound repository is required")
	}
	bound := make(map[int64]struct{}, len(connection.Repositories))
	for _, repository := range connection.Repositories {
		bound[repository.ID] = struct{}{}
	}
	for _, repositoryID := range repositoryIDs {
		if _, ok := bound[repositoryID]; !ok {
			return InstallationToken{}, ErrInvalidInstall.Detail("repository is not bound to the organization")
		}
	}
	token, err := s.provider.InstallationToken(ctx, connection.InstallationID, repositoryIDs)
	if err != nil {
		return InstallationToken{}, err
	}
	token.InstallationID = connection.InstallationID
	token.AccountID = connection.AccountID
	return token, nil
}

func (s *Service) validateIntent(intent githubapps.Intent, actor model.EventActor) error {
	if intent.Consumed || !s.now().Before(intent.ExpiresAt) || intent.Actor != actor.UserSlugValue().String() {
		return ErrInvalidInstall
	}
	return nil
}

func connection(org slug.Slug, v githubapps.VerifiedInstallation, suspended, deleted *time.Time) *Connection {
	return &Connection{
		Organization:   org,
		InstallationID: v.InstallationID, AccountID: v.AccountID, AccountLogin: v.AccountLogin,
		AccountType: v.AccountType, RepositorySelection: v.RepositorySelection, Repositories: v.Repositories,
		SuspendedAt: suspended, DeletedAt: deleted,
	}
}

type githubAppConnectionChangedEvent struct{ organization slug.Slug }

func (e githubAppConnectionChangedEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{SlugPrefix: "evt-github-app-connection-update", EventClass: model.EventClassSecurityAudit, SchemaVersion: model.CurrentEventSchemaVersion, EntityType: model.EventEntityTypeOrganization, EntitySlug: e.organization, Action: model.EventActionUpdate}, nil
}
