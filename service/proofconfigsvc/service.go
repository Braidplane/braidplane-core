package proofconfigsvc

import (
	"context"
	"path"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofconfig"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var (
	ErrConfiguration  = xerror.New(xerror.KindInvalidInput, "PROOF_CONFIGURATION_INVALID", "Proof configuration is invalid")
	ErrSourceNotFound = proofconfig.ErrSourceNotFound
)

type Configuration struct {
	Source   proofconfig.Source
	Targets  []proofconfig.Target
	Contexts []proofconfig.TargetContext
	Status   string
}

type Target struct {
	Project, Environment slug.Slug
	Ref, Path            string
}

type Settings struct {
	Enabled, PublishChecks bool
	ExpectedRepositoryID   string
	Targets                []Target
}

type DataSources interface {
	ListProviderRepositories(context.Context, slug.Slug, slug.Slug) ([]datasourcessvc.ProviderRepository, error)
	EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error
}

type Service struct {
	txm     postgres.TransactionManager
	repo    *proofconfig.Repository
	sources DataSources
	events  workerproducer.EventProducer
}

func New(txm postgres.TransactionManager, repo *proofconfig.Repository, sources DataSources, events workerproducer.EventProducer) (*Service, error) {
	if txm == nil || repo == nil || sources == nil || events == nil {
		return nil, xerror.Unexpected().Detail("Proof configuration dependencies are required")
	}
	return &Service{txm: txm, repo: repo, sources: sources, events: events}, nil
}

func (s *Service) List(ctx context.Context, org slug.Slug) ([]Configuration, error) {
	return s.list(ctx, org, "")
}

func (s *Service) Get(ctx context.Context, org, ds slug.Slug) (*Configuration, error) {
	configurations, err := s.list(ctx, org, ds)
	if err != nil {
		return nil, err
	}
	if len(configurations) != 1 {
		return nil, ErrSourceNotFound
	}
	return &configurations[0], nil
}

func (s *Service) list(ctx context.Context, org, ds slug.Slug) ([]Configuration, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]Configuration, error) {
		sources, err := s.repo.List(ctx, db, org, ds)
		if err != nil {
			return nil, xerror.Wrapf("list Proof sources: %w", err)
		}
		configurations := make([]Configuration, 0, len(sources))
		for _, source := range sources {
			targets, err := s.repo.Targets(ctx, db, org, source.Slug)
			if err != nil {
				return nil, xerror.Wrapf("list Proof targets: %w", err)
			}
			contexts, err := s.repo.Contexts(ctx, db, org, source.Slug)
			if err != nil {
				return nil, xerror.Wrapf("list Proof target contexts: %w", err)
			}
			status := eligibility(source)
			if status == "" {
				switch {
				case len(contexts) == 0:
					status = "Configure a project's Git repository and environment first."
				case !source.MappingsCurrent:
					source.Enabled, source.PublishChecks = false, false
					status = "Provider connection changed. Enable Proof again to confirm the current connection."
				case !source.Enabled:
					status = "Proof is disabled. Repository operations remain available."
				case len(targets) == 0:
					status = "Select an assessment target and pipeline paths."
				case validateTargets(targets, contexts) != nil:
					status = "Assessment targets no longer match the project configuration. Select current targets and save again."
				case source.Provider == "gitlab" && !source.WebhookReady:
					status = "GitLab webhook setup is incomplete. Check that the organization connection has API permission, then retry setup."
				default:
					status = "Proof is enabled. Live connector evidence requires separate environment authorization."
				}
			}
			configurations = append(configurations, Configuration{Source: source, Targets: targets, Contexts: contexts, Status: status})
		}
		return configurations, nil
	})
}

func eligibility(source proofconfig.Source) string {
	switch {
	case source.Provider == "git":
		return "Generic Git supports repository operations without automatic provider Proof."
	case !source.Managed:
		return "Connect a managed provider account and select a repository before enabling Proof."
	case !source.CredentialActive:
		return "Reconnect the provider connection before enabling Proof."
	case !source.RepositoryAccessible:
		return "Restore provider access to the selected repository."
	case !source.Active:
		return "Test the repository connection successfully before enabling Proof."
	default:
		return ""
	}
}

