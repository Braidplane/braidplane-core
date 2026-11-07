package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/stretchr/testify/require"
)

func TestReceiptExpiryAndReplayBeforeAndAfterDeletion(t *testing.T) {
	f := newFixture(t)
	compiler, _ := newReceiptCompiler(t, f)
	receipt, err := compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
	require.NoError(t, err)
	require.Equal(t, 7*24*time.Hour, receipt.Value.RetainUntil.Sub(receipt.Value.CreatedAt))
	// Existing 90-day decisions are retained, but capped at seven days for access.
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_receipts SET created_at=statement_timestamp()-interval '7 days'+interval '1 minute', retain_until=statement_timestamp()+interval '83 days' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	_, err = compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, receipt.Value.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_receipts SET created_at=statement_timestamp()-interval '7 days' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	assertExpiredReceipt(t, f, compiler, receipt.Value.ID)
	_, err = compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound, "expired existing receipt cannot be returned on replay")
	f.retentionAudit.failAction = model.EventActionDelete
	require.ErrorContains(t, f.maintenance.CleanupReceipts(t.Context(), f.scope.Organization), "Proof receipt retention audit unavailable")
	require.Equal(t, 1, receiptRowCount(t, f, "proof_receipts"))
	require.Equal(t, 1, receiptRowCount(t, f, "proof_evidence"))
	require.Zero(t, receiptRowCount(t, f, "events"))
	f.retentionAudit.failAction = model.EventActionUnknown
	require.NoError(t, f.maintenance.Run(t.Context()))
	require.Zero(t, receiptRowCount(t, f, "proof_receipts"))
	require.Zero(t, receiptRowCount(t, f, "proof_evidence"))

	_, err = f.pool.Exec(t.Context(), `INSERT INTO events(organization_id,slug,actor_type,entity_type,action,event_class) SELECT id,'ordinary-activity','system','organization','update','activity' FROM organizations WHERE slug=$1`, f.scope.Organization)
	require.NoError(t, err)
	// Exercise global pruning in a rollback-only transaction so other fixtures
	// and seeded activity remain untouched.
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = events.New().PruneOlderThan(t.Context(), tx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	var remaining int
	require.NoError(t, tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&remaining))
	require.Equal(t, 1, remaining, "global pruning keeps the deletion key and deletes ordinary activity")
	require.NoError(t, tx.Rollback(t.Context()))
	pruned, err := retention.New().DeleteEventsBefore(t.Context(), f.pool, f.scope.Organization, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, pruned, "tenant pruning keeps deletion keys")
	var key string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT metadata->>'proof_receipt_key' FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND action='delete'`, f.scope.Organization).Scan(&key))
	require.NotEmpty(t, key)
	// The static compiler is also used while Proof/live observations are disabled.
	_, err = compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound, "deletion audit prevents recreation")
	require.Zero(t, receiptRowCount(t, f, "proof_evidence"), "failed replay rolls back evidence insertion")
}

func TestReceiptRetentionBatchesChainsSharedEvidenceAndTenantIsolation(t *testing.T) {
	f := newFixture(t)
	other := newFixture(t)
	compiler, _ := newReceiptCompiler(t, f)
	otherCompiler, _ := newReceiptCompiler(t, other)
	retained, err := compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
	require.NoError(t, err)
	_, err = otherCompiler.Compile(t.Context(), other.scope.Organization, other.request.CandidateSlug)
	require.NoError(t, err)
	// 101 expired receipts reference shared evidence and form a linked chain.
	_, err = f.pool.Exec(t.Context(), `INSERT INTO proof_receipts (organization_id,project_id,project_environment_id,mapping_id,candidate_id,evidence_id,slug,receipt_key,spec_hash,binding_fingerprint,capability_fingerprint,compiler_version,policy_version,aggregate_verdict,receipt,created_at,retain_until)
 SELECT r.organization_id,r.project_id,r.project_environment_id,r.mapping_id,r.candidate_id,r.evidence_id,'expired-'||n,'expired-key-'||n,r.spec_hash,r.binding_fingerprint,r.capability_fingerprint,r.compiler_version,r.policy_version,r.aggregate_verdict,r.receipt,statement_timestamp()-interval '8 days',statement_timestamp()+interval '82 days'
 FROM proof_receipts r CROSS JOIN generate_series(1,101) n WHERE r.organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_receipts r SET supersedes_receipt_id=prior.id FROM proof_receipts prior WHERE r.organization_id=prior.organization_id AND r.organization_id=(SELECT id FROM organizations WHERE slug=$1) AND (r.slug='expired-'||((substring(prior.slug from 9))::integer+1) OR r.slug=$2 AND prior.slug='expired-101') AND prior.slug LIKE 'expired-%'`, f.scope.Organization, retained.Value.ID)
	require.NoError(t, err)
	// The first committed batch must survive interruption/audit failure in batch two.
	f.retentionAudit.failAfter = 100
	require.Error(t, f.maintenance.CleanupReceipts(t.Context(), f.scope.Organization))
	require.Equal(t, 2, receiptRowCount(t, f, "proof_receipts"))
	require.Equal(t, 100, receiptRowCount(t, f, "events"))
	require.Equal(t, 1, receiptRowCount(t, f, "proof_evidence"), "retained receipt still references shared evidence")
	require.Equal(t, 1, receiptRowCount(t, other, "proof_receipts"))
	require.Zero(t, receiptRowCount(t, other, "events"))
	f.retentionAudit.failAfter = 0
	require.NoError(t, f.maintenance.CleanupReceipts(t.Context(), f.scope.Organization))
	var pointer *int64
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT supersedes_receipt_id FROM proof_receipts WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&pointer))
	require.Nil(t, pointer)
	require.Equal(t, 101, receiptRowCount(t, f, "events"))
	require.Equal(t, 1, receiptRowCount(t, f, "proof_evidence"))
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_receipts SET retain_until=statement_timestamp() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.CleanupReceipts(t.Context(), f.scope.Organization))
	require.Zero(t, receiptRowCount(t, f, "proof_evidence"))
}

func TestReceiptCompilationSerializesWithDeletionAndConcurrentReplay(t *testing.T) {
	f := newFixture(t)
	compiler, _ := newReceiptCompiler(t, f)
	const callers = 4
	receipts := make(chan *proofreceipts.Receipt, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			receipt, err := compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
			receipts <- receipt
			errors <- err
		})
	}
	group.Wait()
	close(receipts)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var receiptSlug slug.Slug
	for receipt := range receipts {
		if receiptSlug == "" {
			receiptSlug = receipt.Value.ID
		}
		require.Equal(t, receiptSlug, receipt.Value.ID)
	}
	require.Equal(t, 1, receiptRowCount(t, f, "proof_receipts"))
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	repo := proofreceipts.New()
	require.NoError(t, repo.LockTarget(t.Context(), tx, "proof-retention:"+f.scope.Organization.String()))
	_, err = tx.Exec(t.Context(), `UPDATE proof_receipts SET retain_until=statement_timestamp() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("compilation escaped retention lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	// A compiler waiting for retention must not block the organization's audit FK.
	_, err = tx.Exec(t.Context(), `SELECT id FROM organizations WHERE slug=$1 FOR KEY SHARE NOWAIT`, f.scope.Organization)
	require.NoError(t, err)
	// Both waiting paths must observe expiry after the tenant lock is released.
	require.NoError(t, tx.Commit(t.Context()))
	require.NoError(t, f.maintenance.CleanupReceipts(t.Context(), f.scope.Organization))
	require.ErrorIs(t, <-done, proofreceipts.ErrReceiptNotFound)
	require.Zero(t, receiptRowCount(t, f, "proof_receipts"))
}

func assertExpiredReceipt(t *testing.T, f *fixture, compiler *proofreceiptsvc.LiveCompiler, id slug.Slug) {
	t.Helper()
	_, err := compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, id)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	page, err := compiler.List(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, model.ListRequest{Limit: 1})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.False(t, page.HasMore)
}

func receiptRowCount(t *testing.T, f *fixture, table string) int {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), fmt.Sprintf(`SELECT count(*) FROM %s WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, table), f.scope.Organization).Scan(&count))
	return count
}
