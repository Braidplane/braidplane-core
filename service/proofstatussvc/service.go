package proofstatussvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider/providerclient"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
)

type GitAuthResolver interface {
	ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error)
}

type Service interface {
	Publish(context.Context, model.ProofReceipt) error
	Refresh(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (bool, error)
}

type ReceiptReader interface {
	Get(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceiptsvc.ReceiptDetail, error)
}

type PublicationLocker interface {
	PublicationEnabled(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (bool, error)
	LockTarget(context.Context, postgres.DBTX, string) error
}

type service struct {
	txm          postgres.TransactionManager
	locker       PublicationLocker
	auth         GitAuthResolver
	baseURL      string
	receipts     ReceiptReader
	publication  proofreceiptsvc.ReceiptPublicationProducer
	enabled      bool
	newPublisher func(context.Context, repostore.Provider, string, []string) (gitprovider.ProofCheckClient, error)
}

func New(txm postgres.TransactionManager, locker PublicationLocker, auth GitAuthResolver, receipts ReceiptReader, publication proofreceiptsvc.ReceiptPublicationProducer, enabled bool, baseURL string) (Service, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if txm == nil || locker == nil || auth == nil || receipts == nil || publication == nil || err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, xerror.Unexpected().Detail("Proof status dependencies are required")
	}
	return &service{txm: txm, locker: locker, auth: auth, receipts: receipts, publication: publication, enabled: enabled, baseURL: parsed.String(), newPublisher: providerclient.NewProofCheckPublisher}, nil
}

// Refresh reads persisted evidence only; publication retries never compile or dispatch observations.
func (s *service) Refresh(ctx context.Context, org, project, environment, receiptID slug.Slug) (bool, error) {
	detail, err := s.receipts.Get(ctx, org, project, environment, receiptID)
	if err != nil {
		if xerror.Is(err, proofreceipts.ErrReceiptNotFound) {
			return false, nil
		}
		return true, xerror.Wrapf("read Proof receipt for publication refresh: %w", err)
	}
	if !s.enabled {
		return true, nil
	}
	again, err := s.publish(ctx, detail.Receipt)
	if err != nil {
		if xerror.Is(err, gitprovider.ErrChangeStale) || xerror.Is(err, proofreceipts.ErrReceiptNotFound) {
			return false, nil
		}
		return true, err
	}
	return again, nil
}

func (s *service) Publish(ctx context.Context, receipt model.ProofReceipt) error {
	// A provider re-request can recover a receipt compiled while publishing was
	// disabled. Queue monitoring before provider I/O so permission loss cannot lose it.
	if s.enabled && receipt.Candidate.CompilerVersion == proofreceiptsvc.LiveCompilerVersion {
		_, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (struct{}, error) {
			allowed, err := s.locker.PublicationEnabled(ctx, tx, receipt.Candidate.OrganizationSlug, receipt.ID)
			if err != nil || !allowed {
				return struct{}{}, err
			}
			return struct{}{}, s.publication.EnqueueProofReceipt(ctx, tx, receipt)
		})
		if err != nil {
			return xerror.Wrapf("queue Proof publication monitoring: %w", err)
		}
	}
	_, err := s.publish(ctx, receipt)
	return err
}

func (s *service) publish(ctx context.Context, receipt model.ProofReceipt) (bool, error) {
	if !s.enabled {
		return true, nil
	}
	allowed, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
		return s.locker.PublicationEnabled(ctx, db, receipt.Candidate.OrganizationSlug, receipt.ID)
	})
	if err != nil {
		return true, err
	}
	if !allowed {
		return false, nil
	}
	auth, err := s.auth.ResolveGitAuth(ctx, receipt.Candidate.OrganizationSlug, receipt.Candidate.ProviderConnectionSlug)
	if err != nil {
		return true, gitprovider.ErrChangePermissionLost.With(err)
	}
	if auth.Cleanup != nil {
		defer auth.Cleanup()
	}
	provider := repostore.Provider(strings.TrimSpace(auth.Provider))
	publisher, err := s.newPublisher(ctx, provider, auth.RepoURL, auth.Env)
	if err != nil {
		return true, xerror.Wrapf("create Proof check publisher: %w", err)
	}
	checkID := proofCheckID(receipt)
	// The bounded provider call stays under the advisory lock so separate workers cannot create duplicate check runs.
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (bool, error) {
		lockKey := "proof-check:" + checkID
		if err := s.locker.LockTarget(ctx, tx, lockKey); err != nil {
			return true, xerror.Wrapf("lock Proof check publication: %w", err)
		}
		allowed, err := s.locker.PublicationEnabled(ctx, tx, receipt.Candidate.OrganizationSlug, receipt.ID)
		if err != nil {
			return true, err
		}
		if !allowed {
			return false, nil
		}
		// Re-read after locking so a queued immutable result cannot hide expired or revoked evidence.
		c := receipt.Candidate
		detail, err := s.receipts.Get(ctx, c.OrganizationSlug, c.ProjectSlug, c.EnvironmentSlug, receipt.ID)
		if err != nil {
			return true, xerror.Wrapf("read locked Proof receipt for publication: %w", err)
		}
		again := detail.Receipt.AggregateVerdict != model.ProofVerdictSuperseded && detail.Receipt.RetainUntil.After(time.Now())
		return again, s.publishLocked(ctx, publisher, detail.Receipt, checkID)
	})
}

