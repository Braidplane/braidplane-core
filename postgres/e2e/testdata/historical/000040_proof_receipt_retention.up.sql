-- Archived transition regression fixture; outside the applied Core migration catalog.
ALTER TABLE proof_lifecycle_links
    DROP CONSTRAINT fk_proof_lifecycle_links_receipt,
    ADD CONSTRAINT fk_proof_lifecycle_links_receipt
        FOREIGN KEY (organization_id, receipt_id)
        REFERENCES proof_receipts (organization_id, id) ON DELETE CASCADE;

CREATE INDEX idx_proof_receipts_retention
    ON proof_receipts (organization_id, created_at, id);
CREATE INDEX idx_proof_receipts_supersedes
    ON proof_receipts (organization_id, supersedes_receipt_id)
    WHERE supersedes_receipt_id IS NOT NULL;
CREATE INDEX idx_proof_receipts_evidence
    ON proof_receipts (organization_id, evidence_id);
CREATE INDEX idx_events_proof_receipt_deletion
    ON events (organization_id, (metadata->>'proof_receipt_key'))
    WHERE event_class = 'security_audit' AND action = 'delete';

-- Record the new effective policy without rewriting captured retention decisions.
INSERT INTO events (organization_id, slug, actor_type, entity_type, entity_slug,
    action, event_class, schema_version, metadata)
SELECT organization.id, 'evt-proof-retention-seven-days', 'system', 'organization', organization.slug,
    'update', 'security_audit', 1,
    jsonb_build_object('proof_receipt_retention_days', 7)
FROM organizations organization
WHERE EXISTS (SELECT 1 FROM proof_receipts receipt WHERE receipt.organization_id = organization.id);
