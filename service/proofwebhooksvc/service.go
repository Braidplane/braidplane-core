package proofwebhooksvc

import (
	"context"
	"sort"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/proofcandidatesvc"
)

var (
	ErrDeliveryRejected      = xerror.New(xerror.KindInvalidInput, "PROOF_WEBHOOK_DELIVERY_REJECTED", "proof webhook delivery was rejected")
	ErrProcessingUnavailable = xerror.New(xerror.KindUnavailable, "PROOF_WEBHOOK_PROCESSING_UNAVAILABLE", "proof webhook processing is unavailable")
)

type MappingResolver interface {
	ResolveWebhookMappings(context.Context, postgres.DBTX, proofrepo.WebhookMappingLookup) ([]proofrepo.Mapping, error)
}

type DeliveryClaimer interface {
	ClaimWebhookDelivery(context.Context, postgres.DBTX, string, string, string) (bool, error)
}

type DeliveryProducer interface {
	EnqueueProofChange(context.Context, postgres.DBTX, gitprovider.VerifiedChangeEvent) (queue.QueuedJob, error)
}

type GitHubBindingResolver interface {
	Resolve(context.Context, int64, int64) (slug.Slug, error)
}

type ProofCompiler interface {
	Compile(context.Context, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error)
}

type ProofStatusPublisher interface {
	Publish(context.Context, model.ProofReceipt) error
}

type Service struct {
	txm              postgres.TransactionManager
	mappings         MappingResolver
	deliveries       DeliveryClaimer
	candidates       proofcandidatesvc.Service
	compiler         ProofCompiler
	producer         DeliveryProducer
	githubBindings   GitHubBindingResolver
	statusPublisher  ProofStatusPublisher
	gitChecksEnabled bool
}

func New(txm postgres.TransactionManager, mappings MappingResolver, deliveries DeliveryClaimer, candidates proofcandidatesvc.Service, compiler ProofCompiler, producer DeliveryProducer, githubBindings GitHubBindingResolver, statusPublisher ProofStatusPublisher, gitChecksEnabled bool) (*Service, error) {
	if txm == nil || mappings == nil || deliveries == nil || candidates == nil || compiler == nil || producer == nil || githubBindings == nil || statusPublisher == nil {
		return nil, xerror.Unexpected().Detail("proof webhook dependencies are required")
	}
	return &Service{txm: txm, mappings: mappings, deliveries: deliveries, candidates: candidates, compiler: compiler, producer: producer, githubBindings: githubBindings, statusPublisher: statusPublisher, gitChecksEnabled: gitChecksEnabled}, nil
}

func (s *Service) Accept(ctx context.Context, event gitprovider.VerifiedChangeEvent) (queue.QueuedJob, error) {
	if err := event.Validate(); err != nil {
		return queue.QueuedJob{}, err
	}
	if event.Provider == repostore.ProviderGitLab && event.OrganizationSlug == "" {
		return queue.QueuedJob{}, ErrDeliveryRejected.Detail("GitLab binding organization is required")
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (queue.QueuedJob, error) {
		claimed, err := s.deliveries.ClaimWebhookDelivery(ctx, tx, string(event.Provider), event.DeliveryID, event.BodySHA256)
		if err != nil {
			if xerror.Is(err, proofrepo.ErrWebhookDeliveryDigestMismatch) {
				return queue.QueuedJob{}, ErrDeliveryRejected.With(err)
			}
			return queue.QueuedJob{}, xerror.Wrapf("claim proof webhook delivery: %w", err)
		}
		if !claimed {
			return queue.QueuedJob{}, nil
		}
		return s.producer.EnqueueProofChange(ctx, tx, event)
	})
}

func (s *Service) Process(ctx context.Context, event gitprovider.VerifiedChangeEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Provider == repostore.ProviderGitLab && event.OrganizationSlug == "" {
		return ErrDeliveryRejected.Detail("GitLab binding organization is required")
	}
	var boundOrganization slug.Slug
	switch event.Provider {
	case repostore.ProviderGitHub:
		installationID, installErr := strconv.ParseInt(event.ProviderConnectionID, 10, 64)
		repositoryID, repoErr := strconv.ParseInt(event.RepositoryID, 10, 64)
		if installErr != nil || repoErr != nil {
			return ErrDeliveryRejected.Detail("GitHub App binding identity is invalid")
		}
		var resolveErr error
		boundOrganization, resolveErr = s.githubBindings.Resolve(ctx, installationID, repositoryID)
		if resolveErr != nil {
			return ErrDeliveryRejected.With(resolveErr)
		}
		if boundOrganization == "" {
			return ErrDeliveryRejected.Detail("GitHub App binding did not resolve an organization")
		}
	case repostore.ProviderGitLab:
		boundOrganization = event.OrganizationSlug
	case repostore.ProviderGeneric:
		return ErrDeliveryRejected.Detail("proof webhook provider is invalid")
	}
	mappings, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]proofrepo.Mapping, error) {
		return s.mappings.ResolveWebhookMappings(ctx, db, proofrepo.WebhookMappingLookup{
			Provider: string(event.Provider), ProviderConnectionID: event.ProviderConnectionID,
			RepositoryID: event.RepositoryID, TargetRef: event.TargetRef,
		})
	})
	if err != nil {
		return err
	}
	if len(mappings) == 0 {
		return ErrDeliveryRejected.Detail("proof webhook mapping was not found")
	}
	organization := mappings[0].OrganizationSlug
	if boundOrganization != "" && boundOrganization != organization {
		return ErrDeliveryRejected.Detail("provider binding does not match the mapped organization")
	}
	for _, mapping := range mappings {
		if mapping.OrganizationSlug != organization {
			return ErrDeliveryRejected.Detail("proof webhook mapping crosses organizations")
		}
	}
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].PipelinePath != mappings[j].PipelinePath {
			return mappings[i].PipelinePath < mappings[j].PipelinePath
		}
		return mappings[i].ID < mappings[j].ID
	})
	compileTargets := make([]slug.Slug, 0, len(mappings))
	for _, mapping := range mappings {
		change := event.Change(mapping.PipelinePath)
		switch event.Action {
		case gitprovider.ChangeActionUpsert:
			candidate, err := s.candidates.IngestTarget(ctx, mapping, model.ExternalEventActor(), change)
			if err != nil {
				return err
			}
			compileTargets = append(compileTargets, candidate.CandidateSlug)
		case gitprovider.ChangeActionClose:
			if err := s.candidates.CloseTarget(ctx, mapping, model.ExternalEventActor(), change); err != nil {
				return err
			}
		case gitprovider.ChangeActionUnknown:
			return ErrDeliveryRejected.Detail("proof webhook action is invalid")
		default:
			return ErrDeliveryRejected.Detail("proof webhook action is invalid")
		}
	}
	compileErrs := make([]error, 0, len(compileTargets))
	for _, candidateSlug := range compileTargets {
		receipt, err := s.compiler.Compile(ctx, organization, candidateSlug)
		if err != nil {
			compileErrs = append(compileErrs, err)
		} else if s.gitChecksEnabled {
			if err := s.statusPublisher.Publish(ctx, receipt.Value); err != nil {
				compileErrs = append(compileErrs, err)
			}
		}
	}
	return xerror.Join(compileErrs...)
}