func (s *service) publishLocked(ctx context.Context, publisher gitprovider.ProofCheckClient, receipt model.ProofReceipt, checkID string) error {
	proposal, err := publisher.GetProposal(ctx, gitprovider.ProposalGetRequest{ProposalID: receipt.Candidate.ChangeRequestID})
	if err != nil {
		return xerror.Wrapf("read Proof change before publication: %w", err)
	}
	if proposal == nil || proposal.RepositoryID != receipt.Candidate.RepositoryID {
		return gitprovider.ErrChangeStale
	}
	obsolete := proposal.State != gitprovider.ProposalStateOpen || proposal.Closed || proposal.Merged || !strings.EqualFold(proposal.HeadSHA, receipt.Candidate.HeadSHA)
	if obsolete {
		receipt.AggregateVerdict = model.ProofVerdictSuperseded
	} else if receipt.AggregateVerdict == model.ProofVerdictSuperseded {
		// A newer receipt on this head owns the same check; an old job must not overwrite it.
		return nil
	}
	counts := map[model.ProofVerdict]int{}
	evidence := map[model.ProofEvidenceLevel]int{}
	for _, claim := range receipt.Claims {
		counts[claim.Verdict]++
		if claim.ActualEvidenceLevel != nil {
			evidence[*claim.ActualEvidenceLevel]++
		}
	}
	summary := fmt.Sprintf("Informational Proof: %s. Verdicts: %d passed, %d failed, %d stale, %d inconclusive, %d unverified. Evidence: %d static, %d cached observed, %d live read-only, %d bounded execution.",
		receipt.AggregateVerdict.String(), counts[model.ProofVerdictPassed], counts[model.ProofVerdictFailed], counts[model.ProofVerdictStale], counts[model.ProofVerdictInconclusive], counts[model.ProofVerdictUnverified],
		evidence[model.ProofEvidenceLevelStatic], evidence[model.ProofEvidenceLevelCachedObserved], evidence[model.ProofEvidenceLevelLiveReadOnly], evidence[model.ProofEvidenceLevelBoundedExecution])
	var expires time.Time
	for _, claim := range receipt.Claims {
		if claim.ExpiresAt != nil && (expires.IsZero() || claim.ExpiresAt.Before(expires)) {
			expires = *claim.ExpiresAt
		}
	}
	if !expires.IsZero() {
		summary += " Evidence expires no later than " + expires.UTC().Format(time.RFC3339) + "."
	}
	summary += " Snapshot only; current authorization and freshness are shown in the protected Proof view."
	details := fmt.Sprintf("%s/orgs/%s/projects/%s/environments/%s/proof-receipts/%s", s.baseURL,
		receipt.Candidate.OrganizationSlug, receipt.Candidate.ProjectSlug, receipt.Candidate.EnvironmentSlug, receipt.ID)
	if err := publisher.PublishProofCheck(ctx, gitprovider.ProofCheck{
		RepositoryID: receipt.Candidate.RepositoryID, ChangeRequestID: receipt.Candidate.ChangeRequestID,
		HeadSHA: receipt.Candidate.HeadSHA, ExternalID: checkID, DetailsURL: details,
		Summary: summary, Conclusion: "neutral",
		ExistingOnly: obsolete,
	}); err != nil {
		return xerror.Wrapf("publish Proof check: %w", err)
	}
	if obsolete {
		return gitprovider.ErrChangeStale
	}
	return nil
}

func proofCheckID(receipt model.ProofReceipt) string {
	identity := strings.Join([]string{receipt.Candidate.RepositoryID, receipt.Candidate.ChangeRequestID, receipt.Candidate.HeadSHA, receipt.Candidate.PipelinePath}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "proof-check-" + hex.EncodeToString(sum[:16])
}