func (s *Service) Set(ctx context.Context, org, ds slug.Slug, actor model.EventActor, settings Settings) (*Configuration, error) {
	targets := make([]proofconfig.Target, len(settings.Targets))
	for i, target := range settings.Targets {
		targets[i] = proofconfig.Target(target)
	}
	current, err := s.Get(ctx, org, ds)
	if err != nil {
		return nil, err
	}
	if settings.Enabled {
		if status := eligibility(current.Source); status != "" {
			return nil, ErrConfiguration.Detail(status)
		}
		if current.Source.RepositoryID != settings.ExpectedRepositoryID {
			return nil, ErrConfiguration.Detail("Repository changed. Reload and confirm the new repository.")
		}
		if err := validateTargets(targets, current.Contexts); err != nil {
			return nil, err
		}
		repositories, err := s.sources.ListProviderRepositories(ctx, org, ds)
		if err != nil {
			return nil, xerror.Wrapf("verify Proof repository access: %w", err)
		}
		accessible := false
		for _, repository := range repositories {
			if repository.ID == settings.ExpectedRepositoryID {
				accessible = true
				break
			}
		}
		if !accessible {
			return nil, ErrConfiguration.Detail("Restore provider access to the selected repository.")
		}
	}
	err = s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if err := s.repo.Lock(ctx, db, org, ds); err != nil {
			return err
		}
		sources, err := s.repo.List(ctx, db, org, ds)
		if err != nil {
			return err
		}
		if len(sources) != 1 {
			return ErrSourceNotFound
		}
		source := sources[0]
		if settings.Enabled {
			if source.RepositoryID != settings.ExpectedRepositoryID || source.ConnectionID != current.Source.ConnectionID {
				return ErrConfiguration.Detail("Repository connection changed. Reload and confirm the configuration.")
			}
			if status := eligibility(source); status != "" {
				return ErrConfiguration.Detail(status)
			}
			contexts, err := s.repo.Contexts(ctx, db, org, ds)
			if err != nil {
				return err
			}
			if err := validateTargets(targets, contexts); err != nil {
				return err
			}
			existing, err := s.repo.Targets(ctx, db, org, ds)
			if err != nil {
				return err
			}
			if !source.MappingsCurrent || !sameTargets(existing, targets) {
				if err := s.repo.RetireTargets(ctx, db, org, ds); err != nil {
					return err
				}
				for _, target := range targets {
					if err := s.repo.CreateTarget(ctx, db, org, source, target); err != nil {
						return xerror.Wrapf("configure Proof target: %w", err)
					}
				}
			}
		}
		if err := s.repo.SetControls(ctx, db, org, ds, settings.Enabled, settings.Enabled && settings.PublishChecks); err != nil {
			return err
		}
		_, err = s.events.EnqueueRequired(ctx, db, org, actor, configurationEvent{source: ds, enabled: settings.Enabled, checks: settings.Enabled && settings.PublishChecks})
		return err
	})
	if err != nil {
		return nil, xerror.Wrapf("save Proof configuration: %w", err)
	}
	if settings.Enabled && current.Source.Provider == "gitlab" {
		if err := s.sources.EnsureProofWebhook(ctx, org, ds); err != nil {
			return nil, xerror.Wrapf("Proof settings saved; retry GitLab webhook setup: %w", err)
		}
	}
	return s.Get(ctx, org, ds)
}

func validateTargets(targets []proofconfig.Target, contexts []proofconfig.TargetContext) error {
	if len(targets) == 0 || len(targets) > 100 {
		return ErrConfiguration.Detail("Select between one and 100 exact assessment targets.")
	}
	seen := make(map[proofconfig.Target]bool, len(targets))
	for _, target := range targets {
		if seen[target] {
			return ErrConfiguration.Detail("Assessment targets must be unique.")
		}
		seen[target] = true
		if len(target.Ref) == 0 || len(target.Ref) > 255 || len(target.Path) > 1024 || target.Path == "" || strings.ContainsAny(target.Path, "\\\x00\r\n") || path.IsAbs(target.Path) || path.Clean(target.Path) != target.Path {
			return ErrConfiguration.Detail("Pipeline paths must be clean repository-relative paths.")
		}
		valid := false
		for _, candidate := range contexts {
			if candidate.Project == target.Project && candidate.Environment == target.Environment && candidate.Ref == target.Ref && strings.HasPrefix(target.Path, candidate.Directory+"/") {
				valid = true
				break
			}
		}
		if !valid {
			return ErrConfiguration.Detail("Select the project's configured environment, tracked ref, and a path inside its pipeline directory.")
		}
	}
	return nil
}

func sameTargets(a, b []proofconfig.Target) bool {
	if len(a) != len(b) {
		return false
	}
	existing := make(map[proofconfig.Target]bool, len(a))
	for _, target := range a {
		existing[target] = true
	}
	for _, target := range b {
		if !existing[target] {
			return false
		}
	}
	return true
}

type configurationEvent struct {
	source          slug.Slug
	enabled, checks bool
}

func (e configurationEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{SlugPrefix: "evt-proof-config", EventClass: model.EventClassSecurityAudit, EntityType: model.EventEntityTypeDataSource, EntitySlug: e.source, Action: model.EventActionUpdate, SchemaVersion: model.CurrentEventSchemaVersion, Metadata: map[string]any{"proof_enabled": e.enabled, "proof_checks_enabled": e.checks}}, nil
}
